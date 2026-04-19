package container

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestFakeRuntime_SpawnReturnsIDAndRecordsCall(t *testing.T) {
	f := NewFakeRuntime()
	ctx := context.Background()

	h, err := f.Spawn(ctx, Spec{Image: "alpine:3", Cmd: []string{"echo", "hi"}})
	require.NoError(t, err)
	require.NotEmpty(t, h.ID)
	require.True(t, strings.HasPrefix(h.ID, "fake-"))

	calls := f.Calls()
	require.Len(t, calls, 1)
	require.Equal(t, "Spawn", calls[0].Op)
	require.Equal(t, h.ID, calls[0].ID)
	require.NotNil(t, calls[0].Spec)
	require.Equal(t, "alpine:3", calls[0].Spec.Image)
}

func TestFakeRuntime_InspectDefaultsToRunning(t *testing.T) {
	f := NewFakeRuntime()
	ctx := context.Background()

	h, err := f.Spawn(ctx, Spec{Image: "alpine:3"})
	require.NoError(t, err)

	st, err := f.Inspect(ctx, h.ID)
	require.NoError(t, err)
	require.Equal(t, StatusRunning, st.Status)
}

func TestFakeRuntime_SetInspectResultOverridesState(t *testing.T) {
	f := NewFakeRuntime()
	ctx := context.Background()

	h, err := f.Spawn(ctx, Spec{Image: "alpine:3"})
	require.NoError(t, err)

	want := State{Status: StatusExited, ExitCode: 42, OOMKilled: true}
	f.SetInspectResult(h.ID, want)

	got, err := f.Inspect(ctx, h.ID)
	require.NoError(t, err)
	require.Equal(t, want, got)
}

func TestFakeRuntime_DestroyUnknownIDReturnsError(t *testing.T) {
	f := NewFakeRuntime()
	err := f.Destroy(context.Background(), "does-not-exist")
	require.Error(t, err)
}

func TestFakeRuntime_DestroyKnownIDSucceedsAndRecords(t *testing.T) {
	f := NewFakeRuntime()
	ctx := context.Background()

	h, err := f.Spawn(ctx, Spec{Image: "alpine:3"})
	require.NoError(t, err)

	require.NoError(t, f.Destroy(ctx, h.ID))

	// Destroying again must fail — the in-memory entry is gone.
	require.Error(t, f.Destroy(ctx, h.ID))

	calls := f.Calls()
	var destroys int
	for _, c := range calls {
		if c.Op == "Destroy" {
			destroys++
		}
	}
	require.Equal(t, 2, destroys, "both destroy attempts should be recorded")
}

func TestFakeRuntime_StreamLogsReturnsWrittenBytes(t *testing.T) {
	f := NewFakeRuntime()
	ctx := context.Background()
	h, err := f.Spawn(ctx, Spec{Image: "alpine:3"})
	require.NoError(t, err)

	f.WriteLog(h.ID, []byte("line one\n"))
	f.WriteLog(h.ID, []byte("line two\n"))

	rc, err := f.StreamLogs(ctx, h.ID)
	require.NoError(t, err)
	defer rc.Close()

	data, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.Equal(t, "line one\nline two\n", string(data))
}

func TestFakeRuntime_SubscribeEventsDeliversMatchingOnly(t *testing.T) {
	f := NewFakeRuntime()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := f.SubscribeEvents(ctx, EventFilter{Labels: map[string]string{"project": "drem-orch"}})
	require.NoError(t, err)

	// Non-matching event: different project label.
	f.EmitEvent(Event{Type: EventStart, ContainerID: "c1", Labels: map[string]string{"project": "other"}})

	// Matching event: superset of filter labels (extra label is fine).
	f.EmitEvent(Event{Type: EventStart, ContainerID: "c2", Labels: map[string]string{"project": "drem-orch", "agent": "coder"}})

	// Non-matching: missing the filter label entirely.
	f.EmitEvent(Event{Type: EventStart, ContainerID: "c3", Labels: map[string]string{"agent": "coder"}})

	// Matching: another superset.
	f.EmitEvent(Event{Type: EventDie, ContainerID: "c4", Labels: map[string]string{"project": "drem-orch"}, ExitCode: 1})

	select {
	case ev := <-ch:
		require.Equal(t, "c2", ev.ContainerID)
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for first matching event")
	}
	select {
	case ev := <-ch:
		require.Equal(t, "c4", ev.ContainerID)
		require.Equal(t, 1, ev.ExitCode)
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for second matching event")
	}

	// No more events should be pending — the non-matching emits must have
	// been dropped, not queued.
	select {
	case ev, ok := <-ch:
		if ok {
			t.Fatalf("unexpected extra event: %+v", ev)
		}
	case <-time.After(50 * time.Millisecond):
		// Expected: channel is idle.
	}
}

func TestFakeRuntime_SubscribeEventsCancelClosesChannel(t *testing.T) {
	f := NewFakeRuntime()
	ctx, cancel := context.WithCancel(context.Background())

	ch, err := f.SubscribeEvents(ctx, EventFilter{})
	require.NoError(t, err)

	cancel()

	// After cancel the subscription cleanup goroutine closes the channel.
	select {
	case _, ok := <-ch:
		require.False(t, ok, "channel should be closed after ctx cancel")
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for channel close after cancel")
	}
}

func TestFakeRuntime_EmptyFilterMatchesAllEvents(t *testing.T) {
	f := NewFakeRuntime()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := f.SubscribeEvents(ctx, EventFilter{})
	require.NoError(t, err)

	f.EmitEvent(Event{Type: EventStart, ContainerID: "x1", Labels: map[string]string{"a": "b"}})
	f.EmitEvent(Event{Type: EventDie, ContainerID: "x2"})

	got := make([]string, 0, 2)
	for i := 0; i < 2; i++ {
		select {
		case ev := <-ch:
			got = append(got, ev.ContainerID)
		case <-time.After(time.Second):
			t.Fatalf("timeout waiting for event %d", i)
		}
	}
	require.ElementsMatch(t, []string{"x1", "x2"}, got)
}

// Compile-time check: FakeRuntime must satisfy the Runtime interface. This
// pairs with the equivalent check for DockerRuntime in docker_test.go via
// the production code path. Keeping the assertion here ensures that if the
// interface evolves, the fake breaks at build time rather than at test time.
var _ Runtime = (*FakeRuntime)(nil)
var _ Runtime = (*DockerRuntime)(nil)
