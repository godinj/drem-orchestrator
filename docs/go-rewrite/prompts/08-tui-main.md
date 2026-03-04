# Agent: TUI Dashboard & Entry Point

You are working on the `feature/go-rewrite` branch of Drem Orchestrator, a multi-agent task orchestration system being rewritten from Python to Go.
Your task is to implement the Bubble Tea TUI dashboard and the main entry point that wires everything together.

## Context

Read these before starting:
- `docs/go-rewrite/DESIGN.md` (focus on "TUI Dashboard" section — layout, key bindings, Bubble Tea components, communication pattern, and "Entry Point" section)
- `internal/model/models.go` (Go models — Task, Agent, Project, enums)
- `internal/orchestrator/orchestrator.go` (Orchestrator — Run(), Event, HandlePlanApproved/Rejected, HandleTestPassed/Failed, PauseTask, ResumeTask, RetryTask, CreateTask)
- `internal/tmux/tmux.go` (TmuxManager — FocusWindow, CapturePane, ListWindows)
- `internal/db/db.go` (DB init)
- `cmd/drem/config.go` (Config, LoadConfig)
- `CLAUDE.md` (build commands, conventions)

## Dependencies

This agent depends on all previous agents (01-07). If files don't exist yet, create minimal stubs.

## Deliverables

### New file: `internal/tui/styles.go`

Lipgloss styles for the TUI:

```go
package tui

import "github.com/charmbracelet/lipgloss"

var (
    // Colors
    colorPrimary   = lipgloss.Color("62")   // Purple
    colorSecondary = lipgloss.Color("241")  // Gray
    colorSuccess   = lipgloss.Color("42")   // Green
    colorWarning   = lipgloss.Color("214")  // Orange
    colorDanger    = lipgloss.Color("196")  // Red
    colorInfo      = lipgloss.Color("39")   // Blue

    // Status colors
    statusColors = map[model.TaskStatus]lipgloss.Color{
        model.StatusBacklog:       lipgloss.Color("241"),
        model.StatusPlanning:      lipgloss.Color("39"),
        model.StatusPlanReview:    lipgloss.Color("214"),
        model.StatusInProgress:    lipgloss.Color("62"),
        model.StatusTestingReady:  lipgloss.Color("214"),
        model.StatusManualTesting: lipgloss.Color("214"),
        model.StatusMerging:       lipgloss.Color("62"),
        model.StatusPaused:        lipgloss.Color("241"),
        model.StatusDone:          lipgloss.Color("42"),
        model.StatusFailed:        lipgloss.Color("196"),
    }

    // Component styles
    titleStyle      = lipgloss.NewStyle().Bold(true).Foreground(colorPrimary)
    subtitleStyle   = lipgloss.NewStyle().Foreground(colorSecondary)
    selectedStyle   = lipgloss.NewStyle().Bold(true).Background(lipgloss.Color("236"))
    statusBadge     = lipgloss.NewStyle().Padding(0, 1)
    panelStyle      = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
    helpStyle       = lipgloss.NewStyle().Foreground(colorSecondary)
)

// StatusBadge renders a colored status badge.
func StatusBadge(status model.TaskStatus) string

// AgentStatusBadge renders a colored agent status.
func AgentStatusBadge(status model.AgentStatus) string
```

### New file: `internal/tui/keys.go`

Key binding definitions:

```go
package tui

import "github.com/charmbracelet/bubbles/key"

type keyMap struct {
    Up       key.Binding
    Down     key.Binding
    Tab      key.Binding
    Enter    key.Binding
    Approve  key.Binding // a
    Reject   key.Binding // r
    TestPass key.Binding // t
    TestFail key.Binding // f
    Jump     key.Binding // g - jump to agent tmux window
    New      key.Binding // n - new task
    Pause    key.Binding // p - pause/resume
    Retry    key.Binding // R - retry failed
    Log      key.Binding // l - view agent log
    Quit     key.Binding // q
    Esc      key.Binding
}

func defaultKeyMap() keyMap
```

### New file: `internal/tui/board.go`

Task board view — the main panel showing tasks grouped by status:

