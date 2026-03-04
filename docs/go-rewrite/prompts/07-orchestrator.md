# Agent: Orchestrator & Scheduler

You are working on the `feature/go-rewrite` branch of Drem Orchestrator, a multi-agent task orchestration system being rewritten from Python to Go.
Your task is to implement the main orchestration loop and task scheduler — the brain of the system that drives tasks through their lifecycle and assigns agents.

## Context

Read these before starting:
- `docs/go-rewrite/DESIGN.md` (focus on "Orchestrator Loop" section and state machine transitions)
- `src/orchestrator/orchestrator.py` (Python implementation to port — full Orchestrator class, ~998 lines)
- `src/orchestrator/scheduler.py` (Python scheduler to port — Scheduler class, ScheduleSummary)
- `internal/model/models.go` (Go models — Task, Agent, TaskEvent, enums)
- `internal/state/machine.go` (TransitionTask, ValidateTransition)
- `internal/agent/runner.go` (Runner API — CanSpawn, SpawnAgent, Spawn, StopAgent, DrainCompletions, GetAgentOutput, CleanupStaleAgents)
- `internal/worktree/manager.go` (WorktreeManager API — CreateFeature, RemoveFeature, CreateAgentWorktree)
- `internal/merge/merge.go` (MergeOrchestrator API — MergeAllAgentsIntoFeature, MergeFeatureIntoMain, SyncFeaturesAfterMerge)
- `internal/prompt/prompt.go` (Generate prompt API)
- `internal/memory/memory.go` (MemoryManager API — BuildAgentContext, ExtractMemoriesFromOutput)
- `CLAUDE.md` (build commands, conventions)

## Dependencies

This agent depends on all Tier 1 and Tier 2 agents (01-06). If those files don't exist yet, create minimal stubs with the interfaces you need and implement against them.

## Deliverables

### New file: `internal/orchestrator/orchestrator.go`

Port the Python `Orchestrator` class — the main tick loop.

```go
package orchestrator

import (
    "context"
    "encoding/json"
    "fmt"
    "log/slog"
    "os"
    "path/filepath"
    "strings"
    "time"

    "github.com/google/uuid"
    "gorm.io/gorm"

    "github.com/godinj/drem-orchestrator/internal/agent"
    "github.com/godinj/drem-orchestrator/internal/memory"
    "github.com/godinj/drem-orchestrator/internal/merge"
    "github.com/godinj/drem-orchestrator/internal/model"
    "github.com/godinj/drem-orchestrator/internal/prompt"
    "github.com/godinj/drem-orchestrator/internal/state"
    "github.com/godinj/drem-orchestrator/internal/worktree"
)

const MaxPlannerRetries = 3

// Event is sent from the orchestrator to the TUI.
type Event struct {
    Type    string
    Payload any
}

// Orchestrator is the main scheduling loop.
type Orchestrator struct {
    db       *gorm.DB
    runner   *agent.Runner
    worktree *worktree.Manager
    merger   *merge.Orchestrator
    memory   *memory.Manager
    projectID uuid.UUID
    events   chan<- Event
    tick     time.Duration
    stale    time.Duration
    logger   *slog.Logger
}

// New creates an Orchestrator.
func New(
    db *gorm.DB,
    runner *agent.Runner,
    wt *worktree.Manager,
    merger *merge.Orchestrator,
    mem *memory.Manager,
    projectID uuid.UUID,
    events chan<- Event,
    tickInterval time.Duration,
    staleTimeout time.Duration,
) *Orchestrator

// Run starts the main loop. Blocks until ctx is cancelled.
func (o *Orchestrator) Run(ctx context.Context)
```

#### `Run(ctx context.Context)`

Main loop with ticker:

```go
func (o *Orchestrator) Run(ctx context.Context) {
    ticker := time.NewTicker(o.tick)
    defer ticker.Stop()
    o.logger.Info("orchestrator started", "project_id", o.projectID)
    for {
        select {
        case <-ctx.Done():
            o.logger.Info("orchestrator stopping")
            return
        case <-ticker.C:
            o.doTick(ctx)
        }
    }
}
```

#### `doTick(ctx context.Context)`

Single iteration — process tasks in order:

1. **Process BACKLOG** — transition to PLANNING
2. **Drain agent completions** — call `runner.DrainCompletions()`, process each
3. **Process PLANNING** — spawn planner agents or handle plans
4. **Process IN_PROGRESS parents** — schedule subtasks, check completion
5. **Process MERGING** — execute merges
6. **Handle PAUSED** — stop agents on paused tasks
7. **Cleanup stale agents** — call `runner.CleanupStaleAgents()`

