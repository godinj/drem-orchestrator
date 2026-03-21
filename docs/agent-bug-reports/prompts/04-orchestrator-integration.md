# Agent: Orchestrator Integration

You are working on the `worktree-agent-f29d44cf` branch of Drem Orchestrator, a Go-based agent orchestration system.
Your task is to integrate the bug report ingestion into the orchestrator's tick loop and ensure the drop directory is created on startup.

## Context

Read these specs before starting:
- `plans/agent-bug-reports-prd.md` (Filing Mechanism: File-Drop with Tick-Based Ingestion, Further Notes sections)
- `internal/orchestrator/orchestrator.go` (Orchestrator struct, `New()`, `doTick()`, `Run()` — lines 1-200)
- `internal/bugreport/service.go` (Service struct, `New()`, `Ingest()` function)
- `internal/bugreport/ingest.go` (Ingest method signature and behavior)
- `cmd/drem/main.go` (startup initialization — where to create the drop directory)
- `.gitignore` (check if `.drem/` is already listed)

## Dependencies

This agent depends on Agent 03 (Bug Report Service). If `internal/bugreport/` doesn't exist yet, create a minimal stub:

```go
package bugreport

import (
    "github.com/google/uuid"
    "gorm.io/gorm"
)

type Service struct{ db *gorm.DB }
func New(db *gorm.DB) *Service { return &Service{db: db} }
func (s *Service) Ingest(dir string, projectID uuid.UUID) (int, error) { return 0, nil }
```

## Deliverables

### Modified files

#### 1. `internal/orchestrator/orchestrator.go`

Add the bug report service to the Orchestrator:

- Add a `bugreport *bugreport.Service` field to the `Orchestrator` struct
- Add a `bugreportDir string` field to the `Orchestrator` struct
- Update `New()` to accept and store a `*bugreport.Service` parameter and a `bugreportDir string` parameter. Add these as the last positional parameters before any variadic parameters.
- Add the import: `"github.com/godinj/drem-orchestrator/internal/bugreport"`

Hook into `doTick()`:
- At the **beginning** of `doTick()`, call `o.ingestBugReports()` (a new private method)
- `ingestBugReports()` calls `o.bugreport.Ingest(o.bugreportDir, o.projectID)`
- If `o.bugreport` is nil, return early (backward compatibility)
- Log the count at Info level if > 0: `o.logger.Info("ingested bug reports", "count", n)`
- Log errors at Warn level: `o.logger.Warn("bug report ingestion error", "error", err)`
- Ingestion errors do NOT stop the tick — they are logged and ignored

#### 2. `cmd/drem/main.go`

During startup initialization (after DB init, before orchestrator creation):

- Determine the bug report drop directory: use the project's bare repo path to derive `.drem/bug-reports/` — e.g., `filepath.Join(bareRepoPath, ".drem", "bug-reports")`
- Create the directory if it doesn't exist: `os.MkdirAll(bugReportDir, 0o755)`
- Create the bugreport service: `bugReportSvc := bugreport.New(db)`
- Pass both `bugReportSvc` and `bugReportDir` to `orchestrator.New()`

#### 3. `.gitignore`

Add `.drem/bug-reports/` if not already present. Add it near any existing `.drem/` entries, or at the end of the file if none exist.

### Updated call sites

Any file that calls `orchestrator.New()` must be updated with the new parameters. Search for `orchestrator.New(` across the codebase to find all call sites. In test files, pass `nil` for the bugreport service and `""` for the directory to maintain backward compatibility.

### Tests

#### 4. `internal/orchestrator/orchestrator_test.go` (or existing test file)

Add a test for the ingestion integration:
- Set up an in-memory DB and a temp directory for bug report drop
- Write a valid JSON bug report file to the drop directory
- Create an Orchestrator with a real bugreport.Service
- Call `doTick()` (or the public method that triggers it)
- Assert the bug report was ingested into the DB and the JSON file was deleted

If `doTick` is not directly testable, test `ingestBugReports()` directly or test via the `Ingest` method on the service (which is already tested in Agent 03). In that case, a simple integration smoke test that verifies the plumbing is sufficient.

## Scope Limitation

Only modify files in `internal/orchestrator/`, `cmd/drem/`, and `.gitignore`. Do not modify the bugreport package, models, TUI, or prompts.

## Conventions

- Package: `orchestrator`
- Go 1.22+, `gofmt` for formatting
- Exported functions have doc comments
- Error wrapping with `fmt.Errorf("context: %w", err)`
- Table-driven tests
- Build verification: `go build ./... && go test ./internal/orchestrator/...`
