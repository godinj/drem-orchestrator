# Drem Orchestrator — Go Rewrite with TUI + tmux

## Overview

Rewrite the Drem Orchestrator from Python (FastAPI + React) to Go (Bubble Tea TUI + tmux). The new system replaces the web UI with a terminal dashboard and spawns Claude Code agents in tmux windows instead of blind subprocesses, giving the user direct visibility and interactivity with each agent session.

## Goals

1. **Single binary** — no Python runtime, no Node.js, no web server
2. **TUI dashboard** — Bubble Tea-based task board with plan review, test approval, agent status
3. **tmux-native agents** — each Claude Code agent runs in a named tmux window; user can jump to any agent terminal
4. **Feature parity** — same task lifecycle, state machine, worktree management, merge orchestration
5. **SQLite database** — same schema, managed with GORM + goose migrations
6. **Drop Redis** — use Go channels for internal messaging (single-process architecture)

## Architecture

```
┌─────────────────────────────────────────────────────────┐
│                    tmux session                          │
│  ┌─────────────────────┐  ┌──────────────────────────┐  │
│  │  Window 0: TUI      │  │ Window 1: agent-abc123   │  │
│  │  (Bubble Tea app)   │  │ (Claude Code terminal)   │  │
│  │                     │  └──────────────────────────┘  │
│  │  ┌───────────────┐  │  ┌──────────────────────────┐  │
│  │  │  Task Board   │  │  │ Window 2: agent-def456   │  │
│  │  │  Agent List   │  │  │ (Claude Code terminal)   │  │
│  │  │  Log Viewer   │  │  └──────────────────────────┘  │
│  │  └───────────────┘  │                                │
│  └─────────────────────┘                                │
└─────────────────────────────────────────────────────────┘
         │
         ├── Orchestrator loop (goroutine)
         ├── GORM + SQLite
         └── Git worktree exec
```

### Component Map

| Go Package | Replaces Python | Role |
|------------|----------------|------|
| `internal/model` | `models.py`, `enums.py`, `schemas.py` | GORM models, enums, request types |
| `internal/db` | `db.py`, alembic | Database init, migrations, session helpers |
| `internal/state` | `state_machine.py` | Task status transitions and validation |
| `internal/worktree` | `worktree.py`, `git_utils.py` | Git worktree CRUD, merge, sync |
| `internal/agent` | `agent_runner.py`, `heartbeat.py` | Spawn agents in tmux, monitor, heartbeat |
| `internal/orchestrator` | `orchestrator.py`, `scheduler.py` | Main tick loop, task scheduling |
| `internal/prompt` | `agent_prompt.py` | Generate agent prompts |
| `internal/merge` | `merge.py` | Merge orchestration, build verification |
| `internal/memory` | `memory.py`, `compaction.py` | Agent memory persistence |
| `internal/tui` | `ui/` (React), `server.py`, routers | Bubble Tea TUI dashboard |
| `internal/tmux` | (new) | tmux session/window management via CLI |
| `cmd/drem` | — | Main entry point |

## Database Schema

Same tables as the Python version, using GORM with SQLite.

### Models

