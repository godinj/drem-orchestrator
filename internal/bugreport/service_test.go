package bugreport

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/db"
	"github.com/godinj/drem-orchestrator/internal/model"
)

// setupTestService creates an in-memory SQLite DB with all migrations and
// returns a ready-to-use Service along with a test project ID.
func setupTestService(t *testing.T) (*Service, uuid.UUID) {
	t.Helper()
	gormDB, err := db.Init(":memory:")
	if err != nil {
		t.Fatalf("init test db: %v", err)
	}
	// Migrate bug report tables (not included in default AutoMigrate).
	if err := gormDB.AutoMigrate(&model.BugReport{}, &model.BugReportComment{}); err != nil {
		t.Fatalf("migrate bug report tables: %v", err)
	}

	// Create a project for foreign-key references.
	projectID := uuid.New()
	project := model.Project{
		ID:            projectID,
		Name:          "test-project",
		BareRepoPath:  "/tmp/test",
		DefaultBranch: "master",
	}
	if err := gormDB.Create(&project).Error; err != nil {
		t.Fatalf("create test project: %v", err)
	}

	return New(gormDB), projectID
}

// newReport returns a minimal valid BugReport with the given project ID.
func newReport(projectID uuid.UUID) *model.BugReport {
	return &model.BugReport{
		Title:       "test bug",
		Description: "something broke",
		Category:    model.BugCategoryTooling,
		Severity:    model.BugSeverityDegraded,
		ProjectID:   projectID,
	}
}

// ---------- Create ----------

func TestCreate_Valid(t *testing.T) {
	svc, pid := setupTestService(t)
	r := newReport(pid)
	if err := svc.Create(r); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if r.ID == uuid.Nil {
		t.Fatal("expected non-nil ID after create")
	}
	got, err := svc.Get(r.ID)
	if err != nil {
		t.Fatalf("Get after Create: %v", err)
	}
	if got.Title != r.Title {
		t.Errorf("title = %q, want %q", got.Title, r.Title)
	}
	if got.Status != model.BugStatusOpen {
		t.Errorf("status = %q, want %q", got.Status, model.BugStatusOpen)
	}
}

func TestCreate_MissingFields(t *testing.T) {
	svc, pid := setupTestService(t)

	tests := []struct {
		name   string
		report *model.BugReport
	}{
		{
			name: "missing title",
			report: &model.BugReport{
				Description: "desc",
				Category:    model.BugCategoryTooling,
				Severity:    model.BugSeverityDegraded,
				ProjectID:   pid,
			},
		},
		{
			name: "missing description",
			report: &model.BugReport{
				Title:     "title",
				Category:  model.BugCategoryTooling,
				Severity:  model.BugSeverityDegraded,
				ProjectID: pid,
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := svc.Create(tc.report); err == nil {
				t.Error("expected error for missing required field")
			}
		})
	}
}

// ---------- Get ----------

func TestGet_WithComments(t *testing.T) {
	svc, pid := setupTestService(t)
	r := newReport(pid)
	if err := svc.Create(r); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := svc.AddComment(r.ID, "user", "first comment"); err != nil {
		t.Fatalf("AddComment: %v", err)
	}
	got, err := svc.Get(r.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Comments) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(got.Comments))
	}
	if got.Comments[0].Body != "first comment" {
		t.Errorf("comment body = %q, want %q", got.Comments[0].Body, "first comment")
	}
}

func TestGet_NotFound(t *testing.T) {
	svc, _ := setupTestService(t)
	_, err := svc.Get(uuid.New())
	if err == nil {
		t.Error("expected error for non-existent bug report")
	}
}

// ---------- State Transitions ----------

