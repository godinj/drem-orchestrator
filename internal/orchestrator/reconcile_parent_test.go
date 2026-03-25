package orchestrator

import (
	"testing"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
)

// ---------------------------------------------------------------------------
// reconcileCompletedParents
// ---------------------------------------------------------------------------

func TestReconcileCompletedParents_AllSubtasksDone(t *testing.T) {
	orch, db, _ := setupReconcileTest(t)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   orch.projectID,
		Title:       "parent-all-done",
		Description: "parent whose subtasks are all done",
		Status:      model.StatusInProgress,
	}
	db.Create(&parent)

	// Create two DONE subtasks.
	for i := 0; i < 2; i++ {
		sub := model.Task{
			ID:           uuid.New(),
			ProjectID:    orch.projectID,
			ParentTaskID: &parentID,
			Title:        "done-sub",
			Description:  "completed subtask",
			Status:       model.StatusDone,
		}
		db.Create(&sub)
	}

	n, err := orch.reconcileCompletedParents()
	if err != nil {
		t.Fatalf("reconcileCompletedParents() error: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 parent advanced, got %d", n)
	}

	// Parent should no longer be in_progress — checkFeatureCompletion
	// will decide the exact target status.
	var updated model.Task
	db.First(&updated, "id = ?", parentID)
	if updated.Status == model.StatusInProgress {
		t.Errorf("parent should have been advanced from in_progress, still %s", updated.Status)
	}
}

func TestReconcileCompletedParents_SubtasksNotAllDone(t *testing.T) {
	orch, db, _ := setupReconcileTest(t)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   orch.projectID,
		Title:       "parent-mixed",
		Description: "parent with mixed subtask statuses",
		Status:      model.StatusInProgress,
	}
	db.Create(&parent)

	// One DONE, one still IN_PROGRESS.
	sub1 := model.Task{
		ID:           uuid.New(),
		ProjectID:    orch.projectID,
		ParentTaskID: &parentID,
		Title:        "done-sub",
		Description:  "completed subtask",
		Status:       model.StatusDone,
	}
	db.Create(&sub1)

	sub2 := model.Task{
		ID:           uuid.New(),
		ProjectID:    orch.projectID,
		ParentTaskID: &parentID,
		Title:        "wip-sub",
		Description:  "still working",
		Status:       model.StatusInProgress,
	}
	db.Create(&sub2)

	n, err := orch.reconcileCompletedParents()
	if err != nil {
		t.Fatalf("reconcileCompletedParents() error: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 parents advanced (not all done), got %d", n)
	}

	// Parent should remain in_progress.
	var updated model.Task
	db.First(&updated, "id = ?", parentID)
	if updated.Status != model.StatusInProgress {
		t.Errorf("expected parent status in_progress, got %s", updated.Status)
	}
}

func TestReconcileCompletedParents_NoSubtasks(t *testing.T) {
	orch, db, _ := setupReconcileTest(t)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   orch.projectID,
		Title:       "parent-no-subs",
		Description: "parent with no subtasks",
		Status:      model.StatusInProgress,
	}
	db.Create(&parent)

	n, err := orch.reconcileCompletedParents()
	if err != nil {
		t.Fatalf("reconcileCompletedParents() error: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 parents advanced (no subtasks), got %d", n)
	}
}

func TestReconcileCompletedParents_AlreadyTestingReady(t *testing.T) {
	orch, db, _ := setupReconcileTest(t)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   orch.projectID,
		Title:       "parent-testing-ready",
		Description: "already advanced",
		Status:      model.StatusTestingReady,
	}
	db.Create(&parent)

	sub := model.Task{
		ID:           uuid.New(),
		ProjectID:    orch.projectID,
		ParentTaskID: &parentID,
		Title:        "done-sub",
		Description:  "done",
		Status:       model.StatusDone,
	}
	db.Create(&sub)

	n, err := orch.reconcileCompletedParents()
	if err != nil {
		t.Fatalf("reconcileCompletedParents() error: %v", err)
	}
	// Parent is not in_progress, so it should not be touched.
	if n != 0 {
		t.Errorf("expected 0 parents advanced (not in_progress), got %d", n)
	}
}

func TestReconcile_IncludesCompletedParents(t *testing.T) {
	orch, db, _ := setupReconcileTest(t)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   orch.projectID,
		Title:       "parent-via-reconcile",
		Description: "parent checked by top-level Reconcile",
		Status:      model.StatusInProgress,
	}
	db.Create(&parent)

	sub := model.Task{
		ID:           uuid.New(),
		ProjectID:    orch.projectID,
		ParentTaskID: &parentID,
		Title:        "done-sub",
		Description:  "done subtask",
		Status:       model.StatusDone,
	}
	db.Create(&sub)

	fixes, err := orch.Reconcile()
	if err != nil {
		t.Fatalf("Reconcile() error: %v", err)
	}
	if fixes < 1 {
		t.Errorf("expected at least 1 fix from completed parents, got %d", fixes)
	}
}