```go
type Project struct {
    ID            uuid.UUID `gorm:"type:text;primaryKey"`
    Name          string    `gorm:"uniqueIndex;not null"`
    BareRepoPath  string    `gorm:"not null"`
    DefaultBranch string    `gorm:"default:master"`
    Description   string
    CreatedAt     time.Time
    UpdatedAt     time.Time
    Tasks         []Task    `gorm:"foreignKey:ProjectID"`
    Agents        []Agent   `gorm:"foreignKey:ProjectID"`
}

type Task struct {
    ID              uuid.UUID  `gorm:"type:text;primaryKey"`
    ProjectID       uuid.UUID  `gorm:"type:text;not null;index"`
    ParentTaskID    *uuid.UUID `gorm:"type:text;index"`
    Title           string     `gorm:"not null"`
    Description     string     `gorm:"not null"`
    Status          TaskStatus `gorm:"not null;default:backlog"`
    Priority        int        `gorm:"default:0"`
    Labels          JSONArray  `gorm:"type:text"`          // JSON []string
    DependencyIDs   JSONArray  `gorm:"type:text"`          // JSON []string (UUIDs)
    AssignedAgentID *uuid.UUID `gorm:"type:text"`
    Plan            JSONField  `gorm:"type:text"`          // JSON []SubtaskPlan
    PlanFeedback    string
    TestPlan        string
    TestFeedback    string
    WorktreeBranch  string
    PRUrl           string
    Context         JSONField  `gorm:"type:text"`          // JSON map[string]any
    CreatedAt       time.Time
    UpdatedAt       time.Time
    Project         Project    `gorm:"foreignKey:ProjectID"`
    ParentTask      *Task      `gorm:"foreignKey:ParentTaskID"`
    Subtasks        []Task     `gorm:"foreignKey:ParentTaskID"`
    AssignedAgent   *Agent     `gorm:"foreignKey:AssignedAgentID"`
    Events          []TaskEvent `gorm:"foreignKey:TaskID"`
}

type Agent struct {
    ID             uuid.UUID   `gorm:"type:text;primaryKey"`
    ProjectID      uuid.UUID   `gorm:"type:text;not null;index"`
    AgentType      AgentType   `gorm:"not null"`
    Name           string      `gorm:"not null"`
    Status         AgentStatus `gorm:"not null;default:idle"`
    CurrentTaskID  *uuid.UUID  `gorm:"type:text"`
    WorktreePath   string
    WorktreeBranch string
    TmuxWindow     string      // tmux window name (NEW — replaces blind subprocess)
    MemorySummary  string
    HeartbeatAt    *time.Time
    Config         JSONField   `gorm:"type:text"`
    CreatedAt      time.Time
    UpdatedAt      time.Time
}

type TaskEvent struct {
    ID        uuid.UUID `gorm:"type:text;primaryKey"`
    TaskID    uuid.UUID `gorm:"type:text;not null;index"`
    EventType string    `gorm:"not null"`
    OldValue  string
    NewValue  string
    Details   JSONField `gorm:"type:text"`
    Actor     string    `gorm:"not null"`
    CreatedAt time.Time
}

type Memory struct {
    ID         uuid.UUID `gorm:"type:text;primaryKey"`
    AgentID    uuid.UUID `gorm:"type:text;not null;index"`
    TaskID     *uuid.UUID `gorm:"type:text;index"`
    Content    string    `gorm:"not null"`
    MemoryType string    `gorm:"not null"`
    Metadata   JSONField `gorm:"type:text"`
    CreatedAt  time.Time
}
```

### Enums

```go
type TaskStatus string
const (
    StatusBacklog        TaskStatus = "backlog"
    StatusPlanning       TaskStatus = "planning"
    StatusPlanReview     TaskStatus = "plan_review"
    StatusInProgress     TaskStatus = "in_progress"
    StatusTestingReady   TaskStatus = "testing_ready"
    StatusManualTesting  TaskStatus = "manual_testing"
    StatusMerging        TaskStatus = "merging"
    StatusPaused         TaskStatus = "paused"
    StatusDone           TaskStatus = "done"
    StatusFailed         TaskStatus = "failed"
)

type AgentType string
const (
    AgentOrchestrator AgentType = "orchestrator"
    AgentPlanner      AgentType = "planner"
    AgentCoder        AgentType = "coder"
    AgentResearcher   AgentType = "researcher"
)

type AgentStatus string
const (
    AgentIdle    AgentStatus = "idle"
    AgentWorking AgentStatus = "working"
    AgentBlocked AgentStatus = "blocked"
    AgentDead    AgentStatus = "dead"
)
```

## State Machine

Identical transitions to the Python version:

```go
var ValidTransitions = map[TaskStatus][]TaskStatus{
    StatusBacklog:       {StatusPlanning, StatusPaused},
    StatusPlanning:      {StatusPlanReview, StatusFailed, StatusPaused},
    StatusPlanReview:    {StatusInProgress, StatusPlanning},
    StatusInProgress:    {StatusTestingReady, StatusFailed, StatusPaused},
    StatusTestingReady:  {StatusManualTesting},
    StatusManualTesting: {StatusMerging, StatusInProgress},
    StatusMerging:       {StatusDone, StatusFailed},
    StatusPaused:        {StatusBacklog, StatusPlanning, StatusInProgress},
    StatusDone:          {},
    StatusFailed:        {StatusBacklog},
}

var HumanGates = map[TaskStatus]bool{
    StatusPlanReview:    true,
    StatusManualTesting: true,
}
```

