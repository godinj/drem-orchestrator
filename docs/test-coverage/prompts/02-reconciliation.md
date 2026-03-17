# Agent: Orchestrator Reconciliation Tests

You are working on the `master` branch of drem-orchestrator, a Go-based agent orchestration system using GORM+SQLite, tmux, and git worktrees.
Your task is writing tests for the reconciliation methods in `internal/orchestrator/orchestrator.go` that currently have 0% coverage.

## Context

Read these before starting:
- `CLAUDE.md` (project conventions, build/test commands)
- `internal/orchestrator/orchestrator.go` (lines 321-869 — Reconcile, ReconcileResult, and all reconcile* methods, plus recoverStuckAgents at line 870)
- `internal/orchestrator/orchestrator_test.go` (existing patterns: `testOrchestrator`, `initBareRepo`, `createFeatureWorktree`, `createAgentBranch`)
- `internal/orchestrator/lifecycle_test.go` (existing patterns: `setupLifecycleTest`, `createLifecycleTask`, `makePlan`)
- `internal/orchestrator/failure_recovery_test.go` (existing failure handling test patterns — may already have some reconciliation tests; check before writing duplicates)
- `internal/model/models.go` (Task, Agent structs)
- `internal/model/enums.go` (TaskStatus, AgentType, AgentStatus enums)
- `internal/agent/runner.go` (Runner struct, StopAgent, CleanupStaleAgents)
- `internal/agent/session.go` (SessionManager interface)
- `internal/testutil/testutil.go` (NewTestDB, SetupBareRepo, AddWorktree helpers)

## Deliverables

### New file: `internal/orchestrator/reconciliation_test.go`

Write table-driven tests in the `orchestrator` package (white-box tests).

#### 1. TestReconcileStaleSubtasks

Tests `reconcileStaleSubtasks()` (line 383). This method finds IN_PROGRESS subtasks whose assigned agent has a stale heartbeat (older than `o.stale` duration). Verify it:
- Finds subtasks in IN_PROGRESS with agents whose HeartbeatAt is older than stale timeout
- Transitions those subtasks to FAILED with appropriate reason
- Marks the stale agents as DEAD
- Returns count of fixed subtasks
- Does NOT touch subtasks with recent heartbeats
- Does NOT touch subtasks without assigned agents
- Does NOT touch subtasks in other states (DONE, FAILED, BACKLOG)

Setup: Create parent task (IN_PROGRESS) with multiple subtasks. Create Agent records with varying HeartbeatAt times — some stale (e.g., `time.Now().Add(-2 * time.Hour)`), some fresh. Set orchestrator's `stale` field to a known duration (e.g., 30 minutes).

#### 2. TestReconcileOrphanedSubtasks

Tests `reconcileOrphanedSubtasks()` (line 493). This finds subtasks whose parent task no longer exists or is in a terminal state. Verify it:
- Finds IN_PROGRESS/PLANNING subtasks whose parent is DONE/FAILED/REJECTED
- Transitions orphaned subtasks to FAILED
- Returns count of fixed subtasks
- Does NOT touch subtasks whose parent is still active (IN_PROGRESS, TESTING_READY, etc.)
- Handles subtasks with nil ParentTaskID correctly

Setup: Create parent tasks in various states (DONE, FAILED, IN_PROGRESS). Create subtasks under each. Run reconciler. Verify only subtasks under terminal parents are failed.

#### 3. TestReconcileEmptyFeatures

Tests `reconcileEmptyFeatures()` (line 677). This finds feature branches (tasks with WorktreeBranch set) that have no remaining active subtasks. Verify it:
- Finds parent tasks with WorktreeBranch set but zero active subtasks
- Transitions empty features to FAILED (or appropriate terminal state)
- Returns count of fixed features
- Does NOT touch features that still have active subtasks
- Does NOT touch features that are already in terminal state

