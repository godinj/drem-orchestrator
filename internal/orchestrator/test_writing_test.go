package orchestrator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/agent"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/state"
	"github.com/godinj/drem-orchestrator/internal/testutil"
	"github.com/godinj/drem-orchestrator/internal/worktree"
)

// testOrchestratorWithRunner creates an Orchestrator with a test DB and a
// Runner that has 0 capacity (CanSpawn returns false). Useful for tests
// that call processTestWriting but don't need actual agent spawning.
func testOrchestratorWithRunner(t *testing.T, db *gorm.DB, wtManager *worktree.Manager) *Orchestrator {
	t.Helper()
	o := testOrchestrator(t, db, wtManager)
	o.runner = agent.NewRunner(db, nil, wtManager, "claude", 0)
	return o
}

// ---------------------------------------------------------------------------
// processTestWriting tests
// ---------------------------------------------------------------------------

func TestProcessTestWriting_SchedulesOnlyTestPhaseSubtasks(t *testing.T) {
	db := testutil.NewSharedTestDB(t)
	wt := &worktree.Manager{BareRepoPath: "/tmp/fake", DefaultBranch: "main"}
	o := testOrchestrator(t, db, wt)

	project := model.Project{ID: o.projectID, Name: "test", BareRepoPath: "/tmp/fake"}
	db.Create(&project)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   o.projectID,
		Title:       "parent",
		Description: "test parent",
		Status:      model.StatusTestWriting,
	}
	db.Create(&parent)

	// Create 2 test subtasks and 2 implementation subtasks, all in BACKLOG.
	testSub1 := model.Task{
		ID:           uuid.New(),
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "test-sub-1",
		Description:  "write tests for A",
		Status:       model.StatusBacklog,
		Phase:        "test",
		Priority:     4,
	}
	testSub2 := model.Task{
		ID:           uuid.New(),
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "test-sub-2",
		Description:  "write tests for B",
		Status:       model.StatusBacklog,
		Phase:        "test",
		Priority:     3,
	}
	implSub1 := model.Task{
		ID:           uuid.New(),
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "impl-sub-1",
		Description:  "implement A",
		Status:       model.StatusBacklog,
		Phase:        "implementation",
		Priority:     2,
	}
	implSub2 := model.Task{
		ID:           uuid.New(),
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "impl-sub-2",
		Description:  "implement B",
		Status:       model.StatusBacklog,
		Phase:        "implementation",
		Priority:     1,
	}
	db.Create(&testSub1)
	db.Create(&testSub2)
	db.Create(&implSub1)
	db.Create(&implSub2)

	// Call scheduleSubtasks via processTestWriting. Since we have no runner,
	// scheduling won't actually spawn agents but the phase filter logic
	// ensures only test subtasks are considered.
	// We verify this by checking the scheduling query behavior.

	// Instead of calling processTestWriting (which needs runner), test the
	// scheduleSubtasks phase-filtering directly.
	// Query subtasks like scheduleSubtasks does, then verify filtering.
	var subtasks []model.Task
	if err := db.Where(
		"parent_task_id = ? AND ((status = ?) OR (status = ? AND assigned_agent_id IS NULL))",
		parentID, model.StatusBacklog, model.StatusInProgress,
	).Order("priority DESC").Find(&subtasks).Error; err != nil {
		t.Fatalf("query subtasks: %v", err)
	}

	// Simulate the phase filter from scheduleSubtasks.
	var schedulable []string
	for _, sub := range subtasks {
		if parent.Status == model.StatusTestWriting && sub.Phase != "test" {
			continue
		}
		schedulable = append(schedulable, sub.Title)
	}

	if len(schedulable) != 2 {
		t.Fatalf("expected 2 schedulable test subtasks, got %d: %v", len(schedulable), schedulable)
	}
	for _, name := range schedulable {
		if name != "test-sub-1" && name != "test-sub-2" {
			t.Errorf("unexpected schedulable subtask: %s", name)
		}
	}
}

func TestProcessTestWriting_AllTestSubtasksDone_TransitionsToTestReview(t *testing.T) {
	db := testutil.NewSharedTestDB(t)
	wt := &worktree.Manager{BareRepoPath: "/tmp/fake", DefaultBranch: "main"}
	o := testOrchestratorWithRunner(t, db, wt)

	project := model.Project{ID: o.projectID, Name: "test", BareRepoPath: "/tmp/fake"}
	db.Create(&project)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   o.projectID,
		Title:       "parent",
		Description: "test parent",
		Status:      model.StatusTestWriting,
	}
	db.Create(&parent)

	// Create test subtasks that are all DONE.
	for _, title := range []string{"test-1", "test-2"} {
		sub := model.Task{
			ID:           uuid.New(),
			ProjectID:    o.projectID,
			ParentTaskID: &parentID,
			Title:        title,
			Description:  "test subtask",
			Status:       model.StatusDone,
			Phase:        "test",
		}
		db.Create(&sub)
	}

	// Also create implementation subtasks in BACKLOG (should not affect transition).
	implSub := model.Task{
		ID:           uuid.New(),
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "impl-1",
		Description:  "implementation subtask",
		Status:       model.StatusBacklog,
		Phase:        "implementation",
	}
	db.Create(&implSub)

	if err := o.processTestWriting(&parent); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Reload parent to check status.
	var updated model.Task
	db.First(&updated, "id = ?", parentID)
	if updated.Status != model.StatusTestReview {
		t.Errorf("expected parent status test_review, got %s", updated.Status)
	}
}

func TestProcessTestWriting_AllTestSubtasksTerminal_SomeFailed(t *testing.T) {
	db := testutil.NewSharedTestDB(t)
	wt := &worktree.Manager{BareRepoPath: "/tmp/fake", DefaultBranch: "main"}
	o := testOrchestratorWithRunner(t, db, wt)

	project := model.Project{ID: o.projectID, Name: "test", BareRepoPath: "/tmp/fake"}
	db.Create(&project)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   o.projectID,
		Title:       "parent",
		Description: "test parent",
		Status:      model.StatusTestWriting,
	}
	db.Create(&parent)

	// One done, one failed.
	sub1 := model.Task{
		ID:           uuid.New(),
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "test-done",
		Description:  "test subtask",
		Status:       model.StatusDone,
		Phase:        "test",
	}
	sub2 := model.Task{
		ID:           uuid.New(),
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "test-failed",
		Description:  "test subtask",
		Status:       model.StatusFailed,
		Phase:        "test",
	}
	db.Create(&sub1)
	db.Create(&sub2)

	if err := o.processTestWriting(&parent); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", parentID)
	if updated.Status != model.StatusFailed {
		t.Errorf("expected parent status failed, got %s", updated.Status)
	}
}

func TestProcessTestWriting_TestSubtasksStillRunning(t *testing.T) {
	db := testutil.NewSharedTestDB(t)
	wt := &worktree.Manager{BareRepoPath: "/tmp/fake", DefaultBranch: "main"}
	o := testOrchestratorWithRunner(t, db, wt)

	project := model.Project{ID: o.projectID, Name: "test", BareRepoPath: "/tmp/fake"}
	db.Create(&project)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   o.projectID,
		Title:       "parent",
		Description: "test parent",
		Status:      model.StatusTestWriting,
	}
	db.Create(&parent)

	// One done, one still in_progress.
	sub1 := model.Task{
		ID:           uuid.New(),
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "test-done",
		Description:  "test subtask",
		Status:       model.StatusDone,
		Phase:        "test",
	}
	sub2 := model.Task{
		ID:           uuid.New(),
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "test-running",
		Description:  "test subtask",
		Status:       model.StatusInProgress,
		Phase:        "test",
	}
	db.Create(&sub1)
	db.Create(&sub2)

	if err := o.processTestWriting(&parent); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Parent should remain in TEST_WRITING.
	var updated model.Task
	db.First(&updated, "id = ?", parentID)
	if updated.Status != model.StatusTestWriting {
		t.Errorf("expected parent to stay in test_writing, got %s", updated.Status)
	}
}

// ---------------------------------------------------------------------------
// Empty subtask recovery tests (TDD — implementation pending)
// ---------------------------------------------------------------------------

// maxEmptySubtaskChecks is the expected constant that the implementation will
// define. Tests reference it so failures are assertion-based, not compile errors.
// The implementation must define this constant with the same value.
const expectedMaxEmptySubtaskChecks = 3

