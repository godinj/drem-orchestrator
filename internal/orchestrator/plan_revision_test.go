package orchestrator

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/state"
)

func TestRevisePlanAtomicallyResetsReviewWithoutChangingStatus(t *testing.T) {
	orch, db := setupLifecycleTest(t)
	agentID := uuid.New()
	task := createLifecycleTask(t, db, orch.projectID, "adapter plan", model.StatusPlanReview, model.JSONField{
		"subtasks": []any{map[string]any{"title": "old"}},
	})
	require.NoError(t, db.Model(&model.Task{}).Where("id = ?", task.ID).Updates(map[string]any{
		"assigned_agent_id": agentID,
		"plan_feedback":     "old feedback",
		"tdd_exceptions":    model.JSONField{"exceptions": []any{"old"}},
		"context": model.JSONField{
			"review":                         map[string]any{"recommendation": "revise"},
			"automated_review_state_version": float64(task.StateVersion),
			"automated_review_status":        "attention_required",
			"automated_review_detail":        "missing verification",
			"unrelated":                      "preserved",
		},
	}).Error)

	revisedPlan := model.JSONField{"subtasks": []any{map[string]any{"title": "revised"}}}
	require.NoError(t, orch.RevisePlan(task.ID, task.StateVersion, revisedPlan, "codex:thread-1", "address reviewer coverage"))

	var got model.Task
	require.NoError(t, db.First(&got, "id = ?", task.ID).Error)
	require.Equal(t, model.StatusPlanReview, got.Status)
	require.Equal(t, task.StateVersion+1, got.StateVersion)
	require.Equal(t, revisedPlan, got.Plan)
	require.Nil(t, got.AssignedAgentID)
	require.Empty(t, got.PlanFeedback)
	require.Nil(t, got.TDDExceptions)
	require.Equal(t, "preserved", got.Context["unrelated"])
	for _, key := range []string{"review", "automated_review_state_version", "automated_review_status", "automated_review_detail"} {
		require.NotContains(t, got.Context, key)
	}

	var events []model.TaskEvent
	require.NoError(t, db.Where("task_id = ? AND event_type = ?", task.ID, "plan_revised").Find(&events).Error)
	require.Len(t, events, 1)
	require.Equal(t, "codex:thread-1", events[0].Actor)
	require.Equal(t, "address reviewer coverage", events[0].Details["reason"])
	require.NotEqual(t, events[0].Details["old_plan_sha256"], events[0].Details["new_plan_sha256"])

	err := orch.RevisePlan(task.ID, task.StateVersion, revisedPlan, "codex:thread-1", "stale replay")
	require.ErrorIs(t, err, state.ErrStaleTransition)
	require.NoError(t, db.Where("task_id = ? AND event_type = ?", task.ID, "plan_revised").Find(&events).Error)
	require.Len(t, events, 1)
}

func TestRevisePlanRefusesMaterializedSubtasks(t *testing.T) {
	orch, db := setupLifecycleTest(t)
	task := createLifecycleTask(t, db, orch.projectID, "already expanded", model.StatusPlanReview, model.JSONField{"subtasks": []any{}})
	child := createLifecycleTask(t, db, orch.projectID, "child", model.StatusBacklog, nil)
	require.NoError(t, db.Model(&model.Task{}).Where("id = ?", child.ID).Update("parent_task_id", task.ID).Error)

	err := orch.RevisePlan(task.ID, task.StateVersion, model.JSONField{"subtasks": []any{}}, "codex:thread-1", "unsafe")
	require.ErrorContains(t, err, "materialized subtasks")

	var got model.Task
	require.NoError(t, db.First(&got, "id = ?", task.ID).Error)
	require.Equal(t, task.StateVersion, got.StateVersion)
}

func TestRevisePlanRefusesActiveAutomatedReview(t *testing.T) {
	orch, db := setupLifecycleTest(t)
	task := createLifecycleTask(t, db, orch.projectID, "review in flight", model.StatusPlanReview, model.JSONField{"subtasks": []any{}})
	require.NoError(t, db.Model(&model.Task{}).Where("id = ?", task.ID).Update("context", model.JSONField{
		"automated_review_status": "running",
	}).Error)

	err := orch.RevisePlan(task.ID, task.StateVersion, model.JSONField{"subtasks": []any{}}, "codex:thread-1", "too early")
	require.ErrorIs(t, err, state.ErrStaleTransition)
	require.ErrorContains(t, err, "currently running")

	var got model.Task
	require.NoError(t, db.First(&got, "id = ?", task.ID).Error)
	require.Equal(t, task.StateVersion, got.StateVersion)
}