Setup: Create parent tasks with WorktreeBranch set. Under some, create subtasks all in DONE/FAILED. Under others, create at least one active subtask. Run reconciler.

#### 4. TestReconcileOrphanWorktrees

Tests `reconcileOrphanWorktrees()` (line 723). This finds git worktrees on disk that don't correspond to any active task or agent. Verify it:
- Detects worktrees on disk not tracked by any task/agent in DB
- Removes orphaned worktrees
- Returns count of removed worktrees
- Does NOT remove worktrees that are actively in use
- Does NOT remove the main worktree

Setup: Use `testutil.SetupBareRepo` to create a bare repo. Use `testutil.AddWorktree` to create several worktrees. Register some in the DB as task WorktreePaths or agent WorktreePaths. Leave others unregistered. The orchestrator's worktree.Manager must point to the same bare repo.

#### 5. TestReconcileStuckAgents

Tests `reconcileStuckAgents()` (line 783). This finds agents marked WORKING in the DB whose tmux session is no longer alive. Verify it:
- Queries agents with status WORKING
- Checks each agent's tmux session via SessionManager
- Marks agents with dead sessions as DEAD
- Returns count of fixed agents
- Does NOT touch agents with live sessions
- Does NOT touch agents in IDLE or DEAD status

Setup: Create Agent records in WORKING status with TmuxSession names. Set up the Runner's SessionManager mock so that `IsAgentSessionAlive` returns false for some sessions and true for others.

#### 6. TestRecoverStuckAgents

Tests `recoverStuckAgents()` (line 870). This is called during doTick to detect agents stuck in WORKING with an idle signal file present. Verify it:
- Detects agents whose idle signal file exists (meaning they finished but weren't reaped)
- Sends completion for those agents
- Does NOT affect agents still actively working (no idle signal)

Setup: Create running agents. Write idle signal files for some agent worktree paths. Run recoverStuckAgents. Check completions channel.

#### 7. TestReconcile_FullAudit

Tests the top-level `Reconcile()` (line 339) method. Verify it:
- Calls all sub-reconcilers
- Returns a ReconcileResult with counts from each sub-reconciler
- Returns total count of all fixes applied
- Handles errors from individual sub-reconcilers gracefully (continues to next)

Setup: Set up a mix of stale subtasks, orphaned subtasks, empty features, and stuck agents. Run Reconcile() once. Verify the returned ReconcileResult has correct counts for each category.

#### 8. TestReconcile_PeriodicExecution

Tests that `Reconcile()` is invoked on the correct tick interval (every `reconcileInterval = 10` ticks). This tests the logic in `doTick()` (line ~178) that calls Reconcile periodically.

Setup: Create orchestrator with known tick count. Advance tickCount manually and call doTick multiple times. Verify Reconcile is called at the right interval. Alternatively, verify the tickCount modulo check.

## Test Infrastructure Notes

- Use `testutil.NewTestDB(t)` for isolated in-memory SQLite
- Use `testutil.SetupBareRepo(t)` for real git repos (needed for worktree reconciliation)
- Insert model records directly via `db.Create()`
- For stuck agent tests, you need the Runner's SessionManager to be a mock — create a `mockSessionManager` in this test file (or use an existing one if available in the orchestrator test files) that implements the `agent.SessionManager` interface with configurable `IsAgentSessionAlive` responses
- Set heartbeat times using `time.Now().Add(-duration)` for stale detection
- Assert by re-querying DB records and checking Status fields
- Assert TaskEvent records are created for state transitions
- The reconcileInterval constant (10) is defined at line 50 — reference it directly

## Conventions

- Package: `orchestrator` (same package, white-box tests)
- Table-driven tests with `t.Run` subtests
- Error wrapping with `fmt.Errorf("context: %w", err)`
- `gofmt` formatting
- Build verification: `go test ./internal/orchestrator/ -run TestReconcile -v`
- Final verification: `go test ./...` (all tests must pass)