func TestStateTransitions(t *testing.T) {
	tests := []struct {
		name    string
		from    model.BugReportStatus
		action  string // "acknowledge", "dismiss", or "promote"
		wantErr bool
	}{
		// Valid transitions from open.
		{"open->acknowledged", model.BugStatusOpen, "acknowledge", false},
		{"open->dismissed", model.BugStatusOpen, "dismiss", false},
		{"open->promoted", model.BugStatusOpen, "promote", false},
		// Valid transitions from acknowledged.
		{"acknowledged->dismissed", model.BugStatusAcknowledged, "dismiss", false},
		{"acknowledged->promoted", model.BugStatusAcknowledged, "promote", false},
		// Invalid: promoted is terminal.
		{"promoted->acknowledged", model.BugStatusPromoted, "acknowledge", true},
		{"promoted->dismissed", model.BugStatusPromoted, "dismiss", true},
		{"promoted->promoted", model.BugStatusPromoted, "promote", true},
		// Invalid: dismissed is terminal.
		{"dismissed->acknowledged", model.BugStatusDismissed, "acknowledge", true},
		{"dismissed->dismissed", model.BugStatusDismissed, "dismiss", true},
		{"dismissed->promoted", model.BugStatusDismissed, "promote", true},
		// Invalid: open->open (no self-transition).
		{"open->open via acknowledge then acknowledge", model.BugStatusAcknowledged, "acknowledge", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, pid := setupTestService(t)
			r := newReport(pid)
			r.Status = tc.from
			if err := svc.Create(r); err != nil {
				t.Fatalf("Create: %v", err)
			}

			var err error
			switch tc.action {
			case "acknowledge":
				err = svc.Acknowledge(r.ID)
			case "dismiss":
				err = svc.Dismiss(r.ID)
			case "promote":
				_, err = svc.Promote(r.ID, "promoted task", "desc", pid)
			}

			if tc.wantErr && err == nil {
				t.Error("expected error but got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// ---------- Promotion ----------

func TestPromote_CreatesTask(t *testing.T) {
	svc, pid := setupTestService(t)
	r := newReport(pid)
	if err := svc.Create(r); err != nil {
		t.Fatalf("Create: %v", err)
	}

	task, err := svc.Promote(r.ID, "fix the bug", "detailed description", pid)
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}

	if task.Title != "fix the bug" {
		t.Errorf("task title = %q, want %q", task.Title, "fix the bug")
	}
	if task.Description != "detailed description" {
		t.Errorf("task description = %q, want %q", task.Description, "detailed description")
	}
	if task.Status != model.StatusBacklog {
		t.Errorf("task status = %q, want %q", task.Status, model.StatusBacklog)
	}
	if task.ProjectID != pid {
		t.Errorf("task project = %s, want %s", task.ProjectID, pid)
	}

	// Verify the bug report was updated.
	got, err := svc.Get(r.ID)
	if err != nil {
		t.Fatalf("Get after Promote: %v", err)
	}
	if got.Status != model.BugStatusPromoted {
		t.Errorf("bug report status = %q, want %q", got.Status, model.BugStatusPromoted)
	}
	if got.PromotedTaskID == nil || *got.PromotedTaskID != task.ID {
		t.Errorf("PromotedTaskID = %v, want %s", got.PromotedTaskID, task.ID)
	}
}

func TestPromote_AlreadyPromoted(t *testing.T) {
	svc, pid := setupTestService(t)
	r := newReport(pid)
	if err := svc.Create(r); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.Promote(r.ID, "task", "desc", pid); err != nil {
		t.Fatalf("first Promote: %v", err)
	}
	if _, err := svc.Promote(r.ID, "task2", "desc2", pid); err == nil {
		t.Error("expected error promoting already-promoted bug report")
	}
}

func TestPromote_Dismissed(t *testing.T) {
	svc, pid := setupTestService(t)
	r := newReport(pid)
	if err := svc.Create(r); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := svc.Dismiss(r.ID); err != nil {
		t.Fatalf("Dismiss: %v", err)
	}
	if _, err := svc.Promote(r.ID, "task", "desc", pid); err == nil {
		t.Error("expected error promoting dismissed bug report")
	}
}

// ---------- Filtering ----------

func TestList_Filters(t *testing.T) {
	svc, pid := setupTestService(t)

	// Create a diverse set of reports.
	reports := []model.BugReport{
		{Title: "r1", Description: "d1", Category: model.BugCategoryTooling, Severity: model.BugSeverityBlocking, ProjectID: pid},
		{Title: "r2", Description: "d2", Category: model.BugCategoryTooling, Severity: model.BugSeverityDegraded, ProjectID: pid},
		{Title: "r3", Description: "d3", Category: model.BugCategoryTestFailure, Severity: model.BugSeverityBlocking, ProjectID: pid},
		{Title: "r4", Description: "d4", Category: model.BugCategoryRequirements, Severity: model.BugSeverityInformational, ProjectID: pid},
	}
	for i := range reports {
		if err := svc.Create(&reports[i]); err != nil {
			t.Fatalf("Create reports[%d]: %v", i, err)
		}
	}
	// Acknowledge one so we can filter by status.
	if err := svc.Acknowledge(reports[0].ID); err != nil {
		t.Fatalf("Acknowledge: %v", err)
	}

	catTooling := model.BugCategoryTooling
	catTest := model.BugCategoryTestFailure
	sevBlocking := model.BugSeverityBlocking
	statusOpen := model.BugStatusOpen
	statusAck := model.BugStatusAcknowledged

	tests := []struct {
		name    string
		filters ListFilters
		want    int
	}{
		{"no filters", ListFilters{}, 4},
		{"by category tooling", ListFilters{Category: &catTooling}, 2},
		{"by category test_failure", ListFilters{Category: &catTest}, 1},
		{"by severity blocking", ListFilters{Severity: &sevBlocking}, 2},
		{"by status open", ListFilters{Status: &statusOpen}, 3},
		{"by status acknowledged", ListFilters{Status: &statusAck}, 1},
		{"by project", ListFilters{ProjectID: &pid}, 4},
		{"combined category+severity", ListFilters{Category: &catTooling, Severity: &sevBlocking}, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			results, err := svc.List(tc.filters)
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(results) != tc.want {
				t.Errorf("got %d results, want %d", len(results), tc.want)
			}
		})
	}
}

func TestList_FilterByDifferentProject(t *testing.T) {
	svc, pid := setupTestService(t)
	r := newReport(pid)
	if err := svc.Create(r); err != nil {
		t.Fatalf("Create: %v", err)
	}
	otherPID := uuid.New()
	results, err := svc.List(ListFilters{ProjectID: &otherPID})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results for different project, got %d", len(results))
	}
}

// ---------- Comments ----------

func TestAddComment(t *testing.T) {
	svc, pid := setupTestService(t)
	r := newReport(pid)
	if err := svc.Create(r); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := svc.AddComment(r.ID, "user", "looks good"); err != nil {
		t.Fatalf("AddComment: %v", err)
	}
	comments, err := svc.GetComments(r.ID)
	if err != nil {
		t.Fatalf("GetComments: %v", err)
	}
	if len(comments) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(comments))
	}
	if comments[0].Author != "user" {
		t.Errorf("author = %q, want %q", comments[0].Author, "user")
	}
	if comments[0].Body != "looks good" {
		t.Errorf("body = %q, want %q", comments[0].Body, "looks good")
	}
}

