# Agent: Task Model — TaskCategory Enum and Category Field

You are working on the `master` branch of Drem Orchestrator, a terminal-based task orchestrator that coordinates multiple Claude Code agents to work on software projects in parallel.
Your task is the foundation layer: add a `TaskCategory` enum and a `Category` field to the Task model, then update the state machine to allow quickfix-specific transitions.

## Context

Read these specs before starting:
- `docs/quickfix-tasks/prd-quickfix-tasks.md` (sections 4.1, 4.2 — Task Model Changes, Quick Fix Lifecycle)
- `internal/model/enums.go` (existing enum patterns — `TaskStatus`, `AgentType`, `AgentStatus`)
- `internal/model/models.go` (existing `Task` struct)
- `internal/state/machine.go` (existing `ValidTransitions` map and `TransitionTask` function)
- `internal/db/db.go` (auto-migration setup — GORM handles schema changes automatically)
- `internal/testutil/testutil.go` (test helper patterns — `CreateTask` helper)

## Deliverables

### Modified files

#### 1. `internal/model/enums.go`

Add a `TaskCategory` type and constants following the existing enum pattern.

- `type TaskCategory string`
- `const CategoryStandard TaskCategory = "standard"`
- `const CategoryQuickFix TaskCategory = "quickfix"`
- `var allTaskCategories = []TaskCategory{CategoryStandard, CategoryQuickFix}`
- `func (c TaskCategory) String() string` — returns `string(c)`
- `func ParseTaskCategory(s string) (TaskCategory, error)` — same pattern as `ParseTaskStatus`
- `func (c TaskCategory) IsQuickFix() bool` — returns `c == CategoryQuickFix`

#### 2. `internal/model/models.go`

Add a `Category` field to the `Task` struct.

- Add `Category TaskCategory \`gorm:"not null;default:standard"\`` after the `Status` field
- This ensures all existing tasks default to `standard` and the GORM auto-migration adds the column

#### 3. `internal/state/machine.go`

Add a quickfix-specific transition: `backlog → in_progress`. The existing transition from `backlog` only allows `planning` and `paused`. Quick fix tasks need to skip directly to `in_progress`.

- Add `model.StatusInProgress` to the `ValidTransitions[model.StatusBacklog]` slice: `{model.StatusPlanning, model.StatusInProgress, model.StatusPaused}`
- This is safe because the orchestrator controls which transitions actually happen — `processBacklog` will only use `backlog → in_progress` for quickfix tasks

#### 4. `internal/testutil/testutil.go`

Update the `CreateTask` helper to set the `Category` field.

- The existing `CreateTask` helper creates tasks with default values. Ensure it sets `Category: model.CategoryStandard` explicitly so tests are clear about what they're testing
- Add a new `CreateQuickFixTask` helper that mirrors `CreateTask` but sets `Category: model.CategoryQuickFix`:

```go
func CreateQuickFixTask(t *testing.T, db *gorm.DB, projectID uuid.UUID, title string, status model.TaskStatus) *model.Task {
    t.Helper()
    task := &model.Task{
        ID:        uuid.New(),
        ProjectID: projectID,
        Title:     title,
        Description: title,
        Status:    status,
        Category:  model.CategoryQuickFix,
    }
    if err := db.Create(task).Error; err != nil {
        t.Fatalf("create quick fix task: %v", err)
    }
    return task
}
```

### New test file

#### 5. `internal/model/enums_test.go`

Test the new enum type. Follow the table-driven pattern used in existing tests.

- `TestParseTaskCategory` — valid inputs (`"standard"`, `"quickfix"`) return correct values, invalid input returns error
- `TestTaskCategory_String` — returns the string representation
- `TestTaskCategory_IsQuickFix` — `CategoryQuickFix` returns true, `CategoryStandard` returns false

#### 6. `internal/state/machine_test.go`

Add tests for the new transition.

- `TestValidateTransition_BacklogToInProgress` — verify `backlog → in_progress` is now valid
- Verify existing transitions still work (backlog → planning, backlog → paused)

## Scope Limitation

Do NOT modify any files in `internal/orchestrator/` or `internal/tui/`. Those changes belong to other agents. Your job is purely the data model, enum, state machine, and test helpers.

## Conventions

- Namespace: `package model` for enums/models, `package state` for transitions, `package testutil` for helpers
- Follow existing enum patterns exactly (type alias, const block, `allX` slice, `String()`, `ParseX()`)
- All new `.go` files should have a package-level doc comment
- Build verification: `go build ./... && go test ./internal/model/... ./internal/state/... ./internal/testutil/...`
- Format: `gofmt -w .`
