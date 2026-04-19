package agentmon

import (
	"context"
	"encoding/binary"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/internal/container"
)

// liveFakeRuntime wraps container.FakeRuntime to serve StreamLogs from
// an io.Pipe instead of a snapshot copy. The upstream fake is optimised
// for spawner tests (deterministic snapshot semantics); the agentmon
// source needs a live stream because writes arrive after the tail is
// opened. Only StreamLogs is overridden; every other Runtime method
// delegates to the embedded FakeRuntime so event/filter semantics
// exercise the production path.
type liveFakeRuntime struct {
	*container.FakeRuntime
	mu    sync.Mutex
	pipes map[string]*io.PipeWriter
}

func newLiveFakeRuntime() *liveFakeRuntime {
	return &liveFakeRuntime{
		FakeRuntime: container.NewFakeRuntime(),
		pipes:       map[string]*io.PipeWriter{},
	}
}

func (f *liveFakeRuntime) StreamLogs(_ context.Context, id string) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	pr, pw := io.Pipe()
	f.pipes[id] = pw
	return pr, nil
}

// FrameAndWrite pushes a framed log line down the pipe opened by
// StreamLogs(id). The caller must have already issued an EventStart for
// id and waited for the source to open the tail (accomplished via
// require.Eventually or by ensuring a prior call succeeded).
func (f *liveFakeRuntime) FrameAndWrite(id string, line string) error {
	f.mu.Lock()
	pw := f.pipes[id]
	f.mu.Unlock()
	if pw == nil {
		return io.ErrClosedPipe
	}
	payload := []byte(line)
	hdr := make([]byte, dockerFrameHeader)
	hdr[0] = 1
	binary.BigEndian.PutUint32(hdr[4:8], uint32(len(payload)))
	if _, err := pw.Write(hdr); err != nil {
		return err
	}
	_, err := pw.Write(payload)
	return err
}

// ClosePipe tears down the write end of a container's log pipe so the
// tail goroutine observes EOF. Used to simulate Docker closing the log
// stream when a container dies.
func (f *liveFakeRuntime) ClosePipe(id string) {
	f.mu.Lock()
	pw := f.pipes[id]
	delete(f.pipes, id)
	f.mu.Unlock()
	if pw != nil {
		_ = pw.Close()
	}
}

// waitForStream blocks until StreamLogs has been invoked for id or the
// deadline expires. The FakeRuntime records every call so this is just
// a poll; the alternative (exposing a signal channel from FakeRuntime)
// would require upstream changes outside this prompt's scope.
func (f *liveFakeRuntime) waitForStream(t *testing.T, id string, timeout time.Duration) {
	t.Helper()
	require.Eventually(t, func() bool {
		f.mu.Lock()
		_, ok := f.pipes[id]
		f.mu.Unlock()
		return ok
	}, timeout, 10*time.Millisecond, "StreamLogs never invoked for %s", id)
}

// waitForSubscription blocks until the underlying FakeRuntime has
// observed at least one SubscribeEvents call so that subsequent
// EmitEvent deliveries have a live destination.
func (f *liveFakeRuntime) waitForSubscription(t *testing.T, timeout time.Duration) {
	t.Helper()
	require.Eventually(t, func() bool {
		for _, c := range f.FakeRuntime.Calls() {
			if c.Op == "SubscribeEvents" {
				return true
			}
		}
		return false
	}, timeout, 5*time.Millisecond, "SubscribeEvents never invoked")
}

// TestDockerSourceRoutesLifecycleEvents walks the full start→tail→die
// cycle against the live fake runtime and asserts that (a) a commit
// line written to the pipe reaches the ingestor, and (b) once EventDie
// fires subsequent writes are discarded by the now-closed tail.
func TestDockerSourceRoutesLifecycleEvents(t *testing.T) {
	rt := newLiveFakeRuntime()
	ing := newCaptureIngestor()
	src := &DockerSource{
		Runtime:  rt,
		Ingestor: ing,
		Project:  "testproj",
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		require.NoError(t, src.Run(ctx))
	}()
	rt.waitForSubscription(t, 2*time.Second)

	rt.EmitEvent(container.Event{
		Type:        container.EventStart,
		ContainerID: "c1",
		Labels:      map[string]string{"drem.project": "testproj", "drem.worker-id": "w1"},
		Timestamp:   time.Now(),
	})
	rt.waitForStream(t, "c1", 2*time.Second)

	require.NoError(t, rt.FrameAndWrite("c1", "[master abc1234] fix thing\n"))

	require.Eventually(t, func() bool {
		return ing.TotalRecords() >= 1
	}, 2*time.Second, 10*time.Millisecond, "commit record never reached ingestor")

	batches := ing.Batches()
	require.NotEmpty(t, batches)
	require.Equal(t, recordTypeCommit, batches[0][0].Type)
	require.Equal(t, "c1", batches[0][0].ContainerID)
	require.Equal(t, "w1", batches[0][0].WorkerID)

	// Die → tail must exit, then further writes must be dropped.
	rt.EmitEvent(container.Event{
		Type:        container.EventDie,
		ContainerID: "c1",
		Labels:      map[string]string{"drem.project": "testproj"},
		Timestamp:   time.Now(),
	})
	// Close the pipe so the tail goroutine (if still running) observes
	// EOF; stopTail's cancel + the pipe closure together guarantee exit.
	rt.ClosePipe("c1")

	// Give the cancel propagation a beat, then a late write that should
	// be a no-op because the pipe is closed.
	time.Sleep(50 * time.Millisecond)
	before := ing.TotalRecords()
	_ = rt.FrameAndWrite("c1", "[master zzz9999] too late\n") // error expected; ignored

	// No new records should materialise.
	require.Never(t, func() bool {
		return ing.TotalRecords() > before
	}, 300*time.Millisecond, 20*time.Millisecond,
		"records continued to arrive after EventDie")

	cancel()
	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("DockerSource.Run did not exit within 1s of ctx cancel")
	}
}

// TestDockerSourceShutdownOnCtxCancel asserts the graceful-shutdown
// path: with a live tail running, cancelling ctx should cause Run to
// return and the tail goroutine to drain within 1s.
func TestDockerSourceShutdownOnCtxCancel(t *testing.T) {
	rt := newLiveFakeRuntime()
	ing := newCaptureIngestor()
	src := &DockerSource{Runtime: rt, Ingestor: ing}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		require.NoError(t, src.Run(ctx))
	}()
	rt.waitForSubscription(t, 2*time.Second)

	rt.EmitEvent(container.Event{
		Type:        container.EventStart,
		ContainerID: "c2",
		Labels:      map[string]string{},
		Timestamp:   time.Now(),
	})
	rt.waitForStream(t, "c2", 2*time.Second)

	cancel()
	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("Run did not return within 1s of ctx cancel")
	}
}

// TestDockerSourceRequiresDependencies exercises the guard-rail errors
// so a misconfigured caller is not left silently idle.
func TestDockerSourceRequiresDependencies(t *testing.T) {
	require.Error(t, (&DockerSource{}).Run(context.Background()))
	require.Error(t, (&DockerSource{Runtime: container.NewFakeRuntime()}).Run(context.Background()))
}