```go
package tui

// BoardModel renders the task list.
type BoardModel struct {
    tasks    []model.Task
    cursor   int
    width    int
    height   int
}

func NewBoardModel() BoardModel
func (b BoardModel) Update(msg tea.Msg) (BoardModel, tea.Cmd)
func (b BoardModel) View() string

// Selected returns the currently highlighted task.
func (b BoardModel) Selected() *model.Task
```

The board view should show tasks as a flat list (not columns — TUI space is limited), with status badges:

```
  BACKLOG   Add OAuth integration
  PLANNING  Refactor auth module
> REVIEW    Add login flow              ← selected (highlighted)
  ACTIVE    Setup database migrations
  DONE      Initialize project scaffold
```

Use status icons:
- BACKLOG: `○`
- PLANNING: `◌`
- PLAN_REVIEW: `◉`
- IN_PROGRESS: `●`
- TESTING_READY: `◈`
- MANUAL_TESTING: `◇`
- MERGING: `⟳`
- PAUSED: `⏸`
- DONE: `✓`
- FAILED: `✗`

Sort tasks: actionable states first, then human gates, then done/failed.

### New file: `internal/tui/agents.go`

Agent list view — shows running agents:

```go
package tui

// AgentsModel renders the agent sidebar.
type AgentsModel struct {
    agents []model.Agent
    cursor int
    width  int
    height int
}

func NewAgentsModel() AgentsModel
func (a AgentsModel) Update(msg tea.Msg) (AgentsModel, tea.Cmd)
func (a AgentsModel) View() string

// Selected returns the currently highlighted agent.
func (a AgentsModel) Selected() *model.Agent
```

Each agent entry shows:

```
  coder-a1b2c3d4   WORKING
    task: Add login flow
    window: coder-a1b2c3d4
```

### New file: `internal/tui/detail.go`

Task detail view — bottom panel with task info and actions:

```go
package tui

// DetailModel renders task details and available actions.
type DetailModel struct {
    task     *model.Task
    subtasks []model.Task
    agent    *model.Agent
    width    int
    height   int
}

func NewDetailModel() DetailModel
func (d DetailModel) Update(msg tea.Msg) (DetailModel, tea.Cmd)
func (d DetailModel) View() string
```

Shows:
1. Task title and description (truncated)
2. Status, assigned agent name, worktree branch
3. Subtask progress: "Subtasks: 2/5 complete"
4. Available actions based on status:
   - PLAN_REVIEW: "[a]pprove plan  [r]eject plan"
   - MANUAL_TESTING: "[t]est pass  [f]ail test"
   - IN_PROGRESS: "[p]ause"
   - PAUSED: "[p] resume"
   - FAILED: "[R]etry"
   - Any with agent: "[g] jump to agent  [l] view log"

### New file: `internal/tui/create.go`

New task creation form:

```go
package tui

import "github.com/charmbracelet/bubbles/textinput"

// CreateModel is the new-task form.
type CreateModel struct {
    titleInput textinput.Model
    descInput  textinput.Model
    focused    int // 0=title, 1=desc
    err        error
}

func NewCreateModel() CreateModel
func (c CreateModel) Update(msg tea.Msg) (CreateModel, tea.Cmd)
func (c CreateModel) View() string

// Value returns the entered title and description.
func (c CreateModel) Value() (title, description string)
```

Simple 2-field form. Tab to switch fields, Enter to submit, Esc to cancel.

### New file: `internal/tui/feedback.go`

Feedback dialog for plan rejection and test failure:

```go
package tui

import "github.com/charmbracelet/bubbles/textinput"

// FeedbackModel is a text input dialog for rejection/failure feedback.
type FeedbackModel struct {
    input   textinput.Model
    title   string  // "Reject Plan" or "Fail Test"
    visible bool
}

func NewFeedbackModel(title string) FeedbackModel
func (f FeedbackModel) Update(msg tea.Msg) (FeedbackModel, tea.Cmd)
func (f FeedbackModel) View() string

func (f FeedbackModel) Value() string
func (f *FeedbackModel) Show()
func (f *FeedbackModel) Hide()
```

### New file: `internal/tui/app.go`

Main Bubble Tea model that composes everything:

