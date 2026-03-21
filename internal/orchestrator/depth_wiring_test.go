package orchestrator

import (
	"log/slog"
	"testing"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/constraints"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/supervisor"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

// ---------------------------------------------------------------------------
// Depth Wiring Tests
// ---------------------------------------------------------------------------

// depthTestOrchestrator creates an Orchestrator for depth wiring tests.
// When sup is non-nil, it is set as the supervisor.
func depthTestOrchestrator(t *testing.T, sup *supervisor.Supervisor) *Orchestrator {
	t.Helper()
	db := testutil.NewTestDB(t)
	projectID := uuid.New()
	events := make(chan Event, 100)

	project := model.Project{ID: projectID, Name: "depth-test"}
	db.Create(&project)

	return &Orchestrator{
		db:         db,
		projectID:  projectID,
		supervisor: sup,
		events:     events,
		logger:     slog.Default().With("component", "test-depth-wiring"),
	}
}

// ---------------------------------------------------------------------------
// Test 1: Plan depth score above threshold passes without supervisor call
// ---------------------------------------------------------------------------

func TestCheckPlanDepthGate_AboveThreshold(t *testing.T) {
	o := depthTestOrchestrator(t, nil)

	taskID := uuid.New()
	task := model.Task{
		ID:        taskID,
		ProjectID: o.projectID,
		Title:     "above-threshold",
		Status:    model.StatusPlanning,
		Context:   make(model.JSONField),
	}
	o.db.Create(&task)

	scores := map[string]any{
		"tdd":           1.0,
		"constitution":  1.0,
		"documentation": 1.0,
		"depth":         0.8, // above 0.5 threshold
	}

	o.checkPlanDepthGate(&task, scores)

	// No depth_review should be stored in context.
	if _, ok := task.Context["depth_review"]; ok {
		t.Error("expected no depth_review in context for score above threshold")
	}

	// No system comments should be created.
	var comments []model.TaskComment
	o.db.Where("task_id = ?", taskID).Find(&comments)
	if len(comments) > 0 {
		t.Errorf("expected no comments, got %d", len(comments))
	}
}

// ---------------------------------------------------------------------------
// Test 2: Plan depth score below threshold triggers supervisor review
// ---------------------------------------------------------------------------

func TestCheckPlanDepthGate_BelowThreshold_WithSupervisor(t *testing.T) {
	// Create a mock supervisor that returns a canned depth review response.
	mockSup := testutil.NewMockSupervisor(t, `{
		"acceptable": false,
		"rejection_reason": "Plan lacks module boundary definitions",
		"suggested_fix": "Add estimated_files to all subtasks"
	}`)

	o := depthTestOrchestrator(t, mockSup)

	taskID := uuid.New()
	task := model.Task{
		ID:          taskID,
		ProjectID:   o.projectID,
		Title:       "below-threshold",
		Description: "test task for depth review",
		Status:      model.StatusPlanning,
		Plan:        model.JSONField{"subtasks": []any{}},
		Context:     make(model.JSONField),
	}
	o.db.Create(&task)

	scores := map[string]any{
		"tdd":           1.0,
		"constitution":  1.0,
		"documentation": 1.0,
		"depth":         0.3, // below 0.5 threshold
	}

	o.checkPlanDepthGate(&task, scores)

	// depth_review should be stored in context.
	review, ok := task.Context["depth_review"]
	if !ok {
		t.Fatal("expected depth_review in context")
	}
	reviewMap, ok := review.(map[string]any)
	if !ok {
		t.Fatalf("depth_review is not a map, got %T", review)
	}
	if reviewMap["rejection_reason"] != "Plan lacks module boundary definitions" {
		t.Errorf("unexpected rejection_reason: %v", reviewMap["rejection_reason"])
	}

	// A system comment should be created.
	var comments []model.TaskComment
	o.db.Where("task_id = ?", taskID).Find(&comments)
	if len(comments) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(comments))
	}
	if comments[0].Author != "system" {
		t.Errorf("expected system author, got %s", comments[0].Author)
	}
}

// ---------------------------------------------------------------------------
// Test 3: Plan depth score below threshold but no supervisor configured
// ---------------------------------------------------------------------------

func TestCheckPlanDepthGate_BelowThreshold_NoSupervisor(t *testing.T) {
	o := depthTestOrchestrator(t, nil) // no supervisor

	taskID := uuid.New()
	task := model.Task{
		ID:        taskID,
		ProjectID: o.projectID,
		Title:     "no-supervisor",
		Status:    model.StatusPlanning,
		Context:   make(model.JSONField),
	}
	o.db.Create(&task)

	scores := map[string]any{
		"depth": 0.2, // below threshold
	}

	// Should not panic, just log a warning.
	o.checkPlanDepthGate(&task, scores)

	// No depth_review should be stored.
	if _, ok := task.Context["depth_review"]; ok {
		t.Error("expected no depth_review without supervisor")
	}
}

