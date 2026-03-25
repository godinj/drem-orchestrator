package orchestrator

import (
	"testing"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
)

// ---------------------------------------------------------------------------
// reconcileFailedParents
// ---------------------------------------------------------------------------

func TestReconcileFailedParents_AllSubtasksDone(t *testing.T) {
	orch, db, _ := setupReconcileTest(t)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   orch.projectID,
		Title:       "failed-parent-all-done",
		Description: "parent failed due to merge conflict, but all subtasks eventually completed",
		Status:      model.StatusFailed,
	}
	db.Create(&parent)

	// Three DONE subtasks — mirrors the real bug scenario (3/3 done, parent stuck failed).
	for i := 0; i < 3; i++ {
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

	n, err := orch.reconcileFailedParents()
	if err != nil {
		t.Fatalf("reconcileFailedParents() error: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 parent recovered, got %d", n)
	}

	// Parent should no longer be failed — it transitions to in_progress first,
	// then checkFeatureCompletion decides the final status.
	var updated model.Task
	db.First(&updated, "id = ?", parentID)
	if updated.Status == model.StatusFailed {
		t.Errorf("parent should have been recovered from failed, still %s", updated.Status)
	}
}

func TestReconcileFailedParents_SubtasksNotAllDone(t *testing.T) {
	orch, db, _ := setupReconcileTest(t)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   orch.projectID,
		Title:       "failed-parent-mixed",
		Description: "parent failed, subtasks still in mixed statuses",
		Status:      model.StatusFailed,
	}
	db.Create(&parent)

	// One DONE, one still IN_PROGRESS — not ready for recovery.
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

	n, err := orch.reconcileFailedParents()
	if err != nil {
		t.Fatalf("reconcileFailedParents() error: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 parents recovered (not all done), got %d", n)
	}

	// Parent should remain failed.
	var updated model.Task
	db.First(&updated, "id = ?", parentID)
	if updated.Status != model.StatusFailed {
		t.Errorf("expected parent status failed, got %s", updated.Status)
	}
}

func TestReconcileFailedParents_NoSubtasks(t *testing.T) {
	orch, db, _ := setupReconcileTest(t)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   orch.projectID,
		Title:       "failed-parent-no-subs",
		Description: "failed parent with no subtasks should not be recovered",
		Status:      model.StatusFailed,
	}
	db.Create(&parent)

	n, err := orch.reconcileFailedParents()
	if err != nil {
		t.Fatalf("reconcileFailedParents() error: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 parents recovered (no subtasks), got %d", n)
	}
}

func TestReconcileFailedParents_OnlyFailedParentsTouched(t *testing.T) {
	orch, db, _ := setupReconcileTest(t)

	// An in_progress parent with all subtasks done should NOT be touched
	// by reconcileFailedParents (that's reconcileCompletedParents' job).
	ipParentID := uuid.New()
	ipParent := model.Task{
		ID:          ipParentID,
		ProjectID:   orch.projectID,
		Title:       "ip-parent",
		Description: "in-progress parent, not failed",
		Status:      model.StatusInProgress,
	}
	db.Create(&ipParent)

	sub := model.Task{
		ID:           uuid.New(),
		ProjectID:    orch.projectID,
		ParentTaskID: &ipParentID,
		Title:        "done-sub",
		Description:  "done",
		Status:       model.StatusDone,
	}
	db.Create(&sub)

	n, err := orch.reconcileFailedParents()
	if err != nil {
		t.Fatalf("reconcileFailedParents() error: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 parents recovered (parent is in_progress, not failed), got %d", n)
	}
}

func TestReconcileFailedParents_SubtaskWithFailedStatus(t *testing.T) {
	orch, db, _ := setupReconcileTest(t)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   orch.projectID,
		Title:       "failed-parent-failed-sub",
		Description: "parent and one subtask both failed — should not recover",
		Status:      model.StatusFailed,
	}
	db.Create(&parent)

	sub1 := model.Task{
		ID:           uuid.New(),
		ProjectID:    orch.projectID,
		ParentTaskID: &parentID,
		Title:        "done-sub",
		Description:  "completed",
		Status:       model.StatusDone,
	}
	db.Create(&sub1)

	sub2 := model.Task{
		ID:           uuid.New(),
		ProjectID:    orch.projectID,
		ParentTaskID: &parentID,
		Title:        "failed-sub",
		Description:  "also failed",
		Status:       model.StatusFailed,
	}
	db.Create(&sub2)

	n, err := orch.reconcileFailedParents()
	if err != nil {
		t.Fatalf("reconcileFailedParents() error: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 parents recovered (subtask still failed), got %d", n)
	}
}

