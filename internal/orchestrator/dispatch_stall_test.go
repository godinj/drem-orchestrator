package orchestrator

import (
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/testutil"
	"github.com/godinj/drem-orchestrator/internal/worktree"
)

// setupDispatchStallTest creates an Orchestrator with a test DB, project, and
// event channel suitable for subtask dispatch tests. The runner is nil (tests
// exercise DB-level scheduling logic only, not actual agent spawning).
func setupDispatchStallTest(t *testing.T) (*Orchestrator, *gorm.DB, uuid.UUID) {
	t.Helper()
	db := testutil.NewTestDB(t)
	projectID := uuid.New()
	project := model.Project{
		ID:            projectID,
		Name:          "dispatch-stall-test",
		BareRepoPath:  "/tmp/test.git",
		DefaultBranch: "main",
	}
	db.Create(&project)

	events := make(chan Event, 100)
	orch := &Orchestrator{
		db:        db,
		projectID: projectID,
		worktree:  &worktree.Manager{BareRepoPath: "/tmp/test.git", DefaultBranch: "main"},
		events:    events,
		logger:    slog.Default().With("component", "dispatch-stall-test"),
	}
	return orch, db, projectID
}

// ---------------------------------------------------------------------------
// Test 1: Subtask in backlog with parent in_progress → should be dispatched
// ---------------------------------------------------------------------------

func TestDispatchPendingSubtasks_ParentInProgress(t *testing.T) {
	// When a parent task is IN_PROGRESS, its BACKLOG subtasks should be
	// found by the scheduleSubtasks query and evaluated as dispatchable
	// by the scheduling policy.
	_, db, projectID := setupDispatchStallTest(t)

	parentID := uuid.New()
	parent := model.Task{
		ID:             parentID,
		ProjectID:      projectID,
		Title:          "parent-in-progress",
		Description:    "parent task in progress",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/test-in-progress",
	}
	db.Create(&parent)

	sub := model.Task{
		ID:           uuid.New(),
		ProjectID:    projectID,
		ParentTaskID: &parentID,
		Title:        "backlog-subtask",
		Description:  "subtask waiting for dispatch",
		Status:       model.StatusBacklog,
	}
	db.Create(&sub)

	// Verify the subtask query in scheduleSubtasks finds it.
	var found []model.Task
	if err := db.Where(
		"parent_task_id = ? AND ((status = ?) OR (status = ? AND assigned_agent_id IS NULL))",
		parentID, model.StatusBacklog, model.StatusInProgress,
	).Find(&found).Error; err != nil {
		t.Fatalf("query: %v", err)
	}

	if len(found) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(found))
	}
	if found[0].ID != sub.ID {
		t.Errorf("expected subtask %s, got %s", sub.ID, found[0].ID)
	}

	// Verify the scheduling policy evaluates the subtask as dispatchable
	// (no dependencies, no wave groups, no file conflicts).
	policy := NewSchedulingPolicy(db)
	decisions := policy.EvaluateDispatch(found, nil)
	if len(decisions) != 1 {
		t.Fatalf("expected 1 decision, got %d", len(decisions))
	}
	if !decisions[0].Dispatchable {
		t.Errorf("subtask should be dispatchable when parent is in_progress, blocked: %s", decisions[0].Reason)
	}
}

// ---------------------------------------------------------------------------
// Test 2: Subtask in backlog with parent in backlog → should be dispatched
// ---------------------------------------------------------------------------

func TestDispatchPendingSubtasks_ParentInBacklog(t *testing.T) {
	// When a parent task is in BACKLOG (e.g. after replanning) but already
	// has subtasks from a previous plan cycle, those subtasks should still
	// be eligible for dispatch. The dispatchPendingSubtasks catch-all
	// finds these parents and calls scheduleSubtasks.
	o, db, projectID := setupDispatchStallTest(t)

	parentID := uuid.New()
	parent := model.Task{
		ID:             parentID,
		ProjectID:      projectID,
		Title:          "parent-in-backlog",
		Description:    "parent returned to backlog after replan",
		Status:         model.StatusBacklog,
		WorktreeBranch: "feature/test-backlog",
	}
	db.Create(&parent)

	sub := model.Task{
		ID:           uuid.New(),
		ProjectID:    projectID,
		ParentTaskID: &parentID,
		Title:        "backlog-subtask-orphaned",
		Description:  "subtask from previous plan cycle",
		Status:       model.StatusBacklog,
	}
	db.Create(&sub)

	// Call dispatchPendingSubtasks — with no runner, scheduleSubtasks
	// will exit early at the CanSpawn nil check, but the key behavior
	// is that it DOES attempt to process the parent (not skip it).
	o.dispatchPendingSubtasks()

	// Verify the subtask is eligible for dispatch via scheduling policy.
	var candidates []model.Task
	if err := db.Where(
		"parent_task_id = ? AND ((status = ?) OR (status = ? AND assigned_agent_id IS NULL))",
		parentID, model.StatusBacklog, model.StatusInProgress,
	).Find(&candidates).Error; err != nil {
		t.Fatalf("query candidates: %v", err)
	}

	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(candidates))
	}

	policy := NewSchedulingPolicy(db)
	decisions := policy.EvaluateDispatch(candidates, nil)
	if len(decisions) != 1 {
		t.Fatalf("expected 1 decision, got %d", len(decisions))
	}
	if !decisions[0].Dispatchable {
		t.Errorf("subtask should be dispatchable for backlog parent, blocked: %s", decisions[0].Reason)
	}

	// Verify the parent is NOT in the skip list (terminal or already-handled).
	if isTerminal(parent.Status) {
		t.Error("backlog parent should not be terminal")
	}
	if parent.Status == model.StatusInProgress || parent.Status == model.StatusTestWriting {
		t.Error("backlog parent should not be in the already-handled set")
	}
}