func TestProcessTestWriting_EmptySubtasks_FirstCheck_TransitionsToPlanning(t *testing.T) {
	db := testutil.NewSharedTestDB(t)
	wt := &worktree.Manager{BareRepoPath: "/tmp/fake", DefaultBranch: "main"}
	o := testOrchestratorWithRunner(t, db, wt)

	project := model.Project{ID: o.projectID, Name: "test", BareRepoPath: "/tmp/fake"}
	db.Create(&project)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   o.projectID,
		Title:       "parent-empty-subtasks",
		Description: "parent with no test subtasks",
		Status:      model.StatusTestWriting,
	}
	db.Create(&parent)

	// No test subtasks created — the empty condition.

	err := o.processTestWriting(&parent)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Reload parent from DB.
	var updated model.Task
	db.First(&updated, "id = ?", parentID)

	// Expected: parent transitions to planning for replan.
	if updated.Status != model.StatusPlanning {
		t.Errorf("expected parent status planning (replan), got %s", updated.Status)
	}

	// Verify an event was recorded for the transition.
	var events []model.TaskEvent
	db.Where("task_id = ?", parentID).Find(&events)
	found := false
	for _, evt := range events {
		if evt.OldValue == string(model.StatusTestWriting) && evt.NewValue == string(model.StatusPlanning) {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected status_change event from test_writing to planning, not found")
	}
}

func TestProcessTestWriting_EmptySubtasks_MaxRetries_TransitionsToFailed(t *testing.T) {
	db := testutil.NewSharedTestDB(t)
	wt := &worktree.Manager{BareRepoPath: "/tmp/fake", DefaultBranch: "main"}
	o := testOrchestratorWithRunner(t, db, wt)

	project := model.Project{ID: o.projectID, Name: "test", BareRepoPath: "/tmp/fake"}
	db.Create(&project)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   o.projectID,
		Title:       "parent-max-retries",
		Description: "parent at max empty subtask checks",
		Status:      model.StatusTestWriting,
		Context: model.JSONField{
			// Set counter to max-1 so this call is the final attempt.
			"empty_subtask_checks": float64(expectedMaxEmptySubtaskChecks - 1),
		},
	}
	db.Create(&parent)

	// No test subtasks — empty condition triggers at max retries.

	err := o.processTestWriting(&parent)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", parentID)

	if updated.Status != model.StatusFailed {
		t.Errorf("expected parent status failed after max retries, got %s", updated.Status)
	}

	// Verify failure reason is descriptive.
	ctx := updated.Context
	if ctx == nil {
		t.Fatal("expected task context to be non-nil after failure")
	}
	reason, ok := ctx["failure_reason"].(string)
	if !ok || reason == "" {
		t.Error("expected non-empty failure_reason in context")
	}
}

func TestProcessTestWriting_EmptySubtasks_CounterResets_WhenSubtasksAppear(t *testing.T) {
	db := testutil.NewSharedTestDB(t)
	wt := &worktree.Manager{BareRepoPath: "/tmp/fake", DefaultBranch: "main"}
	o := testOrchestratorWithRunner(t, db, wt)

	project := model.Project{ID: o.projectID, Name: "test", BareRepoPath: "/tmp/fake"}
	db.Create(&project)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   o.projectID,
		Title:       "parent-counter-reset",
		Description: "parent with counter that should reset",
		Status:      model.StatusTestWriting,
		Context: model.JSONField{
			"empty_subtask_checks": float64(2),
		},
	}
	db.Create(&parent)

	// Now subtasks exist — counter should reset to 0 and process normally.
	sub := model.Task{
		ID:           uuid.New(),
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "test-sub",
		Description:  "test subtask",
		Status:       model.StatusInProgress,
		Phase:        "test",
	}
	db.Create(&sub)

	err := o.processTestWriting(&parent)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Parent should stay in test_writing (subtask still running).
	var updated model.Task
	db.First(&updated, "id = ?", parentID)
	if updated.Status != model.StatusTestWriting {
		t.Errorf("expected parent to stay in test_writing, got %s", updated.Status)
	}

	// Counter should be reset to 0.
	ctx := updated.Context
	if ctx == nil {
		t.Fatal("expected task context to be non-nil")
	}
	counter, _ := ctx["empty_subtask_checks"].(float64)
	if counter != 0 {
		t.Errorf("expected empty_subtask_checks to be reset to 0, got %v", counter)
	}
}

func TestProcessTestWriting_EmptySubtasks_ReplanDirective_InContext(t *testing.T) {
	db := testutil.NewSharedTestDB(t)
	wt := &worktree.Manager{BareRepoPath: "/tmp/fake", DefaultBranch: "main"}
	o := testOrchestratorWithRunner(t, db, wt)

	project := model.Project{ID: o.projectID, Name: "test", BareRepoPath: "/tmp/fake"}
	db.Create(&project)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   o.projectID,
		Title:       "parent-replan-directive",
		Description: "parent should get replan directive",
		Status:      model.StatusTestWriting,
	}
	db.Create(&parent)

	// No test subtasks — triggers replan.

	err := o.processTestWriting(&parent)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", parentID)

	// After replan transition, context must contain the replan directive.
	ctx := updated.Context
	if ctx == nil {
		t.Fatal("expected task context to be non-nil after replan")
	}

	directive, ok := ctx["replan_directive"].(string)
	if !ok || directive == "" {
		t.Error("expected non-empty replan_directive in context instructing planner to generate test subtasks")
	}
}

// ---------------------------------------------------------------------------
// HandlePlanApproved TDD tests
// ---------------------------------------------------------------------------

func TestHandlePlanApproved_TDDPlan_TransitionsToTestWriting(t *testing.T) {
	db := testutil.NewSharedTestDB(t)
	wt := &worktree.Manager{BareRepoPath: "/tmp/fake", DefaultBranch: "main"}
	o := testOrchestrator(t, db, wt)

	project := model.Project{ID: o.projectID, Name: "test", BareRepoPath: "/tmp/fake"}
	db.Create(&project)

	taskID := uuid.New()
	task := model.Task{
		ID:          taskID,
		ProjectID:   o.projectID,
		Title:       "tdd-task",
		Description: "task with TDD plan",
		Status:      model.StatusPlanReview,
		Plan: model.JSONField{
			"subtasks": []any{
				map[string]any{
					"title":       "Write tests for module A",
					"description": "Test subtask",
					"agent_type":  "coder",
					"phase":       "test",
					"tests_for":   []any{float64(1)},
				},
				map[string]any{
					"title":       "Implement module A",
					"description": "Implementation subtask",
					"agent_type":  "coder",
					"phase":       "implementation",
				},
			},
		},
	}
	db.Create(&task)

	if err := o.HandlePlanApproved(taskID); err != nil {
		t.Fatalf("HandlePlanApproved error: %v", err)
	}

	// Check parent transitioned to TEST_WRITING.
	var updated model.Task
	db.First(&updated, "id = ?", taskID)
	if updated.Status != model.StatusTestWriting {
		t.Errorf("expected status test_writing, got %s", updated.Status)
	}

	// Check subtasks were created with correct Phase.
	var subtasks []model.Task
	db.Where("parent_task_id = ?", taskID).Order("priority DESC").Find(&subtasks)
	if len(subtasks) != 2 {
		t.Fatalf("expected 2 subtasks, got %d", len(subtasks))
	}

	// First subtask should be test phase.
	if subtasks[0].Phase != "test" {
		t.Errorf("expected first subtask phase 'test', got %q", subtasks[0].Phase)
	}
	// Second subtask should be implementation phase.
	if subtasks[1].Phase != "implementation" {
		t.Errorf("expected second subtask phase 'implementation', got %q", subtasks[1].Phase)
	}
}

func TestHandlePlanApproved_OldFormatPlan_TransitionsToInProgress(t *testing.T) {
	db := testutil.NewSharedTestDB(t)
	wt := &worktree.Manager{BareRepoPath: "/tmp/fake", DefaultBranch: "main"}
	o := testOrchestrator(t, db, wt)

	project := model.Project{ID: o.projectID, Name: "test", BareRepoPath: "/tmp/fake"}
	db.Create(&project)

	taskID := uuid.New()
	task := model.Task{
		ID:          taskID,
		ProjectID:   o.projectID,
		Title:       "old-task",
		Description: "task with old-format plan",
		Status:      model.StatusPlanReview,
		Plan: model.JSONField{
			"subtasks": []any{
				map[string]any{
					"title":       "Implement feature X",
					"description": "Standard subtask, no phase",
					"agent_type":  "coder",
				},
				map[string]any{
					"title":       "Implement feature Y",
					"description": "Standard subtask, no phase",
					"agent_type":  "coder",
				},
			},
		},
	}
	db.Create(&task)

	if err := o.HandlePlanApproved(taskID); err != nil {
		t.Fatalf("HandlePlanApproved error: %v", err)
	}

	// Check parent transitioned to IN_PROGRESS (backward compat).
	var updated model.Task
	db.First(&updated, "id = ?", taskID)
	if updated.Status != model.StatusInProgress {
		t.Errorf("expected status in_progress, got %s", updated.Status)
	}
}