```go
package tui

import (
    "github.com/charmbracelet/bubbletea"
    "github.com/google/uuid"
    "gorm.io/gorm"

    "github.com/godinj/drem-orchestrator/internal/model"
    "github.com/godinj/drem-orchestrator/internal/orchestrator"
    tmuxpkg "github.com/godinj/drem-orchestrator/internal/tmux"
)

// Focus tracks which panel has keyboard focus.
type Focus int
const (
    FocusBoard Focus = iota
    FocusAgents
    FocusDetail
    FocusCreate
    FocusFeedback
)

// Model is the root Bubble Tea model.
type Model struct {
    db        *gorm.DB
    orch      *orchestrator.Orchestrator
    tmux      *tmuxpkg.Manager
    projectID uuid.UUID
    events    <-chan orchestrator.Event

    board     BoardModel
    agents    AgentsModel
    detail    DetailModel
    create    CreateModel
    feedback  FeedbackModel

    focus     Focus
    width     int
    height    int
    err       error
}

// NewModel creates the root TUI model.
func NewModel(
    db *gorm.DB,
    orch *orchestrator.Orchestrator,
    tmux *tmuxpkg.Manager,
    projectID uuid.UUID,
    events <-chan orchestrator.Event,
) Model

func (m Model) Init() tea.Cmd
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd)
func (m Model) View() string
```

#### `Init()`

Return commands to:
1. Load initial tasks from DB
2. Load initial agents from DB
3. Start listening for orchestrator events

#### Event bridge: orchestrator channel → tea.Msg

```go
// EventMsg wraps an orchestrator Event as a tea.Msg.
type EventMsg orchestrator.Event

// listenForEvents returns a Cmd that blocks on the events channel.
func listenForEvents(events <-chan orchestrator.Event) tea.Cmd {
    return func() tea.Msg {
        e := <-events
        return EventMsg(e)
    }
}
```

In `Update`, when receiving `EventMsg`, refresh the relevant data from DB and re-listen:

```go
case EventMsg:
    // Refresh tasks and agents from DB
    m.refreshData()
    return m, listenForEvents(m.events)
```

#### `Update(msg tea.Msg)` — Key handling

Handle keys based on current focus:

- **FocusBoard**: j/k navigate tasks, Tab → FocusAgents, Enter → select task for detail, n → FocusCreate
- **FocusAgents**: j/k navigate agents, Tab → FocusBoard, g → jump to agent tmux window
- **FocusCreate**: delegate to CreateModel, Esc → back to board, Enter → create task via `orch.CreateTask()`, then back to board
- **FocusFeedback**: delegate to FeedbackModel, Esc → cancel, Enter → submit

Action keys (when a task is selected):
- `a` → if task is PLAN_REVIEW: `orch.HandlePlanApproved(taskID)`
- `r` → if task is PLAN_REVIEW: show feedback dialog, then `orch.HandlePlanRejected(taskID, feedback)`
- `t` → if task is MANUAL_TESTING: `orch.HandleTestPassed(taskID)`
- `f` → if task is MANUAL_TESTING: show feedback dialog, then `orch.HandleTestFailed(taskID, feedback)`
- `p` → pause or resume depending on current status
- `R` → if task is FAILED: `orch.RetryTask(taskID)`
- `g` → if task has assigned agent: `tmux.FocusWindow(agent.TmuxWindow)`
- `l` → if task has assigned agent: capture pane output and show in detail
- `q` → quit

#### `View()` — Layout

Compose the layout using lipgloss:

```
┌─ Drem Orchestrator ─────────────────────────────────┐
│ [Backlog: N] [Planning: N] [Review: N] ...          │
│                                                      │
│ ┌─ Tasks ──────────────┬─ Agents ──────────────────┐ │
│ │ board.View()         │ agents.View()             │ │
│ │                      │                            │ │
│ └──────────────────────┴────────────────────────────┘ │
│ ┌─ Detail ──────────────────────────────────────────┐ │
│ │ detail.View()                                     │ │
│ └───────────────────────────────────────────────────┘ │
│ [keys help line]                                     │
└──────────────────────────────────────────────────────┘
```

Split horizontally: tasks panel (60%) | agents panel (40%).
Detail panel below, full width.
Status bar at top with task counts per status.
Help bar at bottom with available key bindings.

Handle `tea.WindowSizeMsg` to resize all panels.