func TestReconcileFailedParents_MultipleParentsMixedEligibility(t *testing.T) {
	orch, db, _ := setupReconcileTest(t)

	// Parent 1: failed, all subtasks done — eligible.
	p1ID := uuid.New()
	p1 := model.Task{
		ID:          p1ID,
		ProjectID:   orch.projectID,
		Title:       "eligible-parent",
		Description: "should be recovered",
		Status:      model.StatusFailed,
	}
	db.Create(&p1)
	for i := 0; i < 2; i++ {
		db.Create(&model.Task{
			ID:           uuid.New(),
			ProjectID:    orch.projectID,
			ParentTaskID: &p1ID,
			Title:        "done-sub",
			Description:  "done",
			Status:       model.StatusDone,
		})
	}

	// Parent 2: failed, subtasks not all done — not eligible.
	p2ID := uuid.New()
	p2 := model.Task{
		ID:          p2ID,
		ProjectID:   orch.projectID,
		Title:       "ineligible-parent",
		Description: "should not be recovered",
		Status:      model.StatusFailed,
	}
	db.Create(&p2)
	db.Create(&model.Task{
		ID:           uuid.New(),
		ProjectID:    orch.projectID,
		ParentTaskID: &p2ID,
		Title:        "done-sub",
		Description:  "done",
		Status:       model.StatusDone,
	})
	db.Create(&model.Task{
		ID:           uuid.New(),
		ProjectID:    orch.projectID,
		ParentTaskID: &p2ID,
		Title:        "backlog-sub",
		Description:  "not started",
		Status:       model.StatusBacklog,
	})

	n, err := orch.reconcileFailedParents()
	if err != nil {
		t.Fatalf("reconcileFailedParents() error: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 parent recovered (only the eligible one), got %d", n)
	}

	// Verify: p1 recovered, p2 still failed.
	var updatedP1, updatedP2 model.Task
	db.First(&updatedP1, "id = ?", p1ID)
	db.First(&updatedP2, "id = ?", p2ID)
	if updatedP1.Status == model.StatusFailed {
		t.Errorf("eligible parent should have been recovered, still failed")
	}
	if updatedP2.Status != model.StatusFailed {
		t.Errorf("ineligible parent should remain failed, got %s", updatedP2.Status)
	}
}

func TestReconcileFailedParents_SkipsSubtasks(t *testing.T) {
	orch, db, _ := setupReconcileTest(t)

	// A failed subtask (has ParentTaskID) whose sibling is done — the
	// function should only look at parent tasks (ParentTaskID IS NULL).
	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   orch.projectID,
		Title:       "parent",
		Description: "in-progress parent",
		Status:      model.StatusInProgress,
	}
	db.Create(&parent)

	// This is a failed subtask, not a parent — should be ignored.
	failedSubID := uuid.New()
	failedSub := model.Task{
		ID:           failedSubID,
		ProjectID:    orch.projectID,
		ParentTaskID: &parentID,
		Title:        "failed-subtask",
		Description:  "a subtask that failed",
		Status:       model.StatusFailed,
	}
	db.Create(&failedSub)

	n, err := orch.reconcileFailedParents()
	if err != nil {
		t.Fatalf("reconcileFailedParents() error: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 (failed subtask should not be treated as parent), got %d", n)
	}
}

func TestReconcile_IncludesFailedParents(t *testing.T) {
	orch, db, _ := setupReconcileTest(t)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   orch.projectID,
		Title:       "failed-parent-via-reconcile",
		Description: "failed parent checked by top-level Reconcile",
		Status:      model.StatusFailed,
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
		t.Errorf("expected at least 1 fix from failed parents, got %d", fixes)
	}
}