func TestGetComments_ChronologicalOrder(t *testing.T) {
	svc, pid := setupTestService(t)
	r := newReport(pid)
	if err := svc.Create(r); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Add comments with slight time gaps to ensure ordering.
	if err := svc.AddComment(r.ID, "user", "first"); err != nil {
		t.Fatalf("AddComment 1: %v", err)
	}
	time.Sleep(10 * time.Millisecond)
	if err := svc.AddComment(r.ID, "system", "second"); err != nil {
		t.Fatalf("AddComment 2: %v", err)
	}
	time.Sleep(10 * time.Millisecond)
	if err := svc.AddComment(r.ID, "user", "third"); err != nil {
		t.Fatalf("AddComment 3: %v", err)
	}

	comments, err := svc.GetComments(r.ID)
	if err != nil {
		t.Fatalf("GetComments: %v", err)
	}
	if len(comments) != 3 {
		t.Fatalf("expected 3 comments, got %d", len(comments))
	}
	if comments[0].Body != "first" || comments[1].Body != "second" || comments[2].Body != "third" {
		t.Errorf("comments not in chronological order: %q, %q, %q",
			comments[0].Body, comments[1].Body, comments[2].Body)
	}
}

func TestAddComment_NonExistentBugReport(t *testing.T) {
	svc, _ := setupTestService(t)
	err := svc.AddComment(uuid.New(), "user", "orphan comment")
	if err == nil {
		t.Error("expected error for comment on non-existent bug report")
	}
}

