package agent

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
)

// heartbeatInterval is the default interval between heartbeat updates.
const heartbeatInterval = 30 * time.Second

// heartbeatLoop updates the agent's HeartbeatAt field in the database at a
// regular interval until the context is cancelled. This allows the orchestrator
// to detect stale agents whose processes have died without a clean exit.
func (r *Runner) heartbeatLoop(ctx context.Context, agentID uuid.UUID) {
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := time.Now()
			r.db.Model(&model.Agent{}).Where("id = ?", agentID).Update("heartbeat_at", now)
		}
	}
}
