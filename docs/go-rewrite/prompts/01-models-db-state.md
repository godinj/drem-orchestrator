# Agent: Models, Database & State Machine

You are working on the `feature/go-rewrite` branch of Drem Orchestrator, a multi-agent task orchestration system being rewritten from Python to Go.
Your task is to create the foundational Go module: models, enums, database layer, state machine, and configuration.

## Context

Read these before starting:
- `docs/go-rewrite/DESIGN.md` (full design — focus on "Database Schema", "Enums", "State Machine", "Configuration", "Directory Structure", "Dependencies" sections)
- `src/orchestrator/models.py` (Python ORM models to port)
- `src/orchestrator/enums.py` (Python enums to port)
- `src/orchestrator/schemas.py` (Python Pydantic schemas — port `SubtaskPlan` only, the rest are API-specific)
- `src/orchestrator/state_machine.py` (Python state machine to port)
- `src/orchestrator/config.py` (Python config to port)
- `src/orchestrator/db.py` (Python DB setup to port)

## Deliverables

### Project initialization

Initialize the Go module and install dependencies:

```bash
# Remove all Python/JS files from the worktree (this is a full rewrite)
rm -rf src/ ui/ tests/ alembic/ alembic.ini pyproject.toml uv.lock scripts/

# Initialize Go module
go mod init github.com/godinj/drem-orchestrator
go get gorm.io/gorm gorm.io/driver/sqlite github.com/google/uuid github.com/BurntSushi/toml
```

### New file: `internal/model/enums.go`

Port the Python enums as Go string types with constants:

```go
package model

type TaskStatus string
const (
    StatusBacklog       TaskStatus = "backlog"
    StatusPlanning      TaskStatus = "planning"
    StatusPlanReview    TaskStatus = "plan_review"
    StatusInProgress    TaskStatus = "in_progress"
    StatusTestingReady  TaskStatus = "testing_ready"
    StatusManualTesting TaskStatus = "manual_testing"
    StatusMerging       TaskStatus = "merging"
    StatusPaused        TaskStatus = "paused"
    StatusDone          TaskStatus = "done"
    StatusFailed        TaskStatus = "failed"
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

Add helper methods:
- `func (s TaskStatus) String() string`
- `func (s TaskStatus) IsActionable() bool` — returns true for BACKLOG, PLANNING, IN_PROGRESS, MERGING
- `func (s TaskStatus) IsHumanGate() bool` — returns true for PLAN_REVIEW, MANUAL_TESTING
- `func ParseTaskStatus(s string) (TaskStatus, error)`
- Same pattern for `AgentType` and `AgentStatus`

### New file: `internal/model/json.go`

Custom GORM types for JSON columns:

```go
package model

import (
    "database/sql/driver"
    "encoding/json"
    "fmt"
)

// JSONField stores arbitrary JSON (map[string]any) in a TEXT column
type JSONField map[string]any

func (j JSONField) Value() (driver.Value, error)    // Marshal to JSON string
func (j *JSONField) Scan(value any) error            // Unmarshal from string

// JSONArray stores a JSON string array in a TEXT column
type JSONArray []string

func (j JSONArray) Value() (driver.Value, error)
func (j *JSONArray) Scan(value any) error
```

Both types implement `driver.Valuer` and `sql.Scanner`. Handle nil/empty values gracefully (nil → NULL, empty → "[]" or "{}").

### New file: `internal/model/models.go`

Port all 5 SQLAlchemy models to GORM structs. Use the exact field mappings from DESIGN.md:

- `Project` — ID (uuid, PK), Name (unique), BareRepoPath, DefaultBranch (default "master"), Description, CreatedAt, UpdatedAt, Tasks/Agents relationships
- `Task` — ID (uuid, PK), ProjectID (FK), ParentTaskID (nullable FK, self-referencing), Title, Description, Status (TaskStatus, default "backlog"), Priority (default 0), Labels (JSONArray), DependencyIDs (JSONArray), AssignedAgentID (nullable FK), Plan (JSONField), PlanFeedback, TestPlan, TestFeedback, WorktreeBranch, PRUrl, Context (JSONField), CreatedAt, UpdatedAt, relationships to Project, ParentTask, Subtasks, AssignedAgent, Events
- `Agent` — ID (uuid, PK), ProjectID (FK), AgentType, Name, Status (AgentStatus, default "idle"), CurrentTaskID (nullable FK), WorktreePath, WorktreeBranch, TmuxWindow (new field for tmux integration), MemorySummary, HeartbeatAt (nullable), Config (JSONField), CreatedAt, UpdatedAt
- `TaskEvent` — ID (uuid, PK), TaskID (FK), EventType, OldValue, NewValue, Details (JSONField), Actor, CreatedAt
- `Memory` — ID (uuid, PK), AgentID (FK), TaskID (nullable FK), Content, MemoryType, Metadata (JSONField), CreatedAt

Also add:

```go
// SubtaskPlan is the plan item produced by planner agents
type SubtaskPlan struct {
    Title          string   `json:"title"`
    Description    string   `json:"description"`
    AgentType      string   `json:"agent_type"`
    EstimatedFiles []string `json:"estimated_files"`
}
```

Use `gorm:"type:text"` for all UUID fields (SQLite stores as text). Use `gorm:"primaryKey"` not `gorm:"primary_key"`.

Add a `BeforeCreate` hook on each model to auto-generate UUIDs:

```go
func (t *Task) BeforeCreate(tx *gorm.DB) error {
    if t.ID == uuid.Nil {
        t.ID = uuid.New()
    }
    return nil
}
```

### New file: `internal/db/db.go`

Database initialization and helpers:

```go
package db