#### DB refresh helpers

```go
func (m *Model) refreshData() {
    // Query all tasks for project, sorted by priority then created_at
    m.db.Where("project_id = ?", m.projectID).Order("priority desc, created_at").Find(&m.board.tasks)

    // Query all agents for project
    m.db.Where("project_id = ?", m.projectID).Find(&m.agents.agents)

    // Update detail if a task is selected
    if selected := m.board.Selected(); selected != nil {
        m.db.Where("parent_task_id = ?", selected.ID).Find(&m.detail.subtasks)
        if selected.AssignedAgentID != nil {
            m.db.First(&m.detail.agent, selected.AssignedAgentID)
        }
    }
}
```

### New file: `cmd/drem/main.go`

Entry point that wires everything together:

```go
package main

import (
    "context"
    "flag"
    "fmt"
    "log"
    "os"

    tea "github.com/charmbracelet/bubbletea"
    "github.com/google/uuid"

    "github.com/godinj/drem-orchestrator/internal/agent"
    "github.com/godinj/drem-orchestrator/internal/db"
    "github.com/godinj/drem-orchestrator/internal/memory"
    "github.com/godinj/drem-orchestrator/internal/merge"
    "github.com/godinj/drem-orchestrator/internal/model"
    "github.com/godinj/drem-orchestrator/internal/orchestrator"
    tmuxpkg "github.com/godinj/drem-orchestrator/internal/tmux"
    "github.com/godinj/drem-orchestrator/internal/tui"
    "github.com/godinj/drem-orchestrator/internal/worktree"
)

func main() {
    // Parse flags
    configPath := flag.String("config", "drem.toml", "config file path")
    repoPath := flag.String("repo", "", "bare repo path (required)")
    flag.Parse()

    // Load config
    cfg, err := LoadConfig(*configPath)
    if err != nil && !os.IsNotExist(err) {
        log.Fatalf("config: %v", err)
    }
    if *repoPath != "" {
        cfg.BareRepoPath = *repoPath
    }
    if cfg.BareRepoPath == "" {
        log.Fatal("--repo is required: path to bare git repo")
    }

    // Init database
    database, err := db.Init(cfg.DatabasePath)
    if err != nil {
        log.Fatalf("database: %v", err)
    }

    // Get or create project
    projectName := filepath.Base(cfg.BareRepoPath)
    var project model.Project
    result := database.Where("bare_repo_path = ?", cfg.BareRepoPath).First(&project)
    if result.Error != nil {
        project = model.Project{
            Name:          projectName,
            BareRepoPath:  cfg.BareRepoPath,
            DefaultBranch: cfg.DefaultBranch,
        }
        database.Create(&project)
    }

    // Init components
    tmux := tmuxpkg.NewManager("drem-" + projectName)
    if err := tmux.EnsureSession(); err != nil {
        log.Fatalf("tmux: %v", err)
    }

    wt := worktree.NewManager(cfg.BareRepoPath, cfg.DefaultBranch)
    runner := agent.NewRunner(database, tmux, wt, cfg.ClaudeBin, cfg.MaxConcurrentAgents)
    merger := merge.NewOrchestrator(wt, database)
    mem := memory.NewManager(database)

    events := make(chan orchestrator.Event, 100)
    orch := orchestrator.New(database, runner, wt, merger, mem, project.ID, events, cfg.TickInterval, cfg.StaleTimeout)

    // Start orchestrator in background
    ctx, cancel := context.WithCancel(context.Background())
    go orch.Run(ctx)

    // Start TUI (blocks until quit)
    p := tea.NewProgram(
        tui.NewModel(database, orch, tmux, project.ID, events),
        tea.WithAltScreen(),
    )
    if _, err := p.Run(); err != nil {
        fmt.Fprintf(os.Stderr, "tui error: %v\n", err)
    }

    // Cleanup
    cancel()
}
```

## Conventions

- Go 1.22+, standard library where possible
- `gofmt` for formatting
- Exported functions have doc comments
- Error wrapping: `fmt.Errorf("context: %w", err)`
- Build verification: `go build ./... && go vet ./...`
- TUI should be responsive to terminal resize
- Keep TUI rendering fast — no DB queries in View(), only in Update()
