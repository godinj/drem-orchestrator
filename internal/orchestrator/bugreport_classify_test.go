package orchestrator

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/bugreport"
	dbpkg "github.com/godinj/drem-orchestrator/internal/db"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/supervisor"
	"github.com/godinj/drem-orchestrator/internal/testutil"
	"github.com/godinj/drem-orchestrator/internal/worktree"
)

// setupClassifyIntegrationTest creates an Orchestrator wired to a real
// bugreport.Service and an optional mock supervisor. It returns the
// orchestrator, the bug report service, and the project ID.
func setupClassifyIntegrationTest(t *testing.T, sup *supervisor.Supervisor) (*Orchestrator, *bugreport.Service, uuid.UUID) {
	t.Helper()

	gormDB, err := dbpkg.Init(":memory:")
	if err != nil {
		t.Fatalf("init test db: %v", err)
	}

	projectID := uuid.New()
	project := model.Project{
		ID:            projectID,
		Name:          "classify-test-project",
		BareRepoPath:  "/tmp/test-classify",
		DefaultBranch: "main",
	}
	if err := gormDB.Create(&project).Error; err != nil {
		t.Fatalf("create project: %v", err)
	}

	bugSvc := bugreport.New(gormDB)
	dropDir := t.TempDir()

	wt := &worktree.Manager{BareRepoPath: "/tmp/fake", DefaultBranch: "main"}
	events := make(chan Event, 100)
	orch := &Orchestrator{
		db:              gormDB,
		projectID:       projectID,
		worktree:        wt,
		bugreport:       bugSvc,
		bugreportDir:    dropDir,
		supervisor:      sup,
		events:          events,
		contextWarnPct:  75,
		contextStopPct:  90,
		contextFixerPct: 85,
		logger:          slog.Default().With("component", "classify-integration-test"),
	}

	return orch, bugSvc, projectID
}

// writeClassifyBugReportFile writes a bug report JSON file to the drop directory.
func writeClassifyBugReportFile(t *testing.T, dir string, bf bugreport.BugReportFile) string {
	t.Helper()
	data, err := json.Marshal(bf)
	if err != nil {
		t.Fatalf("marshal bug report file: %v", err)
	}
	filePath := filepath.Join(dir, uuid.New().String()+".json")
	if err := os.WriteFile(filePath, data, 0o644); err != nil {
		t.Fatalf("write bug report file: %v", err)
	}
	return filePath
}