## tmux Integration

### Session Layout

```
Session: drem-<project-name>
├── Window 0: "dashboard"     ← TUI runs here
├── Window 1: "planner-abc"   ← Planner agent for task abc
├── Window 2: "coder-def"     ← Coder agent for subtask def
├── Window 3: "coder-ghi"     ← Coder agent for subtask ghi
└── ...
```

### tmux Manager API

```go
type TmuxManager struct {
    SessionName string
}

// CreateSession creates or attaches to the drem session
func (t *TmuxManager) CreateSession() error

// CreateWindow creates a new tmux window and runs a command in it
func (t *TmuxManager) CreateWindow(name string, cmd string, cwd string) error

// CloseWindow closes a tmux window by name
func (t *TmuxManager) CloseWindow(name string) error

// ListWindows returns all window names in the session
func (t *TmuxManager) ListWindows() ([]string, error)

// FocusWindow switches to a specific window
func (t *TmuxManager) FocusWindow(name string) error

// CapturePane reads the current visible content of a window (for log preview in TUI)
func (t *TmuxManager) CapturePane(name string, lines int) (string, error)

// IsWindowAlive checks if the command in a window is still running
func (t *TmuxManager) IsWindowAlive(name string) (bool, error)

// WaitForExit blocks until the window's command exits, returns exit code
func (t *TmuxManager) WaitForExit(name string) (int, error)
```

### Agent Spawning via tmux

Instead of `asyncio.create_subprocess_exec()` with stdout to a file:

```go
func (r *AgentRunner) SpawnAgent(agent *model.Agent, worktreePath, prompt string) error {
    // Write prompt to .claude/agent-prompt.md
    promptPath := filepath.Join(worktreePath, ".claude", "agent-prompt.md")
    os.MkdirAll(filepath.Dir(promptPath), 0755)
    os.WriteFile(promptPath, []byte(prompt), 0644)

    // Build claude command
    cmd := fmt.Sprintf(
        "claude -p --dangerously-skip-permissions < %s 2>&1 | tee %s",
        promptPath,
        filepath.Join(worktreePath, ".claude", "agent.log"),
    )

    // Create tmux window
    windowName := fmt.Sprintf("%s-%s", agent.AgentType, agent.ID.String()[:8])
    r.tmux.CreateWindow(windowName, cmd, worktreePath)

    // Store window name on agent record
    agent.TmuxWindow = windowName

    // Start monitoring goroutine
    go r.monitorAgent(agent.ID, windowName)
    return nil
}
```

## Git Worktree Management

Port `worktree.py` to Go — exec `git` commands:

```go
type WorktreeManager struct {
    BareRepoPath string
    DefaultBranch string
}

func (w *WorktreeManager) CreateFeature(name string) (*WorktreeInfo, error)
func (w *WorktreeManager) RemoveFeature(name string) error
func (w *WorktreeManager) ListWorktrees() ([]WorktreeInfo, error)
func (w *WorktreeManager) CreateAgentWorktree(featureName string) (*AgentWorktreeInfo, error)
func (w *WorktreeManager) RemoveAgentWorktree(branch string) error
func (w *WorktreeManager) MergeBranch(source string, targetWorktree string) (*MergeResult, error)
func (w *WorktreeManager) SyncAll() ([]SyncResult, error)

func runGit(args []string, cwd string) (string, error)
```

## Orchestrator Loop

Port the Python tick loop to a goroutine with a ticker:

```go
type Orchestrator struct {
    db        *gorm.DB
    runner    *AgentRunner
    worktree  *WorktreeManager
    merger    *MergeOrchestrator
    projectID uuid.UUID
    events    chan Event       // Send events to TUI
}

func (o *Orchestrator) Run(ctx context.Context) {
    ticker := time.NewTicker(5 * time.Second)
    defer ticker.Stop()
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            o.tick()
        }
    }
}

func (o *Orchestrator) tick() {
    // 1. Process BACKLOG → PLANNING
    // 2. Drain agent completions
    // 3. Process PLANNING → spawn planners
    // 4. Process IN_PROGRESS → schedule subtasks
    // 5. Process MERGING → execute merges
    // 6. Handle PAUSED → stop agents
    // 7. Cleanup stale agents
}
```

