package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

func TestStandardParent_FailedChildDoesNotAdvance(t *testing.T) {
	orch, db, _ := setupReconcileTest(t)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   orch.projectID,
		Title:       "standard parent",
		Description: "one child failed and one child completed",
		Status:      model.StatusInProgress,
		Category:    model.CategoryStandard,
	}
	if err := db.Create(&parent).Error; err != nil {
		t.Fatalf("create parent: %v", err)
	}

	children := []model.Task{
		{ID: uuid.New(), ProjectID: orch.projectID, ParentTaskID: &parentID, Title: "failed child", Description: "failed", Status: model.StatusFailed},
		{ID: uuid.New(), ProjectID: orch.projectID, ParentTaskID: &parentID, Title: "done child", Description: "done", Status: model.StatusDone},
	}
	for i := range children {
		if err := db.Create(&children[i]).Error; err != nil {
			t.Fatalf("create child: %v", err)
		}
	}

	if err := orch.checkFeatureCompletion(&parent); err != nil {
		t.Fatalf("checkFeatureCompletion: %v", err)
	}

	var updated model.Task
	if err := db.First(&updated, "id = ?", parentID).Error; err != nil {
		t.Fatalf("load parent: %v", err)
	}
	if updated.Status == model.StatusTestingReady || updated.Status == model.StatusMerging || updated.Status == model.StatusDone {
		t.Fatalf("parent advanced to %s with a failed child", updated.Status)
	}
	if updated.Status != model.StatusFailed {
		t.Fatalf("status = %s, want failed", updated.Status)
	}
	reason, _ := updated.Context["failure_reason"].(string)
	if !strings.Contains(reason, "failed child") {
		t.Fatalf("failure_reason = %q, want failed child name", reason)
	}
}

func TestStandardParent_TestingReadyFailureRecordsSummaryAndDoesNotMerge(t *testing.T) {
	db := testutil.NewTestDB(t)
	featureName := "testing-ready-fails"
	featureDir := filepath.Join(t.TempDir(), "feature", featureName, "integration")
	if err := os.MkdirAll(featureDir, 0o755); err != nil {
		t.Fatalf("create feature dir: %v", err)
	}
	wt := &FakeWorktreeManager{
		BarePath: t.TempDir(),
		Default:  "main",
		Features: map[string]string{featureName: featureDir},
	}
	merger := &stubMerger{results: []stubMergeResult{{result: &MergeResult{Success: true, MergeCommit: "should-not-run"}}}}
	o := testOrchestrator(t, db, wt)
	o.mergeDispatcher = merger
	o.testGate.TestCommand = "sh -c \"printf 'compile-failed\\nlong-details\\n'; exit 1\""

	parent := model.Task{
		ID:             uuid.New(),
		ProjectID:      o.projectID,
		Title:          "testing ready parent",
		Description:    "tests fail before merge",
		Status:         model.StatusTestingReady,
		Category:       model.CategoryStandard,
		WorktreeBranch: "feature/" + featureName,
	}
	if err := db.Create(&parent).Error; err != nil {
		t.Fatalf("create parent: %v", err)
	}

	if err := o.processTestingReady(&parent); err != nil {
		t.Fatalf("processTestingReady: %v", err)
	}

	var updated model.Task
	if err := db.First(&updated, "id = ?", parent.ID).Error; err != nil {
		t.Fatalf("load parent: %v", err)
	}
	if updated.Status != model.StatusTestingReady {
		t.Fatalf("status = %s, want testing_ready", updated.Status)
	}
	if merger.calls != 0 {
		t.Fatalf("merger calls = %d, want 0 before testing_ready passes", merger.calls)
	}
	summary, _ := updated.Context["testing_ready_failure_summary"].(string)
	if summary == "" {
		t.Fatal("testing_ready_failure_summary not recorded")
	}
	if !strings.Contains(summary, "compile-failed") {
		t.Fatalf("summary = %q, want first failing output line", summary)
	}
	if strings.Contains(summary, "\n") {
		t.Fatalf("summary should be concise single-line text, got %q", summary)
	}
	if _, ok := updated.Context["automated_gate_passed"].(bool); ok {
		t.Fatal("automated_gate_passed should not be set after failed tests")
	}
}

func TestStandardParent_MergerTestsFailedRecordsTerminalFailure(t *testing.T) {
	merger := &stubMerger{results: []stubMergeResult{{
		result: &MergeResult{Success: false, FailureReason: "tests_failed", ExitCode: 3},
	}}}
	o, db, projectID := setupMergeTest(t, merger)
	task := createMergingTask(t, db, projectID, model.CategoryStandard)

	if err := o.executeMerge(task); err != nil {
		t.Fatalf("executeMerge: %v", err)
	}

	var updated model.Task
	if err := db.First(&updated, "id = ?", task.ID).Error; err != nil {
		t.Fatalf("load task: %v", err)
	}
	if updated.Status != model.StatusFailed {
		t.Fatalf("status = %s, want failed", updated.Status)
	}
	if got, _ := updated.Context[contextKeyTerminalMergerFailureReason].(string); got != terminalMergerFailureTestsFailed {
		t.Fatalf("terminal merger failure = %q, want %q", got, terminalMergerFailureTestsFailed)
	}
	if got, _ := updated.Context["merger_failure_reason"].(string); got != "tests_failed" {
		t.Fatalf("merger_failure_reason = %q, want tests_failed", got)
	}
	if got := jsonNumberAsInt(updated.Context["merger_exit_code"]); got != 3 {
		t.Fatalf("merger_exit_code = %d, want 3", got)
	}

	var event model.TaskEvent
	if err := db.Where("task_id = ? AND new_value = ?", task.ID, string(model.StatusFailed)).First(&event).Error; err != nil {
		t.Fatalf("load failed transition event: %v", err)
	}
	if got, _ := event.Details["reason"].(string); got != "merge aborted: pre-push tests failed" {
		t.Fatalf("event reason = %q, want pre-push test failure", got)
	}
}

func jsonNumberAsInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	default:
		return 0
	}
}

func TestStandardParent_TerminalMergerTestsFailedPreventsFailedParentRecovery(t *testing.T) {
	orch, db, _ := setupReconcileTest(t)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   orch.projectID,
		Title:       "terminal tests failed parent",
		Description: "all children done but merger tests failed terminally",
		Status:      model.StatusFailed,
		Category:    model.CategoryStandard,
		Context: model.JSONField{
			contextKeyTerminalMergerFailureReason: terminalMergerFailureTestsFailed,
			"failure_reason":                      "merge aborted: pre-push tests failed",
		},
	}
	if err := db.Create(&parent).Error; err != nil {
		t.Fatalf("create parent: %v", err)
	}
	for i := 0; i < 2; i++ {
		child := model.Task{ID: uuid.New(), ProjectID: orch.projectID, ParentTaskID: &parentID, Title: "done child", Description: "done", Status: model.StatusDone}
		if err := db.Create(&child).Error; err != nil {
			t.Fatalf("create child: %v", err)
		}
	}

	n, err := orch.reconcileFailedParents()
	if err != nil {
		t.Fatalf("reconcileFailedParents: %v", err)
	}
	if n != 0 {
		t.Fatalf("recovered parents = %d, want 0", n)
	}
	var updated model.Task
	if err := db.First(&updated, "id = ?", parentID).Error; err != nil {
		t.Fatalf("load parent: %v", err)
	}
	if updated.Status != model.StatusFailed {
		t.Fatalf("status = %s, want failed", updated.Status)
	}
}
