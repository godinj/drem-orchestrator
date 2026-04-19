package orchestrator

import (
	"testing"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

// ---------------------------------------------------------------------------
// HandleTestReviewApproved tests
// ---------------------------------------------------------------------------

func TestHandleTestReviewApproved_HappyPath(t *testing.T) {
	db := testutil.NewSharedTestDB(t)
	wt := &FakeWorktreeManager{BarePath: "/tmp/fake", Default: "main"}
	o := testOrchestrator(t, db, wt)

	project := model.Project{ID: o.projectID, Name: "test", BareRepoPath: "/tmp/fake"}
	db.Create(&project)

	taskID := uuid.New()
	task := model.Task{
		ID:          taskID,
		ProjectID:   o.projectID,
		Title:       "test task",
		Description: "test",
		Status:      model.StatusTestReview,
	}
	db.Create(&task)

	if err := o.HandleTestReviewApproved(taskID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", taskID)
	if updated.Status != model.StatusInProgress {
		t.Errorf("expected status in_progress, got %s", updated.Status)
	}

	// Verify event was created.
	var events []model.TaskEvent
	db.Where("task_id = ?", taskID).Find(&events)
	if len(events) == 0 {
		t.Error("expected at least one event")
	}
}

func TestHandleTestReviewApproved_WrongStatus(t *testing.T) {
	db := testutil.NewSharedTestDB(t)
	wt := &FakeWorktreeManager{BarePath: "/tmp/fake", Default: "main"}
	o := testOrchestrator(t, db, wt)

	project := model.Project{ID: o.projectID, Name: "test", BareRepoPath: "/tmp/fake"}
	db.Create(&project)

	taskID := uuid.New()
	task := model.Task{
		ID:          taskID,
		ProjectID:   o.projectID,
		Title:       "test task",
		Description: "test",
		Status:      model.StatusInProgress,
	}
	db.Create(&task)

	err := o.HandleTestReviewApproved(taskID)
	if err == nil {
		t.Fatal("expected error for wrong status, got nil")
	}
}

// ---------------------------------------------------------------------------
// HandleTestReviewRejected tests
// ---------------------------------------------------------------------------

func TestHandleTestReviewRejected_FirstRejection(t *testing.T) {
	db := testutil.NewSharedTestDB(t)
	wt := &FakeWorktreeManager{BarePath: "/tmp/fake", Default: "main"}
	o := testOrchestrator(t, db, wt)

	project := model.Project{ID: o.projectID, Name: "test", BareRepoPath: "/tmp/fake"}
	db.Create(&project)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   o.projectID,
		Title:       "parent task",
		Description: "test parent",
		Status:      model.StatusTestReview,
	}
	db.Create(&parent)

	// Create test-phase subtasks in DONE state.
	sub1ID := uuid.New()
	sub1 := model.Task{
		ID:           sub1ID,
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "write tests for auth",
		Description:  "original description",
		Status:       model.StatusDone,
		Phase:        "test",
		Priority:     10,
		Context:      model.JSONField{"agent_type": "coder", "estimated_files": []string{"auth_test.go"}},
	}
	db.Create(&sub1)

	sub2ID := uuid.New()
	sub2 := model.Task{
		ID:           sub2ID,
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "write tests for db",
		Description:  "db test description",
		Status:       model.StatusDone,
		Phase:        "test",
		Priority:     5,
	}
	db.Create(&sub2)

	feedback := "Tests don't cover edge cases for empty input"
	if err := o.HandleTestReviewRejected(parentID, feedback); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Parent should be back in TEST_WRITING.
	var updatedParent model.Task
	db.First(&updatedParent, "id = ?", parentID)
	if updatedParent.Status != model.StatusTestWriting {
		t.Errorf("expected parent status test_writing, got %s", updatedParent.Status)
	}

	// Original subtasks should be REJECTED.
	var rejectedSub1 model.Task
	db.First(&rejectedSub1, "id = ?", sub1ID)
	if rejectedSub1.Status != model.StatusRejected {
		t.Errorf("expected sub1 status rejected, got %s", rejectedSub1.Status)
	}

	var rejectedSub2 model.Task
	db.First(&rejectedSub2, "id = ?", sub2ID)
	if rejectedSub2.Status != model.StatusRejected {
		t.Errorf("expected sub2 status rejected, got %s", rejectedSub2.Status)
	}

	// Replacement subtasks should exist in BACKLOG.
	var replacements []model.Task
	db.Where("parent_task_id = ? AND status = ?", parentID, model.StatusBacklog).Find(&replacements)
	if len(replacements) != 2 {
		t.Fatalf("expected 2 replacement subtasks, got %d", len(replacements))
	}

	// Check replacement has feedback in description and revision suffix.
	found := false
	for _, r := range replacements {
		if r.Title == "write tests for auth (revision 1)" {
			found = true
			if r.Phase != "test" {
				t.Errorf("expected replacement phase 'test', got %q", r.Phase)
			}
			if r.Description != "original description\n\n## Rejection Feedback\n\n"+feedback {
				t.Errorf("unexpected replacement description: %s", r.Description)
			}
		}
	}
	if !found {
		t.Error("expected replacement with title 'write tests for auth (revision 1)'")
	}
}

func TestHandleTestReviewRejected_RejectionCountIncrements(t *testing.T) {
	db := testutil.NewSharedTestDB(t)
	wt := &FakeWorktreeManager{BarePath: "/tmp/fake", Default: "main"}
	o := testOrchestrator(t, db, wt)

	project := model.Project{ID: o.projectID, Name: "test", BareRepoPath: "/tmp/fake"}
	db.Create(&project)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   o.projectID,
		Title:       "parent task",
		Description: "test parent",
		Status:      model.StatusTestReview,
	}
	db.Create(&parent)

	// First rejection.
	if err := o.HandleTestReviewRejected(parentID, "feedback 1"); err != nil {
		t.Fatalf("first rejection error: %v", err)
	}

	var after1 model.Task
	db.First(&after1, "id = ?", parentID)
	count1, ok := after1.Context["test_rejection_count"].(float64)
	if !ok || int(count1) != 1 {
		t.Errorf("expected rejection count 1, got %v", after1.Context["test_rejection_count"])
	}

	// Manually set back to TEST_REVIEW for second rejection
	// (simulate agent completing test rewrite and orchestrator re-reviewing).
	after1.Status = model.StatusTestReview
	db.Save(&after1)

	// Second rejection.
	if err := o.HandleTestReviewRejected(parentID, "feedback 2"); err != nil {
		t.Fatalf("second rejection error: %v", err)
	}

	var after2 model.Task
	db.First(&after2, "id = ?", parentID)
	count2, ok := after2.Context["test_rejection_count"].(float64)
	if !ok || int(count2) != 2 {
		t.Errorf("expected rejection count 2, got %v", after2.Context["test_rejection_count"])
	}
}

