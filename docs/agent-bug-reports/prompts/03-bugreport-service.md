# Agent: Bug Report Service Package

You are working on the `worktree-agent-f29d44cf` branch of Drem Orchestrator, a Go-based agent orchestration system with a GORM/SQLite backend.
Your task is to implement the `internal/bugreport/` package — the core service for ingesting, managing, and promoting bug reports.

## Context

Read these specs before starting:
- `plans/agent-bug-reports-prd.md` (Filing Mechanism, State Machine, Promotion Workflow, Testing Decisions sections)
- `internal/model/bugreport.go` (BugReport, BugReportComment GORM models)
- `internal/model/bugreport_enums.go` (BugReportCategory, BugReportSeverity, BugReportStatus enums)
- `internal/state/machine.go` (pattern for state machine: `ValidTransitions` map, `ValidateTransition()`, `TransitionTask()`)
- `internal/model/models.go` (Task struct — needed for promotion workflow)
- `internal/db/db.go` (Init function for test DB setup)
- `internal/constraints/config.go` (pattern for file-based ingestion: load → validate → process)

## Dependencies

This agent depends on Agent 01 (Models & Enums). If the model files don't exist yet, create stub files at `internal/model/bugreport.go` and `internal/model/bugreport_enums.go` with the types from the PRD and implement against them.

## Deliverables

### New files (`internal/bugreport/`)

#### 1. `service.go`

Core service with `*gorm.DB` dependency.

```go
// Service manages bug report lifecycle operations.
type Service struct {
    db *gorm.DB
}

// New creates a Service.
func New(db *gorm.DB) *Service

// Create inserts a new bug report.
func (s *Service) Create(report *model.BugReport) error

// Get retrieves a bug report by ID with preloaded comments.
func (s *Service) Get(id uuid.UUID) (*model.BugReport, error)

// List returns bug reports matching the given filters.
func (s *Service) List(filters ListFilters) ([]model.BugReport, error)

// Acknowledge transitions a bug report from open to acknowledged.
func (s *Service) Acknowledge(id uuid.UUID) error

// Dismiss transitions a bug report to dismissed.
func (s *Service) Dismiss(id uuid.UUID) error

// Promote transitions a bug report to promoted, creating a new Task in backlog status.
// Returns the created task. The caller opens $EDITOR — this function only handles DB operations.
func (s *Service) Promote(id uuid.UUID, taskTitle, taskDescription string, projectID uuid.UUID) (*model.Task, error)

// Delete permanently removes a bug report and its comments from the database.
func (s *Service) Delete(id uuid.UUID) error

// AddComment adds a comment to a bug report.
func (s *Service) AddComment(bugReportID uuid.UUID, author, body string) error

// GetComments returns comments for a bug report in chronological order.
func (s *Service) GetComments(bugReportID uuid.UUID) ([]model.BugReportComment, error)
```

`ListFilters` struct:
```go
type ListFilters struct {
    Category  *model.BugReportCategory
    Severity  *model.BugReportSeverity
    Status    *model.BugReportStatus
    ProjectID *uuid.UUID
}
```

State machine rules (implement inline, no need for a separate state package):
```
Valid transitions:
  open → acknowledged
  open → promoted
  open → dismissed
  acknowledged → promoted
  acknowledged → dismissed
```
Reject any other transition with a descriptive error.

Promotion workflow:
1. Validate bug report exists and is in `open` or `acknowledged` status
2. Create a new `model.Task` with `Status: model.StatusBacklog`, using the provided title/description and the bug report's `ProjectID`
3. Update the bug report: `Status = BugStatusPromoted`, `PromotedTaskID = &task.ID`
4. Wrap in a GORM transaction so both writes succeed or fail together
5. Return the created task

Delete:
- Use `db.Transaction` to delete comments first (cascade), then the bug report itself
- Return error if the bug report doesn't exist

#### 2. `ingest.go`

File-drop ingestion from `.drem/bug-reports/`.

```go
// BugReportFile is the JSON schema agents write to .drem/bug-reports/<uuid>.json.
type BugReportFile struct {
    Title                string `json:"title"`
    Description          string `json:"description"`
    Category             string `json:"category"`
    Severity             string `json:"severity"`
    ReproductionContext  string `json:"reproduction_context"`
    AgentID              string `json:"agent_id"`
    TaskID               string `json:"task_id"`
}

// Ingest scans dir for .json files, parses and validates them, inserts valid
// reports into the database (associating with projectID), and cleans up.
// Invalid files are moved to dir/failed/. Returns the number of reports ingested.
func (s *Service) Ingest(dir string, projectID uuid.UUID) (int, error)
```

Ingestion logic:
1. Read all `*.json` files in `dir` (use `os.ReadDir`, filter by `.json` suffix)
2. For each file:
   a. Parse JSON into `BugReportFile`
   b. Validate: title and description must be non-empty; category and severity must parse via the enum `Parse*` functions
   c. Parse `AgentID` and `TaskID` as UUIDs (if provided — they're optional)
   d. On success: create a `model.BugReport` via `s.Create()` and delete the JSON file
   e. On failure: move the file to `dir/failed/` (create the directory if needed), log the error
3. Return the count of successfully ingested reports

#### 3. `service_test.go`

Comprehensive tests using an in-memory SQLite DB (via `db.Init(":memory:")`).

Test categories (table-driven where applicable):
- **Create**: valid bug report is inserted and retrievable; required fields (title, description) are validated
- **Ingestion**: valid JSON file is parsed, inserted, and deleted; invalid JSON is moved to `failed/`; missing required fields cause failure; file with invalid category/severity is rejected; multiple files in one pass; empty directory returns 0
- **State transitions**: open→acknowledged succeeds; open→promoted succeeds; open→dismissed succeeds; acknowledged→promoted succeeds; acknowledged→dismissed succeeds; promoted→anything fails; dismissed→anything fails; open→open fails
- **Promotion**: creates a task with correct fields; sets PromotedTaskID; transitions status to promoted; fails if bug report is already promoted/dismissed
- **Filtering**: filter by category returns correct subset; filter by severity; filter by status; filter by project; combined filters; no filters returns all
- **Comments**: add comment associates with bug report; comments returned in chronological order; comments on non-existent bug report fails
- **Delete**: record removed from DB; associated comments cascade-deleted; delete non-existent returns error
- **Get**: returns bug report with preloaded comments; returns error for non-existent ID

## Scope Limitation

Only create files in `internal/bugreport/`. Do not modify files in any other package. If model types don't exist, create stubs in this package (but prefer depending on the real models if they exist).

## Conventions

- Package: `bugreport`
- Go 1.22+, `gofmt` for formatting
- Exported functions have doc comments
- Error wrapping with `fmt.Errorf("context: %w", err)`
- Use `context.Context` for cancellation where appropriate (optional for v1)
- Table-driven tests
- Build verification: `go build ./... && go test ./internal/bugreport/...`