## TUI Dashboard

### Layout

```
┌─ Drem Orchestrator ─────────────────────────────────────────────────┐
│                                                                      │
│  [Backlog: 2] [Planning: 1] [Review: 1] [Active: 3] [Done: 5]      │
│                                                                      │
│  ┌─ Tasks ───────────────────────────────┬─ Agents ────────────────┐ │
│  │ ● AUTH-001  Add login flow    ACTIVE  │ coder-a1b2  working     │ │
│  │ ○ AUTH-002  Add OAuth         BACKLOG │   └ task: AUTH-001      │ │
│  │ ◉ DB-001   Add migrations    REVIEW  │   └ window: coder-a1b2  │ │
│  │ ✓ UI-001   Setup scaffold    DONE    │ planner-c3d4 working    │ │
│  │                                       │   └ task: DB-001        │ │
│  │                                       │   └ window: plan-c3d4   │ │
│  └───────────────────────────────────────┴─────────────────────────┘ │
│                                                                      │
│  ┌─ Detail ────────────────────────────────────────────────────────┐ │
│  │ AUTH-001: Add login flow                                        │ │
│  │ Status: IN_PROGRESS | Agent: coder-a1b2 | Branch: feature/...  │ │
│  │ Subtasks: 2/3 complete                                          │ │
│  │                                                                  │ │
│  │ [a]pprove plan  [r]eject plan  [t]est pass  [f]ail test        │ │
│  │ [j]ump to agent  [p]ause  [n]ew task  [q]uit                   │ │
│  └──────────────────────────────────────────────────────────────────┘ │
└──────────────────────────────────────────────────────────────────────┘
```

### Key Bindings

| Key | Action |
|-----|--------|
| `j/k` | Navigate task list |
| `Tab` | Switch between task list and agent list |
| `Enter` | View task detail |
| `a` | Approve plan (when in PLAN_REVIEW) |
| `r` | Reject plan with feedback |
| `t` | Pass test (when in MANUAL_TESTING) |
| `f` | Fail test with feedback |
| `g` | Jump to agent's tmux window |
| `n` | Create new task |
| `p` | Pause/resume task |
| `R` | Retry failed task |
| `l` | View agent log preview (capture-pane) |
| `q` | Quit |

### Bubble Tea Components

```go
// Main model
type Model struct {
    tasks       []model.Task
    agents      []model.Agent
    selected    int
    focus       Focus       // TaskList, AgentList, Detail
    detail      *DetailModel
    createForm  *CreateFormModel
    feedbackInput *textinput.Model
    orchestrator *Orchestrator
    events      <-chan Event
    width, height int
}

// Messages from orchestrator → TUI
type TaskUpdatedMsg struct{ Task model.Task }
type AgentUpdatedMsg struct{ Agent model.Agent }
type PlanReadyMsg struct{ TaskID uuid.UUID; SubtaskCount int }
type TestingReadyMsg struct{ TaskID uuid.UUID }
type MergeCompleteMsg struct{ TaskID uuid.UUID }
type AgentFailedMsg struct{ AgentID uuid.UUID; Error string }
```

### Communication: Orchestrator → TUI

Use a Go channel instead of WebSocket:

```go
type Event struct {
    Type    string
    Payload any
}

// Orchestrator sends events
o.events <- Event{Type: "task_updated", Payload: task}

// TUI receives in Update()
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    switch msg := msg.(type) {
    case EventMsg:
        // Update internal state from orchestrator event
    }
}

// Bridge: channel → tea.Msg
func listenForEvents(events <-chan Event) tea.Cmd {
    return func() tea.Msg {
        e := <-events
        return EventMsg(e)
    }
}
```

## Prompt Generation

Port `agent_prompt.py` — build markdown prompt strings:

```go
func GenerateAgentPrompt(opts PromptOpts) string

type PromptOpts struct {
    Task         *model.Task
    Project      *model.Project
    AgentType    model.AgentType
    WorktreePath string
    Memories     []model.Memory
    ParentCtx    map[string]any
}
```

## Merge Orchestration

Port `merge.py` — sequential merge with build verification:

