package agentmon

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
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

func (f *liveFakeRuntime) StreamLogs(_ context.Context, id string, _ container.LogOptions) (io.ReadCloser, error) {
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

// lockedBuffer wraps bytes.Buffer with a mutex so a slog handler
// writing from background goroutines and the test's asserting
// goroutine do not race on the underlying buffer.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// TestDockerSource_WarnOnZeroEventsAfterWindow asserts that a
// subscription with no events delivered inside Warn0Window produces
// exactly one WARN log line matching the documented grep target in
// plans/agentmon-observability.md. This is the systemic sensor that
// would have caught the 41h silent outage on day one.
func TestDockerSource_WarnOnZeroEventsAfterWindow(t *testing.T) {
	rt := newLiveFakeRuntime()
	ing := newCaptureIngestor()

	// Capture slog output into a buffer via a handler attached to a
	// private logger. We route the package's slog.Warn calls by
	// temporarily swapping slog.Default; restore after the test to
	// avoid polluting the default logger for other tests.
	logBuf := &lockedBuffer{}
	handler := slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelWarn})
	prev := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(prev) })

	src := &DockerSource{
		Runtime:         rt,
		Ingestor:        ing,
		Project:         "obs-test",
		ContainerFilter: container.EventFilter{Labels: map[string]string{"drem.project": "obs-test"}},
		Warn0Window:     50 * time.Millisecond,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		require.NoError(t, src.Run(ctx))
	}()
	rt.waitForSubscription(t, 2*time.Second)

	// No events are emitted; after Warn0Window the WARN must fire.
	require.Eventually(t, func() bool {
		return bytes.Contains([]byte(logBuf.String()),
			[]byte("agentmon docker source: zero events matched in 45s since subscription start"))
	}, 500*time.Millisecond, 10*time.Millisecond,
		"zero-events WARN was not emitted within Warn0Window+slack")

	// The filter labels must appear so operators can see WHICH filter
	// produced zero hits — the whole point of including them.
	require.Contains(t, logBuf.String(), "drem.project")

	cancel()
	<-done
}

// TestDockerSource_WarnSuppressedWhenEventsArrive asserts that at
// least one event in the window suppresses the WARN, so a healthy
// subscription does not produce false positives.
func TestDockerSource_WarnSuppressedWhenEventsArrive(t *testing.T) {
	rt := newLiveFakeRuntime()
	ing := newCaptureIngestor()

	logBuf := &lockedBuffer{}
	handler := slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelWarn})
	prev := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(prev) })

	src := &DockerSource{
		Runtime:     rt,
		Ingestor:    ing,
		Warn0Window: 100 * time.Millisecond,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		require.NoError(t, src.Run(ctx))
	}()
	rt.waitForSubscription(t, 2*time.Second)

	// Emit one event well inside the window.
	rt.EmitEvent(container.Event{
		Type:        container.EventStart,
		ContainerID: "c-quiet",
		Labels:      map[string]string{},
		Timestamp:   time.Now(),
	})

	// Wait past Warn0Window + slack; no zero-events WARN should fire.
	time.Sleep(250 * time.Millisecond)
	require.NotContains(t, logBuf.String(),
		"zero events matched in 45s since subscription start")

	cancel()
	<-done
}