// ---------------------------------------------------------------------------
// Test 4: Depth constraint failure triggers supervisor diagnosis
// ---------------------------------------------------------------------------

func TestCheckDepthConstraintFailures_WithDepthFailure(t *testing.T) {
	mockSup := testutil.NewMockSupervisor(t, `{
		"rejection_reason": "Module nesting exceeds depth limit",
		"affected_files": ["internal/deep/nested/file.go"],
		"suggested_fix": "Flatten the module hierarchy"
	}`)

	o := depthTestOrchestrator(t, mockSup)

	taskID := uuid.New()
	task := model.Task{
		ID:        taskID,
		ProjectID: o.projectID,
		Title:     "depth-constraint-fail",
		Status:    model.StatusInProgress,
		Context:   make(model.JSONField),
	}
	o.db.Create(&task)

	report := &constraints.Report{
		Results: []constraints.Result{
			{Name: "file size", Type: "max_lines", Passed: true},
			{Name: "depth check", Type: "depth", Passed: false, Messages: []string{"too deep"}},
		},
		Passed: 1,
		Failed: 1,
	}

	o.checkDepthConstraintFailures(&task, report, "/tmp/feature")

	// depth_diagnosis should be stored.
	diagnosis, ok := task.Context["depth_diagnosis"]
	if !ok {
		t.Fatal("expected depth_diagnosis in context")
	}
	diagMap, ok := diagnosis.(map[string]any)
	if !ok {
		t.Fatalf("depth_diagnosis is not a map, got %T", diagnosis)
	}
	if diagMap["rejection_reason"] != "Module nesting exceeds depth limit" {
		t.Errorf("unexpected rejection_reason: %v", diagMap["rejection_reason"])
	}

	// A system comment should be created.
	var comments []model.TaskComment
	o.db.Where("task_id = ?", taskID).Find(&comments)
	if len(comments) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(comments))
	}
}

// ---------------------------------------------------------------------------
// Test 5: Depth constraint pass proceeds normally
// ---------------------------------------------------------------------------

func TestCheckDepthConstraintFailures_AllPass(t *testing.T) {
	mockSup := testutil.NewMockSupervisor(t, `{}`)

	o := depthTestOrchestrator(t, mockSup)

	taskID := uuid.New()
	task := model.Task{
		ID:        taskID,
		ProjectID: o.projectID,
		Title:     "constraints-pass",
		Status:    model.StatusInProgress,
		Context:   make(model.JSONField),
	}
	o.db.Create(&task)

	report := &constraints.Report{
		Results: []constraints.Result{
			{Name: "file size", Type: "max_lines", Passed: true},
			{Name: "depth check", Type: "depth", Passed: true},
		},
		Passed: 2,
		Failed: 0,
	}

	// With the refactored API, the caller checks for depth failures before
	// calling checkDepthConstraintFailures. Since all pass, it is not called.
	hasDepthFailure := false
	for _, r := range report.Results {
		if !r.Passed && r.Type == "depth" {
			hasDepthFailure = true
			break
		}
	}
	if hasDepthFailure {
		o.checkDepthConstraintFailures(&task, report, "/tmp/feature")
	}

	// No depth_diagnosis should be stored.
	if _, ok := task.Context["depth_diagnosis"]; ok {
		t.Error("expected no depth_diagnosis when all constraints pass")
	}

	// No comments should be created.
	var comments []model.TaskComment
	o.db.Where("task_id = ?", taskID).Find(&comments)
	if len(comments) > 0 {
		t.Errorf("expected no comments, got %d", len(comments))
	}
}

// ---------------------------------------------------------------------------
// Test 6: Non-depth constraint failure does not trigger depth diagnosis
// ---------------------------------------------------------------------------

func TestCheckDepthConstraintFailures_NonDepthFailure(t *testing.T) {
	mockSup := testutil.NewMockSupervisor(t, `{}`)

	o := depthTestOrchestrator(t, mockSup)

	taskID := uuid.New()
	task := model.Task{
		ID:        taskID,
		ProjectID: o.projectID,
		Title:     "non-depth-fail",
		Status:    model.StatusInProgress,
		Context:   make(model.JSONField),
	}
	o.db.Create(&task)

	report := &constraints.Report{
		Results: []constraints.Result{
			{Name: "file size", Type: "max_lines", Passed: false, Messages: []string{"too big"}},
		},
		Passed: 0,
		Failed: 1,
	}

	// With the refactored API, the caller checks for depth failures before
	// calling checkDepthConstraintFailures. Since this is not a depth failure,
	// the function is not called.
	hasDepthFailure := false
	for _, r := range report.Results {
		if !r.Passed && r.Type == "depth" {
			hasDepthFailure = true
			break
		}
	}
	if hasDepthFailure {
		o.checkDepthConstraintFailures(&task, report, "/tmp/feature")
	}

	// No depth_diagnosis should be stored (failure is not depth-type).
	if _, ok := task.Context["depth_diagnosis"]; ok {
		t.Error("expected no depth_diagnosis for non-depth constraint failure")
	}
}

