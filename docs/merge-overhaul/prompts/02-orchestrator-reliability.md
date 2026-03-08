# Agent: Orchestrator Reliability

You are working on the `master` branch of Drem Orchestrator, a Go-based multi-agent orchestrator that manages Claude Code agents via tmux in git bare repos with worktrees.
Your task is to fix critical reliability issues in the orchestrator: prevent destructive retries of already-merged work, fix premature parent task failure, detect stuck agents with dead tmux sessions, verify agent records after spawn, and add a `failed -> in_progress` state transition.

## Context

Read these before starting:
- `docs/merge-overhaul/prd-merge-reliability.md` (sections 4.6, 4.7, 4.8.2, 4.8.3, 4.11)
- `internal/orchestrator/orchestrator.go` (full file — key functions listed below)
- `internal/state/machine.go` (ValidTransitions map)
- `internal/model/enums.go` (TaskStatus, AgentStatus enums)
- `internal/model/models.go` (Task, Agent model structs)
- `internal/worktree/git.go` (RunGit, BranchHasNewCommits, CommitUnstagedChanges)

Key functions in `orchestrator.go` you will modify:
- `reconcileOrphanedSubtasks()` (~line 348) — finds subtasks with dead agents
- `scheduleSubtasks()` (~line 1268) — spawns agents for BACKLOG subtasks
- `checkFeatureCompletion()` (~line 1378) — decides when parent task is done/failed
- `onAgentFailed()` (~line 1011) — handles agent failure events

## Deliverables

### 1. Already-Merged Check (`internal/orchestrator/orchestrator.go`)

Add a new method:

```go
// isWorkAlreadyMerged checks whether the agent branch's commits are
// already reachable from the feature branch HEAD. Returns true if the
// work has been merged (even if the subtask status says failed).
func (o *Orchestrator) isWorkAlreadyMerged(subtask *model.Task, featureWorktree string) bool
```

Implementation:
1. If `subtask.AssignedAgentID` is nil, return false
2. Load the Agent record. If not found or `WorktreeBranch` is empty, return false
3. Run `git merge-base --is-ancestor <agent.WorktreeBranch> HEAD` in `featureWorktree`
4. If exit code 0, the branch IS an ancestor (already merged) — return true
5. Use `worktree.RunGit()` for the git command

Integrate this check in three places:

**a. `reconcileOrphanedSubtasks()`**: Before resetting a subtask to `backlog`, call `isWorkAlreadyMerged()`. If true, transition to `done` instead of `backlog`. Log this clearly.

**b. `scheduleSubtasks()`**: Before spawning a new agent for a `backlog` subtask that was previously assigned an agent (has `AssignedAgentID`), call `isWorkAlreadyMerged()`. If true, transition to `done` and skip spawning.

**c. `onAgentFailed()`**: Before marking the subtask as `failed`, call `isWorkAlreadyMerged()`. If the agent's commits are already in the feature branch (merge succeeded but DB update failed), transition to `done` instead.

For all three, you need to resolve the feature worktree path. The subtask's parent task has `WorktreeBranch` set to `feature/<name>`. Use `o.worktree.FeatureWorktreePath(<name>)` to get the integration worktree path.

### 2. Parent Task Failure Cascading Fix (`internal/orchestrator/orchestrator.go`)

Modify `checkFeatureCompletion()`. The current logic fails the parent as soon as ANY subtask fails, even while other subtasks are still running.

New logic:

```go
func (o *Orchestrator) checkFeatureCompletion(parent *model.Task) error {
    var subtasks []model.Task
    o.db.Where("parent_task_id = ?", parent.ID).Find(&subtasks)

    if len(subtasks) == 0 {
        return nil
    }

    allTerminal := true
    anyFailed := false
    allDone := true

    for _, st := range subtasks {
        switch st.Status {
        case model.StatusDone:
            // good
        case model.StatusFailed:
            anyFailed = true
            allDone = false
        default:
            allTerminal = false
            allDone = false
        }
    }

    if allDone {
        // All subtasks done -> advance parent
        // (use existing transition logic to testing_ready or whatever the current code does)
        return nil
    }

    if allTerminal && anyFailed {
        // All subtasks finished but some failed -> parent fails
        // Collect failed subtask names for the failure message
        return nil
    }

    // Otherwise: subtasks still running, keep parent in_progress — do nothing
    return nil
}
```

