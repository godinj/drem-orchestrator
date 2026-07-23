# Agent: Orchestrator Tick Loop & Lifecycle Integration Tests

You are working on the `master` branch of drem-orchestrator, a Go-based agent orchestration system using GORM+SQLite, typed worker attempts, container workers, and legacy host runners.
Your task is writing integration tests for `doTick()` and an end-to-end task lifecycle test that exercises the full BACKLOG-to-DONE state machine.

## Context

Read these before starting:
- `CLAUDE.md` (project conventions, build/test commands)
- `internal/orchestrator/orchestrator.go`:
  - `Run(ctx)` (line 162) — main blocking loop
  - `doTick(ctx)` (line 178) — single orchestrator iteration, processes all task states
  - `processBacklog` (line 905) — BACKLOG → PLANNING
  - `processPlanning` (line 937) — spawn planner or advance
  - `scheduleSubtasks` (line 1952) — subtask scheduling within groups
  - `processTestWriting` (line 2198) — TEST_WRITING management
  - `checkFeatureCompletion` (line 2310) — check if parent should advance
  - `processTestingReady` (line 4052) — automated test gate
  - `executeMerge` (line 2393) — MERGING state
  - `handlePaused` (line 2508) — PAUSED cleanup
  - `reconcileWorkerAttemptLifecycles` — consumes typed terminal spawner observations
  - `Reconcile` (line 339) — periodic consistency audit
  - `reconcileInterval = 10` (line 50) — ticks between audits
- `internal/orchestrator/orchestrator_test.go` (existing patterns: `testOrchestrator`, `initBareRepo`)
- `internal/orchestrator/lifecycle_test.go` (existing patterns: `setupLifecycleTest`, `createLifecycleTask`, `makePlan`)
- `internal/orchestrator/scheduler.go` (BuildSchedule, SubtaskGroup, Schedule)
- `internal/agent/runner.go` (Runner, Completion, DrainCompletions, CanSpawn, SpawnAgent)
- `internal/agent/session.go` (SessionManager interface)
- `internal/model/models.go` (Task, Agent structs)
- `internal/model/enums.go` (all status enums)
- `internal/state/machine.go` (ValidTransitions map)
- `internal/testutil/testutil.go` (NewTestDB, SetupBareRepo, AddWorktree, CommitFile)

Task success or failure must not be inferred from worker absence, idle files,
heartbeats, Git topology, or agentmon logs. Lifecycle tests should drive typed
`WorkerAttempt` terminal observations; periodic reconciliation is recovery and
resource cleanup only.

## Dependencies

This agent's tests build on patterns from Agents 01-04. If those tests exist, read them for reusable helpers and mock patterns. If they don't exist yet, create your own test infrastructure.

## Deliverables

### New file: `internal/orchestrator/tick_integration_test.go`

Write integration tests in the `orchestrator` package (white-box tests).

#### 1. TestDoTick_BacklogToPlanning

Tests that `doTick()` processes BACKLOG tasks. Verify it:
- Queries all tasks in BACKLOG status
- Transitions each to PLANNING
- Creates TaskEvent for each transition

Setup: Create 2-3 tasks in BACKLOG. Create orchestrator with mock Runner (CanSpawn returns true). Call `doTick(ctx)` once. Verify all tasks are now PLANNING.

#### 2. TestDoTick_PlanningSpawnsPlanner

Tests that `doTick()` spawns planner agents for PLANNING tasks. Verify it:
- For PLANNING tasks without assigned agents: attempts to spawn a planner
- Respects Runner capacity (CanSpawn check)
- When at capacity: skips spawning, task stays PLANNING

Setup: Create tasks in PLANNING with no AssignedAgentID. Mock Runner to track spawn calls. Call doTick. Verify spawn was attempted (or skipped if at capacity).

#### 3. TestDoTick_DrainCompletions

Tests that `doTick()` drains the agent completion channel. Verify it:
- Calls `runner.DrainCompletions()` at start of tick
- Processes each completion via `processAgentResult`
- Handles multiple completions in a single tick

Setup: Create agents and tasks in appropriate states. Pre-load the Runner's completions channel with Completion structs (you may need to access the channel directly or mock DrainCompletions). Call doTick. Verify the completions were processed (agents marked DEAD, tasks transitioned).

#### 4. TestDoTick_InProgressScheduling

Tests that `doTick()` schedules subtasks for IN_PROGRESS parent tasks. Verify it:
- For IN_PROGRESS parents: calls `scheduleSubtasks`
- Spawns agents for ready subtasks (those in the current group with BACKLOG status)
- Does not spawn beyond Runner capacity

Setup: Create parent task in IN_PROGRESS with a plan containing 3 subtasks in a single group. All subtasks in BACKLOG. Mock Runner to track spawn calls. Call doTick. Verify spawn was called for subtasks.

#### 5. TestDoTick_ReconcileInterval

