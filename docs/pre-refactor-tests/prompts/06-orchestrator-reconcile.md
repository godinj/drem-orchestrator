# Agent: Orchestrator Reconciliation Test Coverage

> Historical pre-refactor prompt; do not execute. Use
> `docs/test-coverage/prompts/02-reconciliation.md` for the current
> recovery-only contract.

You are working on the `master` branch of Drem Orchestrator, a Go multi-agent development orchestrator.
Your task is pre-refactor test hardening: add tests for the orchestrator's consistency audit (Reconcile) functions.

## Context

Read these before starting:
- `internal/orchestrator/orchestrator.go` (focus on lines 320-870: Reconcile, reconcileStaleSubtasks, reconcileOrphanedSubtasks, reconcileEmptyFeatures, reconcileOrphanWorktrees, reconcileStuckAgents, recoverStuckAgents)
- `internal/orchestrator/orchestrator_test.go` (existing test helpers)
- `internal/orchestrator/lifecycle_test.go` (lifecycleTestDB, createLifecycleTask patterns)
- `internal/model/models.go` (Task, Agent models)
- `internal/model/enums.go` (StatusInProgress, StatusDone, StatusBacklog, StatusTestingReady, StatusFailed, AgentWorking, AgentIdle)
- `internal/worktree/worktree.go` (worktree.Manager — FeatureWorktreePath, RemoveAgentWorktree, ListAgentWorktrees)
- `internal/testutil/testutil.go` (SetupBareRepo, AddWorktree, CommitFile)

## Important Design Notes

The reconciliation functions detect and fix state inconsistencies. They:
1. Query DB for tasks/agents in specific states
2. Check git state (do commits exist? is branch empty?)
3. Fix DB records that don't match reality

Each reconcile function returns `(int, error)` — count of fixes applied.

The Orchestrator needs a real `worktree.Manager` for these tests because they check git state. Use `testutil.SetupBareRepo` + real `worktree.NewManager`.

Since these are private methods, write tests in `package orchestrator`. Create file `internal/orchestrator/reconcile_test.go`.

## Deliverables

### New file: `internal/orchestrator/reconcile_test.go`

### 1. Reconcile() top-level

- `TestReconcile_NoIssues` — clean state (no tasks), verify returns 0 fixes, no error
- `TestReconcile_AggregatesFixes` — set up state that triggers multiple sub-reconcilers, verify total count

### 2. reconcileStaleSubtasks

This finds subtasks marked DONE whose agent contributed no commits to the feature branch.

Setup for each test:
1. Create bare repo
2. Create feature worktree (integration branch)
3. Create parent task (status=in_progress, worktree_branch set)
4. Create subtask (status=done, has agent)

Tests:
- `TestReconcileStaleSubtasks_WithCommits` — agent's branch has commits merged to integration. Subtask should stay DONE. Returns 0.
- `TestReconcileStaleSubtasks_NoCommits` — agent's branch has no commits (empty work). Subtask should be reset to BACKLOG. Returns 1.
- `TestReconcileStaleSubtasks_NoParentBranch` — parent has empty WorktreeBranch. Should skip gracefully. Returns 0.

### 3. reconcileOrphanedSubtasks

This finds IN_PROGRESS subtasks whose assigned agent no longer exists in the runner's active set.

- `TestReconcileOrphanedSubtasks_NoOrphans` — all IN_PROGRESS subtasks have live agents. Returns 0.
- `TestReconcileOrphanedSubtasks_DeadAgent` — subtask IN_PROGRESS but agent not in runner. If agent has commits on branch, verify work is salvaged (merged). If no commits, verify subtask reset to BACKLOG.
- `TestReconcileOrphanedSubtasks_WorkAlreadyMerged` — agent's commits already in integration branch. Subtask should transition to DONE.

### 4. reconcileEmptyFeatures

This finds TESTING_READY tasks with empty feature branches (no commits ahead of default branch).

- `TestReconcileEmptyFeatures_NonEmpty` — feature branch has commits. Task stays TESTING_READY. Returns 0.
- `TestReconcileEmptyFeatures_Empty` — feature branch has no commits ahead of default. Task transitions to FAILED. Returns 1.

### 5. reconcileOrphanWorktrees

This finds agent worktree directories under the feature path that have no corresponding active agent.

- `TestReconcileOrphanWorktrees_AllActive` — all worktree dirs correspond to active agents. Returns 0.
- `TestReconcileOrphanWorktrees_StaleDir` — worktree dir exists but no agent record. If dir has no commits, it should be removed. Returns 1.

### 6. reconcileStuckAgents

This finds IN_PROGRESS subtasks whose agent DB status is "working" but the tmux session is dead.

- `TestReconcileStuckAgents_SessionAlive` — agent tmux session exists. No action. Returns 0.
- `TestReconcileStuckAgents_SessionDead_WithCommits` — session dead but agent branch has commits. Salvage work. Returns 1.
- `TestReconcileStuckAgents_SessionDead_NoCommits` — session dead, no commits. Reset subtask. Returns 1.

### 7. recoverStuckAgents

This checks for agents with an idle signal file but no completion event.

- `TestRecoverStuckAgents_NoSignalFile` — no idle file exists. No action.
- `TestRecoverStuckAgents_WithSignalFile` — create idle signal file for a WORKING agent, verify completion is synthesized.

### Test Helper

Create a helper for reconcile tests:

```go
func setupReconcileTest(t *testing.T) (*Orchestrator, *gorm.DB, string) {
    t.Helper()
    db := testutil.NewTestDB(t)
    bareRepo := testutil.SetupBareRepo(t)
    // Create project
    project := model.Project{ID: uuid.New(), Name: "test", BareRepoPath: bareRepo, DefaultBranch: "master"}
    db.Create(&project)
    // Create orchestrator with real worktree manager
    wt := worktree.NewManager(bareRepo, "master")
    orch := testOrchestrator(t, db, wt)
    orch.projectID = project.ID
    return orch, db, bareRepo
}
```

Adapt as needed based on what `testOrchestrator` accepts in the existing test code.

## Conventions

- Package: `orchestrator` (same package for private method access)
- Use `testutil.NewTestDB(t)` for isolated databases
- Use `testutil.SetupBareRepo(t)`, `AddWorktree`, `CommitFile` for git state
- Use real `worktree.NewManager` — these tests verify git-aware logic
- Build verification: `go test ./internal/orchestrator/ -cover -run TestReconcile`