Preserve the existing transition calls and event emissions. Only change the conditional logic that decides WHEN to transition.

### 3. Stuck Agent Reconciliation (`internal/orchestrator/orchestrator.go`)

Add a new method:

```go
// reconcileStuckAgents finds IN_PROGRESS subtasks whose agent tmux
// sessions are dead but no completion was ever received. This catches
// agents that exited without triggering the monitor goroutine.
func (o *Orchestrator) reconcileStuckAgents() error
```

Implementation:
1. Query subtasks with `status = in_progress` and `assigned_agent_id IS NOT NULL`
2. For each, load the Agent record
3. Check if the tmux session is alive using `o.runner` (the runner has methods to check agent status — look for `GetRunningAgents()` or check if the agent is in the runner's `running` map)
4. If the agent is NOT in the runner's running map AND the agent's DB status is still `working`:
   - Log a warning: "detected dead agent session without completion"
   - Check if the agent branch has commits using `worktree.BranchHasNewCommits()`
   - If it has commits: route through the normal completion path by sending to the completions channel
   - If no commits: update agent status to `dead`, transition subtask to `failed` with message "agent session died without producing commits"

Call `reconcileStuckAgents()` from the existing `Reconcile()` method, alongside the other reconciliation functions.

### 4. Agent Record Verification (`internal/orchestrator/orchestrator.go`)

In `scheduleSubtasks()`, after calling `o.runner.SpawnAgent()`, verify the agent record exists in the DB:

```go
// After spawn succeeds, verify agent record was created
var agent model.Agent
if err := o.db.Where("current_task_id = ? AND status = ?",
    subtask.ID, model.AgentWorking).First(&agent).Error; err != nil {
    slog.Error("agent record missing after spawn",
        "subtask", subtask.Title, "error", err)
    // Transition subtask to failed — spawn was incomplete
    // ... transition logic ...
}
```

Add this check right after the existing `SpawnAgent` call in the scheduling loop.

### 5. State Machine: `failed -> in_progress` Transition (`internal/state/machine.go`)

Add `model.StatusInProgress` to the valid transitions from `StatusFailed`:

Current:
```go
model.StatusFailed: {model.StatusBacklog},
```

New:
```go
model.StatusFailed: {model.StatusBacklog, model.StatusInProgress},
```

This allows supervisors to resume work without replanning. The orchestrator loop never uses this transition automatically — it's for supervisor override only.

### 6. Tests

Add tests for:

- **isWorkAlreadyMerged**: Agent branch is ancestor of feature HEAD -> true. Agent branch has diverged -> false. No agent assigned -> false. Agent has no branch -> false.
- **checkFeatureCompletion**: All done -> parent advances. Mix of failed + in_progress -> parent stays in_progress. All terminal + some failed -> parent fails. No subtasks -> no-op.
- **reconcileStuckAgents**: Agent in runner's map -> no action. Agent NOT in runner's map + has commits -> completion sent. Agent NOT in runner's map + no commits -> subtask failed.
- **State machine**: Verify `failed -> in_progress` is now valid. Verify `failed -> backlog` still works.

Use mock/stub implementations for the runner and worktree dependencies where needed.

## Scope Limitation

ONLY modify `internal/orchestrator/orchestrator.go` and `internal/state/machine.go`. Do NOT touch `internal/worktree/`, `internal/merge/`, `internal/agent/`, or `internal/prompt/`. The worktree-level merge improvements and agent lifecycle changes are handled by separate agents.

## Conventions

- Go 1.22+ with standard library
- `gofmt` for formatting
- Exported functions have doc comments
- Error wrapping: `fmt.Errorf("context: %w", err)`
- Table-driven tests
- Build verification: `go build ./... && go test ./...`