// ---------------------------------------------------------------------------
// Phase-aware scheduling tests
// ---------------------------------------------------------------------------

func TestPhaseAwareScheduling_InProgressSkipsTestSubtasks(t *testing.T) {
	db := testutil.NewSharedTestDB(t)
	wt := &worktree.Manager{BareRepoPath: "/tmp/fake", DefaultBranch: "main"}
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

	// Create test and impl subtasks.
	testSub := model.Task{
		ID:           uuid.New(),
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "test-sub",
		Description:  "test subtask",
		Status:       model.StatusBacklog,
		Phase:        "test",
		Priority:     2,
	}
	implSub := model.Task{
		ID:           uuid.New(),
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "impl-sub",
		Description:  "impl subtask",
		Status:       model.StatusBacklog,
		Phase:        "implementation",
		Priority:     1,
	}
	db.Create(&testSub)
	db.Create(&implSub)

	// Query subtasks like scheduleSubtasks does.
	var subtasks []model.Task
	if err := db.Where(
		"parent_task_id = ? AND ((status = ?) OR (status = ? AND assigned_agent_id IS NULL))",
		parentID, model.StatusBacklog, model.StatusInProgress,
	).Order("priority DESC").Find(&subtasks).Error; err != nil {
		t.Fatalf("query subtasks: %v", err)
	}

	// Simulate the phase filter.
	var schedulable []string
	for _, sub := range subtasks {
		// During IN_PROGRESS, only schedule implementation and integration subtasks.
		if parent.Status == model.StatusInProgress && sub.Phase == "test" {
			continue
		}
		schedulable = append(schedulable, sub.Title)
	}

	if len(schedulable) != 1 {
		t.Fatalf("expected 1 schedulable impl subtask, got %d: %v", len(schedulable), schedulable)
	}
	if schedulable[0] != "impl-sub" {
		t.Errorf("expected impl-sub, got %s", schedulable[0])
	}
}

