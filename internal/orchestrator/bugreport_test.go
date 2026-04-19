package orchestrator

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/bugreport"
	dbpkg "github.com/godinj/drem-orchestrator/internal/db"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

// setupOrchestratorWithBugReports creates an Orchestrator wired to a real
// bugreport.Service backed by an in-memory DB. It returns the orchestrator,
// the DB-backed bug report service, the project ID, and the temp drop directory.
func setupOrchestratorWithBugReports(t *testing.T) (*Orchestrator, *bugreport.Service, uuid.UUID) {
	t.Helper()

	// Use db.Init to get the UUID-generation callback registered.
	gormDB, err := dbpkg.Init(":memory:")
	if err != nil {
		t.Fatalf("init test db: %v", err)
	}

	projectID := uuid.New()
	project := model.Project{
		ID:            projectID,
		Name:          "test-project",
		BareRepoPath:  "/tmp/test-bugreport",
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
		logger:          slog.Default().With("component", "test-orchestrator"),
	}

	return orch, bugSvc, projectID
}

// writeBugReportFile writes a valid bug report JSON file to the given directory.
func writeBugReportFile(t *testing.T, dir string, bf bugreport.BugReportFile) string {
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

// ---------------------------------------------------------------------------
// ingestBugReports tests
// ---------------------------------------------------------------------------

func TestIngestBugReports_ValidFile(t *testing.T) {
	orch, bugSvc, projectID := setupOrchestratorWithBugReports(t)

	// Write a valid bug report JSON file.
	filePath := writeBugReportFile(t, orch.bugreportDir, bugreport.BugReportFile{
		Title:               "Build failure in CI",
		Description:         "go build fails with missing dependency",
		Category:            "tooling",
		Severity:            "blocking",
		ReproductionContext: "go build ./... exits with code 1",
	})

	// Run ingestion.
	orch.ingestBugReports()

	// Assert the file was deleted.
	if _, err := os.Stat(filePath); !os.IsNotExist(err) {
		t.Error("expected bug report file to be deleted after ingestion")
	}

	// Assert the bug report was inserted into the DB.
	reports, err := bugSvc.List(bugreport.ListFilters{ProjectID: &projectID})
	if err != nil {
		t.Fatalf("list bug reports: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("expected 1 bug report, got %d", len(reports))
	}
	if reports[0].Title != "Build failure in CI" {
		t.Errorf("title = %q, want %q", reports[0].Title, "Build failure in CI")
	}
	if reports[0].Category != model.BugCategoryTooling {
		t.Errorf("category = %q, want %q", reports[0].Category, model.BugCategoryTooling)
	}
	if reports[0].Severity != model.BugSeverityBlocking {
		t.Errorf("severity = %q, want %q", reports[0].Severity, model.BugSeverityBlocking)
	}
	if reports[0].Status != model.BugStatusPromoted {
		t.Errorf("status = %q, want %q", reports[0].Status, model.BugStatusPromoted)
	}
}

func TestIngestBugReports_MultipleFiles(t *testing.T) {
	orch, bugSvc, projectID := setupOrchestratorWithBugReports(t)

	// Write two valid bug report files.
	writeBugReportFile(t, orch.bugreportDir, bugreport.BugReportFile{
		Title:       "Bug one",
		Description: "first issue",
		Category:    "tooling",
		Severity:    "informational",
	})
	writeBugReportFile(t, orch.bugreportDir, bugreport.BugReportFile{
		Title:       "Bug two",
		Description: "second issue",
		Category:    "test_failure",
		Severity:    "degraded",
	})

	orch.ingestBugReports()

	reports, err := bugSvc.List(bugreport.ListFilters{ProjectID: &projectID})
	if err != nil {
		t.Fatalf("list bug reports: %v", err)
	}
	if len(reports) != 2 {
		t.Fatalf("expected 2 bug reports, got %d", len(reports))
	}
}

func TestIngestBugReports_InvalidFileMovedToFailed(t *testing.T) {
	orch, bugSvc, projectID := setupOrchestratorWithBugReports(t)

	// Write an invalid JSON file (missing required fields).
	invalidPath := filepath.Join(orch.bugreportDir, "bad-report.json")
	if err := os.WriteFile(invalidPath, []byte(`{"title":"","description":""}`), 0o644); err != nil {
		t.Fatal(err)
	}

	orch.ingestBugReports()

	// The invalid file should be moved to failed/.
	failedPath := filepath.Join(orch.bugreportDir, "failed", "bad-report.json")
	if _, err := os.Stat(failedPath); os.IsNotExist(err) {
		t.Error("expected invalid file to be moved to failed/")
	}

	// No bug reports should be in the DB.
	reports, err := bugSvc.List(bugreport.ListFilters{ProjectID: &projectID})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(reports) != 0 {
		t.Errorf("expected 0 bug reports, got %d", len(reports))
	}
}

func TestIngestBugReports_EmptyDirectory(t *testing.T) {
	orch, _, _ := setupOrchestratorWithBugReports(t)

	// Should not panic or error with empty directory.
	orch.ingestBugReports()
}

func TestIngestBugReports_NilService(t *testing.T) {
	db := testutil.NewTestDB(t)
	wt := &FakeWorktreeManager{BarePath: "/tmp/fake", Default: "main"}
	events := make(chan Event, 100)

	// Create an orchestrator without a bugreport service (backward compat).
	orch := &Orchestrator{
		db:              db,
		projectID:       uuid.New(),
		worktree:        wt,
		bugreport:       nil,
		bugreportDir:    "",
		events:          events,
		contextWarnPct:  75,
		contextStopPct:  90,
		contextFixerPct: 85,
		logger:          slog.Default().With("component", "test-orchestrator"),
	}

	// Should return immediately without error.
	orch.ingestBugReports()
}

func TestIngestBugReports_NonJsonFilesIgnored(t *testing.T) {
	orch, bugSvc, projectID := setupOrchestratorWithBugReports(t)

	// Write a non-JSON file that should be ignored.
	txtPath := filepath.Join(orch.bugreportDir, "readme.txt")
	if err := os.WriteFile(txtPath, []byte("not a bug report"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Also write a valid JSON bug report.
	writeBugReportFile(t, orch.bugreportDir, bugreport.BugReportFile{
		Title:       "Real bug",
		Description: "a real issue",
		Category:    "other",
		Severity:    "informational",
	})

	orch.ingestBugReports()

	// The txt file should still be there (ignored, not deleted).
	if _, err := os.Stat(txtPath); os.IsNotExist(err) {
		t.Error("expected non-json file to be left in place")
	}

	// Only the valid JSON should be ingested.
	reports, err := bugSvc.List(bugreport.ListFilters{ProjectID: &projectID})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(reports) != 1 {
		t.Errorf("expected 1 bug report, got %d", len(reports))
	}
}