// TestDockerSource_EventsMatchedLastMinute exercises the per-second
// bucket ring. We inject a deterministic clock, emit events at known
// epochs, and assert the trailing-60s sum. Because the subscription
// happens-before semantics require the event to have reached the Run
// loop before we sample, we use require.Eventually to give the
// goroutine time to increment the bucket.
func TestDockerSource_EventsMatchedLastMinute(t *testing.T) {
	rt := newLiveFakeRuntime()
	ing := newCaptureIngestor()

	var nowSec atomic.Int64
	nowSec.Store(1_000_000)
	clock := func() time.Time { return time.Unix(nowSec.Load(), 0) }

	src := &DockerSource{
		Runtime:     rt,
		Ingestor:    ing,
		Warn0Window: time.Hour, // suppress WARN in this test
		Now:         clock,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		require.NoError(t, src.Run(ctx))
	}()
	rt.waitForSubscription(t, 2*time.Second)

	// Emit 3 events in the current second.
	for i := 0; i < 3; i++ {
		rt.EmitEvent(container.Event{
			Type:        container.EventStart,
			ContainerID: "c-now-" + string(rune('a'+i)),
			Labels:      map[string]string{},
			Timestamp:   clock(),
		})
	}
	require.Eventually(t, func() bool {
		return src.EventsMatchedLastMinute() == 3
	}, 2*time.Second, 10*time.Millisecond,
		"expected 3 events counted in the trailing minute")

	// Advance to +30s, emit 2 more. Both buckets should still be in
	// the trailing 60s window, so total is 5.
	nowSec.Add(30)
	for i := 0; i < 2; i++ {
		rt.EmitEvent(container.Event{
			Type:        container.EventStart,
			ContainerID: "c-30-" + string(rune('a'+i)),
			Labels:      map[string]string{},
			Timestamp:   clock(),
		})
	}
	require.Eventually(t, func() bool {
		return src.EventsMatchedLastMinute() == 5
	}, 2*time.Second, 10*time.Millisecond,
		"expected 3+2=5 events counted in the trailing minute")

	// Advance past the window; now the first 3 fall off and only the
	// most-recent 2 should remain.
	nowSec.Add(31) // total +61, first batch now outside 60s window
	require.Equal(t, 2, src.EventsMatchedLastMinute(),
		"expected first-batch events to have aged out of the trailing minute")

	cancel()
	<-done
}

// TestDockerSource_HasSeenReflectsContainerSightings verifies that
// HasSeen is strict per container ID while subscription liveness stays
// available through EventsMatchedLastMinute.
func TestDockerSource_HasSeenReflectsContainerSightings(t *testing.T) {
	rt := newLiveFakeRuntime()
	ing := newCaptureIngestor()

	src := &DockerSource{
		Runtime:     rt,
		Ingestor:    ing,
		Warn0Window: time.Hour,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		require.NoError(t, src.Run(ctx))
	}()
	rt.waitForSubscription(t, 2*time.Second)

	// Before any event: HasSeen(anything) must be false — nothing has
	// come through the subscription yet.
	require.False(t, src.HasSeen("c-unknown"))

	// Emit a start for c1; HasSeen(c1) should become true once the
	// event is handled.
	rt.EmitEvent(container.Event{
		Type:        container.EventStart,
		ContainerID: "c1",
		Labels:      map[string]string{"drem.worker-id": "w1"},
		Timestamp:   time.Now(),
	})
	require.Eventually(t, func() bool {
		return src.HasSeen("c1")
	}, 2*time.Second, 10*time.Millisecond,
		"HasSeen(c1) never became true after EventStart")

	require.Eventually(t, func() bool {
		return src.EventsMatchedLastMinute() > 0
	}, 2*time.Second, 10*time.Millisecond,
		"subscription liveness bucket never recorded the start event")
	require.False(t, src.HasSeen("c-different"),
		"HasSeen must not fall back to recent traffic from another container")

	// Stop the tail; sighting history should remain true even after the
	// active tail is gone.
	rt.EmitEvent(container.Event{
		Type:        container.EventDie,
		ContainerID: "c1",
		Labels:      map[string]string{},
		Timestamp:   time.Now(),
	})
	rt.ClosePipe("c1")
	require.Eventually(t, func() bool {
		src.mu.Lock()
		_, active := src.tails["c1"]
		src.mu.Unlock()
		return !active
	}, 2*time.Second, 10*time.Millisecond,
		"tail for c1 never stopped after EventDie")
	require.True(t, src.HasSeen("c1"),
		"HasSeen should preserve per-container sighting after tail exits")

	cancel()
	<-done
}
