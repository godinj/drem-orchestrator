package orchdto

import "time"

// SubmitCodexGoalUsageRequest records the final usage returned when the
// supervising Codex thread completes or blocks an explicit goal.
type SubmitCodexGoalUsageRequest struct {
	Actor           string    `json:"actor"`
	ThreadID        string    `json:"thread_id"`
	GoalObjective   string    `json:"goal_objective"`
	GoalStatus      string    `json:"goal_status"`
	TokensUsed      int64     `json:"tokens_used"`
	ElapsedMS       int64     `json:"elapsed_ms"`
	UsageCapturedAt time.Time `json:"usage_captured_at,omitempty"`
	IdempotencyKey  string    `json:"idempotency_key"`
}

type CodexGoalUsageDTO struct {
	ID              string    `json:"id"`
	TaskID          string    `json:"task_id"`
	Actor           string    `json:"actor"`
	ThreadID        string    `json:"thread_id"`
	GoalObjective   string    `json:"goal_objective"`
	GoalStatus      string    `json:"goal_status"`
	TokensUsed      int64     `json:"tokens_used"`
	ElapsedMS       int64     `json:"elapsed_ms"`
	Source          string    `json:"source"`
	UsageCapturedAt time.Time `json:"usage_captured_at"`
	CreatedAt       time.Time `json:"created_at"`
}
