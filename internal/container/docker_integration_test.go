//go:build integration

package container

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestDockerRuntime_SpawnAndWaitForDie exercises the real Docker daemon. It
// is skipped unless the integration build tag is set AND a Docker daemon is
// reachable. Run with:
//
//	go test -tags=integration ./internal/container/...
func TestDockerRuntime_SpawnAndWaitForDie(t *testing.T) {
	rt, err := NewDockerRuntime()
	if err != nil {
		t.Skipf("docker unavailable: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })

	// Sanity-ping the daemon. If this fails the test is meaningless.
	pingCtx, pingCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer pingCancel()
	if _, err := rt.cli.Ping(pingCtx); err != nil {
		t.Skipf("docker daemon unreachable: %v", err)
	}

	subCtx, subCancel := context.WithCancel(context.Background())
	defer subCancel()
	events, err := rt.SubscribeEvents(subCtx, EventFilter{Labels: map[string]string{"drem.test": "1"}})
	require.NoError(t, err)

	spawnCtx, spawnCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer spawnCancel()
	h, err := rt.Spawn(spawnCtx, Spec{
		Image:  "alpine:3",
		Cmd:    []string{"echo", "hello"},
		Labels: map[string]string{"drem.test": "1"},
	})
	require.NoError(t, err)
	require.NotEmpty(t, h.ID)

	defer func() {
		// Best-effort cleanup — the test's final Destroy is the primary
		// path, this guards against early-return cases.
		destroyCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = rt.Destroy(destroyCtx, h.ID)
	}()

	// Wait for the die event for our container.
	deadline := time.After(30 * time.Second)
WAIT:
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				t.Fatal("event channel closed before die event")
			}
			if ev.ContainerID == h.ID && ev.Type == EventDie {
				break WAIT
			}
		case <-deadline:
			t.Fatal("timeout waiting for die event")
		}
	}

	inspectCtx, inspectCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer inspectCancel()
	st, err := rt.Inspect(inspectCtx, h.ID)
	require.NoError(t, err)
	require.Equal(t, 0, st.ExitCode)

	destroyCtx, destroyCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer destroyCancel()
	require.NoError(t, rt.Destroy(destroyCtx, h.ID))
}