Each step queries tasks for the project in the relevant status. Use a single DB transaction per tick where possible.

#### `processBacklog(task *model.Task) error`

Transition BACKLOG → PLANNING:

```go
event, err := state.TransitionTask(task, model.StatusPlanning, "orchestrator", nil)
// Save event to DB
o.emit("task_updated", task)
```

#### `processPlanning(task *model.Task) error`

Handle tasks in PLANNING state:

1. If `task.Plan != nil` → transition to PLAN_REVIEW, emit "plan_ready"
2. If `task.AssignedAgentID != nil` → check if agent is still running
   - If agent is dead/missing: clear assignment, increment retry count in `task.Context`
   - If retry count >= MaxPlannerRetries: transition to FAILED
   - Otherwise: do nothing (wait for agent)
3. If no agent assigned and `runner.CanSpawn()`:
   - Get project from DB
   - Create feature worktree if `task.WorktreeBranch == ""`
   - Generate planner prompt using `prompt.Generate()`
   - Spawn planner agent: `runner.SpawnAgent(task, featureName, AgentPlanner, prompt)`
   - Set `task.AssignedAgentID`
   - Emit "planner_spawned"

Feature name derivation: slugify task title — lowercase, replace spaces/special chars with hyphens, truncate to 40 chars. Prepend task ID short prefix for uniqueness.

```go
func taskFeatureName(task *model.Task) string {
    slug := strings.ToLower(task.Title)
    slug = regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(slug, "-")
    slug = strings.Trim(slug, "-")
    if len(slug) > 40 {
        slug = slug[:40]
    }
    return fmt.Sprintf("%s-%s", task.ID.String()[:8], slug)
}
```

#### `processAgentResult(comp agent.Completion) error`

Handle a completed agent:

1. Load agent from DB
2. Load agent's current task
3. If return code == 0: `onAgentCompleted(agent, task)`
4. If return code != 0: `onAgentFailed(agent, task)`

#### `onPlannerCompleted(ag *model.Agent, task *model.Task) error`

Handle successful planner:

1. Read `plan.json` from agent's worktree: `<worktree>/plan.json`
2. Parse JSON into `{"subtasks": [...]}`
3. Transform into `[]model.SubtaskPlan`
4. If no plan or empty: log warning, clear assignment, stay in PLANNING for retry
5. Set `task.Plan` (marshal to JSONField)
6. Clean up planner's agent worktree
7. Update agent status to IDLE, clear assignment
8. Transition task to PLAN_REVIEW
9. Emit "plan_ready" with subtask count

#### `onAgentCompleted(ag *model.Agent, task *model.Task) error`

Handle successful coder/researcher:

1. If agent type is planner → delegate to `onPlannerCompleted`
2. Read agent output for memory extraction
3. Merge agent branch into feature: use merger
4. Clean up agent worktree
5. Update agent status to IDLE
6. Fast-track subtask through states: IN_PROGRESS → TESTING_READY → MANUAL_TESTING → MERGING → DONE
   - Create TaskEvents for each transition
7. Check if parent task's all subtasks are DONE → transition parent to TESTING_READY

#### `onAgentFailed(ag *model.Agent, task *model.Task) error`

Handle failed agent:

1. Read agent output, store error in task.Context
2. If planner: clear assignment, stay in PLANNING for retry (up to MaxPlannerRetries)
3. If coder/researcher: transition task to FAILED
4. Clean up agent worktree
5. Update agent status to DEAD
6. Emit appropriate failure event

#### `scheduleSubtasks(parent *model.Task) error`

For an IN_PROGRESS parent task, schedule its BACKLOG subtasks:

1. Query subtasks where `parent_task_id = parent.ID AND status = 'backlog'`
2. For each subtask:
   - Check dependencies met (all tasks in `subtask.DependencyIDs` are DONE)
   - If not met, skip
   - If `runner.CanSpawn()` is false, break
   - Determine agent type from `subtask.Context["agent_type"]` (default: coder)
   - Create agent worktree: `worktree.CreateAgentWorktree(featureName)`
   - Build prompt with `prompt.Generate()`
   - Spawn agent and assign to subtask
   - Transition subtask: BACKLOG → IN_PROGRESS (fast-track through PLANNING/PLAN_REVIEW)

#### `checkFeatureCompletion(parent *model.Task) error`

Check if all subtasks of a parent are DONE:

1. Query subtasks where `parent_task_id = parent.ID`
2. If all have status DONE → transition parent to TESTING_READY, emit "testing_ready"
3. If any FAILED → transition parent to FAILED