import (
    "gorm.io/gorm"
    "gorm.io/driver/sqlite"
    "github.com/godinj/drem-orchestrator/internal/model"
)

// Init opens the SQLite database and auto-migrates all models.
func Init(dbPath string) (*gorm.DB, error)

// AutoMigrate creates/updates all tables.
func AutoMigrate(db *gorm.DB) error
```

`Init` should:
1. Open SQLite with WAL mode (`?_journal_mode=WAL&_busy_timeout=5000`)
2. Call `AutoMigrate` for all 5 models
3. Return the `*gorm.DB`

### New file: `internal/state/machine.go`

Port the Python state machine exactly:

```go
package state

import (
    "fmt"
    "time"
    "github.com/google/uuid"
    "github.com/godinj/drem-orchestrator/internal/model"
)

// ValidTransitions defines which status transitions are allowed.
var ValidTransitions = map[model.TaskStatus][]model.TaskStatus{
    model.StatusBacklog:       {model.StatusPlanning, model.StatusPaused},
    model.StatusPlanning:      {model.StatusPlanReview, model.StatusFailed, model.StatusPaused},
    model.StatusPlanReview:    {model.StatusInProgress, model.StatusPlanning},
    model.StatusInProgress:    {model.StatusTestingReady, model.StatusFailed, model.StatusPaused},
    model.StatusTestingReady:  {model.StatusManualTesting},
    model.StatusManualTesting: {model.StatusMerging, model.StatusInProgress},
    model.StatusMerging:       {model.StatusDone, model.StatusFailed},
    model.StatusPaused:        {model.StatusBacklog, model.StatusPlanning, model.StatusInProgress},
    model.StatusDone:          {},
    model.StatusFailed:        {model.StatusBacklog},
}

// ValidateTransition checks if a transition from current to target is allowed.
func ValidateTransition(current, target model.TaskStatus) error

// GetAvailableTransitions returns valid next states.
func GetAvailableTransitions(current model.TaskStatus) []model.TaskStatus

// IsHumanGate checks if the status requires human approval.
func IsHumanGate(status model.TaskStatus) bool

// TransitionTask validates the transition, updates task.Status, and returns a new TaskEvent.
// Returns error if the transition is invalid.
func TransitionTask(task *model.Task, target model.TaskStatus, actor string, details map[string]any) (*model.TaskEvent, error)
```

`TransitionTask` must:
1. Call `ValidateTransition(task.Status, target)`
2. Create a `TaskEvent` with old/new status, actor, details, and `time.Now()`
3. Update `task.Status = target` and `task.UpdatedAt = time.Now()`
4. Return the event (caller saves to DB)

### New file: `cmd/drem/config.go`

Configuration struct with TOML loading and CLI flag defaults:

```go
package main

import (
    "time"
    "github.com/BurntSushi/toml"
)

type Config struct {
    DatabasePath        string        `toml:"database_path"`
    BareRepoPath        string        `toml:"bare_repo_path"`
    DefaultBranch       string        `toml:"default_branch"`
    ClaudeBin           string        `toml:"claude_bin"`
    MaxConcurrentAgents int           `toml:"max_concurrent_agents"`
    TickInterval        time.Duration `toml:"tick_interval"`
    HeartbeatInterval   time.Duration `toml:"heartbeat_interval"`
    StaleTimeout        time.Duration `toml:"stale_timeout"`
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() Config

// LoadConfig reads from drem.toml if present, then applies CLI flag overrides.
func LoadConfig(path string) (Config, error)
```

Defaults: DatabasePath="./drem.db", DefaultBranch="master", ClaudeBin="claude", MaxConcurrentAgents=5, TickInterval=5s, HeartbeatInterval=30s, StaleTimeout=5m.

### Update: `CLAUDE.md`

Replace the existing CLAUDE.md with Go-specific content:

```markdown
# Drem Orchestrator (Go)

## Build & Run

\```bash
go build -o drem ./cmd/drem
./drem --repo /path/to/bare-repo.git
\```

## Test

\```bash
go test ./...
\```

## Architecture

- Go 1.22+, Bubble Tea TUI, GORM + SQLite, tmux
- `cmd/drem/` — entry point and config
- `internal/model/` — GORM models, enums, JSON types
- `internal/db/` — database init and migrations
- `internal/state/` — task status state machine
- `internal/worktree/` — git worktree management
- `internal/agent/` — agent process lifecycle via tmux
- `internal/tmux/` — tmux session/window management
- `internal/orchestrator/` — main scheduling loop
- `internal/prompt/` — agent prompt generation
- `internal/merge/` — merge orchestration
- `internal/memory/` — agent memory persistence
- `internal/tui/` — Bubble Tea dashboard

## Conventions

- Go 1.22+ with standard library where possible
- `gofmt` for formatting
- Exported functions have doc comments
- Error wrapping with `fmt.Errorf("context: %w", err)`
- Use `context.Context` for cancellation
- Table-driven tests
```

## Conventions

- Go 1.22+, standard library where possible
- `gofmt` for formatting
- Exported functions have doc comments
- Error wrapping: `fmt.Errorf("context: %w", err)`
- Build verification: `go build ./... && go vet ./...`
