# Agent: Orchestrator Merge Execution & Pause Handler Tests

You are working on the `master` branch of drem-orchestrator, a Go-based agent orchestration system using GORM+SQLite, tmux, and git worktrees.
Your task is writing tests for `executeMerge()`, `handlePaused()`, `processBacklog()`, and `processPlanning()` in `internal/orchestrator/orchestrator.go`.

## Context

Read these before starting:
- `CLAUDE.md` (project conventions, build/test commands)
- `internal/orchestrator/orchestrator.go`:
  - `processBacklog` (line 905) — BACKLOG → PLANNING transition
  - `processPlanning` (line 937) — PLANNING state: spawn planner or transition to PLAN_REVIEW
  - `executeMerge` (line 2393) — MERGING state: executes git merge to main
  - `handlePaused` (line 2508) — PAUSED state: kills agents, cleans up
- `internal/orchestrator/orchestrator_test.go` (existing patterns: `testOrchestrator`, `initBareRepo`, `createFeatureWorktree`, `createAgentBranch`)
- `internal/orchestrator/lifecycle_test.go` (existing patterns: `setupLifecycleTest`, `createLifecycleTask`, `makePlan`)
- `internal/merge/merge.go` (MergeFeatureIntoMain method at line 329, MergeAllAgentsIntoFeature at line 231)
- `internal/agent/runner.go` (Runner, StopAgent, GetRunningAgents)
- `internal/agent/session.go` (SessionManager interface)
- `internal/model/models.go` (Task, Agent structs)
- `internal/model/enums.go` (TaskStatus, AgentStatus enums)
- `internal/state/machine.go` (ValidTransitions: MERGING → DONE or FAILED)
- `internal/testutil/testutil.go` (NewTestDB, SetupBareRepo, AddWorktree, CommitFile)

## Deliverables

### New file: `internal/orchestrator/merge_pause_test.go`

Write table-driven tests in the `orchestrator` package (white-box tests).

#### 1. TestProcessBacklog

Tests `processBacklog(task *model.Task)` (line 905). Verify it:
- Transitions task from BACKLOG to PLANNING
- Creates a TaskEvent recording the transition
- Does not fail on a well-formed task

Setup: Create task in BACKLOG status via `createLifecycleTask` or direct DB insert.

#### 2. TestProcessPlanning

Tests `processPlanning(task *model.Task)` (line 937). Verify it:
- When task has no assigned agent: spawns a planner agent (or attempts to)
- When task already has an assigned planner agent: does nothing (waits for completion)
- When agent has completed (plan is populated): transitions to PLAN_REVIEW
- Handles spawn failure gracefully (Runner at capacity)

Setup: Create task in PLANNING status. For the "spawn planner" path, you need a Runner that can attempt spawning. The Runner needs a mock SessionManager. If CanSpawn() returns false, the method should skip.

#### 3. TestExecuteMerge_Success

Tests `executeMerge(task *model.Task)` (line 2393) with a clean merge. Verify it:
- Calls merger.MergeFeatureIntoMain (or MergeAllAgentsIntoFeature + merge to main)
- On success: transitions task from MERGING to DONE
- Creates TaskEvent recording the transition
- Cleans up feature worktree after merge

Setup: Use `initBareRepo` to create bare repo. Create feature worktree with committed files. Create parent task in MERGING status with WorktreeBranch set to the feature branch name. Create completed subtasks (DONE) with agents that have WorktreeBranch set. The orchestrator's merger must be a real `merge.Orchestrator` connected to the same repo.

#### 4. TestExecuteMerge_Failure

Tests `executeMerge()` when the merge fails. Verify it:
- On merge failure: transitions task from MERGING to FAILED
- Records failure reason in TaskEvent or task context
- Does not corrupt the feature worktree (state is recoverable)

Setup: Create a conflict scenario — commit conflicting changes on feature and main branches before attempting merge.

#### 5. TestExecuteMerge_BuildFailure

Tests `executeMerge()` when the merge succeeds but build verification fails. Verify it:
- Merge completes but build/test fails
- Task transitions to FAILED with build failure reason
- Merge is rolled back (main branch reset to pre-merge state)

Setup: This requires a project where build verification detects a failure. You may need to create a go.mod + failing test file in the merged worktree, or verify the path where VerifyBuild returns false.

#### 6. TestHandlePaused

Tests `handlePaused(task *model.Task)` (line 2508). Verify it:
- Kills all agents assigned to the task's subtasks
- Stops running agents via Runner.StopAgent
- Marks killed agents as DEAD in DB
- Does not transition the task (it stays PAUSED until resumed)
- Handles case where no agents are running (no-op)

Setup: Create parent task in PAUSED status. Create subtasks with assigned agents in WORKING status. Set up Runner with mock SessionManager that tracks kill calls. Call handlePaused. Verify agents are DEAD in DB and kill was called.

#### 7. TestHandlePaused_NoAgents

Tests `handlePaused()` when task has no active agents. Verify it:
- Completes without error
- No agents are affected

Setup: Create PAUSED task with subtasks but no assigned agents (AssignedAgentID is nil).

#### 8. TestDeleteSubtask

Tests `DeleteSubtask(subtaskID uuid.UUID)` (line 3178). Verify it:
- Removes the subtask from DB
- Stops the agent if one is assigned and running
- Handles non-existent subtask ID gracefully
- Handles subtask with no agent gracefully

Setup: Create parent task with subtasks. Assign agent to one subtask.

## Test Infrastructure Notes

- Use `testutil.NewTestDB(t)` for isolated in-memory SQLite
- Use `testutil.SetupBareRepo(t)` + `testutil.AddWorktree` for real git repos
- Use `testutil.CommitFile` to create commits on branches
- For merge tests, you need a complete git setup:
  1. Create bare repo with initial commit
  2. Create main worktree
  3. Create feature worktree from main
  4. Commit changes on feature branch
  5. Create orchestrator with real merge.Orchestrator and worktree.Manager
- For pause tests, you need a Runner with mock SessionManager:
  - Create a `mockSessionManager` implementing `agent.SessionManager`
  - Track calls to `KillAgentSession` to verify cleanup
  - Construct Runner via the existing helper or directly
- The `testOrchestrator` helper may not set up all dependencies needed for merge — check what it provides and extend if necessary

## Conventions

- Package: `orchestrator` (same package, white-box tests)
- Table-driven tests with `t.Run` subtests
- Error wrapping with `fmt.Errorf("context: %w", err)`
- `gofmt` formatting
- Build verification: `go test ./internal/orchestrator/ -run TestExecuteMerge -v && go test ./internal/orchestrator/ -run TestHandlePaused -v && go test ./internal/orchestrator/ -run TestProcessBacklog -v`
- Final verification: `go test ./...` (all tests must pass)