func TestClassifyNewBugReports_CreatesQuickFixTask(t *testing.T) {
	mockResp := `{
		"category": "quickfix",
		"title": "Fix line length in orchestrator.go",
		"description": "Line 42 exceeds the 120-char limit. Wrap the long call.",
		"target_files": ["internal/orchestrator/orchestrator.go"],
		"rationale": "Single constraint violation with clear file reference."
	}`
	sup := testutil.NewMockSupervisor(t, mockResp)
	orch, bugSvc, projectID := setupClassifyIntegrationTest(t, sup)

	// Write a bug report file and ingest it.
	writeClassifyBugReportFile(t, orch.bugreportDir, bugreport.BugReportFile{
		Title:               "Line too long in orchestrator.go",
		Description:         "orchestrator.go line 42 is 135 chars, limit is 120",
		Category:            "constraint_violation",
		Severity:            "informational",
		ReproductionContext: "go vet reported line length violation",
	})

	orch.ingestBugReports()

	// Verify a task was created with CategoryQuickFix.
	var tasks []model.Task
	if err := orch.db.Where("project_id = ?", projectID).Find(&tasks).Error; err != nil {
		t.Fatalf("query tasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	task := tasks[0]
	if task.Category != model.CategoryQuickFix {
		t.Errorf("task category = %q, want %q", task.Category, model.CategoryQuickFix)
	}
	if task.Status != model.StatusBacklog {
		t.Errorf("task status = %q, want %q", task.Status, model.StatusBacklog)
	}
	if task.Title != "Fix line length in orchestrator.go" {
		t.Errorf("task title = %q, want %q", task.Title, "Fix line length in orchestrator.go")
	}

	// Verify description includes bug report context.
	if !strings.Contains(task.Description, "Bug Report Context") {
		t.Errorf("task description should contain bug report context, got: %s", task.Description)
	}
	if !strings.Contains(task.Description, "constraint_violation") {
		t.Errorf("task description should contain original category, got: %s", task.Description)
	}

	// Verify bug report was promoted and linked.
	reports, err := bugSvc.List(bugreport.ListFilters{ProjectID: &projectID})
	if err != nil {
		t.Fatalf("list bug reports: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("expected 1 bug report, got %d", len(reports))
	}
	report := reports[0]
	if report.Status != model.BugStatusPromoted {
		t.Errorf("bug report status = %q, want %q", report.Status, model.BugStatusPromoted)
	}
	if report.PromotedTaskID == nil {
		t.Fatal("bug report PromotedTaskID should not be nil")
	}
	if *report.PromotedTaskID != task.ID {
		t.Errorf("bug report PromotedTaskID = %s, want %s", *report.PromotedTaskID, task.ID)
	}
}

func TestClassifyNewBugReports_CreatesStandardTask(t *testing.T) {
	mockResp := `{
		"category": "standard",
		"title": "Refactor agent lifecycle management",
		"description": "The agent lifecycle spans multiple files and needs architectural changes.",
		"target_files": ["internal/orchestrator/orchestrator.go", "internal/agent/runner.go"],
		"rationale": "Multi-file change requiring design review and planning."
	}`
	sup := testutil.NewMockSupervisor(t, mockResp)
	orch, _, projectID := setupClassifyIntegrationTest(t, sup)

	writeClassifyBugReportFile(t, orch.bugreportDir, bugreport.BugReportFile{
		Title:       "Agent lifecycle has race condition",
		Description: "Multiple goroutines access agent state without synchronization",
		Category:    "upstream_code",
		Severity:    "blocking",
	})

	orch.ingestBugReports()

	var tasks []model.Task
	if err := orch.db.Where("project_id = ?", projectID).Find(&tasks).Error; err != nil {
		t.Fatalf("query tasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	if tasks[0].Category != model.CategoryStandard {
		t.Errorf("task category = %q, want %q", tasks[0].Category, model.CategoryStandard)
	}
}

func TestClassifyNewBugReports_NilSupervisor_SkipsClassification(t *testing.T) {
	// No supervisor — classifier returns nil, reports stay open.
	orch, bugSvc, projectID := setupClassifyIntegrationTest(t, nil)

	writeClassifyBugReportFile(t, orch.bugreportDir, bugreport.BugReportFile{
		Title:       "Some bug",
		Description: "needs manual triage",
		Category:    "other",
		Severity:    "informational",
	})

	orch.ingestBugReports()

	// No tasks should be created.
	var tasks []model.Task
	if err := orch.db.Where("project_id = ?", projectID).Find(&tasks).Error; err != nil {
		t.Fatalf("query tasks: %v", err)
	}
	if len(tasks) != 0 {
		t.Errorf("expected 0 tasks, got %d", len(tasks))
	}

	// Bug report should remain open.
	reports, err := bugSvc.List(bugreport.ListFilters{ProjectID: &projectID})
	if err != nil {
		t.Fatalf("list bug reports: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("expected 1 bug report, got %d", len(reports))
	}
	if reports[0].Status != model.BugStatusOpen {
		t.Errorf("bug report status = %q, want %q", reports[0].Status, model.BugStatusOpen)
	}
}

func TestClassifyNewBugReports_ClassifierError_SkipsReport(t *testing.T) {
	// Mock supervisor returns invalid JSON that will cause a parse error.
	mockResp := `not valid json at all`
	sup := testutil.NewMockSupervisor(t, mockResp)
	orch, bugSvc, projectID := setupClassifyIntegrationTest(t, sup)

	writeClassifyBugReportFile(t, orch.bugreportDir, bugreport.BugReportFile{
		Title:       "Failing classifier test",
		Description: "this should fail classification",
		Category:    "tooling",
		Severity:    "degraded",
	})

	orch.ingestBugReports()

	// No tasks should be created.
	var tasks []model.Task
	if err := orch.db.Where("project_id = ?", projectID).Find(&tasks).Error; err != nil {
		t.Fatalf("query tasks: %v", err)
	}
	if len(tasks) != 0 {
		t.Errorf("expected 0 tasks, got %d", len(tasks))
	}

	// Bug report should remain open.
	reports, err := bugSvc.List(bugreport.ListFilters{ProjectID: &projectID})
	if err != nil {
		t.Fatalf("list bug reports: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("expected 1 bug report, got %d", len(reports))
	}
	if reports[0].Status != model.BugStatusOpen {
		t.Errorf("bug report status = %q, want %q", reports[0].Status, model.BugStatusOpen)
	}
}

func TestClassifyNewBugReports_TargetFilesStoredInContext(t *testing.T) {
	mockResp := `{
		"category": "quickfix",
		"title": "Fix import order",
		"description": "Reorder imports in main.go",
		"target_files": ["cmd/main.go", "internal/util/helper.go"],
		"rationale": "Simple formatting fix."
	}`
	sup := testutil.NewMockSupervisor(t, mockResp)
	orch, _, projectID := setupClassifyIntegrationTest(t, sup)

	writeClassifyBugReportFile(t, orch.bugreportDir, bugreport.BugReportFile{
		Title:       "Import order wrong",
		Description: "imports not sorted in main.go",
		Category:    "constraint_violation",
		Severity:    "informational",
	})

	orch.ingestBugReports()

	var tasks []model.Task
	if err := orch.db.Where("project_id = ?", projectID).Find(&tasks).Error; err != nil {
		t.Fatalf("query tasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}

	task := tasks[0]
	if task.Context == nil {
		t.Fatal("task context should not be nil")
	}
	targetFiles, ok := task.Context["target_files"]
	if !ok {
		t.Fatal("task context should contain target_files key")
	}

	// The JSON field stores target_files as []interface{}.
	files, ok := targetFiles.([]interface{})
	if !ok {
		t.Fatalf("target_files should be a slice, got %T", targetFiles)
	}
	if len(files) != 2 {
		t.Errorf("expected 2 target files, got %d", len(files))
	}
}

func TestClassifyNewBugReports_AlreadyPromotedSkipped(t *testing.T) {
	mockResp := `{
		"category": "quickfix",
		"title": "Should not be created",
		"description": "This task should not be created because the report is already promoted.",
		"target_files": [],
		"rationale": "Already promoted."
	}`
	sup := testutil.NewMockSupervisor(t, mockResp)
	orch, bugSvc, projectID := setupClassifyIntegrationTest(t, sup)

	// Manually create a bug report that is already promoted.
	existingTaskID := uuid.New()
	existingTask := model.Task{
		ID:          existingTaskID,
		ProjectID:   projectID,
		Title:       "Existing task",
		Description: "previously promoted task",
		Status:      model.StatusBacklog,
		Category:    model.CategoryStandard,
	}
	if err := orch.db.Create(&existingTask).Error; err != nil {
		t.Fatalf("create existing task: %v", err)
	}

	promotedReport := &model.BugReport{
		Title:          "Already promoted bug",
		Description:    "this was already promoted",
		Category:       model.BugCategoryTooling,
		Severity:       model.BugSeverityInformational,
		ProjectID:      projectID,
		Status:         model.BugStatusOpen,
		PromotedTaskID: &existingTaskID,
	}
	if err := bugSvc.Create(promotedReport); err != nil {
		t.Fatalf("create promoted report: %v", err)
	}

	// Also write a new bug report file to trigger ingestion + classification.
	// This ensures classifyNewBugReports is actually called.
	writeClassifyBugReportFile(t, orch.bugreportDir, bugreport.BugReportFile{
		Title:       "New bug to trigger classification",
		Description: "this will be classified",
		Category:    "other",
		Severity:    "informational",
	})

	orch.ingestBugReports()

	// Count total tasks — should be 2: the pre-existing one plus the newly classified one.
	// The already-promoted report should NOT have created an additional task.
	var tasks []model.Task
	if err := orch.db.Where("project_id = ?", projectID).Find(&tasks).Error; err != nil {
		t.Fatalf("query tasks: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("expected 2 tasks (1 existing + 1 new), got %d", len(tasks))
	}

	// The promoted report should still have the original PromotedTaskID, not a new one.
	var reloadedReport model.BugReport
	if err := orch.db.First(&reloadedReport, "id = ?", promotedReport.ID).Error; err != nil {
		t.Fatalf("reload promoted report: %v", err)
	}
	if *reloadedReport.PromotedTaskID != existingTaskID {
		t.Errorf("promoted report's PromotedTaskID changed from %s to %s",
			existingTaskID, *reloadedReport.PromotedTaskID)
	}
}