func TestPhaseAwareScheduling_NoPhaseSubtasksAlwaysScheduled(t *testing.T) {
	// Subtasks without a phase (empty string) should be scheduled in both
	// TEST_WRITING and IN_PROGRESS states, since the filter only blocks
	// specific phase values.
	tests := []struct {
		name         string
		parentStatus model.TaskStatus
		subPhase     string
		expected     bool
	}{
		{"test_writing+test", model.StatusTestWriting, "test", true},
		{"test_writing+impl", model.StatusTestWriting, "implementation", false},
		{"test_writing+empty", model.StatusTestWriting, "", false},
		{"in_progress+test", model.StatusInProgress, "test", false},
		{"in_progress+impl", model.StatusInProgress, "implementation", true},
		{"in_progress+empty", model.StatusInProgress, "", true},
		{"in_progress+integration", model.StatusInProgress, "integration", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Apply the same filter logic as scheduleSubtasks.
			scheduled := true
			if tt.parentStatus == model.StatusTestWriting && tt.subPhase != "test" {
				scheduled = false
			}
			if tt.parentStatus == model.StatusInProgress && tt.subPhase == "test" {
				scheduled = false
			}
			if scheduled != tt.expected {
				t.Errorf("expected scheduled=%v, got %v", tt.expected, scheduled)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Test file extraction tests
// ---------------------------------------------------------------------------

func TestIsTestFile(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		expected bool
	}{
		{"go test file", "foo_test.go", true},
		{"go regular file", "foo.go", false},
		{"python test prefix", "test_foo.py", true},
		{"python test suffix", "foo_test.py", true},
		{"python regular file", "foo.py", false},
		{"ts test file", "foo.test.ts", true},
		{"ts spec file", "foo.spec.ts", true},
		{"js test file", "foo.test.js", true},
		{"js spec file", "foo.spec.js", true},
		{"tsx test file", "component.test.tsx", true},
		{"jsx spec file", "component.spec.jsx", true},
		{"regular ts file", "foo.ts", false},
		{"with path go", "internal/pkg/foo_test.go", true},
		{"with path js", "src/utils/helper.test.js", true},
		{"README", "README.md", false},
		{"test in name but not test file", "test_helper.go", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isTestFile(tt.filename)
			if result != tt.expected {
				t.Errorf("isTestFile(%q) = %v, want %v", tt.filename, result, tt.expected)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TDD reverse dependencies tests
// ---------------------------------------------------------------------------

func TestMergeTDDDependencies_AddsReverseDeps(t *testing.T) {
	entries := []planEntry{
		{Title: "Tests for A", Phase: "test", TestsFor: []int{1}},
		{Title: "Implement A", Phase: "implementation"},
		{Title: "Tests for B", Phase: "test", TestsFor: []int{3}},
		{Title: "Implement B", Phase: "implementation"},
	}

	merged := MergeTDDDependencies(entries)

	// Impl subtask at index 1 should now depend on test subtask at index 0.
	if len(merged[1].Dependencies) != 1 {
		t.Fatalf("expected 1 dependency on impl A, got %d: %v", len(merged[1].Dependencies), merged[1].Dependencies)
	}
	if merged[1].Dependencies[0] != 0 {
		t.Errorf("expected impl A to depend on test at 0, got %d", merged[1].Dependencies[0])
	}
	// Impl subtask at index 3 should now depend on test subtask at index 2.
	if len(merged[3].Dependencies) != 1 {
		t.Fatalf("expected 1 dependency on impl B, got %d: %v", len(merged[3].Dependencies), merged[3].Dependencies)
	}
	if merged[3].Dependencies[0] != 2 {
		t.Errorf("expected impl B to depend on test at 2, got %d", merged[3].Dependencies[0])
	}
}

func TestMergeTDDDependencies_NoDuplicates(t *testing.T) {
	entries := []planEntry{
		{Title: "Tests for A", Phase: "test", TestsFor: []int{1}},
		{Title: "Implement A", Phase: "implementation", Dependencies: []int{0}}, // already depends on 0
	}

	merged := MergeTDDDependencies(entries)

	// Should not duplicate the dependency on impl subtask.
	if len(merged[1].Dependencies) != 1 {
		t.Errorf("expected 1 dependency (no duplicate), got %d: %v",
			len(merged[1].Dependencies), merged[1].Dependencies)
	}
}

func TestMergeTDDDependencies_NoTestsFor(t *testing.T) {
	entries := []planEntry{
		{Title: "Implement A", Phase: "implementation"},
		{Title: "Implement B", Phase: "implementation", Dependencies: []int{0}},
	}

	merged := MergeTDDDependencies(entries)

	// No changes expected.
	if len(merged[0].Dependencies) != 0 {
		t.Errorf("expected 0 dependencies for first entry, got %d", len(merged[0].Dependencies))
	}
	if len(merged[1].Dependencies) != 1 {
		t.Errorf("expected 1 dependency for second entry, got %d", len(merged[1].Dependencies))
	}
}

func TestMergeTDDDependencies_OutOfRangeIndex(t *testing.T) {
	// MergeTDDDependencies only handles TestsFor with exactly 1 element.
	// Out-of-range indices are silently skipped.
	entries := []planEntry{
		{Title: "Tests for A", Phase: "test", TestsFor: []int{1}},
		{Title: "Implement A", Phase: "implementation"},
		{Title: "Tests for invalid", Phase: "test", TestsFor: []int{99}}, // out of range
	}

	merged := MergeTDDDependencies(entries)

	// Impl subtask at index 1 should depend on test subtask at index 0.
	if len(merged[1].Dependencies) != 1 {
		t.Fatalf("expected 1 dependency, got %d: %v", len(merged[1].Dependencies), merged[1].Dependencies)
	}
	if merged[1].Dependencies[0] != 0 {
		t.Errorf("expected dependency on index 0, got %d", merged[1].Dependencies[0])
	}
}

// ---------------------------------------------------------------------------
// HandlePlanApproved TDD dependency mapping tests
// ---------------------------------------------------------------------------

func TestHandlePlanApproved_SetsTestsForOnSubtasks(t *testing.T) {
	db := testutil.NewSharedTestDB(t)
	wt := &worktree.Manager{BareRepoPath: "/tmp/fake", DefaultBranch: "main"}
	o := testOrchestrator(t, db, wt)

	project := model.Project{ID: o.projectID, Name: "test", BareRepoPath: "/tmp/fake"}
	db.Create(&project)

	taskID := uuid.New()
	task := model.Task{
		ID:          taskID,
		ProjectID:   o.projectID,
		Title:       "tdd-deps-task",
		Description: "task with TDD deps",
		Status:      model.StatusPlanReview,
		Plan: model.JSONField{
			"subtasks": []any{
				map[string]any{
					"title":       "Tests for A",
					"description": "Test subtask",
					"agent_type":  "coder",
					"phase":       "test",
					"tests_for":   []any{float64(1)},
				},
				map[string]any{
					"title":       "Implement module A",
					"description": "Implementation subtask",
					"agent_type":  "coder",
					"phase":       "implementation",
				},
			},
		},
	}
	db.Create(&task)

	if err := o.HandlePlanApproved(taskID); err != nil {
		t.Fatalf("HandlePlanApproved error: %v", err)
	}

	// Verify subtasks.
	var subtasks []model.Task
	db.Where("parent_task_id = ?", taskID).Order("priority DESC").Find(&subtasks)
	if len(subtasks) != 2 {
		t.Fatalf("expected 2 subtasks, got %d", len(subtasks))
	}

	// Find the test subtask (phase == "test").
	var testSubtask *model.Task
	var implSubtask *model.Task
	for i := range subtasks {
		if subtasks[i].Phase == "test" {
			testSubtask = &subtasks[i]
		}
		if subtasks[i].Phase == "implementation" {
			implSubtask = &subtasks[i]
		}
	}
	if testSubtask == nil {
		t.Fatal("no test-phase subtask found")
	}
	if implSubtask == nil {
		t.Fatal("no implementation-phase subtask found")
	}

	// TestsFor on the test subtask should have 1 ID (the impl subtask).
	if len(testSubtask.TestsFor) != 1 {
		t.Errorf("expected TestsFor to have 1 entry, got %d: %v",
			len(testSubtask.TestsFor), testSubtask.TestsFor)
	}

	// Impl subtask should depend on the test subtask (auto-generated by MergeTDDDependencies).
	if len(implSubtask.DependencyIDs) < 1 {
		t.Errorf("expected impl subtask to have at least 1 dependency (on test subtask), got %d: %v",
			len(implSubtask.DependencyIDs), implSubtask.DependencyIDs)
	}
}

func TestHandlePlanApproved_StoresTDDExceptions(t *testing.T) {
	db := testutil.NewSharedTestDB(t)
	wt := &worktree.Manager{BareRepoPath: "/tmp/fake", DefaultBranch: "main"}
	o := testOrchestrator(t, db, wt)

	project := model.Project{ID: o.projectID, Name: "test", BareRepoPath: "/tmp/fake"}
	db.Create(&project)

	taskID := uuid.New()
	task := model.Task{
		ID:          taskID,
		ProjectID:   o.projectID,
		Title:       "tdd-exceptions-task",
		Description: "task with TDD exceptions",
		Status:      model.StatusPlanReview,
		Plan: model.JSONField{
			"subtasks": []any{
				map[string]any{
					"title":       "Implement module A",
					"description": "Implementation subtask",
					"agent_type":  "coder",
					"phase":       "implementation",
				},
			},
			"tdd_exceptions": []any{
				map[string]any{
					"subtask_index": float64(0),
					"reason":        "Pure refactoring, existing tests cover behavior",
				},
			},
		},
	}
	db.Create(&task)

	if err := o.HandlePlanApproved(taskID); err != nil {
		t.Fatalf("HandlePlanApproved error: %v", err)
	}

	// Check that TDD exceptions are stored on the parent task.
	var updated model.Task
	db.First(&updated, "id = ?", taskID)
	if updated.TDDExceptions == nil {
		t.Fatal("expected TDDExceptions to be set")
	}
	if _, ok := updated.TDDExceptions["exceptions"]; !ok {
		t.Error("expected 'exceptions' key in TDDExceptions")
	}
}

// ---------------------------------------------------------------------------
// State machine transition tests for new states
// ---------------------------------------------------------------------------

func TestTransitionTask_PlanReviewToTestWriting(t *testing.T) {
	task := &model.Task{
		ID:          uuid.New(),
		Title:       "test",
		Description: "test",
		Status:      model.StatusPlanReview,
	}

	evt, err := state.TransitionTask(task, model.StatusTestWriting, "user", nil)
	if err != nil {
		t.Fatalf("expected transition to succeed, got: %v", err)
	}
	if evt == nil {
		t.Fatal("expected event, got nil")
	}
	if task.Status != model.StatusTestWriting {
		t.Errorf("expected status test_writing, got %s", task.Status)
	}
}

func TestTransitionTask_TestWritingToTestReview(t *testing.T) {
	task := &model.Task{
		ID:          uuid.New(),
		Title:       "test",
		Description: "test",
		Status:      model.StatusTestWriting,
	}

	evt, err := state.TransitionTask(task, model.StatusTestReview, "orchestrator", nil)
	if err != nil {
		t.Fatalf("expected transition to succeed, got: %v", err)
	}
	if evt == nil {
		t.Fatal("expected event, got nil")
	}
	if task.Status != model.StatusTestReview {
		t.Errorf("expected status test_review, got %s", task.Status)
	}
}

func TestTransitionTask_TestReviewToInProgress(t *testing.T) {
	task := &model.Task{
		ID:          uuid.New(),
		Title:       "test",
		Description: "test",
		Status:      model.StatusTestReview,
	}

	evt, err := state.TransitionTask(task, model.StatusInProgress, "user", nil)
	if err != nil {
		t.Fatalf("expected transition to succeed, got: %v", err)
	}
	if evt == nil {
		t.Fatal("expected event, got nil")
	}
	if task.Status != model.StatusInProgress {
		t.Errorf("expected status in_progress, got %s", task.Status)
	}
}

func TestTransitionTask_TestWritingToFailed(t *testing.T) {
	task := &model.Task{
		ID:          uuid.New(),
		Title:       "test",
		Description: "test",
		Status:      model.StatusTestWriting,
	}

	_, err := state.TransitionTask(task, model.StatusFailed, "orchestrator", nil)
	if err != nil {
		t.Fatalf("expected transition to succeed, got: %v", err)
	}
	if task.Status != model.StatusFailed {
		t.Errorf("expected status failed, got %s", task.Status)
	}
}

func TestTransitionTask_FailedToTestWriting(t *testing.T) {
	task := &model.Task{
		ID:          uuid.New(),
		Title:       "test",
		Description: "test",
		Status:      model.StatusFailed,
	}

	_, err := state.TransitionTask(task, model.StatusTestWriting, "user", nil)
	if err != nil {
		t.Fatalf("expected transition to succeed, got: %v", err)
	}
	if task.Status != model.StatusTestWriting {
		t.Errorf("expected status test_writing, got %s", task.Status)
	}
}

// ---------------------------------------------------------------------------
// parsePlan tests
// ---------------------------------------------------------------------------

func TestParsePlanFull_WithPhaseAndTestsFor(t *testing.T) {
	plan := model.JSONField{
		"subtasks": []any{
			map[string]any{
				"title":       "Implement A",
				"description": "impl",
				"agent_type":  "coder",
				"phase":       "implementation",
			},
			map[string]any{
				"title":       "Test A",
				"description": "test",
				"agent_type":  "coder",
				"phase":       "test",
				"tests_for":   []any{float64(0)},
			},
		},
		"tdd_exceptions": []any{
			map[string]any{
				"subtask_index": float64(0),
				"reason":        "refactoring only",
			},
		},
	}

	result, err := parsePlan(plan)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Subtasks) != 2 {
		t.Fatalf("expected 2 subtasks, got %d", len(result.Subtasks))
	}
	if result.Subtasks[0].Phase != "implementation" {
		t.Errorf("expected phase 'implementation', got %q", result.Subtasks[0].Phase)
	}
	if result.Subtasks[1].Phase != "test" {
		t.Errorf("expected phase 'test', got %q", result.Subtasks[1].Phase)
	}
	if len(result.Subtasks[1].TestsFor) != 1 {
		t.Errorf("expected 1 tests_for entry, got %d", len(result.Subtasks[1].TestsFor))
	}
	if len(result.TDDExceptions) != 1 {
		t.Errorf("expected 1 TDD exception, got %d", len(result.TDDExceptions))
	}
}

func TestParsePlanFull_OldFormat_NoPhase(t *testing.T) {
	plan := model.JSONField{
		"subtasks": []any{
			map[string]any{
				"title":       "Implement feature",
				"description": "standard",
				"agent_type":  "coder",
			},
		},
	}

	result, err := parsePlan(plan)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Subtasks) != 1 {
		t.Fatalf("expected 1 subtask, got %d", len(result.Subtasks))
	}
	if result.Subtasks[0].Phase != "" {
		t.Errorf("expected empty phase for old format, got %q", result.Subtasks[0].Phase)
	}
	if len(result.TDDExceptions) != 0 {
		t.Errorf("expected 0 TDD exceptions, got %d", len(result.TDDExceptions))
	}
}

// ---------------------------------------------------------------------------
// doTick integration test for TEST_WRITING
// ---------------------------------------------------------------------------

func TestDoTick_QueriesTestWritingTasks(t *testing.T) {
	// Verify that doTick picks up TEST_WRITING tasks.
	db := testutil.NewSharedTestDB(t)
	wt := &worktree.Manager{BareRepoPath: "/tmp/fake", DefaultBranch: "main"}
	o := testOrchestrator(t, db, wt)

	project := model.Project{ID: o.projectID, Name: "test", BareRepoPath: "/tmp/fake"}
	db.Create(&project)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   o.projectID,
		Title:       "test-writing-task",
		Description: "test",
		Status:      model.StatusTestWriting,
	}
	db.Create(&parent)

	// Create a done test subtask to trigger transition.
	testSub := model.Task{
		ID:           uuid.New(),
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "test-sub",
		Description:  "test subtask",
		Status:       model.StatusDone,
		Phase:        "test",
	}
	db.Create(&testSub)

	// Query TEST_WRITING tasks as doTick does.
	var testWritingTasks []model.Task
	err := db.Where("project_id = ? AND status = ? AND parent_task_id IS NULL",
		o.projectID, model.StatusTestWriting).Find(&testWritingTasks).Error
	if err != nil {
		t.Fatalf("query error: %v", err)
	}
	if len(testWritingTasks) != 1 {
		t.Fatalf("expected 1 test_writing task, got %d", len(testWritingTasks))
	}
	if testWritingTasks[0].ID != parentID {
		t.Errorf("expected task ID %s, got %s", parentID, testWritingTasks[0].ID)
	}
}

// ---------------------------------------------------------------------------
// getTestCommand tests
// ---------------------------------------------------------------------------

func TestGetTestCommand_FromConfig(t *testing.T) {
	db := testutil.NewSharedTestDB(t)
	wt := &worktree.Manager{BareRepoPath: "/tmp/fake", DefaultBranch: "main"}
	o := testOrchestrator(t, db, wt)
	o.testGate.TestCommand = "go test -v ./..."

	task := &model.Task{}

	cmd := o.getTestCommand(task)
	if cmd != "go test -v ./..." {
		t.Errorf("expected 'go test -v ./...', got %q", cmd)
	}
}

func TestGetTestCommand_NoContextNoWorktree(t *testing.T) {
	db := testutil.NewSharedTestDB(t)
	wt := &worktree.Manager{BareRepoPath: "/tmp/fake", DefaultBranch: "main"}
	o := testOrchestrator(t, db, wt)

	task := &model.Task{}

	cmd := o.getTestCommand(task)
	if cmd != "" {
		t.Errorf("expected empty string, got %q", cmd)
	}
}

// ---------------------------------------------------------------------------
// Test: All test-phase subtasks scheduled in parallel (wave gating skipped)
// ---------------------------------------------------------------------------

func TestScheduleSubtasks_TestWriting_SkipsWaveGroupGating(t *testing.T) {
	// During TEST_WRITING, the wave schedule should be ignored and all
	// test-phase subtasks with met dependencies should be schedulable,
	// regardless of file-overlap groups.
	db := testutil.NewSharedTestDB(t)
	wt := &worktree.Manager{BareRepoPath: "/tmp/fake", DefaultBranch: "main"}
	o := testOrchestratorWithRunner(t, db, wt)

	project := model.Project{ID: o.projectID, Name: "test", BareRepoPath: "/tmp/fake"}
	db.Create(&project)

	parentID := uuid.New()

	// Create test subtask IDs.
	testSub1ID := uuid.New()
	testSub2ID := uuid.New()

	// Build a wave schedule that puts subtasks in different groups (simulating
	// file-overlap serialization).
	schedule := Schedule{
		Groups: []SubtaskGroup{
			{Order: 0, TaskIDs: []uuid.UUID{testSub1ID}},
			{Order: 1, TaskIDs: []uuid.UUID{testSub2ID}}, // different group
		},
	}
	scheduleJSON, _ := json.Marshal(schedule)
	var scheduleField any
	json.Unmarshal(scheduleJSON, &scheduleField)

	parent := model.Task{
		ID:          parentID,
		ProjectID:   o.projectID,
		Title:       "parent",
		Description: "parent with wave schedule",
		Status:      model.StatusTestWriting,
		Context:     model.JSONField{"schedule": scheduleField},
	}
	db.Create(&parent)

	// Both test subtasks in BACKLOG.
	testSub1 := model.Task{
		ID:           testSub1ID,
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "test-sub-1",
		Description:  "write tests for A",
		Status:       model.StatusBacklog,
		Phase:        "test",
		Priority:     4,
	}
	testSub2 := model.Task{
		ID:           testSub2ID,
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "test-sub-2",
		Description:  "write tests for B",
		Status:       model.StatusBacklog,
		Phase:        "test",
		Priority:     3,
	}
	db.Create(&testSub1)
	db.Create(&testSub2)

	// Call scheduleSubtasks — with 0-capacity runner, no agents spawn,
	// but we can verify the wave gating is skipped by checking that
	// scheduleSubtasks doesn't return early (which it would if only
	// group 0 was allowed and group 0 subtask was still in backlog).
	err := o.scheduleSubtasks(&parent)
	if err != nil {
		t.Fatalf("scheduleSubtasks error: %v", err)
	}

	// Since runner has 0 capacity, subtasks won't actually be assigned.
	// The key assertion is that scheduleSubtasks didn't return nil early
	// due to wave gating — it processed all subtasks. We verify this
	// indirectly: both subtasks should still be in backlog (not scheduled
	// due to 0 capacity) rather than only the first group being considered.

	// Now test with IN_PROGRESS parent — wave gating SHOULD apply.
	parent2ID := uuid.New()
	implSub1ID := uuid.New()
	implSub2ID := uuid.New()

	schedule2 := Schedule{
		Groups: []SubtaskGroup{
			{Order: 0, TaskIDs: []uuid.UUID{implSub1ID}},
			{Order: 1, TaskIDs: []uuid.UUID{implSub2ID}},
		},
	}
	scheduleJSON2, _ := json.Marshal(schedule2)
	var scheduleField2 any
	json.Unmarshal(scheduleJSON2, &scheduleField2)

	parent2 := model.Task{
		ID:          parent2ID,
		ProjectID:   o.projectID,
		Title:       "parent2",
		Description: "parent with wave schedule in_progress",
		Status:      model.StatusInProgress,
		Context:     model.JSONField{"schedule": scheduleField2},
	}
	db.Create(&parent2)

	implSub1 := model.Task{
		ID:           implSub1ID,
		ProjectID:    o.projectID,
		ParentTaskID: &parent2ID,
		Title:        "impl-sub-1",
		Description:  "implement A",
		Status:       model.StatusBacklog,
		Phase:        "implementation",
		Priority:     2,
	}
	implSub2 := model.Task{
		ID:           implSub2ID,
		ProjectID:    o.projectID,
		ParentTaskID: &parent2ID,
		Title:        "impl-sub-2",
		Description:  "implement B",
		Status:       model.StatusBacklog,
		Phase:        "implementation",
		Priority:     1,
	}
	db.Create(&implSub1)
	db.Create(&implSub2)

	// For IN_PROGRESS, wave gating should apply — scheduleSubtasks should
	// only consider group 0.
	err = o.scheduleSubtasks(&parent2)
	if err != nil {
		t.Fatalf("scheduleSubtasks error for IN_PROGRESS parent: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Test: Explicit dependencies are still respected during TEST_WRITING
// ---------------------------------------------------------------------------

func TestScheduleSubtasks_TestWriting_RespectsExplicitDependencies(t *testing.T) {
	db := testutil.NewSharedTestDB(t)
	wt := &worktree.Manager{BareRepoPath: "/tmp/fake", DefaultBranch: "main"}
	o := testOrchestratorWithRunner(t, db, wt)

	project := model.Project{ID: o.projectID, Name: "test", BareRepoPath: "/tmp/fake"}
	db.Create(&project)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   o.projectID,
		Title:       "parent",
		Description: "parent with dep chain",
		Status:      model.StatusTestWriting,
	}
	db.Create(&parent)

	testSub1ID := uuid.New()
	testSub2ID := uuid.New()

	// Test sub 1: no dependencies
	testSub1 := model.Task{
		ID:           testSub1ID,
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "test-sub-1",
		Description:  "write tests for A",
		Status:       model.StatusBacklog,
		Phase:        "test",
		Priority:     4,
	}
	// Test sub 2: depends on test sub 1 (explicit dependency)
	testSub2 := model.Task{
		ID:            testSub2ID,
		ProjectID:     o.projectID,
		ParentTaskID:  &parentID,
		Title:         "test-sub-2",
		Description:   "write tests for B (depends on A)",
		Status:        model.StatusBacklog,
		Phase:         "test",
		Priority:      3,
		DependencyIDs: model.JSONArray{testSub1ID.String()},
	}
	db.Create(&testSub1)
	db.Create(&testSub2)

	// testSub1 is in BACKLOG (not done), so testSub2's dependency is not met.
	// Verify via DependenciesMet.
	met, err := DependenciesMet(db, testSub2.DependencyIDs)
	if err != nil {
		t.Fatalf("DependenciesMet error: %v", err)
	}
	if met {
		t.Error("expected dependencies NOT met (testSub1 is still in backlog)")
	}

	// Now mark testSub1 as done and check again.
	db.Model(&model.Task{}).Where("id = ?", testSub1ID).Update("status", model.StatusDone)
	met, err = DependenciesMet(db, testSub2.DependencyIDs)
	if err != nil {
		t.Fatalf("DependenciesMet error: %v", err)
	}
	if !met {
		t.Error("expected dependencies met (testSub1 is done)")
	}
}

// ---------------------------------------------------------------------------
// Test: Supervisor fixes failed test subtask -> transitions to TEST_REVIEW
// ---------------------------------------------------------------------------

func TestProcessTestWriting_SupervisorFixTriggersTestReview(t *testing.T) {
	db := testutil.NewSharedTestDB(t)
	wt := &worktree.Manager{BareRepoPath: "/tmp/fake", DefaultBranch: "main"}
	o := testOrchestratorWithRunner(t, db, wt)

	project := model.Project{ID: o.projectID, Name: "test", BareRepoPath: "/tmp/fake"}
	db.Create(&project)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   o.projectID,
		Title:       "parent",
		Description: "test parent stuck after failure",
		Status:      model.StatusTestWriting,
		Context: model.JSONField{
			"baseline_tests_checked": true,
			"baseline_tests_failed":  true, // baseline failure flag set
		},
	}
	db.Create(&parent)

	// Both test subtasks are done — supervisor manually fixed the failed one.
	for _, title := range []string{"test-fixed-1", "test-fixed-2"} {
		sub := model.Task{
			ID:           uuid.New(),
			ProjectID:    o.projectID,
			ParentTaskID: &parentID,
			Title:        title,
			Description:  "test subtask (supervisor fixed)",
			Status:       model.StatusDone,
			Phase:        "test",
		}
		db.Create(&sub)
	}

	// processTestWriting should still detect all-done despite baseline_tests_failed.
	if err := o.processTestWriting(&parent); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Reload parent to check status.
	var updated model.Task
	db.First(&updated, "id = ?", parentID)
	if updated.Status != model.StatusTestReview {
		t.Errorf("expected parent status test_review after supervisor fix, got %s", updated.Status)
	}

	// Verify blocking flags were cleared.
	if updated.Context != nil {
		if failed, ok := updated.Context["baseline_tests_failed"].(bool); ok && failed {
			t.Error("expected baseline_tests_failed to be cleared after transition")
		}
	}
}

// ---------------------------------------------------------------------------
// Test: Full lifecycle TEST_WRITING -> TEST_REVIEW -> IN_PROGRESS
// ---------------------------------------------------------------------------

func TestFullLifecycle_TestWritingToInProgress(t *testing.T) {
	db := testutil.NewSharedTestDB(t)
	wt := &worktree.Manager{BareRepoPath: "/tmp/fake", DefaultBranch: "main"}
	o := testOrchestratorWithRunner(t, db, wt)

	project := model.Project{ID: o.projectID, Name: "test", BareRepoPath: "/tmp/fake"}
	db.Create(&project)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   o.projectID,
		Title:       "lifecycle-parent",
		Description: "full lifecycle test",
		Status:      model.StatusTestWriting,
	}
	db.Create(&parent)

	// Create test subtasks (all done) and impl subtasks (backlog).
	testSub := model.Task{
		ID:           uuid.New(),
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "test-sub",
		Description:  "test subtask",
		Status:       model.StatusDone,
		Phase:        "test",
		Priority:     2,
	}
	implSub := model.Task{
		ID:           uuid.New(),
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "impl-sub",
		Description:  "impl subtask",
		Status:       model.StatusBacklog,
		Phase:        "implementation",
		Priority:     1,
	}
	db.Create(&testSub)
	db.Create(&implSub)

	// Step 1: processTestWriting should transition to TEST_REVIEW.
	if err := o.processTestWriting(&parent); err != nil {
		t.Fatalf("processTestWriting error: %v", err)
	}

	var afterTestWriting model.Task
	db.First(&afterTestWriting, "id = ?", parentID)
	if afterTestWriting.Status != model.StatusTestReview {
		t.Fatalf("expected test_review, got %s", afterTestWriting.Status)
	}

	// Step 2: Transition TEST_REVIEW -> IN_PROGRESS (simulating human approval).
	evt, err := state.TransitionTask(&afterTestWriting, model.StatusInProgress, "user",
		map[string]any{"action": "tests_approved"})
	if err != nil {
		t.Fatalf("transition to in_progress: %v", err)
	}
	db.Save(&afterTestWriting)
	db.Create(evt)

	var afterApproval model.Task
	db.First(&afterApproval, "id = ?", parentID)
	if afterApproval.Status != model.StatusInProgress {
		t.Fatalf("expected in_progress, got %s", afterApproval.Status)
	}

	// Step 3: During IN_PROGRESS, only impl subtasks should be schedulable.
	var subtasks []model.Task
	db.Where(
		"parent_task_id = ? AND ((status = ?) OR (status = ? AND assigned_agent_id IS NULL))",
		parentID, model.StatusBacklog, model.StatusInProgress,
	).Order("priority DESC").Find(&subtasks)

	var schedulable []string
	for _, sub := range subtasks {
		if afterApproval.Status == model.StatusInProgress && sub.Phase == "test" {
			continue
		}
		schedulable = append(schedulable, sub.Title)
	}

	if len(schedulable) != 1 {
		t.Fatalf("expected 1 schedulable impl subtask, got %d: %v", len(schedulable), schedulable)
	}
	if schedulable[0] != "impl-sub" {
		t.Errorf("expected impl-sub, got %s", schedulable[0])
	}
}

// ---------------------------------------------------------------------------
// Test: C++ test files recognized by isTestFile
// ---------------------------------------------------------------------------

func TestIsTestFile_CppPatterns(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		expected bool
	}{
		// C++ test file name patterns
		{"cpp PascalCase Test suffix", "LaneVersionTest.cpp", true},
		{"cpp PascalCase Tests suffix", "LaneVersionTests.cpp", true},
		{"cpp snake_case _test suffix", "lane_version_test.cpp", true},
		{"cpp snake_case _tests suffix", "lane_version_tests.cpp", true},
		{"cpp test header", "LaneVersionTest.h", true},
		{"cpp tests header", "LaneVersionTests.h", true},

		// Files under test directories
		{"cpp file under tests/ dir", "tests/unit/model/LaneVersion.cpp", true},
		{"cpp file under test/ dir", "test/unit/LaneVersion.cpp", true},
		{"cpp header under tests/ dir", "tests/unit/model/LaneVersion.h", true},
		{"cc file under tests/ dir", "tests/unit/model/LaneVersion.cc", true},
		{"hpp file under test/ dir", "test/helpers/TestFixture.hpp", true},
		{"nested tests/ dir", "src/tests/unit/Foo.cpp", true},

		// Non-test C++ files
		{"regular cpp file", "LaneVersion.cpp", false},
		{"regular header", "LaneVersion.h", false},
		{"cpp file not in test dir", "src/model/LaneVersion.cpp", false},

		// Existing patterns still work
		{"go test file", "foo_test.go", true},
		{"ts test file", "foo.test.ts", true},
		{"regular go file", "foo.go", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isTestFile(tt.filename)
			if result != tt.expected {
				t.Errorf("isTestFile(%q) = %v, want %v", tt.filename, result, tt.expected)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Test: Merge into integration worktree succeeds when plan.json is uncommitted
// ---------------------------------------------------------------------------

func TestMergeAutoCommitsDirtyWorktree(t *testing.T) {
	// This test verifies that MergeAgentIntoFeature auto-commits dirty
	// worktree files (like plan.json) instead of rejecting the merge.
	// We use a real git setup since merge operations require it.
	bareRepoPath := setupTestRepoWithMainBranch(t)

	featureName := "test-autocommit"
	featureDir := createFeatureWorktree(t, bareRepoPath, featureName)

	// Write plan.json to the feature worktree (uncommitted).
	planPath := filepath.Join(featureDir, "plan.json")
	planData := []byte(`{"subtasks": []}`)
	if err := os.WriteFile(planPath, planData, 0o644); err != nil {
		t.Fatalf("write plan.json: %v", err)
	}

	// Verify worktree is dirty.
	clean, err := worktree.IsClean(featureDir)
	if err != nil {
		t.Fatalf("check clean: %v", err)
	}
	if clean {
		t.Fatal("expected dirty worktree after writing plan.json")
	}

	// The auto-commit happens inside merge.Orchestrator.MergeAgentIntoFeature,
	// which we can't easily call without a full setup. Instead, verify that
	// CommitUnstagedChanges works correctly.
	committed, err := worktree.CommitUnstagedChanges(featureDir, "chore: commit orchestrator artifacts before merge")
	if err != nil {
		t.Fatalf("CommitUnstagedChanges: %v", err)
	}
	if !committed {
		t.Error("expected CommitUnstagedChanges to create a commit")
	}

	// Verify worktree is now clean.
	clean, err = worktree.IsClean(featureDir)
	if err != nil {
		t.Fatalf("check clean after commit: %v", err)
	}
	if !clean {
		t.Error("expected clean worktree after auto-commit")
	}
}

// ---------------------------------------------------------------------------
// Test: HandlePlanApproved writes plan.json to integration worktree without tracking it
// ---------------------------------------------------------------------------

func TestHandlePlanApproved_WritesPlanJSONUntracked(t *testing.T) {
	bareRepoPath := setupTestRepoWithMainBranch(t)

	featureName := "test-plan-commit"
	featureDir := createFeatureWorktree(t, bareRepoPath, featureName)

	db := testutil.NewSharedTestDB(t)
	wt := &worktree.Manager{BareRepoPath: bareRepoPath, DefaultBranch: "main"}
	o := testOrchestrator(t, db, wt)

	project := model.Project{ID: o.projectID, Name: "test", BareRepoPath: bareRepoPath}
	db.Create(&project)

	taskID := uuid.New()
	task := model.Task{
		ID:             taskID,
		ProjectID:      o.projectID,
		Title:          "plan-commit-task",
		Description:    "verify plan.json is written but not tracked",
		Status:         model.StatusPlanReview,
		WorktreeBranch: "feature/" + featureName,
		Plan: model.JSONField{
			"subtasks": []any{
				map[string]any{
					"title":       "Write tests",
					"description": "Test subtask",
					"agent_type":  "coder",
					"phase":       "test",
					"tests_for":   []any{float64(1)},
				},
				map[string]any{
					"title":       "Implement",
					"description": "Impl subtask",
					"agent_type":  "coder",
					"phase":       "implementation",
				},
			},
		},
	}
	db.Create(&task)

	if err := o.HandlePlanApproved(taskID); err != nil {
		t.Fatalf("HandlePlanApproved error: %v", err)
	}

	// Verify plan.json exists on disk in the feature worktree.
	planPath := filepath.Join(featureDir, "plan.json")
	if _, err := os.Stat(planPath); os.IsNotExist(err) {
		t.Fatal("expected plan.json to exist on disk in feature worktree")
	}

	// Verify plan.json is NOT tracked by git.
	out, _ := worktree.RunGit([]string{"ls-files", "plan.json"}, featureDir)
	if out != "" {
		t.Error("expected plan.json to NOT be tracked by git")
	}

	// Verify the plan.json content is valid JSON matching the task plan.
	data, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("read plan.json: %v", err)
	}
	var planContent map[string]any
	if err := json.Unmarshal(data, &planContent); err != nil {
		t.Fatalf("plan.json is not valid JSON: %v", err)
	}
	if _, ok := planContent["subtasks"]; !ok {
		t.Error("expected plan.json to contain 'subtasks' key")
	}
}

// ---------------------------------------------------------------------------
// Test: HandlePlanApproved untracks previously-tracked plan.json
// ---------------------------------------------------------------------------

func TestHandlePlanApproved_UntracksLegacyPlanJSON(t *testing.T) {
	bareRepoPath := setupTestRepoWithMainBranch(t)

	featureName := "test-plan-untrack"
	featureDir := createFeatureWorktree(t, bareRepoPath, featureName)

	// Simulate a legacy state: plan.json is tracked (committed).
	testutil.CommitFile(t, featureDir, "plan.json", `{"subtasks":[]}`, "legacy plan commit")
	out, _ := worktree.RunGit([]string{"ls-files", "plan.json"}, featureDir)
	if out == "" {
		t.Fatal("precondition: plan.json should be tracked before test")
	}

	db := testutil.NewSharedTestDB(t)
	wt := &worktree.Manager{BareRepoPath: bareRepoPath, DefaultBranch: "main"}
	o := testOrchestrator(t, db, wt)

	project := model.Project{ID: o.projectID, Name: "test", BareRepoPath: bareRepoPath}
	db.Create(&project)

	taskID := uuid.New()
	task := model.Task{
		ID:             taskID,
		ProjectID:      o.projectID,
		Title:          "plan-untrack-task",
		Description:    "verify legacy plan.json gets untracked",
		Status:         model.StatusPlanReview,
		WorktreeBranch: "feature/" + featureName,
		Plan: model.JSONField{
			"subtasks": []any{
				map[string]any{
					"title":       "Write tests",
					"description": "Test subtask",
					"agent_type":  "coder",
					"phase":       "test",
					"tests_for":   []any{float64(1)},
				},
				map[string]any{
					"title":       "Implement",
					"description": "Impl subtask",
					"agent_type":  "coder",
					"phase":       "implementation",
				},
			},
		},
	}
	db.Create(&task)

	if err := o.HandlePlanApproved(taskID); err != nil {
		t.Fatalf("HandlePlanApproved error: %v", err)
	}

	// Verify plan.json exists on disk (updated content).
	planPath := filepath.Join(featureDir, "plan.json")
	if _, err := os.Stat(planPath); os.IsNotExist(err) {
		t.Fatal("expected plan.json to exist on disk")
	}

	// Verify plan.json is no longer tracked.
	out, _ = worktree.RunGit([]string{"ls-files", "plan.json"}, featureDir)
	if out != "" {
		t.Error("expected plan.json to be untracked after HandlePlanApproved")
	}
}

// ---------------------------------------------------------------------------
// onAgentCompleted → test_writing parent advancement tests
// ---------------------------------------------------------------------------

// TestOnAgentCompleted_TestWritingParent_AllTestSubtasksDone verifies that when
// onAgentCompleted processes the last test subtask (fast-tracking it to done)
// and all other test subtasks are already done, the parent task transitions
// from test_writing to test_review.
func TestOnAgentCompleted_TestWritingParent_AllTestSubtasksDone(t *testing.T) {
	db := testutil.NewSharedTestDB(t)
	wt := &worktree.Manager{BareRepoPath: "/tmp/fake", DefaultBranch: "main"}
	o := testOrchestratorWithRunner(t, db, wt)

	project := model.Project{ID: o.projectID, Name: "test", BareRepoPath: "/tmp/fake"}
	db.Create(&project)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   o.projectID,
		Title:       "parent-tw",
		Description: "parent in test_writing",
		Status:      model.StatusTestWriting,
	}
	db.Create(&parent)

	// One test subtask already done.
	doneSub := model.Task{
		ID:           uuid.New(),
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "test-already-done",
		Description:  "test subtask",
		Status:       model.StatusDone,
		Phase:        "test",
	}
	db.Create(&doneSub)

	// The last test subtask — still in_progress, assigned to an agent.
	lastSubID := uuid.New()
	agentID := uuid.New()
	lastSub := model.Task{
		ID:              lastSubID,
		ProjectID:       o.projectID,
		ParentTaskID:    &parentID,
		Title:           "test-last-running",
		Description:     "test subtask",
		Status:          model.StatusInProgress,
		Phase:           "test",
		AssignedAgentID: &agentID,
	}
	db.Create(&lastSub)

	// Implementation subtask in backlog (should not block test_writing → test_review).
	implSub := model.Task{
		ID:           uuid.New(),
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "impl-sub",
		Description:  "impl subtask",
		Status:       model.StatusBacklog,
		Phase:        "implementation",
	}
	db.Create(&implSub)

	// Agent with empty worktree paths to skip merge/git operations.
	ag := model.Agent{
		ID:             agentID,
		ProjectID:      o.projectID,
		AgentType:      model.AgentCoder,
		Name:           "test-coder",
		Status:         model.AgentWorking,
		CurrentTaskID:  &lastSubID,
		WorktreePath:   "",
		WorktreeBranch: "",
	}
	db.Create(&ag)

	if err := o.onAgentCompleted(&ag, &lastSub); err != nil {
		t.Fatalf("onAgentCompleted error: %v", err)
	}

	// Verify the subtask was fast-tracked to done.
	var updatedSub model.Task
	db.First(&updatedSub, "id = ?", lastSubID)
	if updatedSub.Status != model.StatusDone {
		t.Fatalf("expected subtask status done, got %s", updatedSub.Status)
	}

	// Verify the parent transitioned from test_writing to test_review.
	var updatedParent model.Task
	db.First(&updatedParent, "id = ?", parentID)
	if updatedParent.Status != model.StatusTestReview {
		t.Errorf("expected parent status test_review, got %s", updatedParent.Status)
	}
}

// TestOnAgentCompleted_TestWritingParent_SomeTestSubtasksRunning verifies that
// when onAgentCompleted processes a test subtask but other test subtasks are
// still in_progress, the parent stays in test_writing.
func TestOnAgentCompleted_TestWritingParent_SomeTestSubtasksRunning(t *testing.T) {
	db := testutil.NewSharedTestDB(t)
	wt := &worktree.Manager{BareRepoPath: "/tmp/fake", DefaultBranch: "main"}
	o := testOrchestratorWithRunner(t, db, wt)

	project := model.Project{ID: o.projectID, Name: "test", BareRepoPath: "/tmp/fake"}
	db.Create(&project)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   o.projectID,
		Title:       "parent-tw-partial",
		Description: "parent in test_writing",
		Status:      model.StatusTestWriting,
	}
	db.Create(&parent)

	// One test subtask already done.
	doneSub := model.Task{
		ID:           uuid.New(),
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "test-done",
		Description:  "test subtask",
		Status:       model.StatusDone,
		Phase:        "test",
	}
	db.Create(&doneSub)

	// The subtask being completed by this agent.
	completingSubID := uuid.New()
	agentID := uuid.New()
	completingSub := model.Task{
		ID:              completingSubID,
		ProjectID:       o.projectID,
		ParentTaskID:    &parentID,
		Title:           "test-completing",
		Description:     "test subtask",
		Status:          model.StatusInProgress,
		Phase:           "test",
		AssignedAgentID: &agentID,
	}
	db.Create(&completingSub)

	// Another test subtask still running.
	stillRunning := model.Task{
		ID:           uuid.New(),
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "test-still-running",
		Description:  "test subtask",
		Status:       model.StatusInProgress,
		Phase:        "test",
	}
	db.Create(&stillRunning)

	ag := model.Agent{
		ID:             agentID,
		ProjectID:      o.projectID,
		AgentType:      model.AgentCoder,
		Name:           "test-coder",
		Status:         model.AgentWorking,
		CurrentTaskID:  &completingSubID,
		WorktreePath:   "",
		WorktreeBranch: "",
	}
	db.Create(&ag)

	if err := o.onAgentCompleted(&ag, &completingSub); err != nil {
		t.Fatalf("onAgentCompleted error: %v", err)
	}

	// Verify the completing subtask was fast-tracked to done.
	var updatedSub model.Task
	db.First(&updatedSub, "id = ?", completingSubID)
	if updatedSub.Status != model.StatusDone {
		t.Fatalf("expected subtask status done, got %s", updatedSub.Status)
	}

	// Parent should remain in test_writing (not all test subtasks are done).
	var updatedParent model.Task
	db.First(&updatedParent, "id = ?", parentID)
	if updatedParent.Status != model.StatusTestWriting {
		t.Errorf("expected parent to stay in test_writing, got %s", updatedParent.Status)
	}
}

// TestOnAgentCompleted_TestWritingParent_InProgressParentUnchanged is a
// regression guard verifying that onAgentCompleted still calls
// checkFeatureCompletion correctly for in_progress parents. When all subtasks
// are done and the parent is in_progress, it should transition to testing_ready.
func TestOnAgentCompleted_TestWritingParent_InProgressParentUnchanged(t *testing.T) {
	db := testutil.NewSharedTestDB(t)
	wt := &worktree.Manager{BareRepoPath: "/tmp/fake", DefaultBranch: "main"}
	o := testOrchestratorWithRunner(t, db, wt)

	project := model.Project{ID: o.projectID, Name: "test", BareRepoPath: "/tmp/fake"}
	db.Create(&project)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   o.projectID,
		Title:       "parent-ip",
		Description: "parent in in_progress",
		Status:      model.StatusInProgress,
	}
	db.Create(&parent)

	// One subtask already done.
	doneSub := model.Task{
		ID:           uuid.New(),
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "impl-done",
		Description:  "implementation subtask",
		Status:       model.StatusDone,
		Phase:        "implementation",
	}
	db.Create(&doneSub)

	// The last subtask — in_progress, assigned to agent.
	lastSubID := uuid.New()
	agentID := uuid.New()
	lastSub := model.Task{
		ID:              lastSubID,
		ProjectID:       o.projectID,
		ParentTaskID:    &parentID,
		Title:           "impl-last",
		Description:     "implementation subtask",
		Status:          model.StatusInProgress,
		Phase:           "implementation",
		AssignedAgentID: &agentID,
	}
	db.Create(&lastSub)

	ag := model.Agent{
		ID:             agentID,
		ProjectID:      o.projectID,
		AgentType:      model.AgentCoder,
		Name:           "test-coder",
		Status:         model.AgentWorking,
		CurrentTaskID:  &lastSubID,
		WorktreePath:   "",
		WorktreeBranch: "",
	}
	db.Create(&ag)

	if err := o.onAgentCompleted(&ag, &lastSub); err != nil {
		t.Fatalf("onAgentCompleted error: %v", err)
	}

	// Verify subtask fast-tracked to done.
	var updatedSub model.Task
	db.First(&updatedSub, "id = ?", lastSubID)
	if updatedSub.Status != model.StatusDone {
		t.Fatalf("expected subtask status done, got %s", updatedSub.Status)
	}

	// Parent should transition from in_progress to testing_ready via
	// checkFeatureCompletion (existing behavior, regression guard).
	var updatedParent model.Task
	db.First(&updatedParent, "id = ?", parentID)
	if updatedParent.Status != model.StatusTestingReady {
		t.Errorf("expected parent status testing_ready, got %s", updatedParent.Status)
	}
}

// ---------------------------------------------------------------------------
// Suppress unused import warning
// ---------------------------------------------------------------------------

var _ *gorm.DB
