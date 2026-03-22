# Agent: TUI Quick Fix — Checkbox and Visual Distinction

You are working on the `master` branch of Drem Orchestrator, a terminal-based task orchestrator that coordinates multiple Claude Code agents to work on software projects in parallel.
Your task is TUI integration: add a "quick fix" checkbox to the task creation form, pass the category through to `CreateTask`, and add a visual "[QF]" label in the board view.

## Context

Read these specs before starting:
- `docs/quickfix-tasks/prd-quickfix-tasks.md` (sections 4.5, 4.8 — Human-Created Quick Fix Flow, TUI Changes)
- `internal/tui/create.go` (`CreateModel` struct, `NewCreateModel`, `Update`, `View`, `Value`, `Reset`)
- `internal/tui/board.go` (`BoardModel`, `buildDisplayList`, `View` method — the board rendering, `statusColors`, `statusIcons`, `taskAnnotation`)
- `internal/tui/keyhandlers.go` (`handleCreateKeys` — where Enter creates the task, line ~246–270; `FocusCreate` handling)
- `internal/orchestrator/orchestrator.go` (`CreateTask` method — line ~1055, current signature: `func (o *Orchestrator) CreateTask(title, description string, priority int) (*model.Task, error)`)
- `internal/model/enums.go` (`TaskCategory` — `CategoryStandard`, `CategoryQuickFix`)
- `internal/tui/model.go` (main TUI `Model` struct — how `create`, `board`, `orch` are wired)

## Dependencies

This agent depends on Agent 01 (Task Model). If `model.TaskCategory` or `model.CategoryQuickFix` don't exist yet, create stubs:

```go
type TaskCategory string
const CategoryStandard TaskCategory = "standard"
const CategoryQuickFix TaskCategory = "quickfix"
```

## Deliverables

### Modified files

#### 1. `internal/tui/create.go`

Add a "quick fix" checkbox to the `CreateModel`.

**Modify `CreateModel` struct** — add a `quickFix bool` field:

```go
type CreateModel struct {
    titleInput textinput.Model
    descInput  textarea.Model
    quickFix   bool   // true when "quick fix" checkbox is checked
    focused    int    // 0=title, 1=desc, 2=quickfix (add new focus state)
    err        error
}
```

**Modify `Update`** — handle Tab cycling through 3 fields (title → desc → quickfix → title). When focused on quickfix (focused == 2), handle Space to toggle the checkbox:

```go
case "tab":
    c.focused = (c.focused + 1) % 3
    // update focus/blur on inputs accordingly
case "shift+tab":
    c.focused = (c.focused + 2) % 3
    // update focus/blur on inputs accordingly
```

When `c.focused == 2` and key is `" "` (space), toggle `c.quickFix`.

**Modify `View`** — render the checkbox after the description field:

```go
// After the description textarea:
checkbox := "[ ]"
if c.quickFix {
    checkbox = "[x]"
}
qfLabel := fmt.Sprintf("  Quick fix:   %s", checkbox)
if c.focused == 2 {
    qfLabel = lipgloss.NewStyle().Bold(true).Render(qfLabel)
}
b.WriteString(qfLabel + "\n")
```

Update the help line to include the new interaction:

```go
helpStyle.Render("  [tab] switch field  [space] toggle  [enter] create  [esc] cancel")
```

**Modify `Value`** — update return signature to include the quickfix flag:

```go
func (c CreateModel) Value() (title, description string, quickFix bool) {
    return strings.TrimSpace(c.titleInput.Value()), strings.TrimSpace(c.descInput.Value()), c.quickFix
}
```

**Modify `Reset`** — reset the quickFix field:

```go
func (c *CreateModel) Reset() {
    c.titleInput.Reset()
    c.descInput.Reset()
    c.quickFix = false
    c.focused = 0
    c.titleInput.Focus()
    c.descInput.Blur()
    c.err = nil
}
```

#### 2. `internal/tui/keyhandlers.go`

**Modify `handleCreateKeys`** — update the Enter handler to pass quickfix to CreateTask.

Current code (around line 253–262):

```go
case "enter":
    title, desc := m.create.Value()
    ...
    _, err := m.orch.CreateTask(title, desc, 0)
```

Change to:

```go
case "enter":
    title, desc, quickFix := m.create.Value()
    ...
    _, err := m.orch.CreateTask(title, desc, 0, quickFix)
```

Note: The `CreateTask` signature change is below.

#### 3. `internal/orchestrator/orchestrator.go`

**Modify `CreateTask`** — add `quickFix bool` parameter.

Current signature:
```go
func (o *Orchestrator) CreateTask(title, description string, priority int) (*model.Task, error)
```

New signature:
```go
func (o *Orchestrator) CreateTask(title, description string, priority int, quickFix bool) (*model.Task, error)
```

Implementation change — set `Category` based on the new parameter:

```go
category := model.CategoryStandard
if quickFix {
    category = model.CategoryQuickFix
}
task := &model.Task{
    ID:          uuid.New(),
    ProjectID:   o.projectID,
    Title:       title,
    Description: description,
    Status:      model.StatusBacklog,
    Priority:    priority,
    Category:    category,
}
```

**Update all existing callers of `CreateTask`** in the codebase. Search for `CreateTask(` and add `false` as the last argument to any call that doesn't already have the quickFix parameter. Expected callers:
- `internal/tui/keyhandlers.go` (already updated above)
- `internal/tui/bugreports.go` (bug report promotion — should remain `false` for now; the promotion flow creates standard tasks)
- Any test files — search and update

#### 4. `internal/tui/board.go`

**Modify the `View` method** — add a `[QF]` label to quickfix tasks in the board display.

In the `View()` method, after determining the `title` string and before applying `taskAnnotation`, add a quickfix label:

```go
// After: title := task.Title
// Add quickfix label
if task.Category.IsQuickFix() {
    title = "[QF] " + title
}
```

This should appear before the collapse/expand indicator logic so that the [QF] prefix is part of the title text. Place it right after `title := task.Title` (around line 267).

Also, reduce `tw` by 5 chars to account for the `[QF] ` prefix to prevent title truncation.

### Test updates

#### 5. Update existing test callers

Search for `CreateTask(` in all test files and add `false` as the 4th argument. Key files:
- `internal/orchestrator/lifecycle_test.go`
- `internal/orchestrator/coverage_gap_test.go`

## Scope Limitation

- Do NOT modify `processBacklog`, `processQuickFix`, or lifecycle routing — that belongs to Agent 02
- Do NOT create `classifier.go` — that belongs to Agent 03
- Do NOT modify `ingestBugReports` — that belongs to Agent 05
- You own: `create.go`, `board.go`, `keyhandlers.go` (create-related changes only), and `orchestrator.go` (`CreateTask` signature only)

## Conventions

- Namespace: `package tui` for TUI files, `package orchestrator` for CreateTask
- Checkbox rendering: use `[x]` / `[ ]` (ASCII, no Unicode) for terminal compatibility
- Color for QF label: use `colorInfo` (lipgloss.Color("39") — blue) to match the informational style
- Build verification: `go build ./... && go test ./internal/tui/... ./internal/orchestrator/...`
- Format: `gofmt -w .`
