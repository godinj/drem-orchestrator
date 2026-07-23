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
	require.Equal(t, "orchestrator", event.Actor)
}

func TestDirectReviewPhaseSeparatesPlanAndTestReview(t *testing.T) {
	require.Equal(t, "plan_review", directReviewPhase("plan"))
	require.Equal(t, "test", directReviewPhase("tests"))
	require.Equal(t, "test", directReviewPhase("test_review"))
}
