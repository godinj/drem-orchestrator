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
