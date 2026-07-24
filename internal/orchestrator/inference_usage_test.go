package orchestrator

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

func TestRecordInferenceUsagePersistsCorrelatedMeasurement(t *testing.T) {
	db := testutil.NewTestDB(t)
	project := testutil.CreateProject(t, db, "usage", "/tmp/usage.git", "master")
	task := testutil.CreateTask(t, db, project.ID, "review", model.StatusPlanReview)
	orch := &Orchestrator{db: db}

	require.NoError(t, orch.recordInferenceUsage(
		task.ID, "plan_review", "reviewer", "sglang-direct", "gemma4-26b",
		1840, 103, 1500*time.Millisecond,
	))

	var event model.TaskEvent
	require.NoError(t, db.Where("task_id = ? AND event_type = ?", task.ID, inferenceUsageEventType).First(&event).Error)
	require.Equal(t, "plan_review", event.Details["phase"])
	require.EqualValues(t, 1840, event.Details["tokens_in"])
	require.EqualValues(t, 103, event.Details["tokens_out"])
	require.EqualValues(t, 1500, event.Details["duration_ms"])
	require.Equal(t, "completed", event.Details["outcome"])
	require.Equal(t, "orchestrator", event.Actor)
}

func TestRecordInferenceAttemptPersistsFailedMeasuredCompletion(t *testing.T) {
	db := testutil.NewTestDB(t)
	project := testutil.CreateProject(t, db, "failed-usage", "/tmp/failed-usage.git", "master")
	task := testutil.CreateTask(t, db, project.ID, "review", model.StatusPlanReview)
	orch := &Orchestrator{db: db}

	require.NoError(t, orch.recordInferenceAttempt(task.ID, InferenceAttempt{
		Phase: "plan_review", Role: "reviewer", Provider: "sglang-direct", ModelID: "qwen",
		TokensIn: 5406, TokensOut: 1024, Duration: 12 * time.Second,
		Outcome: "failed", FailureCode: "empty_visible_completion", FinishReason: "length",
	}))

	var event model.TaskEvent
	require.NoError(t, db.Where("task_id = ? AND event_type = ?", task.ID, inferenceUsageEventType).First(&event).Error)
	require.Equal(t, "failed", event.Details["outcome"])
	require.Equal(t, "empty_visible_completion", event.Details["failure_code"])
	require.Equal(t, "length", event.Details["finish_reason"])
	require.EqualValues(t, 5406, event.Details["tokens_in"])
	require.EqualValues(t, 1024, event.Details["tokens_out"])
}

func TestDirectReviewPhaseSeparatesPlanAndTestReview(t *testing.T) {
	require.Equal(t, "plan_review", directReviewPhase("plan"))
	require.Equal(t, "test", directReviewPhase("tests"))
	require.Equal(t, "test", directReviewPhase("test_review"))
}
