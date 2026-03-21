# Agent: TUI Bug Report Screen

You are working on the `worktree-agent-f29d44cf` branch of Drem Orchestrator, a Go-based agent orchestration system with a Bubble Tea TUI.
Your task is to implement a dedicated bug report screen in the TUI, accessible via the `b` keybinding.

## Context

Read these specs before starting:
- `plans/agent-bug-reports-prd.md` (TUI: Dedicated Bug Report Screen section — keybindings, columns, detail pane, filter bar)
- `internal/tui/app.go` (root Model struct, Focus enum, `NewModel()`, `Update()`, `View()`, panel layout patterns)
- `internal/tui/keyhandlers.go` (key routing — `handleKey()`, `handleBoardKeys()`, per-focus dispatch)
- `internal/tui/board.go` (BoardModel pattern — list with cursor, scroll, display entries)
- `internal/tui/detail.go` (DetailModel pattern — scrollable detail pane with sections)
- `internal/tui/styles.go` (lipgloss style definitions — `panelStyle`, `colorPrimary`, `colorDanger`, etc.)
- `internal/tui/feedback.go` (FeedbackModel — reusable text input overlay for comments)
- `internal/tui/create.go` (CreateModel — overlay pattern reference)
- `internal/bugreport/service.go` (Service API — `List()`, `Get()`, `Acknowledge()`, `Dismiss()`, `Promote()`, `Delete()`, `AddComment()`, `ListFilters`)
- `internal/model/bugreport.go` (BugReport, BugReportComment structs)
- `internal/model/bugreport_enums.go` (BugReportCategory, BugReportSeverity, BugReportStatus enums)

## Dependencies

This agent depends on Agent 03 (Bug Report Service). If `internal/bugreport/` doesn't exist yet, create a minimal stub with the `Service` type and method signatures so the TUI compiles against it.

## Deliverables

### New files (`internal/tui/`)

#### 1. `bugreports.go`

Main bug report screen sub-model.

```go
// BugReportsModel manages the bug report list screen.
type BugReportsModel struct {
    db          *gorm.DB
    bugreportSvc *bugreport.Service
    projectID   uuid.UUID
    reports     []model.BugReport
    cursor      int
    scrollOffset int
    width       int
    height      int
    filters     bugReportFilters

    // Detail state (inline, not a separate sub-model)
    selectedReport *model.BugReport
    comments       []model.BugReportComment

    // Filter UI state
    filterMode     bool
    filterCursor   int
}

type bugReportFilters struct {
    category  *model.BugReportCategory
    severity  *model.BugReportSeverity
    status    *model.BugReportStatus
    showDismissed bool  // when false (default), dismissed reports are hidden
}

func NewBugReportsModel(db *gorm.DB, svc *bugreport.Service, projectID uuid.UUID) BugReportsModel
```

**List View** — render columns:
- Severity icon: `!` (blocking, red), `~` (degraded, yellow), `·` (informational, dim)
- Category tag: truncated to 12 chars, e.g., `[tooling]`, `[merge_conf…]`
- Title: truncated to fit available width
- Agent type: from the associated Agent (if loaded), or blank
- Associated task title: from the associated Task (if loaded), or blank
- Status: colored — open (white), acknowledged (cyan), promoted (green), dismissed (dim)
- Timestamp: relative format ("2m ago", "1h ago", "3d ago")

**Detail Pane** — when a report is selected, show below the list (or in a split):
- Full title
- Full description
- Reproduction context (if present)
- Comments list
- Status, category, severity, filing agent, associated task
- Available actions help line

**Filter Bar** — activated via `/`:
- Cycle through filter dimensions with Tab
- Cycle values within a dimension with j/k or arrow keys
- Enter to apply, Esc to cancel
- Show active filters in the header

#### 2. `bugreport_actions.go`

Action handlers for the bug report screen.

```go
// handleBugReportKeys dispatches keys when the bug report screen is focused.
func (m Model) handleBugReportKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd)
```

Keybindings:
- `j/k` or `down/up`: navigate list
- `a`: acknowledge selected report (call `svc.Acknowledge()`, refresh)
- `p`: promote selected report — create temp file with title/description, spawn `$EDITOR`, on exit create task via `svc.Promote()`, refresh
- `D`: dismiss selected report (call `svc.Dismiss()`, refresh)
- `x`: hard-delete with confirmation — show a confirm prompt ("Delete bug report? y/n"), on `y` call `svc.Delete()`, refresh
- `c`: add comment — open the FeedbackModel overlay, on submit call `svc.AddComment()`, refresh
- `/`: toggle filter mode
- `b` or `Esc`: return to main dashboard
- `Enter`: toggle detail view for selected report

Promotion workflow:
1. Get the selected bug report
2. Write a temp file: `os.CreateTemp("", "bugreport-promote-*.md")` with title on line 1, blank line, description
3. Spawn `$EDITOR` (or `vi` as fallback) on the temp file using `tea.ExecProcess`
4. On return, read the temp file: first line = task title, rest = task description
5. Call `svc.Promote(bugReportID, title, description, projectID)`
6. Clean up temp file
7. Refresh the list

### Modified files

#### 3. `internal/tui/app.go`

Integrate the bug report screen:

- Add `FocusBugReports` to the `Focus` enum (after `FocusFeedback`)
- Add `bugreports BugReportsModel` field to the `Model` struct
- Add `bugreportSvc *bugreport.Service` field to the `Model` struct
- Update `NewModel()` to accept a `*bugreport.Service` parameter and initialize `BugReportsModel`
- In `Update()`: add `case bugReportsLoadedMsg:` handler to update the bug report list
- In `View()`: when `m.focus == FocusBugReports`, render the bug report screen instead of the main dashboard

#### 4. `internal/tui/keyhandlers.go`

- In `handleKey()`: add `case FocusBugReports:` to dispatch to `handleBugReportKeys()`
- In `handleBoardKeys()`: add `case "b":` to switch focus to `FocusBugReports` and trigger a bug report data load

#### 5. `internal/tui/styles.go`

Add any new styles needed for the bug report screen:
- Severity colors: blocking = red/`colorDanger`, degraded = yellow/`colorWarning` (add if missing), informational = dim
- Category tag style
- Status-specific colors for bug report statuses

### Updated call sites

Any file that calls `tui.NewModel()` must be updated with the new `*bugreport.Service` parameter. Search for `tui.NewModel(` across the codebase. In test files, pass `nil` for the service.

### Tea messages

```go
type bugReportsLoadedMsg struct {
    reports []model.BugReport
}

type bugReportActionMsg struct {
    err error
}

type editorFinishedMsg struct {
    tempFile string
    err      error
}
```

## Scope Limitation

Only create/modify files in `internal/tui/` and files that call `tui.NewModel()`. Do not modify the bugreport service, models, orchestrator, or prompts.

## Conventions

- Package: `tui`
- Go 1.22+, `gofmt` for formatting
- Exported functions have doc comments
- Follow existing Bubble Tea patterns: Init/Update/View, tea.Cmd for async, tea.Msg for events
- Use lipgloss for styling — reuse existing style variables from `styles.go`
- Vim-style navigation (j/k)
- Build verification: `go build ./... && go test ./internal/tui/...`
