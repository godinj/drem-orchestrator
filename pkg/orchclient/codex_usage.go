package orchclient

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/pkg/orchdto"
)

// SubmitCodexGoalUsage attaches final explicit-goal usage from the supervising
// Codex thread to one orchestrated task.
func (c *Client) SubmitCodexGoalUsage(ctx context.Context, project string, taskID uuid.UUID, req orchdto.SubmitCodexGoalUsageRequest) (orchdto.CodexGoalUsageDTO, error) {
	if strings.TrimSpace(req.Actor) == "" || strings.TrimSpace(req.GoalObjective) == "" ||
		strings.TrimSpace(req.GoalStatus) == "" || strings.TrimSpace(req.IdempotencyKey) == "" || req.ElapsedMS <= 0 {
		return orchdto.CodexGoalUsageDTO{}, &ErrBadRequest{Message: "Codex goal usage requires actor, objective, terminal status, positive elapsed_ms, and idempotency_key"}
	}
	var out orchdto.CodexGoalUsageDTO
	if err := c.postGate(ctx, gatePath(project, taskID, "codex-usage"), req, &out); err != nil {
		return orchdto.CodexGoalUsageDTO{}, err
	}
	return out, nil
}