Tests that `doTick()` calls Reconcile every `reconcileInterval` ticks. Verify it:
- On tick 1-9: Reconcile is NOT called
- On tick 10: Reconcile IS called
- On tick 11-19: Reconcile is NOT called
- On tick 20: Reconcile IS called again

Setup: Create orchestrator. Manually set `o.tickCount` to various values. Call doTick repeatedly. Verify reconciliation runs at the correct interval. You can detect this by setting up stale data that only Reconcile would fix, or by checking tickCount after doTick.

#### 6. TestDoTick_MultipleStates

Tests that a single `doTick()` call processes tasks in ALL states concurrently. Verify it:
- Creates tasks in BACKLOG, PLANNING, IN_PROGRESS, TESTING_READY, MERGING, PAUSED
- A single doTick processes all of them appropriately
- State transitions happen correctly for each
- No cross-contamination between task processing

Setup: Create one task in each active state. Configure the orchestrator so that transitions are possible (mock Runner, real merge.Orchestrator for MERGING, etc.). Call doTick once. Verify each task transitioned to the expected next state.

#### 7. TestLifecycle_BacklogToDone

End-to-end lifecycle test. This is the most important test — it exercises the full state machine through a realistic flow. Verify the complete path:

```
BACKLOG → PLANNING → PLAN_REVIEW → IN_PROGRESS → TESTING_READY → MERGING → DONE
```

Steps:
1. Create a root task in BACKLOG
2. Call doTick → task moves to PLANNING
3. Simulate planner completion (insert agent, plan, feed completion into Runner)
4. Call doTick → task moves to PLAN_REVIEW (planner produced a plan)
5. Call HandlePlanApproved → task moves to IN_PROGRESS, subtasks created
6. Call doTick → subtask agents spawned
7. Simulate coder completion (commit files on agent branch, feed completion)
8. Call doTick → subtask merged to feature, check completion
9. Repeat for all subtasks until parent advances to TESTING_READY
10. Call HandleTestPassed → task moves to MERGING
11. Call doTick → executeMerge runs, task moves to DONE
12. Verify final state: task is DONE, all subtasks DONE, agents DEAD, feature merged to main

This requires a real git repo setup with bare repo, feature worktrees, and agent branches. Use `initBareRepo`, `createFeatureWorktree`, and `createAgentBranch` helpers.

**Critical**: This test needs careful orchestration of mock Runner behavior. The Runner needs to:
- Track spawn calls and record agent IDs
- Provide completions when asked (via DrainCompletions)
- Report CanSpawn correctly at each stage
You'll likely need a purpose-built mock or test double for Runner that allows you to control the completion flow.

#### 8. TestLifecycle_FailureAndRetry

Tests the failure-retry lifecycle. Verify:
```
BACKLOG → PLANNING → IN_PROGRESS → FAILED → BACKLOG → PLANNING → ... → DONE
```

Steps:
1. Create task, advance to IN_PROGRESS
2. Simulate agent failure (ReturnCode != 0)
3. doTick processes failure → task moves to FAILED (or retries)
4. Call RetryTask → task returns to BACKLOG
5. Continue through the pipeline again to DONE

## Test Infrastructure Notes

- Use `testutil.NewTestDB(t)` for isolated in-memory SQLite
- Use `testutil.SetupBareRepo(t)` for real git operations
- The key challenge is mocking the Runner. You need a Runner that:
  - Implements CanSpawn, SpawnAgent, DrainCompletions, StopAgent, GetRunningAgents
  - Lets you inject completions
  - Tracks spawn calls
  - Runner is a concrete struct, not an interface, so you'll need to either:
    (a) Construct a real Runner with a mock SessionManager, or
    (b) Create a thin wrapper
  - The mock SessionManager from `agent/runner_mock_test.go` is a good reference
- For lifecycle tests with real git:
  1. `SetupBareRepo(t)` → bare repo path
  2. Create worktree.Manager pointing at bare repo
  3. Create merge.Orchestrator with worktree.Manager
  4. Create Runner with mock SessionManager
  5. Create Orchestrator with all dependencies
  6. Use `CommitFile` to simulate agent work on branches
- For completion injection: the Runner's `completions` channel is unexported. You may need to:
  - Use Runner.SpawnAgent (which starts monitorAgent goroutine) and then kill the tmux session to trigger completion
  - Or construct the Runner and manually interact with it
  - Or create the Orchestrator with a nil Runner and call methods directly (not via doTick)

## Conventions

- Package: `orchestrator` (same package, white-box tests)
- Table-driven tests where applicable, but lifecycle tests are naturally sequential
- Error wrapping with `fmt.Errorf("context: %w", err)`
- `gofmt` formatting
- Build verification: `go test ./internal/orchestrator/ -run TestDoTick -v && go test ./internal/orchestrator/ -run TestLifecycle -v`
- Final verification: `go test ./...` (all tests must pass)