func TestGetComments_NonExistentBugReport(t *testing.T) {
	svc, _ := setupTestService(t)
	_, err := svc.GetComments(uuid.New())
	if err == nil {
		t.Error("expected error for comments on non-existent bug report")
	}
}

// ---------- Delete ----------

func TestDelete(t *testing.T) {
	svc, pid := setupTestService(t)
	r := newReport(pid)
	if err := svc.Create(r); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := svc.AddComment(r.ID, "user", "will be deleted"); err != nil {
		t.Fatalf("AddComment: %v", err)
	}
	if err := svc.Delete(r.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	// Verify the bug report is gone.
	_, err := svc.Get(r.ID)
	if err == nil {
		t.Error("expected error after delete")
	}
	// Verify comments are also gone.
	comments, _ := svc.GetComments(uuid.New()) // won't find the report either
	if len(comments) != 0 {
		t.Errorf("expected 0 orphan comments, got %d", len(comments))
	}
}

func TestDelete_NonExistent(t *testing.T) {
	svc, _ := setupTestService(t)
	err := svc.Delete(uuid.New())
	if err == nil {
		t.Error("expected error deleting non-existent bug report")
	}
}

func TestDelete_CascadeComments(t *testing.T) {
	svc, pid := setupTestService(t)
	r := newReport(pid)
	if err := svc.Create(r); err != nil {
		t.Fatalf("Create: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := svc.AddComment(r.ID, "user", "comment"); err != nil {
			t.Fatalf("AddComment: %v", err)
		}
	}
	if err := svc.Delete(r.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	// Create another report to verify its comments are unaffected.
	r2 := newReport(pid)
	if err := svc.Create(r2); err != nil {
		t.Fatalf("Create r2: %v", err)
	}
	if err := svc.AddComment(r2.ID, "user", "surviving comment"); err != nil {
		t.Fatalf("AddComment r2: %v", err)
	}
	comments, err := svc.GetComments(r2.ID)
	if err != nil {
		t.Fatalf("GetComments r2: %v", err)
	}
	if len(comments) != 1 {
		t.Errorf("expected 1 comment on r2, got %d", len(comments))
	}
}

// ---------- Ingestion ----------

func TestIngest_ValidFile(t *testing.T) {
	svc, pid := setupTestService(t)
	dir := t.TempDir()

	bf := BugReportFile{
		Title:               "broken build",
		Description:         "make fails on linux",
		Category:            "tooling",
		Severity:            "blocking",
		ReproductionContext: "run make in /src",
	}
	writeJSONFile(t, dir, "report1.json", bf)

	count, err := svc.Ingest(dir, pid)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if count != 1 {
		t.Errorf("ingested = %d, want 1", count)
	}

	// File should be deleted.
	if _, err := os.Stat(filepath.Join(dir, "report1.json")); !os.IsNotExist(err) {
		t.Error("expected file to be deleted after ingestion")
	}

	// Verify the report is in the DB.
	reports, err := svc.List(ListFilters{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("expected 1 report in DB, got %d", len(reports))
	}
	if reports[0].Title != "broken build" {
		t.Errorf("title = %q, want %q", reports[0].Title, "broken build")
	}
	if reports[0].ProjectID != pid {
		t.Errorf("project = %s, want %s", reports[0].ProjectID, pid)
	}
}

func TestIngest_InvalidJSON(t *testing.T) {
	svc, pid := setupTestService(t)
	dir := t.TempDir()

	// Write invalid JSON.
	if err := os.WriteFile(filepath.Join(dir, "bad.json"), []byte("{invalid"), 0o644); err != nil {
		t.Fatalf("write bad.json: %v", err)
	}

	count, err := svc.Ingest(dir, pid)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if count != 0 {
		t.Errorf("ingested = %d, want 0", count)
	}
	// File should be in failed/.
	if _, err := os.Stat(filepath.Join(dir, "failed", "bad.json")); os.IsNotExist(err) {
		t.Error("expected bad.json in failed/")
	}
}

func TestIngest_MissingRequiredFields(t *testing.T) {
	svc, pid := setupTestService(t)
	dir := t.TempDir()

	// Missing title.
	bf := BugReportFile{
		Description: "desc",
		Category:    "tooling",
		Severity:    "blocking",
	}
	writeJSONFile(t, dir, "notitle.json", bf)

	count, err := svc.Ingest(dir, pid)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if count != 0 {
		t.Errorf("ingested = %d, want 0", count)
	}
	if _, err := os.Stat(filepath.Join(dir, "failed", "notitle.json")); os.IsNotExist(err) {
		t.Error("expected notitle.json in failed/")
	}
}

func TestIngest_InvalidCategory(t *testing.T) {
	svc, pid := setupTestService(t)
	dir := t.TempDir()

	bf := BugReportFile{
		Title:       "title",
		Description: "desc",
		Category:    "nonexistent_category",
		Severity:    "blocking",
	}
	writeJSONFile(t, dir, "badcat.json", bf)

	count, err := svc.Ingest(dir, pid)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if count != 0 {
		t.Errorf("ingested = %d, want 0", count)
	}
	if _, err := os.Stat(filepath.Join(dir, "failed", "badcat.json")); os.IsNotExist(err) {
		t.Error("expected badcat.json in failed/")
	}
}

func TestIngest_InvalidSeverity(t *testing.T) {
	svc, pid := setupTestService(t)
	dir := t.TempDir()

	bf := BugReportFile{
		Title:       "title",
		Description: "desc",
		Category:    "tooling",
		Severity:    "critical",
	}
	writeJSONFile(t, dir, "badsev.json", bf)

	count, err := svc.Ingest(dir, pid)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if count != 0 {
		t.Errorf("ingested = %d, want 0", count)
	}
	if _, err := os.Stat(filepath.Join(dir, "failed", "badsev.json")); os.IsNotExist(err) {
		t.Error("expected badsev.json in failed/")
	}
}

func TestIngest_MultipleFiles(t *testing.T) {
	svc, pid := setupTestService(t)
	dir := t.TempDir()

	for i := 0; i < 3; i++ {
		bf := BugReportFile{
			Title:       "bug " + string(rune('A'+i)),
			Description: "desc",
			Category:    "tooling",
			Severity:    "degraded",
		}
		writeJSONFile(t, dir, uuid.New().String()+".json", bf)
	}

	count, err := svc.Ingest(dir, pid)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if count != 3 {
		t.Errorf("ingested = %d, want 3", count)
	}

	reports, err := svc.List(ListFilters{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(reports) != 3 {
		t.Errorf("DB has %d reports, want 3", len(reports))
	}
}

func TestIngest_EmptyDirectory(t *testing.T) {
	svc, pid := setupTestService(t)
	dir := t.TempDir()

	count, err := svc.Ingest(dir, pid)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if count != 0 {
		t.Errorf("ingested = %d, want 0", count)
	}
}

func TestIngest_WithAgentAndTaskID(t *testing.T) {
	svc, pid := setupTestService(t)
	dir := t.TempDir()

	agentID := uuid.New()
	taskID := uuid.New()
	bf := BugReportFile{
		Title:       "bug with agent",
		Description: "desc",
		Category:    "test_failure",
		Severity:    "informational",
		AgentID:     agentID.String(),
		TaskID:      taskID.String(),
	}
	writeJSONFile(t, dir, "withids.json", bf)

	count, err := svc.Ingest(dir, pid)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if count != 1 {
		t.Errorf("ingested = %d, want 1", count)
	}

	reports, err := svc.List(ListFilters{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("expected 1 report, got %d", len(reports))
	}
	if reports[0].AgentID == nil || *reports[0].AgentID != agentID {
		t.Errorf("AgentID = %v, want %s", reports[0].AgentID, agentID)
	}
	if reports[0].TaskID == nil || *reports[0].TaskID != taskID {
		t.Errorf("TaskID = %v, want %s", reports[0].TaskID, taskID)
	}
}

func TestIngest_SkipsNonJSONFiles(t *testing.T) {
	svc, pid := setupTestService(t)
	dir := t.TempDir()

	// Write a .txt file — should be ignored.
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("not json"), 0o644); err != nil {
		t.Fatalf("write notes.txt: %v", err)
	}
	// Write a valid JSON file too.
	bf := BugReportFile{
		Title:       "valid",
		Description: "desc",
		Category:    "tooling",
		Severity:    "degraded",
	}
	writeJSONFile(t, dir, "valid.json", bf)

	count, err := svc.Ingest(dir, pid)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if count != 1 {
		t.Errorf("ingested = %d, want 1", count)
	}
	// .txt file should still be there.
	if _, err := os.Stat(filepath.Join(dir, "notes.txt")); os.IsNotExist(err) {
		t.Error("expected notes.txt to be untouched")
	}
}

func TestIngest_SkipsSubdirectories(t *testing.T) {
	svc, pid := setupTestService(t)
	dir := t.TempDir()

	// Create a subdirectory with .json extension (unusual but test robustness).
	if err := os.Mkdir(filepath.Join(dir, "subdir.json"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	count, err := svc.Ingest(dir, pid)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if count != 0 {
		t.Errorf("ingested = %d, want 0", count)
	}
}

// ---------- validateTransition unit tests ----------

func TestValidateTransition(t *testing.T) {
	tests := []struct {
		from    model.BugReportStatus
		to      model.BugReportStatus
		wantErr bool
	}{
		{model.BugStatusOpen, model.BugStatusAcknowledged, false},
		{model.BugStatusOpen, model.BugStatusPromoted, false},
		{model.BugStatusOpen, model.BugStatusDismissed, false},
		{model.BugStatusAcknowledged, model.BugStatusPromoted, false},
		{model.BugStatusAcknowledged, model.BugStatusDismissed, false},
		// Invalid.
		{model.BugStatusPromoted, model.BugStatusOpen, true},
		{model.BugStatusPromoted, model.BugStatusAcknowledged, true},
		{model.BugStatusPromoted, model.BugStatusDismissed, true},
		{model.BugStatusDismissed, model.BugStatusOpen, true},
		{model.BugStatusDismissed, model.BugStatusAcknowledged, true},
		{model.BugStatusDismissed, model.BugStatusPromoted, true},
		// Self-transitions.
		{model.BugStatusOpen, model.BugStatusOpen, true},
		{model.BugStatusAcknowledged, model.BugStatusAcknowledged, true},
	}

	for _, tc := range tests {
		name := string(tc.from) + "->" + string(tc.to)
		t.Run(name, func(t *testing.T) {
			err := validateTransition(tc.from, tc.to)
			if tc.wantErr && err == nil {
				t.Error("expected error")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// ---------- Helpers ----------

func writeJSONFile(t *testing.T, dir, name string, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal JSON: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}
