// attach.go implements container-mode attach semantics for agent.Manager. The
// shape mirrors the old tmux.Manager.Attach/ErrDashboardRespawned surface so
// callers that previously attached to a tmux session can continue to reason
// about attach-on-handle and respawn-on-dashboard-exit in one place, just
// against container IDs instead of tmux session names.
//
// Attach itself is implemented as a thin Inspect-style probe on the configured
// Spawner — it verifies the container is still live and returns an error when
// it is not. The CLI that wants an interactive shell wraps this with an
// explicit `docker exec` (since the spawner service owns the docker socket,
// not the orchestrator). Putting the probe here keeps the state machine in
// one package without bleeding docker into internal/agent/.
package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/godinj/drem-orchestrator/internal/model"
)

// ErrDashboardRespawned is returned by RespawnIfDashboardExited when the
// caller's dashboard pane / session was observed to have exited and a fresh
// worker was spawned to replace it. Callers should re-attach to the new
// Handle (or re-read it from the DB) instead of continuing with the stale
// one. The sentinel mirrors tmux.ErrDashboardRespawned so existing callers
// migrating off tmux can keep their switch-on-error logic.
var ErrDashboardRespawned = errors.New("dashboard respawned; re-attach to new agent session")

// AttachProbe is the subset of Spawner used by Attach to verify a container is
// still live. Declared here (rather than extending Spawner itself) so
// FakeSpawner implementations in tests can opt in to attach behaviour without
// having to implement Inspect.
type AttachProbe interface {
	// Inspect returns the current runtime status of the named container.
	// Implementations should treat a missing container as a non-error with
	// Status="" / Alive=false so Attach can surface "not found" uniformly.
	Inspect(ctx context.Context, containerID string) (AttachStatus, error)
}

// AttachStatus is the minimal snapshot Attach needs to decide whether the
// target container is still live. It is intentionally narrower than the
// spawner's full InspectWorkerResult so internal/agent/ does not have to
// import internal/spawner.
type AttachStatus struct {
	// Alive indicates the container exists and its main process is still
	// running (not exited, dead, or removed).
	Alive bool
	// Status carries the underlying runtime's status string for logging
	// (e.g. "running", "exited"). Not interpreted by Attach.
	Status string
}

// Attach verifies that the agent identified by agentID has an associated live
// worker container, using AttachProbe when one is configured on the Manager.
// It returns the Handle recorded on the agent row so the caller can drive a
// `docker exec` shell or an HTTP shell endpoint. When no probe is configured,
// Attach still returns the handle — absence of a probe means the caller opted
// out of liveness verification and accepts the risk of attaching to a dead
// container.
//
// Attach does not itself spawn a replacement; that belongs to
// RespawnIfDashboardExited so the two concerns stay separable.
func (m *Manager) Attach(ctx context.Context, agentID string) (*Handle, error) {
	if agentID == "" {
		return nil, fmt.Errorf("agent manager: attach: agent id is required")
	}
	h, err := m.loadHandle(agentID)
	if err != nil {
		return nil, fmt.Errorf("agent manager: attach: %w", err)
	}

	if m.probe == nil {
		return h, nil
	}
	st, err := m.probe.Inspect(ctx, h.ContainerID)
	if err != nil {
		return nil, fmt.Errorf("agent manager: attach: inspect container %s: %w", h.ContainerID, err)
	}
	if !st.Alive {
		return h, fmt.Errorf("agent manager: attach: container %s is not live (status=%q)", h.ContainerID, st.Status)
	}
	return h, nil
}

// RespawnIfDashboardExited inspects the handle's container. If the container
// has exited (the container-mode equivalent of the old tmux dashboard pane
// dying), it tears the old handle down and returns a fresh handle spawned via
// the supplied SpawnRequest, along with ErrDashboardRespawned so the caller
// can short-circuit their attach path. When the container is still live, the
// original handle is returned and error is nil.
//
// The split between "inspect + respawn" and "attach" keeps the tmux-era
// "dashboard respawned, re-attach" semantics available without coupling
// attach to spawn inside a single method.
func (m *Manager) RespawnIfDashboardExited(ctx context.Context, h *Handle, req SpawnRequest) (*Handle, error) {
	if h == nil {
		return nil, fmt.Errorf("agent manager: respawn: handle is nil")
	}
	if m.probe == nil {
		// No probe configured — cannot detect exit, preserve handle.
		return h, nil
	}

	st, err := m.probe.Inspect(ctx, h.ContainerID)
	if err != nil {
		return nil, fmt.Errorf("agent manager: respawn: inspect container %s: %w", h.ContainerID, err)
	}
	if st.Alive {
		return h, nil
	}

	// Old container is gone. Tear down best-effort then spawn a replacement.
	if tdErr := m.Teardown(ctx, h); tdErr != nil {
		// Teardown failure on an already-dead container is expected; log by
		// returning the combined error so the caller sees both signals.
		m.logger().Warn("agent manager: teardown during respawn reported error", "error", tdErr)
	}

	fresh, err := m.Spawn(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("agent manager: respawn: spawn replacement: %w", err)
	}
	return fresh, ErrDashboardRespawned
}

// loadHandle reconstructs a Handle from the agent row's Config map, which is
// where Manager.Spawn stores the container_id and container_image. The
// Handle's StartedAt is approximated from the agent's CreatedAt if present;
// otherwise it is the current time (the exact wall clock is not load-bearing
// for attach).
func (m *Manager) loadHandle(agentID string) (*Handle, error) {
	if m.db == nil {
		return nil, fmt.Errorf("no DB configured")
	}
	var ag model.Agent
	if err := m.db.Select("id", "created_at", "config").
		Where("id = ?", agentID).
		Take(&ag).Error; err != nil {
		return nil, fmt.Errorf("load agent %s: %w", agentID, err)
	}

	containerID, _ := ag.Config["container_id"].(string)
	image, _ := ag.Config["container_image"].(string)
	if containerID == "" {
		return nil, fmt.Errorf("agent %s has no container_id recorded", agentID)
	}
	started := ag.CreatedAt
	if started.IsZero() {
		started = m.now()
	}
	return &Handle{
		ContainerID: containerID,
		AgentID:     agentID,
		StartedAt:   started,
		Image:       image,
	}, nil
}
