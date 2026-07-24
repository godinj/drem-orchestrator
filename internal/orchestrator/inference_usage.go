package orchestrator

import (
	"time"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
)

const inferenceUsageEventType = "inference_usage"

type InferenceAttempt struct {
	Phase, Role, Provider, ModelID string
	TokensIn, TokensOut            int
	Duration                       time.Duration
	Outcome, FailureCode           string
	FinishReason                   string
	ContentBytes                   int
}

// recordInferenceUsage makes warm/direct inference visible independently of
// ephemeral agent ownership. Container workers persist the same information
// on WorkerAttempt; this event is for in-process planner/reviewer calls that do
// not have a durable container attempt.
func (o *Orchestrator) recordInferenceUsage(taskID uuid.UUID, phase, role, provider, modelID string, tokensIn, tokensOut int, duration time.Duration) error {
	return o.recordInferenceAttempt(taskID, InferenceAttempt{
		Phase: phase, Role: role, Provider: provider, ModelID: modelID,
		TokensIn: tokensIn, TokensOut: tokensOut, Duration: duration, Outcome: "completed",
	})
}

func (o *Orchestrator) recordInferenceAttempt(taskID uuid.UUID, attempt InferenceAttempt) error {
	return o.db.Create(&model.TaskEvent{
		TaskID: taskID, EventType: inferenceUsageEventType, Actor: "orchestrator",
		Details: model.JSONField{
			"phase": attempt.Phase, "role": attempt.Role, "provider": attempt.Provider, "model_id": attempt.ModelID,
			"tokens_in": attempt.TokensIn, "tokens_out": attempt.TokensOut, "duration_ms": attempt.Duration.Milliseconds(),
			"outcome": attempt.Outcome, "failure_code": attempt.FailureCode,
			"finish_reason": attempt.FinishReason, "content_bytes": attempt.ContentBytes,
		},
	}).Error
}

func (o *Orchestrator) recordReviewAttemptFailure(taskID uuid.UUID, reviewKind string, attempt InferenceAttempt, runErr error) error {
	return o.db.Create(&model.TaskEvent{
		TaskID: taskID, EventType: "review_attempt_failed", Actor: "orchestrator",
		Details: model.JSONField{
			"review_kind": reviewKind, "failure_code": attempt.FailureCode,
			"finish_reason": attempt.FinishReason, "content_bytes": attempt.ContentBytes,
			"message": runErr.Error(),
		},
	}).Error
}

func directReviewPhase(reviewKind string) string {
	if reviewKind == "tests" || reviewKind == "test" || reviewKind == "test_review" {
		return "test"
	}
	return "plan_review"
}
