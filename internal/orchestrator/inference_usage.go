package orchestrator

import (
	"time"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
)

const inferenceUsageEventType = "inference_usage"

// recordInferenceUsage makes warm/direct inference visible independently of
// ephemeral agent ownership. Container workers persist the same information
// on WorkerAttempt; this event is for in-process planner/reviewer calls that do
// not have a durable container attempt.
func (o *Orchestrator) recordInferenceUsage(taskID uuid.UUID, phase, role, provider, modelID string, tokensIn, tokensOut int, duration time.Duration) error {
	return o.db.Create(&model.TaskEvent{
		TaskID: taskID, EventType: inferenceUsageEventType, Actor: "orchestrator",
		Details: model.JSONField{
			"phase": phase, "role": role, "provider": provider, "model_id": modelID,
			"tokens_in": tokensIn, "tokens_out": tokensOut, "duration_ms": duration.Milliseconds(),
		},
	}).Error
}

func directReviewPhase(reviewKind string) string {
	if reviewKind == "tests" || reviewKind == "test" || reviewKind == "test_review" {
		return "test"
	}
	return "plan_review"
}
