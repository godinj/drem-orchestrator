//go:build integration

package agent

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/godinj/drem-orchestrator/internal/container"
	"github.com/godinj/drem-orchestrator/internal/spawner"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

// rpcAdapter wraps a spawner.Client as an agent.Spawner. It lives in
// this test file so the non-test code in the agent package does not
// depend on internal/spawner. Production callers declare an equivalent
// adapter in their own package (see internal/agent/README.md).
type rpcAdapter struct {
	c *spawner.Client
}

func (a *rpcAdapter) SpawnWorker(ctx context.Context, p WorkerSpawnParams) (WorkerSpawnResult, error) {
	res, err := a.c.SpawnWorker(ctx, spawner.SpawnWorkerParams{
		Project:       p.Project,
		AgentType:     p.AgentType,
		WorkerID:      p.WorkerID,
		Branch:        p.Branch,
		Labels:        p.Labels,
		Image:         p.Image,
		Env:           p.Env,
		BareRepoMount: p.BareRepoMount,
	})
	if err != nil {
		return WorkerSpawnResult{}, err
	}
	return WorkerSpawnResult{ContainerID: res.ContainerID, Endpoint: res.Endpoint}, nil
}

func (a *rpcAdapter) DestroyWorker(ctx context.Context, containerID string) error {
	return a.c.DestroyWorker(ctx, spawner.DestroyWorkerParams{ContainerID: containerID})
}

// TestManager_Spawn_Integration wires a real spawner.Service against a
// FakeRuntime over a temp Unix socket and drives Manager.Spawn through
// the RPC client end-to-end. It proves that the params Manager builds
// survive the JSON-RPC round trip and land on the runtime's Spawn call
// with the expected labels and image.
func TestManager_Spawn_Integration(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "spawner.sock")

	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	defer ln.Close()

	rt := container.NewFakeRuntime()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serveDone := make(chan error, 1)
	go func() {
		serveDone <- spawner.Serve(ctx, ln, rt)
	}()

	// Give Serve a moment to enter Accept; it's a benign sync against
	// goroutine scheduling rather than a real wait.
	time.Sleep(10 * time.Millisecond)

	db := testutil.NewTestDB(t)
	client := spawner.NewClientWithTimeout(sock, 5*time.Second)
	m := NewManager(db, &rpcAdapter{c: client}, &ImageResolver{Language: "go"}, "")

	h, err := m.Spawn(ctx, SpawnRequest{
		Project:   "demo",
		AgentType: "coder",
		AgentID:   "agent-int-1",
		Branch:    "master",
	})
	if err != nil {
		t.Fatalf("Spawn over RPC: %v", err)
	}
	if h.ContainerID == "" {
		t.Errorf("Handle.ContainerID is empty")
	}
	if h.Image != "localhost:5000/drem-worker-go:latest" {
		t.Errorf("Handle.Image = %q, want worker-go:latest", h.Image)
	}

	// The runtime saw exactly one Spawn call with the right image and
	// identifying labels.
	var spawnCalls int
	for _, c := range rt.Calls() {
		if c.Op != "Spawn" || c.Spec == nil {
			continue
		}
		spawnCalls++
		if c.Spec.Image != "localhost:5000/drem-worker-go:latest" {
			t.Errorf("fake runtime Spec.Image = %q, want worker-go:latest", c.Spec.Image)
		}
		if c.Spec.Labels["drem.project"] != "demo" {
			t.Errorf("fake runtime Spec.Labels[drem.project] = %q, want demo", c.Spec.Labels["drem.project"])
		}
		if c.Spec.Labels["drem.agent_type"] != "coder" {
			t.Errorf("fake runtime Spec.Labels[drem.agent_type] = %q, want coder", c.Spec.Labels["drem.agent_type"])
		}
		if c.Spec.Labels["drem.worker_id"] != "agent-int-1" {
			t.Errorf("fake runtime Spec.Labels[drem.worker_id] = %q, want agent-int-1", c.Spec.Labels["drem.worker_id"])
		}
	}
	if spawnCalls != 1 {
		t.Errorf("fake runtime Spawn calls = %d, want 1", spawnCalls)
	}

	// Teardown round trip: Destroy is routed through the service.
	if err := m.Teardown(ctx, h); err != nil {
		t.Fatalf("Teardown over RPC: %v", err)
	}
	var destroyCalls int
	for _, c := range rt.Calls() {
		if c.Op == "Destroy" {
			destroyCalls++
		}
	}
	if destroyCalls != 1 {
		t.Errorf("fake runtime Destroy calls = %d, want 1", destroyCalls)
	}
}
