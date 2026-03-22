package orchestrator

import (
	"log/slog"
	"testing"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/supervisor"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

// setupClassifierTest creates an Orchestrator with a mock supervisor backed by
// the given JSON response string. Pass nil for the supervisor pointer to test
// the nil-supervisor path.
func setupClassifierTest(t *testing.T, sup *supervisor.Supervisor) *Orchestrator {
	t.Helper()
	return &Orchestrator{
		db:         testutil.NewTestDB(t),
		projectID:  uuid.New(),
		supervisor: sup,
		events:     make(chan Event, 100),
		logger:     slog.Default().With("component", "classifier-test"),
	}
}

// sampleBugReport returns a BugReport suitable for classifier tests.
func sampleBugReport(title, desc, category, severity, reproCtx string) *model.BugReport {
	return &model.BugReport{
		ID:                  uuid.New(),
		Title:               title,
		Description:         desc,
		Category:            model.BugReportCategory(category),
		Severity:            model.BugReportSeverity(severity),
		ReproductionContext: reproCtx,
		ProjectID:           uuid.New(),
	}
}

func TestClassifyBugReport_QuickFix(t *testing.T) {
	mockResp := `{
		"category": "quickfix",
		"title": "Fix line length in orchestrator.go",
		"description": "Line 42 in orchestrator.go exceeds the 120-character limit. Wrap the long fmt.Sprintf call.",
		"target_files": ["internal/orchestrator/orchestrator.go"],
		"rationale": "Single constraint violation with a clear file and line reference."
	}`
	sup := testutil.NewMockSupervisor(t, mockResp)
	o := setupClassifierTest(t, sup)

	report := sampleBugReport(
		"Line too long in orchestrator.go",
		"orchestrator.go line 42 is 135 characters, limit is 120",
		"constraint_violation",
		"informational",
		"go vet reported line length violation",
	)

	result, err := o.classifyBugReport(report)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.Category != "quickfix" {
		t.Errorf("category = %q, want %q", result.Category, "quickfix")
	}
	if result.Title == "" {
		t.Error("title should not be empty")
	}
	if len(result.TargetFiles) == 0 {
		t.Error("target_files should not be empty")
	}
}

func TestClassifyBugReport_Standard(t *testing.T) {
	mockResp := `{
		"category": "standard",
		"title": "Refactor agent lifecycle to support parallel merges",
		"description": "The agent lifecycle needs changes across multiple packages to support parallel merge operations. This affects the merge orchestrator, worktree manager, and agent runner.",
		"target_files": ["internal/merge/orchestrator.go", "internal/worktree/manager.go", "internal/agent/runner.go"],
		"rationale": "Multi-file architectural change affecting three packages."
	}`
	sup := testutil.NewMockSupervisor(t, mockResp)
	o := setupClassifierTest(t, sup)

	report := sampleBugReport(
		"Parallel merges cause data races",
		"When two agents merge simultaneously, the worktree manager panics with a concurrent map write",
		"upstream_code",
		"blocking",
		"stack trace showing concurrent map write in worktree.Manager",
	)

	result, err := o.classifyBugReport(report)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.Category != "standard" {
		t.Errorf("category = %q, want %q", result.Category, "standard")
	}
	if len(result.TargetFiles) < 2 {
		t.Errorf("expected multiple target files for standard task, got %d", len(result.TargetFiles))
	}
}

func TestClassifyBugReport_NilSupervisor(t *testing.T) {
	o := setupClassifierTest(t, nil)

	report := sampleBugReport(
		"Some bug",
		"Some description",
		"other",
		"informational",
		"",
	)

	result, err := o.classifyBugReport(report)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil result when supervisor is nil, got %+v", result)
	}
}

func TestClassifyBugReport_InvalidCategory(t *testing.T) {
	mockResp := `{
		"category": "urgent",
		"title": "Some task",
		"description": "Some description",
		"target_files": [],
		"rationale": "Invalid category value"
	}`
	sup := testutil.NewMockSupervisor(t, mockResp)
	o := setupClassifierTest(t, sup)

	report := sampleBugReport(
		"Some bug",
		"Some description",
		"other",
		"informational",
		"",
	)

	result, err := o.classifyBugReport(report)
	if err == nil {
		t.Fatal("expected error for invalid category, got nil")
	}
	if result != nil {
		t.Errorf("expected nil result on error, got %+v", result)
	}
}

func TestClassifyBugReport_AllFieldsPopulated(t *testing.T) {
	mockResp := `{
		"category": "quickfix",
		"title": "Fix typo in README",
		"description": "The word 'recieve' on line 10 of README.md should be 'receive'.",
		"target_files": ["README.md"],
		"rationale": "Simple typo fix in a single file."
	}`
	sup := testutil.NewMockSupervisor(t, mockResp)
	o := setupClassifierTest(t, sup)

	report := sampleBugReport(
		"Typo in README",
		"'recieve' should be 'receive'",
		"other",
		"informational",
		"",
	)

	result, err := o.classifyBugReport(report)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.Category == "" {
		t.Error("category should not be empty")
	}
	if result.Title == "" {
		t.Error("title should not be empty")
	}
	if result.Description == "" {
		t.Error("description should not be empty")
	}
	if len(result.TargetFiles) == 0 {
		t.Error("target_files should not be empty")
	}
	if result.Rationale == "" {
		t.Error("rationale should not be empty")
	}
}

func TestEnrichQuickFixDescription_Enriches(t *testing.T) {
	mockResp := `{
		"description": "The function maxSlugLen in internal/orchestrator/orchestrator.go truncates slugs at 40 characters. Update the constant to 50 and adjust the slugify function accordingly.",
		"target_files": ["internal/orchestrator/orchestrator.go"]
	}`
	sup := testutil.NewMockSupervisor(t, mockResp)
	o := setupClassifierTest(t, sup)

	desc, files, err := o.enrichQuickFixDescription("Increase slug length limit", "slug limit too short")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if desc == "slug limit too short" {
		t.Error("description should have been enriched, but got original")
	}
	if desc == "" {
		t.Error("enriched description should not be empty")
	}
	if len(files) == 0 {
		t.Error("target_files should not be empty after enrichment")
	}
}

func TestEnrichQuickFixDescription_NilSupervisor(t *testing.T) {
	o := setupClassifierTest(t, nil)

	origDesc := "fix the thing"
	desc, files, err := o.enrichQuickFixDescription("Fix thing", origDesc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if desc != origDesc {
		t.Errorf("description = %q, want original %q", desc, origDesc)
	}
	if files != nil {
		t.Errorf("expected nil target_files when supervisor is nil, got %v", files)
	}
}
