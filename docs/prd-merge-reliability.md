# PRD: Merge Pipeline & Agent Lifecycle Reliability

**Date**: 2026-03-07
**Status**: Draft
**Source**: [Merge Reliability Findings Report](merge-reliability-findings.md)
**Scope**: `internal/merge/`, `internal/orchestrator/`, `internal/agent/`, `internal/worktree/`, `internal/state/`, `internal/prompt/`

---

## 1. Problem Statement

The orchestrator's merge pipeline and agent lifecycle management are the dominant source of task failures. Across 6 feature tasks on the drem-canvas project, every single one required manual supervisor intervention. The supervisor spent the vast majority of its time performing the same manual merge-and-DB-update cycle repeatedly.

**Key stats from production use:**
- 6/6 feature tasks required manual supervisor merge intervention
- Automation Lanes: 0% orchestrator merge success (0/11 subtasks, 7 consecutive supervisor sessions)
- Clip Editing: 7/10 subtasks failed on merge
- MIDI Clip Preview: 6/6 subtasks failed on merge
- Track Freeze & Bounce: 3/6 subtasks failed on merge

The orchestrator currently has **zero reliability** for the merge-detect-retry cycle it is responsible for.

---

## 2. Goals

1. **Merge success rate > 90%** for subtask→feature merges without supervisor intervention
2. **Zero silent failures** — every merge failure includes actionable git error output
3. **No destructive retries** — never retry work that is already merged
4. **Correct parent task lifecycle** — parent does not fail while subtasks are still running
5. **Reliable agent completion detection** — no stuck-in-progress subtasks with dead agents
6. **Prompt fidelity** — agents always receive complete, correct instructions

## 3. Non-Goals

- Changing the planner agent's decomposition strategy (separate effort)
- Adding new agent types (e.g., dedicated merge-resolution agent — future work)
- Changing the TUI dashboard
- Modifying the task state machine beyond what's needed for the fixes below

---

## 4. Design

### 4.1 Sequential Subtask Scheduling with Dependency Awareness

**Problem**: All subtasks launch concurrently from the same base commit. When agents touch overlapping files, subsequent merges conflict. This caused failures in 5/6 features.

**Current behavior** (`orchestrator.go` — `scheduleSubtasks()`): All subtasks with status `backlog` are scheduled simultaneously up to agent capacity.

**New behavior**: Subtasks are scheduled in waves based on file overlap analysis.

#### 4.1.1 File Overlap Analysis at Plan Time