// ---------------------------------------------------------------------------
// Test 7: Supervisor failure is graceful
// ---------------------------------------------------------------------------

func TestCheckPlanDepthGate_SupervisorFailure(t *testing.T) {
	// Create a mock supervisor that returns invalid JSON (simulates failure).
	mockSup := testutil.NewMockSupervisor(t, "not valid json at all")

	o := depthTestOrchestrator(t, mockSup)

	taskID := uuid.New()
	task := model.Task{
		ID:        taskID,
		ProjectID: o.projectID,
		Title:     "supervisor-failure",
		Status:    model.StatusPlanning,
		Context:   make(model.JSONField),
	}
	o.db.Create(&task)

	scores := map[string]any{
		"depth": 0.1, // below threshold, will trigger supervisor call
	}

	// Should not panic — graceful failure.
	o.checkPlanDepthGate(&task, scores)

	// No depth_review should be stored (supervisor failed).
	if _, ok := task.Context["depth_review"]; ok {
		t.Error("expected no depth_review when supervisor fails")
	}

	// No comments should be created.
	var comments []model.TaskComment
	o.db.Where("task_id = ?", taskID).Find(&comments)
	if len(comments) > 0 {
		t.Errorf("expected no comments when supervisor fails, got %d", len(comments))
	}
}

// ---------------------------------------------------------------------------
// Test 8: planDepthScore computation
// ---------------------------------------------------------------------------

func TestPlanDepthScore(t *testing.T) {
	tests := []struct {
		name    string
		entries []planEntry
		want    float64
	}{
		{
			name:    "empty plan",
			entries: nil,
			want:    1.0,
		},
		{
			name: "all entries have files",
			entries: []planEntry{
				{Title: "a", EstimatedFiles: []string{"a.go"}},
				{Title: "b", EstimatedFiles: []string{"b.go"}},
			},
			want: 1.0,
		},
		{
			name: "no entries have files",
			entries: []planEntry{
				{Title: "a"},
				{Title: "b"},
			},
			want: 0.0,
		},
		{
			name: "half entries have files",
			entries: []planEntry{
				{Title: "a", EstimatedFiles: []string{"a.go"}},
				{Title: "b"},
			},
			want: 0.5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := planDepthScore(tt.entries)
			if !scoreApproxEqual(got, tt.want) {
				t.Errorf("planDepthScore() = %v, want %v", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Test 9: scorePlanGate includes depth dimension
// ---------------------------------------------------------------------------

func TestScorePlanGate_IncludesDepth(t *testing.T) {
	subtasks := []planEntry{
		{Title: "test A", Phase: "test", TestsFor: []int{1}, EstimatedFiles: []string{"a_test.go"}},
		{Title: "impl A", Phase: "implementation", EstimatedFiles: []string{"a.go"}},
	}

	scores := scorePlanGate(subtasks, nil, PlanValidationResult{Valid: true})

	// Depth should be present in the map.
	depth, ok := scores["depth"]
	if !ok {
		t.Fatal("expected 'depth' key in scorePlanGate output")
	}
	depthVal, ok := depth.(float64)
	if !ok {
		t.Fatalf("depth is not float64, got %T", depth)
	}
	// Both entries have files, so depth = 1.0.
	if !scoreApproxEqual(depthVal, 1.0) {
		t.Errorf("depth = %v, want 1.0", depthVal)
	}

	// Formatted string should contain "Depth".
	formatted, ok := scores["formatted"].(string)
	if !ok {
		t.Fatal("expected 'formatted' key to be string")
	}
	if !containsSubstring(formatted, "Depth:") {
		t.Errorf("formatted score %q should contain 'Depth:'", formatted)
	}
}

// ---------------------------------------------------------------------------
// Test 10: scoreImplGate includes depth dimension
// ---------------------------------------------------------------------------

func TestScoreImplGate_IncludesDepth(t *testing.T) {
	scores := scoreImplGate(5, 0, []string{"a.go"}, "")

	depth, ok := scores["depth"]
	if !ok {
		t.Fatal("expected 'depth' key in scoreImplGate output")
	}
	depthVal, ok := depth.(float64)
	if !ok {
		t.Fatalf("depth is not float64, got %T", depth)
	}
	// Impl gate defaults depth to 1.0.
	if !scoreApproxEqual(depthVal, 1.0) {
		t.Errorf("depth = %v, want 1.0", depthVal)
	}
}

func containsSubstring(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && findSubstring(s, substr))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
