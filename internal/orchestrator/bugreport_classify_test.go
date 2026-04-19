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
)

// setupClassifyIntegrationTest creates an Orchestrator wired to a real
// bugreport.Service. It returns the orchestrator, the bug report service,
// and the project ID.
func setupClassifyIntegrationTest(t *testing.T, _ interface{}) (*Orchestrator, *bugreport.Service, uuid.UUID) {
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

	wt := &FakeWorktreeManager{BarePath: "/tmp/fake", Default: "main"}
	events := make(chan Event, 100)
	orch := &Orchestrator{
		db:              gormDB,
		projectID:       projectID,
		worktree:        wt,
		bugreport:       bugSvc,
		bugreportDir:    dropDir,
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
	// After the refactor, all tasks are created in CLASSIFYING with CategoryStandard.
	// The classifier agent will determine the actual category later.
	orch, bugSvc, projectID := setupClassifyIntegrationTest(t, nil)

	writeClassifyBugReportFile(t, orch.bugreportDir, bugreport.BugReportFile{
		Title:               "Line too long in orchestrator.go",
		Description:         "orchestrator.go line 42 is 135 chars, limit is 120",
		Category:            "constraint_violation",
		Severity:            "informational",
		ReproductionContext: "go vet reported line length violation",
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
	if task.Status != model.StatusClassifying {
		t.Errorf("task status = %q, want %q", task.Status, model.StatusClassifying)
	}
	if task.Title != "Line too long in orchestrator.go" {
		t.Errorf("task title = %q, want %q", task.Title, "Line too long in orchestrator.go")
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
	// After the refactor, all tasks are created in CLASSIFYING with CategoryStandard.
	orch, _, projectID := setupClassifyIntegrationTest(t, nil)

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
	if tasks[0].Status != model.StatusClassifying {
		t.Errorf("task status = %q, want %q", tasks[0].Status, model.StatusClassifying)
	}
	if tasks[0].Category != model.CategoryStandard {
		t.Errorf("task category = %q, want %q", tasks[0].Category, model.CategoryStandard)
	}
}

func TestClassifyNewBugReports_NilSupervisor_SkipsClassification(t *testing.T) {
	// After the refactor, tasks are created even without a supervisor — the
	// classifier agent handles classification later.
	orch, bugSvc, projectID := setupClassifyIntegrationTest(t, nil)

	writeClassifyBugReportFile(t, orch.bugreportDir, bugreport.BugReportFile{
		Title:       "Some bug",
		Description: "needs manual triage",
		Category:    "other",
		Severity:    "informational",
	})

	orch.ingestBugReports()

	// A task should be created in CLASSIFYING.
	var tasks []model.Task
	if err := orch.db.Where("project_id = ?", projectID).Find(&tasks).Error; err != nil {
		t.Fatalf("query tasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	if tasks[0].Status != model.StatusClassifying {
		t.Errorf("task status = %q, want %q", tasks[0].Status, model.StatusClassifying)
	}

	// Bug report should be promoted.
	reports, err := bugSvc.List(bugreport.ListFilters{ProjectID: &projectID})
	if err != nil {
		t.Fatalf("list bug reports: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("expected 1 bug report, got %d", len(reports))
	}
	if reports[0].Status != model.BugStatusPromoted {
		t.Errorf("bug report status = %q, want %q", reports[0].Status, model.BugStatusPromoted)
	}
}

func TestClassifyNewBugReports_ClassifierError_SkipsReport(t *testing.T) {
	// After the refactor, there's no classifier error path — tasks are always
	// created in CLASSIFYING. This test verifies the task is created.
	orch, bugSvc, projectID := setupClassifyIntegrationTest(t, nil)

	writeClassifyBugReportFile(t, orch.bugreportDir, bugreport.BugReportFile{
		Title:       "Failing classifier test",
		Description: "this should create a task in classifying",
		Category:    "tooling",
		Severity:    "degraded",
	})

	orch.ingestBugReports()

	// Task should be created in CLASSIFYING.
	var tasks []model.Task
	if err := orch.db.Where("project_id = ?", projectID).Find(&tasks).Error; err != nil {
		t.Fatalf("query tasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	if tasks[0].Status != model.StatusClassifying {
		t.Errorf("task status = %q, want %q", tasks[0].Status, model.StatusClassifying)
	}

	// Bug report should be promoted.
	reports, err := bugSvc.List(bugreport.ListFilters{ProjectID: &projectID})
	if err != nil {
		t.Fatalf("list bug reports: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("expected 1 bug report, got %d", len(reports))
	}
	if reports[0].Status != model.BugStatusPromoted {
		t.Errorf("bug report status = %q, want %q", reports[0].Status, model.BugStatusPromoted)
	}
}

func TestClassifyNewBugReports_TargetFilesStoredInContext(t *testing.T) {
	// After the refactor, bug report context is stored directly (not target_files
	// from classifier). Verify context contains bug report metadata.
	orch, _, projectID := setupClassifyIntegrationTest(t, nil)

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

	// Context should contain bug report metadata.
	wantKeys := []string{"title", "category", "severity", "description"}
	for _, key := range wantKeys {
		if _, ok := task.Context[key]; !ok {
			t.Errorf("task context missing key %q", key)
		}
	}
}

func TestClassifyNewBugReports_AlreadyPromotedSkipped(t *testing.T) {
	orch, bugSvc, projectID := setupClassifyIntegrationTest(t, nil)

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
	writeClassifyBugReportFile(t, orch.bugreportDir, bugreport.BugReportFile{
		Title:       "New bug to trigger classification",
		Description: "this will be classified",
		Category:    "other",
		Severity:    "informational",
	})

	orch.ingestBugReports()

	// Count total tasks — should be 2: the pre-existing one plus the newly created one.
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

// TestClassifyNewBugReports_CreatesTaskInClassifying verifies that after the
// one-shot classifier is removed, classifyNewBugReports creates a task in
// StatusClassifying (not StatusBacklog) with no inline LLM call. A nil
// supervisor must NOT prevent task creation — the task is created for the
// classifier agent to pick up later.
func TestClassifyNewBugReports_CreatesTaskInClassifying(t *testing.T) {
	// Nil supervisor: no LLM call should be made. The refactored code
	// should create a task in CLASSIFYING regardless.
	orch, bugSvc, projectID := setupClassifyIntegrationTest(t, nil)

	writeClassifyBugReportFile(t, orch.bugreportDir, bugreport.BugReportFile{
		Title:               "Flaky test in agent_test.go",
		Description:         "TestAgentHeartbeat fails intermittently under load",
		Category:            "test_failure",
		Severity:            "degraded",
		ReproductionContext: "go test -count=10 ./internal/agent/...",
	})

	orch.ingestBugReports()

	// A task should have been created even though supervisor is nil.
	var tasks []model.Task
	if err := orch.db.Where("project_id = ?", projectID).Find(&tasks).Error; err != nil {
		t.Fatalf("query tasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}

	task := tasks[0]

	// The task must be in CLASSIFYING, not BACKLOG.
	if task.Status != model.StatusClassifying {
		t.Errorf("task status = %q, want %q", task.Status, model.StatusClassifying)
	}

	// The bug report must be linked and promoted.
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

// TestClassifyNewBugReports_StoresBugReportContext verifies that after the
// refactor, classifyNewBugReports stores full bug report metadata in the
// task's Context field so the classifier agent has everything it needs.
func TestClassifyNewBugReports_StoresBugReportContext(t *testing.T) {
	orch, _, projectID := setupClassifyIntegrationTest(t, nil)

	writeClassifyBugReportFile(t, orch.bugreportDir, bugreport.BugReportFile{
		Title:               "Memory leak in worktree cleanup",
		Description:         "Worktree temp dirs accumulate after agent teardown",
		Category:            "upstream_code",
		Severity:            "blocking",
		ReproductionContext: "du -sh /tmp/drem-* after 10 agent cycles",
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

	// The context must contain all bug report fields for the classifier agent.
	wantKeys := []string{"title", "category", "severity", "description", "reproduction_context"}
	for _, key := range wantKeys {
		if _, ok := task.Context[key]; !ok {
			t.Errorf("task context missing key %q", key)
		}
	}

	// Spot-check values match the bug report.
	if got, ok := task.Context["title"].(string); !ok || got != "Memory leak in worktree cleanup" {
		t.Errorf("context[title] = %v, want %q", task.Context["title"], "Memory leak in worktree cleanup")
	}
	if got, ok := task.Context["category"].(string); !ok || got != "upstream_code" {
		t.Errorf("context[category] = %v, want %q", task.Context["category"], "upstream_code")
	}
	if got, ok := task.Context["severity"].(string); !ok || got != "blocking" {
		t.Errorf("context[severity] = %v, want %q", task.Context["severity"], "blocking")
	}
	if got, ok := task.Context["description"].(string); !ok || !strings.Contains(got, "Worktree temp dirs") {
		t.Errorf("context[description] = %v, want to contain %q", task.Context["description"], "Worktree temp dirs")
	}
	if got, ok := task.Context["reproduction_context"].(string); !ok || !strings.Contains(got, "du -sh") {
		t.Errorf("context[reproduction_context] = %v, want to contain %q", task.Context["reproduction_context"], "du -sh")
	}
}
