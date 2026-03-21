# Agent: Bug Report Models & Enums

You are working on the `worktree-agent-f29d44cf` branch of Drem Orchestrator, a Go-based agent orchestration system with a GORM/SQLite backend.
Your task is to add the GORM models, enums, and DB migration for the bug report feature.

## Context

Read these specs before starting:
- `plans/agent-bug-reports-prd.md` (New Entity: BugReport, New Entity: BugReportComment, State Machine sections)
- `internal/model/models.go` (existing model patterns — Project, Task, Agent, TaskComment structs)
- `internal/model/enums.go` (existing enum patterns — TaskStatus, AgentType, AgentStatus with String/Parse functions)
- `internal/model/json.go` (JSONField, JSONArray custom types)
- `internal/db/db.go` (AutoMigrate function that must include new models)

## Deliverables

### New files (`internal/model/`)

#### 1. `bugreport.go`

GORM model definitions for the bug report feature.

- `BugReport` struct with fields:
  - `ID uuid.UUID` (gorm:"type:text;primaryKey")
  - `Title string` (gorm:"not null")
  - `Description string` (gorm:"not null")
  - `Category BugReportCategory` (gorm:"not null")
  - `Severity BugReportSeverity` (gorm:"not null")
  - `Status BugReportStatus` (gorm:"not null;default:open")
  - `ReproductionContext string`
  - `AgentID *uuid.UUID` (gorm:"type:text;index")
  - `TaskID *uuid.UUID` (gorm:"type:text;index")
  - `ProjectID uuid.UUID` (gorm:"type:text;not null;index")
  - `PromotedTaskID *uuid.UUID` (gorm:"type:text")
  - `CreatedAt time.Time`
  - `UpdatedAt time.Time`
  - GORM associations: `Agent *Agent`, `Task *Task`, `Project Project`, `PromotedTask *Task`, `Comments []BugReportComment`

- `BugReportComment` struct with fields:
  - `ID uuid.UUID` (gorm:"type:text;primaryKey")
  - `BugReportID uuid.UUID` (gorm:"type:text;not null;index")
  - `Author string` (gorm:"not null") — "user" or "system"
  - `Body string` (gorm:"not null")
  - `CreatedAt time.Time`

#### 2. `bugreport_enums.go`

Enum types following the exact pattern in `enums.go`.

- `BugReportCategory` (string type) with constants:
  - `BugCategoryTooling` = "tooling"
  - `BugCategoryMergeConflict` = "merge_conflict"
  - `BugCategoryRequirements` = "requirements"
  - `BugCategoryConstraintViolation` = "constraint_violation"
  - `BugCategoryUpstreamCode` = "upstream_code"
  - `BugCategoryTestFailure` = "test_failure"
  - `BugCategoryEnvironment` = "environment"
  - `BugCategoryOther` = "other"
  - `allBugReportCategories` slice
  - `String()` method
  - `ParseBugReportCategory(s string) (BugReportCategory, error)` function

- `BugReportSeverity` (string type) with constants:
  - `BugSeverityBlocking` = "blocking"
  - `BugSeverityDegraded` = "degraded"
  - `BugSeverityInformational` = "informational"
  - `allBugReportSeverities` slice
  - `String()` method
  - `ParseBugReportSeverity(s string) (BugReportSeverity, error)` function

- `BugReportStatus` (string type) with constants:
  - `BugStatusOpen` = "open"
  - `BugStatusAcknowledged` = "acknowledged"
  - `BugStatusPromoted` = "promoted"
  - `BugStatusDismissed` = "dismissed"
  - `allBugReportStatuses` slice
  - `String()` method
  - `ParseBugReportStatus(s string) (BugReportStatus, error)` function

### Migration

#### 3. `internal/db/db.go`

Add `&model.BugReport{}` and `&model.BugReportComment{}` to the `AutoMigrate` call in the `AutoMigrate` function (around line 61). Do NOT modify any other logic in this file.

### Tests (`internal/model/`)

#### 4. `bugreport_test.go`

Table-driven tests for:
- All three `Parse*` functions accept valid values and reject invalid ones
- `String()` methods return expected values
- `BugReport` and `BugReportComment` can be created via GORM and round-tripped (use `internal/db` to init an in-memory SQLite DB for this)
- Foreign key associations: BugReport → Agent, BugReport → Task, BugReport → Project, BugReportComment → BugReport
- Nullable fields (AgentID, TaskID, PromotedTaskID) work correctly when nil

Follow the test patterns in `internal/supervisor/supervisor_test.go` and `internal/state/machine_test.go` for style.

## Scope Limitation

Only create/modify files in `internal/model/` and `internal/db/db.go`. Do not touch any other packages.

## Conventions

- Package: `model` (for model files), `db` (for migration)
- Go 1.22+, `gofmt` for formatting
- Exported functions have doc comments
- Error wrapping with `fmt.Errorf("context: %w", err)`
- Table-driven tests
- Build verification: `go build ./... && go test ./internal/model/... ./internal/db/...`