func TestHandleTestReviewRejected_ThirdRejectionPauses(t *testing.T) {
	db := testutil.NewSharedTestDB(t)
	wt := &FakeWorktreeManager{BarePath: "/tmp/fake", Default: "main"}
	o := testOrchestrator(t, db, wt)

	project := model.Project{ID: o.projectID, Name: "test", BareRepoPath: "/tmp/fake"}
	db.Create(&project)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   o.projectID,
		Title:       "parent task",
		Description: "test parent",
		Status:      model.StatusTestReview,
		Context: model.JSONField{
			"test_rejection_count":      float64(2),
			"test_rejection_feedback_1": "feedback 1",
			"test_rejection_feedback_2": "feedback 2",
		},
	}
	db.Create(&parent)

	if err := o.HandleTestReviewRejected(parentID, "feedback 3"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", parentID)

	// Should be PAUSED.
	if updated.Status != model.StatusPaused {
		t.Errorf("expected status paused, got %s", updated.Status)
	}

	// Rejection count should be 3.
	count, ok := updated.Context["test_rejection_count"].(float64)
	if !ok || int(count) != 3 {
		t.Errorf("expected rejection count 3, got %v", updated.Context["test_rejection_count"])
	}

	// Diagnostic flag should be set.
	diagRequired, ok := updated.Context["diagnostic_required"].(bool)
	if !ok || !diagRequired {
		t.Error("expected diagnostic_required to be true")
	}

	// Diagnostic prompt should be stored.
	if _, ok := updated.Context["diagnostic_prompt"].(string); !ok {
		t.Error("expected diagnostic_prompt to be set in context")
	}
}

func TestHandleTestReviewRejected_WrongStatus(t *testing.T) {
	db := testutil.NewSharedTestDB(t)
	wt := &FakeWorktreeManager{BarePath: "/tmp/fake", Default: "main"}
	o := testOrchestrator(t, db, wt)

	project := model.Project{ID: o.projectID, Name: "test", BareRepoPath: "/tmp/fake"}
	db.Create(&project)

	taskID := uuid.New()
	task := model.Task{
		ID:          taskID,
		ProjectID:   o.projectID,
		Title:       "test task",
		Description: "test",
		Status:      model.StatusInProgress,
	}
	db.Create(&task)

	err := o.HandleTestReviewRejected(taskID, "some feedback")
	if err == nil {
		t.Fatal("expected error for wrong status, got nil")
	}
}

func TestHandleTestReviewRejected_ReplacementPreservesPhaseAndTestsFor(t *testing.T) {
	db := testutil.NewSharedTestDB(t)
	wt := &FakeWorktreeManager{BarePath: "/tmp/fake", Default: "main"}
	o := testOrchestrator(t, db, wt)

	project := model.Project{ID: o.projectID, Name: "test", BareRepoPath: "/tmp/fake"}
	db.Create(&project)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   o.projectID,
		Title:       "parent task",
		Description: "test parent",
		Status:      model.StatusTestReview,
	}
	db.Create(&parent)

	implID := uuid.New()
	subID := uuid.New()
	sub := model.Task{
		ID:           subID,
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "write unit tests",
		Description:  "test desc",
		Status:       model.StatusDone,
		Phase:        "test",
		TestsFor:     model.JSONArray{implID.String()},
		Context:      model.JSONField{"agent_type": "coder"},
	}
	db.Create(&sub)

	if err := o.HandleTestReviewRejected(parentID, "needs more coverage"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Find the replacement subtask.
	var replacements []model.Task
	db.Where("parent_task_id = ? AND status = ?", parentID, model.StatusBacklog).Find(&replacements)
	if len(replacements) != 1 {
		t.Fatalf("expected 1 replacement subtask, got %d", len(replacements))
	}

	r := replacements[0]
	if r.Phase != "test" {
		t.Errorf("expected replacement phase 'test', got %q", r.Phase)
	}
	if len(r.TestsFor) != 1 || r.TestsFor[0] != implID.String() {
		t.Errorf("expected replacement TestsFor = [%s], got %v", implID, r.TestsFor)
	}
}

