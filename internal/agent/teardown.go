// teardown.go implements the counterpart to Manager.Spawn: it tells the
// spawner to destroy the worker container and records the final status
// on the matching agent row. Kept in its own file to keep spawn.go
// focused on the forward path.
package agent

import (
	"context"
	"fmt"

	"github.com/godinj/drem-orchestrator/internal/model"
)

// Teardown destroys the worker container associated with handle and
// marks the matching agent row dead. The DB update is best-effort: a
// missing row does not fail the call because operators sometimes tear
// down containers whose rows were already reaped. A spawner error does
// fail the call so callers can retry or escalate.
func (m *Manager) Teardown(ctx context.Context, handle *Handle) error {
	if handle == nil {
		return fmt.Errorf("agent manager: teardown: handle is nil")
	}
	if m.spawner == nil {
		return fmt.Errorf("agent manager: teardown: spawner is not configured")
	}
	if handle.ContainerID == "" {
		return fmt.Errorf("agent manager: teardown: handle has empty container id")
	}

	if err := m.spawner.DestroyWorker(ctx, handle.ContainerID); err != nil {
		return fmt.Errorf("agent manager: teardown: %w", err)
	}

	m.markDead(handle.AgentID)
	return nil
}

// markDead flips the agent row to status=dead. Silent when no row exists
// or the DB handle is nil; see Teardown for rationale.
func (m *Manager) markDead(agentID string) {
	if m.db == nil || agentID == "" {
		return
	}
	m.db.Model(&model.Agent{}).Where("id = ?", agentID).Update("status", model.AgentDead)
}