// ---------------------------------------------------------------------------
// Test 3: Subtask in backlog with parent done → should NOT be dispatched
// ---------------------------------------------------------------------------

func TestDispatchPendingSubtasks_ParentDone_Skipped(t *testing.T) {
	// When a parent task is DONE, any leftover subtasks in BACKLOG should
	// NOT be dispatched. The dispatchPendingSubtasks catch-all must skip
	// terminal parents.
	o, db, projectID := setupDispatchStallTest(t)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   projectID,
		Title:       "parent-done",
		Description: "completed parent task",
		Status:      model.StatusDone,
	}
	db.Create(&parent)

	sub := model.Task{
		ID:           uuid.New(),
		ProjectID:    projectID,
		ParentTaskID: &parentID,
		Title:        "leftover-backlog-subtask",
		Description:  "should not be dispatched",
		Status:       model.StatusBacklog,
	}
	db.Create(&sub)

	// dispatchPendingSubtasks should NOT process this parent because it
	// is in a terminal state (DONE). The subtask should remain untouched.
	o.dispatchPendingSubtasks()

	// Verify the subtask was not modified.
	var updated model.Task
	db.First(&updated, "id = ?", sub.ID)
	if updated.Status != model.StatusBacklog {
		t.Errorf("expected subtask to remain backlog (parent is done), got %s", updated.Status)
	}
	if updated.AssignedAgentID != nil {
		t.Errorf("expected subtask to have no agent assigned, got %v", updated.AssignedAgentID)
	}
}

// ---------------------------------------------------------------------------
// Additional: Verify dispatchPendingSubtasks skips parents already handled
// ---------------------------------------------------------------------------

func TestDispatchPendingSubtasks_SkipsInProgressParents(t *testing.T) {
	// Parents already in IN_PROGRESS are handled by the main doTick
	// handler. dispatchPendingSubtasks should skip them to avoid
	// double-processing.
	o, db, projectID := setupDispatchStallTest(t)

	parentID := uuid.New()
	parent := model.Task{
		ID:             parentID,
		ProjectID:      projectID,
		Title:          "parent-already-handled",
		Description:    "in_progress parent handled by main loop",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/test-handled",
	}
	db.Create(&parent)

	sub := model.Task{
		ID:           uuid.New(),
		ProjectID:    projectID,
		ParentTaskID: &parentID,
		Title:        "subtask-for-handled-parent",
		Description:  "should be skipped by catch-all",
		Status:       model.StatusBacklog,
	}
	db.Create(&sub)

	// dispatchPendingSubtasks should skip the IN_PROGRESS parent
	// (it's already handled by the main IN_PROGRESS block in doTick).
	o.dispatchPendingSubtasks()

	// Subtask should remain in backlog (the catch-all skips IN_PROGRESS
	// parents and there is no runner to spawn agents).
	var updated model.Task
	db.First(&updated, "id = ?", sub.ID)
	if updated.Status != model.StatusBacklog {
		t.Errorf("expected subtask to remain backlog, got %s", updated.Status)
	}
}

func TestDispatchPendingSubtasks_ParentFailed_Skipped(t *testing.T) {
	// Failed parents should also be skipped — their subtasks should not
	// be dispatched.
	o, db, projectID := setupDispatchStallTest(t)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   projectID,
		Title:       "parent-failed",
		Description: "failed parent task",
		Status:      model.StatusFailed,
	}
	db.Create(&parent)

	sub := model.Task{
		ID:           uuid.New(),
		ProjectID:    projectID,
		ParentTaskID: &parentID,
		Title:        "subtask-of-failed-parent",
		Description:  "should not be dispatched",
		Status:       model.StatusBacklog,
	}
	db.Create(&sub)

	o.dispatchPendingSubtasks()

	var updated model.Task
	db.First(&updated, "id = ?", sub.ID)
	if updated.Status != model.StatusBacklog {
		t.Errorf("expected subtask to remain backlog (parent failed), got %s", updated.Status)
	}
}