// ---------------------------------------------------------------------------
// isTerminal tests
// ---------------------------------------------------------------------------

func TestIsTerminal(t *testing.T) {
	tests := []struct {
		status   model.TaskStatus
		expected bool
	}{
		{model.StatusDone, true},
		{model.StatusFailed, true},
		{model.StatusRejected, true},
		{model.StatusCancelled, true},
		{model.StatusBacklog, false},
		{model.StatusInProgress, false},
		{model.StatusPlanning, false},
		{model.StatusPlanReview, false},
		{model.StatusTestWriting, false},
		{model.StatusTestReview, false},
		{model.StatusTestingReady, false},
		{model.StatusMerging, false},
		{model.StatusPaused, false},
	}

	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			got := isTerminal(tt.status)
			if got != tt.expected {
				t.Errorf("isTerminal(%s) = %v, want %v", tt.status, got, tt.expected)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// REJECTED subtasks excluded from scheduling
// ---------------------------------------------------------------------------

func TestCheckFeatureCompletion_RejectedSubtasksAreTerminal(t *testing.T) {
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
		Description: "test parent",
		Status:      model.StatusInProgress,
	}
	db.Create(&parent)

	// Create one DONE subtask and one REJECTED subtask.
	sub1 := model.Task{
		ID:           uuid.New(),
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "done-sub",
		Description:  "test subtask",
		Status:       model.StatusDone,
	}
	db.Create(&sub1)

	sub2 := model.Task{
		ID:           uuid.New(),
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "rejected-sub",
		Description:  "test subtask",
		Status:       model.StatusRejected,
	}
	db.Create(&sub2)

	// Also add a BACKLOG replacement (non-terminal).
	sub3 := model.Task{
		ID:           uuid.New(),
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "replacement-sub",
		Description:  "test subtask",
		Status:       model.StatusBacklog,
	}
	db.Create(&sub3)

	if err := o.checkFeatureCompletion(&parent); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Parent should stay in_progress since sub3 (BACKLOG) is non-terminal.
	var updated model.Task
	db.First(&updated, "id = ?", parentID)
	if updated.Status != model.StatusInProgress {
		t.Errorf("expected parent to stay in_progress, got %s", updated.Status)
	}
}

func TestScheduleSubtasks_RejectedNotInQuery(t *testing.T) {
	// Verify that the scheduleSubtasks query (BACKLOG or unassigned IN_PROGRESS)
	// does not match REJECTED subtasks. We test this at the DB query level
	// since scheduleSubtasks requires a runner to be initialized.
	db := testutil.NewSharedTestDB(t)

	projectID := uuid.New()
	project := model.Project{ID: projectID, Name: "test", BareRepoPath: "/tmp/fake"}
	db.Create(&project)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   projectID,
		Title:       "parent",
		Description: "test parent",
		Status:      model.StatusTestWriting,
	}
	db.Create(&parent)

	// Create REJECTED test subtask — should NOT be picked up.
	rejectedSub := model.Task{
		ID:           uuid.New(),
		ProjectID:    projectID,
		ParentTaskID: &parentID,
		Title:        "rejected test",
		Description:  "test subtask",
		Status:       model.StatusRejected,
		Phase:        "test",
	}
	db.Create(&rejectedSub)

	// Create BACKLOG replacement — should be schedulable.
	backlogSub := model.Task{
		ID:           uuid.New(),
		ProjectID:    projectID,
		ParentTaskID: &parentID,
		Title:        "replacement test",
		Description:  "test subtask",
		Status:       model.StatusBacklog,
		Phase:        "test",
	}
	db.Create(&backlogSub)

	// Run the same query scheduleSubtasks uses.
	var subtasks []model.Task
	err := db.Where(
		"parent_task_id = ? AND ((status = ?) OR (status = ? AND assigned_agent_id IS NULL))",
		parentID, model.StatusBacklog, model.StatusInProgress,
	).Order("priority DESC").Find(&subtasks).Error
	if err != nil {
		t.Fatalf("query error: %v", err)
	}

	// Should only find the BACKLOG subtask, not the REJECTED one.
	if len(subtasks) != 1 {
		t.Fatalf("expected 1 schedulable subtask, got %d", len(subtasks))
	}
	if subtasks[0].ID != backlogSub.ID {
		t.Errorf("expected backlog subtask %s, got %s", backlogSub.ID, subtasks[0].ID)
	}
}
