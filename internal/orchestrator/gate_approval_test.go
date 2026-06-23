package orchestrator

import (
	"testing"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

// TestHandleTestPassed_AtomicUpdate verifies that HandleTestPassed transitions
// a task from TESTING_READY to MERGING and persists a status_change event with
// the correct old/new values.
func TestHandleTestPassed_AtomicUpdate(t *testing.T) {
	db := testutil.NewSharedTestDB(t)
	wt := &FakeWorktreeManager{BarePath: "/tmp/fake", Default: "main"}
	o := testOrchestrator(t, db, wt)

	project := model.Project{ID: o.projectID, Name: "test", BareRepoPath: "/tmp/fake"}
	db.Create(&project)

	task := model.Task{
		ID:          uuid.New(),
		ProjectID:   o.projectID,
		Title:       "gate-task",
		Description: "ready for gate",
		Status:      model.StatusTestingReady,
	}
	db.Create(&task)

	if err := o.HandleTestPassed(task.ID); err != nil {
		t.Fatalf("HandleTestPassed: unexpected error: %v", err)
	}

	var updated model.Task
	if err := db.First(&updated, "id = ?", task.ID).Error; err != nil {
		t.Fatalf("load task: %v", err)
	}
	if updated.Status != model.StatusMerging {
		t.Fatalf("expected task status %q, got %q", model.StatusMerging, updated.Status)
	}

	var event model.TaskEvent
	if err := db.Where("task_id = ? AND event_type = ?", task.ID, "status_change").
		First(&event).Error; err != nil {
		t.Fatalf("load status_change event: %v", err)
	}
	if event.OldValue != string(model.StatusTestingReady) {
		t.Errorf("event.OldValue = %q, want %q", event.OldValue, model.StatusTestingReady)
	}
	if event.NewValue != string(model.StatusMerging) {
		t.Errorf("event.NewValue = %q, want %q", event.NewValue, model.StatusMerging)
	}
}

// TestHandleTestPassed_WrongStatusRejected verifies that HandleTestPassed
// refuses to transition a task that is not in TESTING_READY and leaves the
// stored status untouched.
func TestHandleTestPassed_WrongStatusRejected(t *testing.T) {
	db := testutil.NewSharedTestDB(t)
	wt := &FakeWorktreeManager{BarePath: "/tmp/fake", Default: "main"}
	o := testOrchestrator(t, db, wt)

	project := model.Project{ID: o.projectID, Name: "test", BareRepoPath: "/tmp/fake"}
	db.Create(&project)

	task := model.Task{
		ID:          uuid.New(),
		ProjectID:   o.projectID,
		Title:       "wrong-status",
		Description: "not ready for gate",
		Status:      model.StatusMerging,
	}
	db.Create(&task)

	if err := o.HandleTestPassed(task.ID); err == nil {
		t.Fatal("HandleTestPassed: expected error for non-testing_ready task, got nil")
	}

	var reloaded model.Task
	if err := db.First(&reloaded, "id = ?", task.ID).Error; err != nil {
		t.Fatalf("load task: %v", err)
	}
	if reloaded.Status != model.StatusMerging {
		t.Fatalf("expected task status to remain %q, got %q", model.StatusMerging, reloaded.Status)
	}
}

func TestRetryFailedParentThenApproveDoesNotDuplicateActiveSubtasksOrStaleSchedule(t *testing.T) {
	db := testutil.NewSharedTestDB(t)
	wt := &FakeWorktreeManager{BarePath: "/tmp/fake", Default: "main"}
	o := testOrchestrator(t, db, wt)

	project := model.Project{ID: o.projectID, Name: "test", BareRepoPath: "/tmp/fake"}
	db.Create(&project)
	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   o.projectID,
		Title:       "parent",
		Description: "parent",
		Status:      model.StatusFailed,
		Plan: model.JSONField{"subtasks": []any{
			map[string]any{"title": "implement thing", "description": "do it", "phase": "implementation", "agent_type": "coder"},
		}},
		Context: model.JSONField{"schedule": map[string]any{"stale": true}},
	}
	db.Create(&parent)
	staleAgentID := uuid.New()
	staleChild := model.Task{
		ID:              uuid.New(),
		ProjectID:       o.projectID,
		ParentTaskID:    &parentID,
		Title:           "implement thing",
		Description:     "old",
		Status:          model.StatusInProgress,
		Phase:           "implementation",
		AssignedAgentID: &staleAgentID,
	}
	db.Create(&staleChild)

	if err := o.RetryTask(parentID); err != nil {
		t.Fatalf("RetryTask: %v", err)
	}

	var retried model.Task
	db.First(&retried, "id = ?", parentID)
	if _, ok := retried.Context["schedule"]; ok {
		t.Fatalf("expected stale schedule cleared on retry")
	}
	var detached model.Task
	db.First(&detached, "id = ?", staleChild.ID)
	if detached.Status != model.StatusCancelled || detached.ParentTaskID != nil || detached.AssignedAgentID != nil {
		t.Fatalf("expected stale child cancelled, detached, and unassigned; got status=%s parent=%v assigned=%v",
			detached.Status, detached.ParentTaskID, detached.AssignedAgentID)
	}

	retried.Status = model.StatusPlanReview
	db.Save(&retried)
	if err := o.HandlePlanApproved(parentID); err != nil {
		t.Fatalf("HandlePlanApproved: %v", err)
	}

	var activeChildren []model.Task
	db.Where("parent_task_id = ? AND status <> ?", parentID, model.StatusCancelled).Find(&activeChildren)
	if len(activeChildren) != 1 {
		t.Fatalf("expected exactly one active child after retry+approve, got %d", len(activeChildren))
	}
	if activeChildren[0].ID == staleChild.ID {
		t.Fatalf("stale child was reused as active child")
	}
}