```go
type MergeOrchestrator struct {
    worktree *WorktreeManager
}

func (m *MergeOrchestrator) MergeAgentIntoFeature(agentBranch, featureWorktree string) (*MergeResult, error)
func (m *MergeOrchestrator) MergeFeatureIntoMain(task *model.Task) (*MergeResult, error)
func (m *MergeOrchestrator) VerifyBuild(worktreePath string) (bool, string, error)
func (m *MergeOrchestrator) SyncFeaturesAfterMerge(mergedFeature string) ([]SyncResult, error)
```

## Directory Structure

```
cmd/
└── drem/
    └── main.go                 # Entry point
internal/
├── model/
│   ├── models.go               # GORM models
│   ├── enums.go                # TaskStatus, AgentType, AgentStatus
│   └── json.go                 # JSONField, JSONArray custom types
├── db/
│   ├── db.go                   # Init, migrate, session helpers
│   └── migrations/             # goose SQL migrations
├── state/
│   └── machine.go              # ValidTransitions, TransitionTask()
├── worktree/
│   ├── manager.go              # WorktreeManager
│   └── git.go                  # runGit(), CommitInfo, etc.
├── agent/
│   ├── runner.go               # AgentRunner (spawn, monitor, stop)
│   └── heartbeat.go            # Heartbeat tracking
├── tmux/
│   └── tmux.go                 # TmuxManager
├── orchestrator/
│   ├── orchestrator.go         # Main tick loop
│   └── scheduler.go            # Task assignment, dependency checks
├── prompt/
│   └── prompt.go               # GenerateAgentPrompt
├── merge/
│   └── merge.go                # MergeOrchestrator
├── memory/
│   ├── memory.go               # MemoryManager
│   └── compaction.go           # Memory compaction
└── tui/
    ├── app.go                  # Main Bubble Tea model
    ├── board.go                # Task board view
    ├── detail.go               # Task detail view
    ├── agents.go               # Agent list view
    ├── create.go               # New task form
    ├── feedback.go             # Plan review / test feedback dialog
    ├── styles.go               # Lipgloss styles
    └── keys.go                 # Key bindings
go.mod
go.sum
```

## Dependencies

```
github.com/charmbracelet/bubbletea      # TUI framework
github.com/charmbracelet/lipgloss       # TUI styling
github.com/charmbracelet/bubbles        # TUI components (textinput, list, viewport)
github.com/google/uuid                  # UUIDs
gorm.io/gorm                            # ORM
gorm.io/driver/sqlite                   # SQLite driver
github.com/pressly/goose/v3             # DB migrations
```

## Configuration

```go
type Config struct {
    DatabasePath       string // default: "./drem.db"
    BareRepoPath       string // required: path to bare git repo
    DefaultBranch      string // default: "master"
    ClaudeBin          string // default: "claude"
    MaxConcurrentAgents int   // default: 5
    TickInterval       time.Duration // default: 5s
    HeartbeatInterval  time.Duration // default: 30s
    StaleTimeout       time.Duration // default: 5m
}
```

Config loaded from `drem.toml` or CLI flags.

## Entry Point

```go
func main() {
    cfg := loadConfig()
    db := db.Init(cfg.DatabasePath)
    tmux := tmux.NewManager("drem-" + projectName)
    worktree := worktree.NewManager(cfg.BareRepoPath, cfg.DefaultBranch)
    runner := agent.NewRunner(db, tmux, worktree, cfg)
    merger := merge.NewOrchestrator(worktree)
    orch := orchestrator.New(db, runner, worktree, merger, projectID, events)

    // Start orchestrator in background
    ctx, cancel := context.WithCancel(context.Background())
    go orch.Run(ctx)

    // Start TUI in foreground (this blocks)
    p := tea.NewProgram(tui.NewModel(db, orch, events), tea.WithAltScreen())
    if _, err := p.Run(); err != nil {
        log.Fatal(err)
    }
    cancel()
}
```

## What NOT to Port

- **FastAPI server** — replaced by TUI
- **React UI** — replaced by TUI
- **WebSocket router** — replaced by Go channels
- **Redis pub/sub** — replaced by Go channels (single process)
- **Alembic** — replaced by goose
- **Pydantic schemas** — replaced by Go structs
- **overlap_detector.py** — can be reimplemented later if needed
