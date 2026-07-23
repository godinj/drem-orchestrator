package orchestrator

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

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

func TestOnAgentCompleted_ClearsAssignmentWhenMarkingDone(t *testing.T) {
	db := testutil.NewSharedTestDB(t)
	wt := &FakeWorktreeManager{BarePath: "/tmp/fake", Default: "main"}
	o := testOrchestrator(t, db, wt)

	project := model.Project{ID: o.projectID, Name: "test", BareRepoPath: "/tmp/fake"}
	db.Create(&project)
	parentID := uuid.New()
	parent := model.Task{ID: parentID, ProjectID: o.projectID, Title: "parent", Description: "parent", Status: model.StatusInProgress}
	db.Create(&parent)
	task := model.Task{ID: uuid.New(), ProjectID: o.projectID, ParentTaskID: &parentID, Title: "subtask", Description: "subtask", Status: model.StatusInProgress}
	db.Create(&task)
	ag := testutil.CreateAgent(t, db, task.ID, model.AgentCoder, model.AgentWorking)
	task.AssignedAgentID = &ag.ID
	db.Save(&task)

	if err := o.onAgentCompleted(&ag, &task); err != nil {
		t.Fatalf("onAgentCompleted: %v", err)
	}

	var reloaded model.Task
	db.First(&reloaded, "id = ?", task.ID)
	if reloaded.Status != model.StatusDone {
		t.Fatalf("expected done, got %s", reloaded.Status)
	}
	if reloaded.AssignedAgentID != nil {
		t.Fatalf("expected assignment cleared, got %s", *reloaded.AssignedAgentID)
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
			if r.Context["agent_type"] != "coder" {
				t.Errorf("expected replacement agent_type coder, got %v", r.Context["agent_type"])
			}
			if files := getEstimatedFiles(r.Context); len(files) != 1 || files[0] != "auth_test.go" {
				t.Errorf("expected replacement estimated_files preserved, got %v", files)
			}
			if skip, ok := r.Context["skip_existing_work_dedup"].(bool); !ok || !skip {
				t.Errorf("expected replacement skip_existing_work_dedup true, got %v", r.Context["skip_existing_work_dedup"])
			}
			if r.Context["skip_existing_work_dedup_reason"] != "test_review_rejected" {
				t.Errorf("expected replacement skip reason test_review_rejected, got %v", r.Context["skip_existing_work_dedup_reason"])
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

func TestHandleTestReviewRejected_RepeatedRejectionUsesCanonicalTitle(t *testing.T) {
	db := testutil.NewSharedTestDB(t)
	wt := &FakeWorktreeManager{BarePath: "/tmp/fake", Default: "main"}
	o := testOrchestrator(t, db, wt)

	project := model.Project{ID: o.projectID, Name: "test", BareRepoPath: "/tmp/fake"}
	db.Create(&project)

	parentID := uuid.New()
	parent := model.Task{ID: parentID, ProjectID: o.projectID, Title: "parent", Description: "parent", Status: model.StatusTestReview}
	db.Create(&parent)

	sub := model.Task{
		ID:           uuid.New(),
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "write tests for auth",
		Description:  "tests",
		Status:       model.StatusDone,
		Phase:        "test",
	}
	db.Create(&sub)

	if err := o.HandleTestReviewRejected(parentID, "feedback 1"); err != nil {
		t.Fatalf("first rejection: %v", err)
	}

	var rev1 model.Task
	if err := db.Where("parent_task_id = ? AND title = ?", parentID, "write tests for auth (revision 1)").First(&rev1).Error; err != nil {
		t.Fatalf("load revision 1: %v", err)
	}
	rev1.Status = model.StatusDone
	db.Save(&rev1)

	var reloadedParent model.Task
	db.First(&reloadedParent, "id = ?", parentID)
	reloadedParent.Status = model.StatusTestReview
	db.Save(&reloadedParent)

	if err := o.HandleTestReviewRejected(parentID, "feedback 2"); err != nil {
		t.Fatalf("second rejection: %v", err)
	}

	var nestedCount int64
	db.Model(&model.Task{}).Where("parent_task_id = ? AND title = ?", parentID, "write tests for auth (revision 1) (revision 2)").Count(&nestedCount)
	if nestedCount != 0 {
		t.Fatalf("nested revision title was created")
	}

	var rev2Count int64
	db.Model(&model.Task{}).Where("parent_task_id = ? AND title = ?", parentID, "write tests for auth (revision 2)").Count(&rev2Count)
	if rev2Count != 1 {
		t.Fatalf("expected canonical revision 2 replacement, got %d", rev2Count)
	}
}

func TestHandleTestReviewRejected_DeduplicatesCanonicalReplacementTitles(t *testing.T) {
	db := testutil.NewSharedTestDB(t)
	wt := &FakeWorktreeManager{BarePath: "/tmp/fake", Default: "main"}
	o := testOrchestrator(t, db, wt)

	project := model.Project{ID: o.projectID, Name: "test", BareRepoPath: "/tmp/fake"}
	db.Create(&project)

	parentID := uuid.New()
	parent := model.Task{ID: parentID, ProjectID: o.projectID, Title: "parent", Description: "parent", Status: model.StatusTestReview}
	db.Create(&parent)

	olderID := uuid.New()
	newerID := uuid.New()
	older := model.Task{
		ID:           olderID,
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "write tests for auth",
		Description:  "older stale test attempt",
		Status:       model.StatusDone,
		Phase:        "test",
		CreatedAt:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	newer := model.Task{
		ID:           newerID,
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "write tests for auth (revision 1) (revision 2)",
		Description:  "newer stale test attempt",
		Status:       model.StatusDone,
		Phase:        "test",
		CreatedAt:    time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
	}
	db.Create(&older)
	db.Create(&newer)

	if err := o.HandleTestReviewRejected(parentID, "needs fresh test evidence"); err != nil {
		t.Fatalf("reject test review: %v", err)
	}

	var rejectedCount int64
	db.Model(&model.Task{}).Where("id IN ? AND status = ?", []uuid.UUID{olderID, newerID}, model.StatusRejected).Count(&rejectedCount)
	if rejectedCount != 2 {
		t.Fatalf("expected both stale done subtasks rejected, got %d", rejectedCount)
	}

	var replacements []model.Task
	db.Where("parent_task_id = ? AND status = ? AND title = ?", parentID, model.StatusBacklog, "write tests for auth (revision 1)").Find(&replacements)
	if len(replacements) != 1 {
		t.Fatalf("expected one canonical replacement, got %d", len(replacements))
	}
	if replacements[0].Description != "newer stale test attempt\n\n## Rejection Feedback\n\nneeds fresh test evidence" {
		t.Fatalf("expected replacement to use newest stale attempt description, got %q", replacements[0].Description)
	}

	var event model.TaskEvent
	if err := db.Where("task_id = ? AND event_type = ? AND new_value = ?", parentID, "status_change", string(model.StatusTestWriting)).First(&event).Error; err != nil {
		t.Fatalf("load parent transition event: %v", err)
	}
	evidence, _ := event.Details["evidence"].(map[string]any)
	references, _ := evidence["references"].(map[string]any)
	if references["subtasks_cloned"] != float64(1) {
		t.Fatalf("expected one cloned subtask in evidence references, got %v", references["subtasks_cloned"])
	}
}

func TestHandleTestReviewRejected_PreservesDistinctTestsForLanesWithSameTitle(t *testing.T) {
	db := testutil.NewSharedTestDB(t)
	wt := &FakeWorktreeManager{BarePath: "/tmp/fake", Default: "main"}
	o := testOrchestrator(t, db, wt)

	project := model.Project{ID: o.projectID, Name: "test", BareRepoPath: "/tmp/fake"}
	db.Create(&project)

	parentID := uuid.New()
	parent := model.Task{ID: parentID, ProjectID: o.projectID, Title: "parent", Description: "parent", Status: model.StatusTestReview}
	db.Create(&parent)

	sub1 := model.Task{
		ID:           uuid.New(),
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "write tests for auth",
		Description:  "tests for impl 0",
		Status:       model.StatusDone,
		Phase:        "test",
		TestsFor:     model.JSONArray{"0"},
	}
	sub2 := model.Task{
		ID:           uuid.New(),
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "write tests for auth",
		Description:  "tests for impl 1",
		Status:       model.StatusDone,
		Phase:        "test",
		TestsFor:     model.JSONArray{"1"},
	}
	db.Create(&sub1)
	db.Create(&sub2)

	if err := o.HandleTestReviewRejected(parentID, "needs coverage"); err != nil {
		t.Fatalf("reject test review: %v", err)
	}

	var replacements []model.Task
	db.Where("parent_task_id = ? AND status = ? AND title = ?", parentID, model.StatusBacklog, "write tests for auth (revision 1)").Find(&replacements)
	if len(replacements) != 2 {
		t.Fatalf("expected distinct TestsFor lanes preserved, got %d replacements", len(replacements))
	}

	found := map[string]bool{}
	for _, replacement := range replacements {
		if len(replacement.TestsFor) != 1 {
			t.Fatalf("expected one TestsFor entry, got %v", replacement.TestsFor)
		}
		found[replacement.TestsFor[0]] = true
	}
	if !found["0"] || !found["1"] {
		t.Fatalf("expected replacements for TestsFor 0 and 1, got %v", found)
	}
}

func TestHandleTestReviewRejected_PreservesUnrevisedSameTitleWithoutLineage(t *testing.T) {
	db := testutil.NewSharedTestDB(t)
	wt := &FakeWorktreeManager{BarePath: "/tmp/fake", Default: "main"}
	o := testOrchestrator(t, db, wt)

	project := model.Project{ID: o.projectID, Name: "test", BareRepoPath: "/tmp/fake"}
	db.Create(&project)

	parentID := uuid.New()
	parent := model.Task{ID: parentID, ProjectID: o.projectID, Title: "parent", Description: "parent", Status: model.StatusTestReview}
	db.Create(&parent)

	for _, description := range []string{"first same-title lane", "second same-title lane"} {
		sub := model.Task{
			ID:           uuid.New(),
			ProjectID:    o.projectID,
			ParentTaskID: &parentID,
			Title:        "write tests for auth",
			Description:  description,
			Status:       model.StatusDone,
			Phase:        "test",
		}
		db.Create(&sub)
	}

	if err := o.HandleTestReviewRejected(parentID, "needs coverage"); err != nil {
		t.Fatalf("reject test review: %v", err)
	}

	var replacementCount int64
	db.Model(&model.Task{}).Where("parent_task_id = ? AND status = ? AND title = ?", parentID, model.StatusBacklog, "write tests for auth (revision 1)").Count(&replacementCount)
	if replacementCount != 2 {
		t.Fatalf("expected unrevised same-title lanes preserved, got %d replacements", replacementCount)
	}
}

func TestHandleTestReviewRejected_ClearsDoneSubtaskAssignment(t *testing.T) {
	db := testutil.NewSharedTestDB(t)
	wt := &FakeWorktreeManager{BarePath: "/tmp/fake", Default: "main"}
	o := testOrchestrator(t, db, wt)

	project := model.Project{ID: o.projectID, Name: "test", BareRepoPath: "/tmp/fake"}
	db.Create(&project)

	parentID := uuid.New()
	parent := model.Task{ID: parentID, ProjectID: o.projectID, Title: "parent", Description: "parent", Status: model.StatusTestReview}
	db.Create(&parent)
	agentID := uuid.New()
	sub := model.Task{
		ID:              uuid.New(),
		ProjectID:       o.projectID,
		ParentTaskID:    &parentID,
		Title:           "write tests",
		Description:     "tests",
		Status:          model.StatusDone,
		Phase:           "test",
		AssignedAgentID: &agentID,
	}
	db.Create(&sub)

	if err := o.HandleTestReviewRejected(parentID, "feedback"); err != nil {
		t.Fatalf("reject test review: %v", err)
	}

	var reloaded model.Task
	db.First(&reloaded, "id = ?", sub.ID)
	if reloaded.AssignedAgentID != nil {
		t.Fatalf("expected rejected done subtask assignment cleared, got %s", *reloaded.AssignedAgentID)
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

func TestRepairSupersededTestDependenciesUsesCompletedRevision(t *testing.T) {
	db := testutil.NewSharedTestDB(t)
	parentID := uuid.New()
	implementationID := uuid.New()
	originalTestID := uuid.New()
	revisionTestID := uuid.New()
	for _, task := range []model.Task{
		{
			ID: originalTestID, ProjectID: uuid.New(), ParentTaskID: &parentID,
			Title: "original test", Status: model.StatusRejected, Phase: "test",
			TestsFor: model.JSONArray{implementationID.String()},
		},
		{
			ID: revisionTestID, ProjectID: uuid.New(), ParentTaskID: &parentID,
			Title: "test revision", Status: model.StatusDone, Phase: "test",
			TestsFor: model.JSONArray{implementationID.String()},
		},
		{
			ID: implementationID, ProjectID: uuid.New(), ParentTaskID: &parentID,
			Title: "implementation", Status: model.StatusBacklog, Phase: "implementation",
			DependencyIDs: model.JSONArray{originalTestID.String()},
		},
	} {
		require.NoError(t, db.Create(&task).Error)
	}

	require.NoError(t, repairSupersededTestDependencies(db, parentID))
	var implementation model.Task
	require.NoError(t, db.First(&implementation, "id = ?", implementationID).Error)
	require.Equal(t, model.JSONArray{revisionTestID.String()}, implementation.DependencyIDs)
}

func TestSupersededRejectedTestIDsRequiresDoneRevisionCoverage(t *testing.T) {
	implementationID := uuid.New().String()
	rejected := model.Task{
		ID: uuid.New(), Phase: "test", Status: model.StatusRejected,
		TestsFor: model.JSONArray{implementationID},
	}
	doneRevision := model.Task{
		ID: uuid.New(), Phase: "test", Status: model.StatusDone,
		TestsFor: model.JSONArray{implementationID},
	}
	unsuperseded := model.Task{
		ID: uuid.New(), Phase: "test", Status: model.StatusRejected,
		TestsFor: model.JSONArray{uuid.New().String()},
	}

	got := supersededRejectedTestIDs([]model.Task{rejected, doneRevision, unsuperseded})
	require.Contains(t, got, rejected.ID)
	require.NotContains(t, got, unsuperseded.ID)
	required := requiredSubtasksForParentTarget(
		[]model.Task{rejected, doneRevision, unsuperseded}, model.StatusTestingReady,
	)
	require.NotContains(t, required, rejected.ID.String())
	require.Contains(t, required, doneRevision.ID.String())
	require.Contains(t, required, unsuperseded.ID.String())
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

	// A rejected required child terminalizes the parent immediately rather than
	// leaving it blocked behind a replacement that can no longer be useful.
	var updated model.Task
	db.First(&updated, "id = ?", parentID)
	if updated.Status != model.StatusFailed {
		t.Errorf("expected parent to fail, got %s", updated.Status)
	}
	var replacement model.Task
	db.First(&replacement, "id = ?", sub3.ID)
	if replacement.Status != model.StatusCancelled {
		t.Errorf("expected blocked replacement to be cancelled, got %s", replacement.Status)
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
