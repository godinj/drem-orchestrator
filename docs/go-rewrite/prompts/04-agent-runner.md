# Agent: Agent Runner & Heartbeat

You are working on the `feature/go-rewrite` branch of Drem Orchestrator, a multi-agent task orchestration system being rewritten from Python to Go.
Your task is to implement the agent process lifecycle — spawning Claude Code agents in tmux windows, monitoring them, heartbeat tracking, and graceful shutdown.

## Context

Read these before starting:
- `docs/go-rewrite/DESIGN.md` (focus on "tmux Integration" → "Agent Spawning via tmux", and the component map)
- `src/orchestrator/agent_runner.py` (Python implementation to port — AgentProcess, AgentRunner class)
- `src/orchestrator/heartbeat.py` (Python heartbeat monitor — port to in-process instead of Redis)
- `internal/model/models.go` (Go models — Agent, Task, AgentStatus, AgentType)
- `internal/tmux/tmux.go` (TmuxManager API — CreateWindow, WaitForExit, CloseWindow, IsWindowAlive, CapturePane)
- `internal/worktree/manager.go` (WorktreeManager API — CreateAgentWorktree, RemoveAgentWorktree)
- `CLAUDE.md` (build commands, conventions)

## Dependencies

This agent depends on Agent 01 (Models/DB), Agent 02 (tmux Manager), and Agent 03 (Worktree Manager).
If those files don't exist yet, create minimal stubs with the interfaces you need and implement against them.

## Deliverables

### New file: `internal/agent/runner.go`

Port `AgentRunner` from Python, replacing `asyncio.create_subprocess_exec` with tmux window creation.

```go
package agent

import (
    "context"
    "fmt"
    "os"
    "path/filepath"
    "sync"
    "time"

    "github.com/google/uuid"
    "gorm.io/gorm"

    "github.com/godinj/drem-orchestrator/internal/model"
    "github.com/godinj/drem-orchestrator/internal/tmux"
    "github.com/godinj/drem-orchestrator/internal/worktree"
)

// Completion records the result of an agent process exit.
type Completion struct {
    AgentID    uuid.UUID
    ReturnCode int
}

// RunningAgent tracks an active agent process.
type RunningAgent struct {
    AgentID      uuid.UUID
    TaskID       uuid.UUID
    WorktreePath string
    Branch       string
    TmuxWindow   string
    StartedAt    time.Time
    LogPath      string
    cancel       context.CancelFunc // cancels the monitor goroutine
}

// Runner manages Claude Code agent lifecycles via tmux.
type Runner struct {
    db           *gorm.DB
    tmux         *tmux.Manager
    worktree     *worktree.Manager
    claudeBin    string
    maxConcurrent int

    mu          sync.Mutex
    running     map[uuid.UUID]*RunningAgent
    completions chan Completion
    semaphore   chan struct{} // buffered channel of size maxConcurrent
}

// NewRunner creates an AgentRunner.
func NewRunner(db *gorm.DB, tmux *tmux.Manager, wt *worktree.Manager, claudeBin string, maxConcurrent int) *Runner
```

The `semaphore` is a buffered channel: `make(chan struct{}, maxConcurrent)`. Send to acquire, receive to release.

#### `CanSpawn() bool`

Returns whether there's capacity for another agent:

```go
func (r *Runner) CanSpawn() bool {
    r.mu.Lock()
    defer r.mu.Unlock()
    return len(r.running) < r.maxConcurrent
}
```

#### `SpawnAgent(task *model.Task, featureName string, agentType model.AgentType, prompt string) (*model.Agent, error)`

High-level spawn that creates everything:

1. Acquire semaphore (non-blocking — return error if full)
2. Create agent worktree via `r.worktree.CreateAgentWorktree(featureName)`
3. Create `model.Agent` DB record with status=WORKING, set worktree path/branch, generate name like `<type>-<uuid[:8]>`
4. Write prompt to `<worktree>/.claude/agent-prompt.md`
5. Build command: `claude -p --dangerously-skip-permissions < <prompt-path> 2>&1 | tee <worktree>/.claude/agent.log`
6. Create tmux window via `r.tmux.CreateWindow(windowName, cmd, worktreePath)`
7. Store `RunningAgent` in `r.running`
8. Start monitor goroutine: `go r.monitorAgent(ctx, agent.ID, windowName)`
9. Start heartbeat goroutine: `go r.heartbeatLoop(ctx, agent.ID)`
10. Return the Agent record

Window name format: `<agentType>-<uuid[:8]>` (e.g., `planner-a1b2c3d4`, `coder-e5f6g7h8`).

#### `Spawn(agentID, taskID uuid.UUID, worktreePath, branch, prompt string) error`

Low-level spawn for a pre-existing Agent record (used by the orchestrator when it already created the agent and worktree). Same as steps 4-9 of SpawnAgent but skips creating the worktree and DB record. Reads the agent from DB to get the window name.

#### `StopAgent(agentID uuid.UUID) error`

Graceful shutdown:

1. Look up `RunningAgent` from `r.running`
2. Cancel the monitor/heartbeat goroutines via context
3. Close the tmux window via `r.tmux.CloseWindow(ra.TmuxWindow)`
4. Update agent DB status to DEAD
5. Remove from `r.running`
6. Release semaphore

#### `GetAgentOutput(agentID uuid.UUID) (string, error)`

Read the agent's log file from `<worktree>/.claude/agent.log`. Return empty string if file doesn't exist. Limit to last 50KB if file is large.

#### `GetRunningAgents() []RunningAgent`

Return a copy of all entries in `r.running`.

#### `DrainCompletions() []Completion`

Non-blocking drain of the completions channel:

```go
func (r *Runner) DrainCompletions() []Completion {
    var results []Completion
    for {
        select {
        case c := <-r.completions:
            results = append(results, c)
        default:
            return results
        }
    }
}
```

#### `CleanupStaleAgents(timeout time.Duration) error`

Find agents in DB with status=WORKING and heartbeat older than timeout. For each:
1. Stop their tmux window if it exists
2. Update DB status to DEAD
3. Remove from `r.running`
4. Release semaphore

### Internal: `monitorAgent(ctx context.Context, agentID uuid.UUID, windowName string)`

Background goroutine that waits for the tmux window's process to exit:

```go
func (r *Runner) monitorAgent(ctx context.Context, agentID uuid.UUID, windowName string) {
    // WaitForExit blocks until the command exits
    exitCode, err := r.tmux.WaitForExit(windowName)

    // Send completion
    r.completions <- Completion{AgentID: agentID, ReturnCode: exitCode}

    // Update running map
    r.mu.Lock()
    delete(r.running, agentID)
    r.mu.Unlock()

    // Release semaphore
    <-r.semaphore
}
```

Handle context cancellation (StopAgent) gracefully — if ctx is done, don't send to completions.

### New file: `internal/agent/heartbeat.go`

In-process heartbeat (no Redis — just update the DB):

```go
package agent

// heartbeatLoop updates agent.HeartbeatAt in the DB every interval.
func (r *Runner) heartbeatLoop(ctx context.Context, agentID uuid.UUID) {
    ticker := time.NewTicker(30 * time.Second)
    defer ticker.Stop()
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            now := time.Now()
            r.db.Model(&model.Agent{}).Where("id = ?", agentID).Update("heartbeat_at", now)
        }
    }
}
```

## Conventions

- Go 1.22+, standard library where possible
- `gofmt` for formatting
- Exported functions have doc comments
- Error wrapping: `fmt.Errorf("context: %w", err)`
- Use `context.Context` for goroutine cancellation
- Build verification: `go build ./... && go vet ./...`
