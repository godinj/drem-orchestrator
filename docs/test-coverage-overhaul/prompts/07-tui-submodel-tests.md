# Agent: TUI Sub-Model Tests

You are working on the `master` branch of drem-orchestrator, a Go TUI tool that orchestrates Claude Code agents via tmux.
Your task is to add tests for the pure functions in the TUI sub-models (`DetailModel` and `AgentsModel`), raising coverage from 4.3% toward ~25%.

## Context

Read these before starting:
- `CLAUDE.md` (build commands, conventions)
- `docs/test-coverage-overhaul/prd-test-coverage.md` (Phase 2c section)
- `internal/tui/detail.go` (DetailModel — ~300 LOC)
- `internal/tui/agents.go` (AgentsModel — ~226 LOC)
- `internal/tui/board.go` (BoardModel — 365 LOC, already has tests)
- `internal/tui/board_test.go` (existing tests — pattern to follow)
- `internal/tui/app.go` (Model — 1,348 LOC, for understanding types and imports)
- `internal/model/models.go` (model types: Task, Agent, TaskComment)

The only existing TUI tests are in `board_test.go` covering `buildDisplayList()` and `Selected()`. Follow the same test patterns.

## Deliverables

### New files

#### 1. `internal/tui/detail_test.go` (~120–150 LOC)

Test the pure functions in DetailModel. Read `detail.go` carefully to understand the struct fields and method signatures.

```go
func TestDeletableItems(t *testing.T)
```
Table-driven:
- Task with 2 comments and a 3-step plan → returns items for all deletable things
- Task with no comments and no plan → returns empty list
- Task with comments only → returns comment items
- Task with plan steps only → returns plan step items
- Verify each returned item has correct type/label (read `deletableItems()` to understand the item struct)

```go
func TestSelectedDeleteItem(t *testing.T)
```
- Set up DetailModel with deletable items and cursor at index 0 → returns first item
- Cursor at last index → returns last item
- Cursor at -1 or beyond bounds → returns nil or zero value (check actual behavior)
- No deletable items, cursor at 0 → returns nil

```go
func TestIsDeleteTarget(t *testing.T)
```
- Read the actual function to understand what constitutes a "delete target" vs other item types
- Test with valid delete target → true
- Test with non-deletable item (e.g., section header) → false

```go
func TestIsDeleteSection(t *testing.T)
```
- Section header item → true
- Non-section item → false

#### 2. `internal/tui/agents_test.go` (~120–150 LOC)

Test the pure functions in AgentsModel. Read `agents.go` carefully to understand the struct fields and method signatures.

```go
func TestVisibleAgents(t *testing.T)
```
Table-driven:
- No filter set → returns all agents
- Filter set to specific taskID → returns only agents matching that task
- Filter set to taskID with subtasks → returns agents for task AND its subtasks
- No agents match filter → returns empty slice
- Empty agents list → returns empty slice

```go
func TestIsSubtaskID(t *testing.T)
```
Read the method to understand how it determines subtask relationships.
- ID that is a subtask of the filtered task → true
- ID that is unrelated → false
- No filter set → behavior depends on implementation (test actual behavior)

```go
func TestSetTaskFilter(t *testing.T)
```
- Set filter → verify the filter field is updated
- Set filter → verify agent cursor is reset to 0
- Set nil/zero filter → verify filter is cleared

```go
func TestClampAgentCursor(t *testing.T)
```
Table-driven:
- Cursor at 0 with 5 agents → stays 0
- Cursor at 4 with 5 agents → stays 4
- Cursor at 5 with 5 agents (out of bounds) → clamped to 4
- Cursor at 3, agents reduced to 2 → clamped to 1
- 0 agents → cursor clamped to 0
- Negative cursor (if possible) → clamped to 0

```go
func TestAgentsModel_Selected(t *testing.T)
```
If there's a `Selected()` method on AgentsModel:
- Valid cursor position → returns correct agent
- Empty list → returns nil
- Out of bounds → returns nil

## Scope Limitation

- Only create new test files in `internal/tui/`
- Do NOT modify any source files (`detail.go`, `agents.go`, `app.go`, etc.)
- Do NOT test `app.go` Update/View cycle — that's out of scope
- Follow the patterns in `board_test.go` for constructing test data
- If struct fields are private and you need test setup, construct via exported constructors or by setting exported fields

## Verification

```bash
go test ./internal/tui/ -v -cover
```

All existing and new tests must pass. Coverage should reach ~20-25%.

## Conventions

- `gofmt` for formatting
- Table-driven tests with `t.Run(tc.name, ...)`
- `t.Helper()` on test helpers
- Follow the style established in `board_test.go`
