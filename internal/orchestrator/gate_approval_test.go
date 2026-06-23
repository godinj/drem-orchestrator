package orchestrator

import (
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/state"
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

func TestRetryFailedParentWithChildrenIsRefusedWithoutMutation(t *testing.T) {
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

	if err := o.RetryTask(parentID); err == nil {
		t.Fatal("RetryTask: expected error for parent with child history, got nil")
	} else if !errors.Is(err, ErrRetryParentHasChildren) {
		t.Fatalf("RetryTask: expected ErrRetryParentHasChildren, got %v", err)
	}

	var reloadedParent model.Task
	db.First(&reloadedParent, "id = ?", parentID)
	if reloadedParent.Status != model.StatusFailed {
		t.Fatalf("expected parent to remain failed, got %s", reloadedParent.Status)
	}
	if _, ok := reloadedParent.Context["schedule"]; !ok {
		t.Fatalf("expected stale schedule preserved when retry is refused")
	}
	var reloadedChild model.Task
	db.First(&reloadedChild, "id = ?", staleChild.ID)
	if reloadedChild.Status != model.StatusInProgress || reloadedChild.ParentTaskID == nil || *reloadedChild.ParentTaskID != parentID || reloadedChild.AssignedAgentID == nil {
		t.Fatalf("expected stale child preserved; got status=%s parent=%v assigned=%v",
			reloadedChild.Status, reloadedChild.ParentTaskID, reloadedChild.AssignedAgentID)
	}
}

func TestHandlePlanApprovedConcurrentDoubleApproveExactlyOnce(t *testing.T) {
	db := testutil.NewTestDBFileWAL(t)
	requireMigrateCore(t, db)
	wt := &FakeWorktreeManager{BarePath: "/tmp/fake", Default: "main"}
	o := testOrchestrator(t, db, wt)

	project := model.Project{ID: o.projectID, Name: "test", BareRepoPath: "/tmp/fake"}
	db.Create(&project)

	task := model.Task{
		ID:          uuid.New(),
		ProjectID:   o.projectID,
		Title:       "plan-review",
		Description: "approve once",
		Status:      model.StatusPlanReview,
		Plan: model.JSONField{"subtasks": []any{
			map[string]any{"title": "write tests", "description": "tests", "phase": "test", "agent_type": "coder"},
			map[string]any{"title": "implement", "description": "impl", "phase": "implementation", "agent_type": "coder", "dependencies": []any{0}},
		}},
	}
	db.Create(&task)

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- o.HandlePlanApproved(task.ID)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	successes := 0
	stale := 0
	for err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, state.ErrStaleTransition):
			stale++
		default:
			t.Fatalf("HandlePlanApproved unexpected error: %v", err)
		}
	}
	if successes != 1 || stale != 1 {
		t.Fatalf("expected one success and one stale error, got successes=%d stale=%d", successes, stale)
	}

	var reloaded model.Task
	if err := db.First(&reloaded, "id = ?", task.ID).Error; err != nil {
		t.Fatalf("load task: %v", err)
	}
	if reloaded.Status != model.StatusTestWriting {
		t.Fatalf("expected task status %q, got %q", model.StatusTestWriting, reloaded.Status)
	}

	var childCount int64
	if err := db.Model(&model.Task{}).Where("parent_task_id = ?", task.ID).Count(&childCount).Error; err != nil {
		t.Fatalf("count children: %v", err)
	}
	if childCount != 2 {
		t.Fatalf("expected exactly 2 children, got %d", childCount)
	}

	var eventCount int64
	if err := db.Model(&model.TaskEvent{}).
		Where("task_id = ? AND event_type = ? AND old_value = ?", task.ID, "status_change", string(model.StatusPlanReview)).
		Count(&eventCount).Error; err != nil {
		t.Fatalf("count status events: %v", err)
	}
	if eventCount != 1 {
		t.Fatalf("expected exactly one plan_review status event, got %d", eventCount)
	}
}

func requireMigrateCore(t *testing.T, db interface{ AutoMigrate(...any) error }) {
	t.Helper()
	if err := db.AutoMigrate(
		&model.Project{},
		&model.Task{},
		&model.Agent{},
		&model.TaskEvent{},
		&model.Memory{},
		&model.TaskComment{},
	); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}
}