When the planner produces a plan, each subtask already includes a list of files it will touch (in the plan JSON's `files` field). After plan approval (transition `plan_review → in_progress`), the orchestrator builds a dependency graph:

```go
// internal/orchestrator/scheduling.go

// SubtaskGroup represents a set of subtasks that can run concurrently.
type SubtaskGroup struct {
    Order    int
    Subtasks []uuid.UUID
}

// BuildSchedule analyzes file overlap between subtasks and produces
// an ordered list of groups. Subtasks within a group have no file
// overlap and can run concurrently. Groups run sequentially — group
// N+1 starts only after all subtasks in group N are merged.
func BuildSchedule(subtasks []model.Task) []SubtaskGroup
```

**Algorithm**: Greedy graph coloring on the file-overlap conflict graph.
1. Build an undirected graph where each subtask is a node and edges connect subtasks with overlapping `files` lists.
2. Greedy-color the graph (order nodes by degree descending).
3. Each color becomes a `SubtaskGroup`. Groups are ordered by color index.
4. Store the schedule in `task.Context["schedule"]` as JSON.

**Fallback**: If the plan has no `files` data, all subtasks go into a single group (current behavior). This preserves backward compatibility.

#### 4.1.2 Wave-Based Scheduling

```go
// internal/orchestrator/orchestrator.go — scheduleSubtasks() changes

func (o *Orchestrator) scheduleSubtasks(parent *model.Task) {
    schedule := parent.Context["schedule"]
    if schedule == nil {
        // Legacy: schedule all at once
        o.scheduleAll(parent)
        return
    }

    currentGroup := o.currentGroup(parent, schedule)

    // Only schedule subtasks in the current group
    for _, subtaskID := range currentGroup.Subtasks {
        subtask := o.loadSubtask(subtaskID)
        if subtask.Status == model.StatusBacklog {
            o.spawnCoderForSubtask(parent, subtask)
        }
    }
}

// currentGroup returns the earliest group that has unfinished subtasks.
func (o *Orchestrator) currentGroup(parent *model.Task, schedule []SubtaskGroup) *SubtaskGroup
```

A group is "finished" when all its subtasks are in `done` or `failed` status. When a group finishes, the next group's subtasks are scheduled. Failed subtasks within a group do not block advancement — they can be retried later.

#### 4.1.3 Plan JSON Schema Extension

The planner prompt already asks for a `files` array per subtask. We formalize this:

```json
{
  "subtasks": [
    {
      "title": "...",
      "description": "...",
      "files": ["path/to/file1.go", "path/to/file2.go"],
      "depends_on": []
    }
  ]
}
```

The `depends_on` field (already in schema but unused) becomes an explicit override. If populated, it takes precedence over file-overlap analysis: a subtask with `depends_on: ["subtask-uuid"]` waits for those subtasks regardless of file overlap.

---

### 4.2 Rebase-Before-Merge

**Problem**: Agent branches diverge from the integration branch as earlier agents' work is merged. Even non-overlapping changes cause `git merge` to fail when the merge base is stale.

**Current behavior** (`merge.go` — `MergeAgentIntoFeature()`): Direct `git merge --no-ff <agentBranch>` in the feature worktree.

**New behavior**: Rebase the agent branch onto the current feature HEAD before merging.

```go
// internal/worktree/git.go — new method

// RebaseBranch rebases sourceBranch onto the HEAD of targetWorktree.
// It performs the rebase in the source worktree to avoid disrupting
// the target. Returns a RebaseResult indicating success or conflicts.
func (m *Manager) RebaseBranch(sourceBranch string, targetWorktree string) (*RebaseResult, error) {
    // 1. Resolve target HEAD
    targetHEAD := m.RunGit(["rev-parse", "HEAD"], targetWorktree)

    // 2. Find the source worktree for this branch
    sourceWorktree := m.findWorktreeByBranch(sourceBranch)

    // 3. Attempt rebase
    result := m.RunGit(["rebase", targetHEAD], sourceWorktree)

    // 4. On conflict: abort rebase, return conflicts
    if result.ExitCode != 0 {
        m.RunGit(["rebase", "--abort"], sourceWorktree)
        return &RebaseResult{Success: false, Conflicts: parseConflicts(result.Stderr)}, nil
    }

    return &RebaseResult{Success: true}, nil
}
```

**Updated merge flow** (`merge.go` — `MergeAgentIntoFeature()`):

```
1. Rebase agent branch onto feature HEAD
2. If rebase succeeds → git merge --no-ff (now guaranteed fast-forward-able)
3. If rebase fails → return MergeResult with conflicts (no merge attempted)
```

This eliminates the class of failures where merges fail due to a stale merge base but the actual changes don't conflict.

---

### 4.3 Diagnostic Logging for Merge Failures

**Problem**: Merge failures produce a generic string `"merge into feature branch failed, agent branch preserved"` with no git stderr. Diagnosis is impossible without manual investigation. This was cited in 3/6 features.

**Current behavior** (`orchestrator.go` — `onAgentCompleted()`):
```go
if mergeResult != nil && !mergeResult.Success {
    // logs generic message, no git stderr
}
```

The underlying `MergeBranch()` in `worktree/git.go` returns a `GitError` with stderr, but this is discarded.

**New behavior**: Propagate and log the full git error output.

#### 4.3.1 Enrich MergeResult

```go
// internal/worktree/git.go — MergeResult changes

type MergeResult struct {
    Success     bool
    MergeCommit string
    Conflicts   []string
    GitStderr   string   // NEW: raw git stderr output
    GitCommand  string   // NEW: the exact git command that was run
}
```

`MergeBranch()` populates `GitStderr` and `GitCommand` on failure.

#### 4.3.2 Structured Merge Failure Events

```go
// internal/orchestrator/orchestrator.go — onAgentCompleted() changes

if mergeResult != nil && !mergeResult.Success {
    details := map[string]any{
        "agent_branch":    ag.WorktreeBranch,
        "feature_branch":  task.WorktreeBranch,
        "conflicts":       mergeResult.Conflicts,
        "git_stderr":      mergeResult.GitStderr,
        "git_command":     mergeResult.GitCommand,
        "merge_base":      mergeBase, // resolved before merge
        "feature_head":    featureHEAD,
        "agent_head":      agentHEAD,
    }
    o.emitEvent(task.ID, "merge_failed", details)

    slog.Error("merge failed",
        "task", task.Title,
        "agent_branch", ag.WorktreeBranch,
        "conflicts", mergeResult.Conflicts,
        "git_stderr", mergeResult.GitStderr,
    )
}
```

This gives supervisors (human or automated) the exact information needed to diagnose failures without manual git investigation.

---

### 4.4 Pre-Merge Fetch

**Problem**: In the Automation Lanes feature, clean merges failed 11/11 times. One likely cause is that the agent branch ref isn't visible in the integration worktree. In a bare repo with multiple worktrees, branch refs created in one worktree may not be immediately visible in another without a fetch.

**Current behavior** (`worktree/git.go` — `MergeBranch()`): Directly runs `git merge --no-ff <sourceBranch>` without ensuring the ref is resolvable.

**New behavior**: Resolve and fetch the branch ref before merging.

```go
// internal/worktree/git.go — MergeBranch() changes

func (m *Manager) MergeBranch(sourceBranch, targetWorktree string) (*MergeResult, error) {
    // NEW: Verify the branch ref is resolvable in the target worktree
    _, err := m.RunGit([]string{"rev-parse", "--verify", sourceBranch}, targetWorktree)
    if err != nil {
        // Branch not visible — run fetch to update refs
        // In a bare repo, worktrees share the object store but ref visibility
        // can lag. Running fetch against the local bare repo refreshes refs.
        slog.Warn("branch ref not visible, fetching",
            "branch", sourceBranch,
            "worktree", targetWorktree,
        )
        m.RunGit([]string{"fetch", ".", sourceBranch + ":" + sourceBranch}, targetWorktree)

        // Re-verify
        _, err = m.RunGit([]string{"rev-parse", "--verify", sourceBranch}, targetWorktree)
        if err != nil {
            return nil, fmt.Errorf("branch %s not resolvable after fetch: %w", sourceBranch, err)
        }
    }

    // Proceed with merge
    result, err := m.RunGit([]string{"merge", "--no-ff", sourceBranch}, targetWorktree)
    // ... existing merge logic ...
}
```

---

### 4.5 Merge Retry with Backoff

**Problem**: Transient failures (ref visibility, lock contention on the SQLite-backed git index) cause permanent merge failures because the orchestrator never retries.

**Current behavior**: One attempt, then mark `failed`.

**New behavior**: Retry up to 3 times with exponential backoff.

```go
// internal/merge/merge.go — MergeAgentIntoFeature() changes

const maxMergeRetries = 3

func (mo *Orchestrator) MergeAgentIntoFeature(agentBranch, featureWorktree string) (*MergeResult, error) {
    var lastResult *MergeResult

    for attempt := 1; attempt <= maxMergeRetries; attempt++ {
        // Rebase (from §4.2), then merge
        rebaseResult, err := mo.wt.RebaseBranch(agentBranch, featureWorktree)
        if err != nil {
            return nil, fmt.Errorf("rebase attempt %d: %w", attempt, err)
        }
        if !rebaseResult.Success {
            // Real conflict — don't retry
            return &MergeResult{
                Success:   false,
                Conflicts: rebaseResult.Conflicts,
                GitStderr: rebaseResult.GitStderr,
            }, nil
        }

        lastResult, err = mo.wt.MergeBranch(agentBranch, featureWorktree)
        if err != nil {
            return nil, fmt.Errorf("merge attempt %d: %w", attempt, err)
        }

        if lastResult.Success {
            return lastResult, nil
        }

        // If conflicts are real file conflicts, don't retry
        if len(lastResult.Conflicts) > 0 {
            return lastResult, nil
        }

        // Transient failure — retry after backoff
        slog.Warn("merge failed transiently, retrying",
            "attempt", attempt,
            "agent_branch", agentBranch,
            "stderr", lastResult.GitStderr,
        )
        time.Sleep(time.Duration(attempt) * 2 * time.Second)
    }

    return lastResult, nil
}
```

---

### 4.6 Already-Merged Check Before Retry

**Problem**: The orchestrator retries failed subtasks without checking whether their work was already merged into the integration branch. The new agent re-does the same work, which conflicts with the already-merged version.

**Current behavior** (`orchestrator.go`): When a subtask is in `failed` status, the reconciliation loop resets it to `backlog` for retry. No check for existing work.

**New behavior**: Before retrying a failed subtask, check if its commits are already in the integration branch.

```go
// internal/orchestrator/orchestrator.go — new method

// isWorkAlreadyMerged checks whether the agent branch's commits are
// already reachable from the feature branch HEAD. Returns true if the
// work has been merged (even if the subtask status says failed).
func (o *Orchestrator) isWorkAlreadyMerged(subtask *model.Task, featureWorktree string) bool {
    if subtask.AssignedAgentID == nil {
        return false
    }

    var agent model.Agent
    if err := o.db.First(&agent, subtask.AssignedAgentID).Error; err != nil {
        return false
    }

    if agent.WorktreeBranch == "" {
        return false
    }

    // Check if agent branch tip is an ancestor of feature HEAD
    _, err := o.wt.RunGit(
        []string{"merge-base", "--is-ancestor", agent.WorktreeBranch, "HEAD"},
        featureWorktree,
    )
    return err == nil // exit code 0 means it IS an ancestor
}
```

**Integration points**:

1. **In `reconcileOrphanedSubtasks()`**: Before resetting to `backlog`, check `isWorkAlreadyMerged()`. If true, transition to `done` instead.

2. **In `scheduleSubtasks()`**: Before spawning a new agent for a `backlog` subtask that was previously `failed`, check `isWorkAlreadyMerged()`. If true, fast-track to `done`.

3. **In `processAgentResult()` for `onAgentFailed()`**: Before marking the subtask as `failed`, check if the agent's commits were already merged (e.g., merge succeeded but DB update failed). If so, mark `done`.

---

### 4.7 Parent Task Failure Cascading Fix

**Problem**: The parent task transitions to `failed` as soon as any subtask fails, even while other subtasks are still `in_progress` with agents actively running. This orphans running agents and prevents the task from completing naturally.

**Current behavior** (`orchestrator.go` — `checkFeatureCompletion()`): If any subtask is `failed`, the parent transitions to `failed`.

**New behavior**: The parent only fails when all subtasks are terminal (done or failed) AND at least one is failed. While any subtask is still `in_progress`, `planning`, or `backlog`, the parent stays `in_progress`.

```go
// internal/orchestrator/orchestrator.go — checkFeatureCompletion() changes

func (o *Orchestrator) checkFeatureCompletion(parent *model.Task) {
    var subtasks []model.Task
    o.db.Where("parent_task_id = ?", parent.ID).Find(&subtasks)

    allTerminal := true
    anyFailed := false
    allDone := true

    for _, st := range subtasks {
        switch st.Status {
        case model.StatusDone:
            // good
        case model.StatusFailed:
            anyFailed = true
        default:
            allTerminal = false
            allDone = false
        }
    }

    if allDone {
        // All subtasks done → advance parent to testing_ready
        o.transition(parent, model.StatusTestingReady, "orchestrator",
            "all subtasks completed successfully")
        return
    }

    if allTerminal && anyFailed {
        // All subtasks finished but some failed → parent fails
        failedNames := collectFailedNames(subtasks)
        o.transition(parent, model.StatusFailed, "orchestrator",
            fmt.Sprintf("subtasks failed: %s", strings.Join(failedNames, ", ")))
        return
    }

    // Otherwise: subtasks still running, keep parent in_progress
}
```

---

### 4.8 Robust Agent Completion Detection

**Problem**: The orchestrator fails to detect when agents have finished their work. Agents complete, commit, and exit, but the orchestrator never triggers the merge step. In one case, subtasks transitioned to `in_progress` but no agents were ever created.

#### 4.8.1 Agent Spawn Verification

**Current behavior**: `SpawnAgent()` creates a DB record and starts a tmux session. If the tmux session fails to start or dies immediately, the subtask sits in `in_progress` forever.

**New behavior**: Verify agent is alive shortly after spawn.

```go
// internal/agent/runner.go — SpawnAgent() changes

func (r *Runner) SpawnAgent(...) error {
    // ... existing spawn logic ...

    // NEW: Schedule a spawn verification check
    go r.verifySpawn(agentID, sessionName, 10*time.Second)

    return nil
}

// verifySpawn checks that the agent tmux session is alive after a
// short delay. If the session doesn't exist or the pane is already
// dead, it sends a failure completion.
func (r *Runner) verifySpawn(agentID uuid.UUID, sessionName string, delay time.Duration) {
    time.Sleep(delay)

    r.mu.Lock()
    _, stillTracked := r.running[agentID]
    r.mu.Unlock()
    if !stillTracked {
        return // already completed or stopped
    }

    alive, err := r.tmux.IsAgentSessionAlive(sessionName)
    if err != nil || !alive {
        slog.Error("agent failed spawn verification",
            "agent_id", agentID,
            "session", sessionName,
            "error", err,
        )
        r.completions <- Completion{AgentID: agentID, ExitCode: 1}
    }
}
```

#### 4.8.2 Fallback Completion Detection via Commit Polling

**Current behavior**: Completion detection relies entirely on the idle signal file + tmux pane death. If the signal file isn't created (e.g., settings.json hook misconfigured) and the monitor goroutine dies, the agent is stuck.

**New behavior**: Add a secondary detection mechanism in the reconciliation loop.

```go
// internal/orchestrator/orchestrator.go — reconcileStuckAgents() (new method)

// reconcileStuckAgents finds IN_PROGRESS subtasks whose agents have
// been running longer than expectedMaxDuration and checks for
// completion signals that may have been missed.
func (o *Orchestrator) reconcileStuckAgents() {
    var subtasks []model.Task
    o.db.Where("status = ? AND assigned_agent_id IS NOT NULL",
        model.StatusInProgress).Find(&subtasks)

    for _, st := range subtasks {
        var agent model.Agent
        if err := o.db.First(&agent, st.AssignedAgentID).Error; err != nil {
            continue
        }

        // Check if tmux session is dead (agent exited without being detected)
        alive, _ := o.tmux.IsAgentSessionAlive(agent.TmuxSession)
        if alive {
            continue // agent still running
        }

        // Agent session is dead but we never got a completion
        slog.Warn("detected dead agent session without completion",
            "agent_id", agent.ID,
            "task", st.Title,
            "session", agent.TmuxSession,
        )

        // Check if agent has commits
        hasCommits, _ := o.wt.BranchHasNewCommits(agent.WorktreePath, st.WorktreeBranch)
        if hasCommits {
            // Attempt to commit any unstaged work
            o.wt.CommitUnstagedChanges(agent.WorktreePath, "auto-commit: agent exited with unstaged changes")

            // Route through normal completion path
            o.processAgentResult(Completion{AgentID: agent.ID, ExitCode: 0})
        } else {
            // No work produced — mark agent dead, subtask failed
            o.runner.markAgentDead(agent.ID)
            o.transition(&st, model.StatusFailed, "orchestrator",
                "agent session died without producing commits")
        }
    }
}
```

This is called during reconciliation (every 10 ticks) as a safety net.

#### 4.8.3 Agent Record Existence Verification

**Problem**: Subtasks transition to `in_progress` but no agent records are created.

**New behavior**: After scheduling, verify the agent record exists.

```go
// internal/orchestrator/orchestrator.go — scheduleSubtasks() changes

func (o *Orchestrator) spawnCoderForSubtask(parent, subtask *model.Task) {
    err := o.runner.SpawnAgent(subtask, featureName, model.AgentTypeCoder, prompt)
    if err != nil {
        slog.Error("failed to spawn agent", "subtask", subtask.Title, "error", err)
        o.transition(subtask, model.StatusFailed, "orchestrator",
            fmt.Sprintf("agent spawn failed: %v", err))
        return
    }

    // NEW: Verify agent record was created
    var agent model.Agent
    if err := o.db.Where("current_task_id = ? AND status = ?",
        subtask.ID, model.AgentStatusWorking).First(&agent).Error; err != nil {
        slog.Error("agent record missing after spawn",
            "subtask", subtask.Title, "error", err)
        o.transition(subtask, model.StatusFailed, "orchestrator",
            "agent record not found after spawn")
        return
    }
}
```

---

### 4.9 Prompt Delivery Integrity

**Problem**: Agent prompts are being truncated mid-sentence, leaving agents with incomplete instructions.

**Current behavior** (`agent/runner.go` — `startAgent()`): The prompt is written to `.claude/agent-prompt.md` and passed to claude via `claude --dangerously-skip-permissions "$(cat .claude/agent-prompt.md)"`. Shell command substitution (`$(cat ...)`) can truncate if the prompt contains special characters or exceeds shell argument limits.

**New behavior**: Use `--prompt-file` or stdin redirection instead of command substitution.

```go
// internal/agent/runner.go — startAgent() changes

func (r *Runner) startAgent(..., prompt string) error {
    promptPath := filepath.Join(worktreePath, ".claude", "agent-prompt.md")

    // Write prompt to file
    if err := os.WriteFile(promptPath, []byte(prompt), 0644); err != nil {
        return fmt.Errorf("write prompt: %w", err)
    }

    // NEW: Verify prompt was written completely
    written, err := os.ReadFile(promptPath)
    if err != nil || len(written) != len(prompt) {
        return fmt.Errorf("prompt write verification failed: wrote %d of %d bytes",
            len(written), len(prompt))
    }

    // NEW: Use stdin redirection instead of $(cat ...) to avoid shell
    // argument length limits and special character issues
    cmd := fmt.Sprintf(
        "cat %q | %s --dangerously-skip-permissions -p -",
        promptPath, r.claudeBin,
    )

    // ... create tmux session with cmd ...
}
```

If `claude` doesn't support `-p -` (stdin), fall back to a heredoc-based approach that avoids `$(cat ...)`:

```go
cmd := fmt.Sprintf(
    "%s --dangerously-skip-permissions --resume --prompt-file %q",
    r.claudeBin, promptPath,
)
```

**Additionally**: Log the prompt byte count in the agent spawn event for post-hoc debugging:

```go
o.emitEvent(task.ID, "agent_spawned", map[string]any{
    "agent_id":     agentID,
    "prompt_bytes": len(prompt),
    "agent_type":   agentType,
})
```

---

### 4.10 Supervisor Journal Improvements

**Problem**: 22/27 journal entries are identical boilerplate stubs. Real journals are written into ephemeral worktrees.

#### 4.10.1 Eliminate Stub Entries

**Current behavior**: Every supervisor session spawn creates a journal entry with the template text and no content.

**New behavior**: Only create journal entries when the supervisor has actual findings to report. The initial spawn event is logged as a `TaskEvent` instead.

```go
// internal/orchestrator/orchestrator.go — supervisor spawn changes

// Replace journal stub creation with a lightweight TaskEvent
o.emitEvent(task.ID, "supervisor_session_started", map[string]any{
    "session_type": "on_demand",
})
// Do NOT write a journal entry here — only write journals on findings
```

#### 4.10.2 Copy Journals on Worktree Cleanup

**Current behavior**: `RemoveFeature()` deletes the worktree directory, including `supervisor-journal.md`.

**New behavior**: Before removing a feature worktree, copy any journal files to the main repo.

```go
// internal/worktree/manager.go — RemoveFeature() changes

func (m *Manager) RemoveFeature(name string) error {
    integrationPath := m.featureIntegrationPath(name)

    // NEW: Preserve journals before removal
    journalSrc := filepath.Join(integrationPath, "supervisor-journal.md")
    if _, err := os.Stat(journalSrc); err == nil {
        journalDst := filepath.Join(m.bareRepoPath, "journals",
            fmt.Sprintf("%s-integration.md", name))
        copyFile(journalSrc, journalDst)
    }

    // ... existing removal logic ...
}
```

---

### 4.11 State Machine Additions

Two targeted additions to the state machine to support the fixes above.

#### 4.11.1 `failed → in_progress` Transition (Supervisor Override)

**Problem**: `failed → backlog` triggers the full lifecycle including replanning, which gates on human approval. Supervisors need to resume work without replanning.

```go
// internal/state/machine.go — ValidTransitions changes

StatusFailed: {StatusBacklog, StatusInProgress}, // ADD StatusInProgress
```

This transition is restricted to actor `"supervisor"` — the orchestrator loop never uses it automatically.

#### 4.11.2 Guard on `in_progress → failed` for Parent Tasks

The `checkFeatureCompletion()` changes in §4.7 handle this at the orchestrator level. No state machine change needed — the guard is in the orchestrator logic, not the state machine itself.

---

## 5. Implementation Plan

Ordered by impact and dependency. Each phase is independently shippable.

### Phase 1: Diagnostics & Safety (Unblocks debugging)

| Item | Section | Files | Description |
|------|---------|-------|-------------|
| 1a | §4.3 | `internal/worktree/git.go`, `internal/orchestrator/orchestrator.go` | Enrich MergeResult with GitStderr/GitCommand, log on failure |
| 1b | §4.4 | `internal/worktree/git.go` | Pre-merge ref verification and fetch |
| 1c | §4.6 | `internal/orchestrator/orchestrator.go` | Already-merged check before retry |
| 1d | §4.7 | `internal/orchestrator/orchestrator.go` | Fix parent task failure cascading |

**Estimated complexity**: Small — each item is a localized change to 1-2 functions.

### Phase 2: Merge Reliability (Fixes the core failure mode)

| Item | Section | Files | Description |
|------|---------|-------|-------------|
| 2a | §4.2 | `internal/worktree/git.go`, `internal/merge/merge.go` | Rebase-before-merge |
| 2b | §4.5 | `internal/merge/merge.go` | Merge retry with backoff |

**Estimated complexity**: Medium — adds a new git operation (rebase) and retry loop.

### Phase 3: Scheduling (Prevents conflicts proactively)

| Item | Section | Files | Description |
|------|---------|-------|-------------|
| 3a | §4.1.1 | `internal/orchestrator/scheduling.go` (new) | File overlap analysis and schedule builder |
| 3b | §4.1.2 | `internal/orchestrator/orchestrator.go` | Wave-based scheduling in `scheduleSubtasks()` |
| 3c | §4.1.3 | `internal/prompt/prompt.go` | Ensure planner prompt requests `files` list |

**Estimated complexity**: Medium — new algorithm (graph coloring), but isolated in a new file.

### Phase 4: Agent Lifecycle Hardening

| Item | Section | Files | Description |
|------|---------|-------|-------------|
| 4a | §4.8.1 | `internal/agent/runner.go` | Agent spawn verification |
| 4b | §4.8.2 | `internal/orchestrator/orchestrator.go` | Fallback completion detection via reconciliation |
| 4c | §4.8.3 | `internal/orchestrator/orchestrator.go` | Agent record existence verification |
| 4d | §4.9 | `internal/agent/runner.go` | Prompt delivery integrity |

**Estimated complexity**: Small-Medium — targeted additions to existing functions.

### Phase 5: Quality of Life

| Item | Section | Files | Description |
|------|---------|-------|-------------|
| 5a | §4.10.1 | `internal/orchestrator/orchestrator.go` | Eliminate supervisor stub journals |
| 5b | §4.10.2 | `internal/worktree/manager.go` | Preserve journals on worktree cleanup |
| 5c | §4.11.1 | `internal/state/machine.go` | Add `failed → in_progress` transition |

**Estimated complexity**: Small.

---

## 6. Testing Strategy

### Unit Tests

| Component | Test |
|-----------|------|
| `BuildSchedule()` | Graph coloring: no overlap → single group; full overlap → sequential; partial overlap → mixed groups; no files data → single group fallback |
| `RebaseBranch()` | Clean rebase; conflicting rebase (returns conflicts, aborts cleanly); target HEAD unchanged on failure |
| `MergeBranch()` (with fetch) | Ref not visible → fetch → merge succeeds; ref not visible → fetch fails → error with message |
| `isWorkAlreadyMerged()` | Agent branch is ancestor → true; agent branch diverged → false; no agent branch → false |
| `checkFeatureCompletion()` | All done → testing_ready; some failed + some in_progress → stays in_progress; all terminal + some failed → failed |
| Merge retry | Transient failure → retry succeeds; real conflict → no retry; max retries exhausted → fail |
| Prompt write verification | Complete write → proceed; partial write → error before agent spawn |
| `verifySpawn()` | Session alive → no action; session dead → completion sent |

### Integration Tests

| Scenario | Expected Outcome |
|----------|-----------------|
| Two agents modify the same file, scheduled sequentially | Second agent rebases cleanly onto first agent's merged work |
| Agent completes but monitor goroutine dies | Reconciliation sweep detects dead session, triggers merge |
| Agent spawn fails silently (tmux error) | Spawn verification fires, subtask marked failed within 10s |
| Failed subtask whose work is already merged | Detected as already-merged, fast-tracked to done |
| Parent with 3 subtasks, 1 fails, 2 still running | Parent stays in_progress until all finish |
| Merge fails due to ref visibility | Pre-merge fetch resolves it, merge succeeds on first attempt |

---

## 7. Success Metrics

Track these across the next 3 feature tasks:

| Metric | Current | Target |
|--------|---------|--------|
| Subtask→feature merge success rate | ~30% | >90% |
| Supervisor merge interventions per feature | 5-11 | <1 |
| Silent merge failures (no diagnostic info) | ~100% | 0% |
| Destructive retries (re-doing merged work) | 2-3 per feature | 0 |
| Orphaned agents (dead session, still in_progress) | 1-2 per feature | 0 |
| Prompt truncation incidents | 2 observed | 0 |
| Parent task premature failures | 2+ per feature | 0 |

---

## 8. Open Questions

1. **Conflict resolution agent**: Should the orchestrator spawn a dedicated merge-resolution agent instead of immediately failing when rebase-before-merge encounters conflicts? This was recommended in 3/6 features but is deferred as a future enhancement to keep this PRD focused on reliability.

2. **Planner file prediction accuracy**: The scheduling algorithm depends on the planner correctly predicting which files each subtask will touch. How accurate is this in practice? May need a feedback mechanism to improve predictions over time.

3. **Rebase vs. merge semantics**: Rebasing rewrites history on the agent branch. Since agent branches are ephemeral and never shared, this should be safe. Confirm there are no workflows that depend on agent branch commit SHAs being stable.

4. **SQLite contention**: Multiple concurrent git operations + DB writes during merge retries could hit SQLite write contention. The WAL mode helps but may need connection pooling or write serialization if issues arise.

5. **`claude` CLI prompt delivery**: Need to verify the exact CLI flags for prompt file input (`--prompt-file`, `-p -`, etc.) against the version of `claude` being used.