#### `executeMerge(task *model.Task) error`

Handle tasks in MERGING state:

1. Call `merger.MergeFeatureIntoMain(task)`
2. If merge succeeds → transition to DONE, emit "merge_complete"
3. If merge fails (conflicts) → transition to FAILED, emit "merge_conflict"

#### `handlePaused(task *model.Task) error`

Stop agents on paused tasks:

1. If `task.AssignedAgentID != nil`: `runner.StopAgent(agentID)`
2. Clear `task.AssignedAgentID`
3. For subtasks: cascade pause — stop their agents too

#### Public methods for TUI interaction

These are called by the TUI when the user takes action:

```go
// HandlePlanApproved creates subtask records from the plan and transitions to IN_PROGRESS.
func (o *Orchestrator) HandlePlanApproved(taskID uuid.UUID) error

// HandlePlanRejected clears the plan and transitions back to PLANNING.
func (o *Orchestrator) HandlePlanRejected(taskID uuid.UUID, feedback string) error

// HandleTestPassed transitions from MANUAL_TESTING to MERGING.
func (o *Orchestrator) HandleTestPassed(taskID uuid.UUID) error

// HandleTestFailed transitions from MANUAL_TESTING back to IN_PROGRESS.
func (o *Orchestrator) HandleTestFailed(taskID uuid.UUID, feedback string) error

// PauseTask pauses a task and stops its agents.
func (o *Orchestrator) PauseTask(taskID uuid.UUID) error

// ResumeTask resumes a paused task to its previous status.
func (o *Orchestrator) ResumeTask(taskID uuid.UUID) error

// RetryTask transitions a FAILED task back to BACKLOG.
func (o *Orchestrator) RetryTask(taskID uuid.UUID) error

// CreateTask creates a new task in BACKLOG.
func (o *Orchestrator) CreateTask(title, description string, priority int) (*model.Task, error)
```

`HandlePlanApproved` must:
1. Load task, verify status is PLAN_REVIEW
2. Parse `task.Plan` as `[]model.SubtaskPlan`
3. For each subtask plan, create a `model.Task` record:
   - ParentTaskID = task.ID
   - ProjectID = task.ProjectID
   - Status = BACKLOG
   - Title, Description from plan
   - Context = `{"agent_type": plan.AgentType, "estimated_files": plan.EstimatedFiles}`
   - Set DependencyIDs from plan dependencies (map indices to created task IDs)
4. Transition task to IN_PROGRESS

### New file: `internal/orchestrator/scheduler.go`

Helper types and scheduling utilities:

```go
package orchestrator

import (
    "github.com/google/uuid"
    "github.com/godinj/drem-orchestrator/internal/model"
    "gorm.io/gorm"
)

// ScheduleSummary provides an overview of the scheduling state.
type ScheduleSummary struct {
    TasksByStatus  map[model.TaskStatus]int
    AgentsByStatus map[model.AgentStatus]int
    BlockedTasks   []BlockedTask
    QueueDepth     int // assignable tasks waiting for agents
}

// BlockedTask describes a task waiting on dependencies.
type BlockedTask struct {
    TaskID      uuid.UUID
    BlockingIDs []uuid.UUID
}

// GetScheduleSummary returns an overview of the project's scheduling state.
func GetScheduleSummary(db *gorm.DB, projectID uuid.UUID) (*ScheduleSummary, error)

// GetAssignableTasks returns BACKLOG subtasks with met dependencies and IN_PROGRESS parents.
func GetAssignableTasks(db *gorm.DB, projectID uuid.UUID) ([]model.Task, error)

// DependenciesMet checks if all tasks in dependencyIDs have status DONE.
func DependenciesMet(db *gorm.DB, dependencyIDs []string) (bool, error)

// GetBlockingTasks returns the subset of dependencyIDs that are not DONE.
func GetBlockingTasks(db *gorm.DB, dependencyIDs []string) ([]uuid.UUID, error)
```

### Helper: `emit`

Send events to the TUI:

```go
func (o *Orchestrator) emit(eventType string, payload any) {
    select {
    case o.events <- Event{Type: eventType, Payload: payload}:
    default:
        o.logger.Warn("event channel full, dropping event", "type", eventType)
    }
}
```

Use a buffered channel (size 100) to avoid blocking the orchestrator.

## Conventions

- Go 1.22+, standard library where possible
- `gofmt` for formatting
- Exported functions have doc comments
- Error wrapping: `fmt.Errorf("context: %w", err)`
- Use `context.Context` for cancellation
- Use `log/slog` for structured logging
- Build verification: `go build ./... && go vet ./...`
