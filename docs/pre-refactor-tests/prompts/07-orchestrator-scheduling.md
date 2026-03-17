# Agent: Orchestrator Scheduling & Context Test Coverage

You are working on the `master` branch of Drem Orchestrator, a Go multi-agent development orchestrator.
Your task is pre-refactor test hardening: add tests for the orchestrator's scheduling, planning, and context monitoring functions.

## Context

Read these before starting:
- `internal/orchestrator/orchestrator.go` (focus on: processPlanning, processBacklog, processTestWriting, scheduleSubtasks, findCurrentGroup, checkFeatureCompletion, checkContextUsage, getAgentContextInfos, handlePaused, doTick)
- `internal/orchestrator/scheduler_test.go` (existing scheduler tests — already 97% coverage, study patterns)
- `internal/orchestrator/test_writing_test.go` (existing test writing tests)
- `internal/orchestrator/lifecycle_test.go` (lifecycleTestDB, createLifecycleTask helpers)
- `internal/orchestrator/orchestrator_test.go` (testOrchestrator helper)
- `internal/agent/runner.go` (Runner, Completion, GetRunningAgents)
- `internal/model/models.go` (Task, Agent models)
- `internal/model/enums.go` (all status and type constants)
- `internal/testutil/testutil.go` (test helpers)

## Important Design Notes

- `processPlanning` spawns planner agents for tasks in PLANNING status. When runner is nil, it should be tested for the state-check logic only.
- `processBacklog` transitions BACKLOG tasks to PLANNING. It checks for existing plans (replanning).
- `scheduleSubtasks` is the core scheduler — already 97% covered. Focus on the uncovered branches.
- `checkFeatureCompletion` checks if all subtasks of an IN_PROGRESS parent are DONE and transitions parent to TESTING_READY.
- `checkContextUsage` monitors agent context window percentages and applies escalation thresholds.
- `handlePaused` stops agents on paused tasks.

Since these are private methods, write tests in `package orchestrator`. Create file `internal/orchestrator/scheduling_test.go`.

## Deliverables

### New file: `internal/orchestrator/scheduling_test.go`

### 1. processPlanning

- `TestProcessPlanning_SpawnsPlannerWhenNoAgent` — task in PLANNING with no assigned agent, runner is nil (skip actual spawn), verify the function attempts to proceed without panic
- `TestProcessPlanning_SkipsWhenAgentAssigned` — task in PLANNING already has AssignedAgentID. Verify no duplicate spawn attempt.
- `TestProcessPlanning_ReplanningWithFeedback` — task in PLANNING with PlanFeedback set (replanning after rejection). Verify feedback is passed through.

### 2. processBacklog

- `TestProcessBacklog_TransitionsToPlanning` — task in BACKLOG with no plan. Verify it transitions to PLANNING and a TaskEvent is created.
- `TestProcessBacklog_DetachForReplanning` — task in BACKLOG that already has a Plan (was rejected and reset). Verify it handles the existing plan correctly.
- `TestProcessBacklog_WithDependencies` — task with DependencyIDs where deps are not all DONE. Verify it remains in BACKLOG (this is tested at the doTick level, but verify the function's guard).

### 3. checkFeatureCompletion

- `TestCheckFeatureCompletion_AllSubtasksDone` — parent IN_PROGRESS with 3 subtasks all DONE. Verify parent transitions to TESTING_READY.
- `TestCheckFeatureCompletion_SomeSubtasksPending` — parent IN_PROGRESS with 2/3 subtasks DONE, 1 still IN_PROGRESS. Verify parent stays IN_PROGRESS.
- `TestCheckFeatureCompletion_NoSubtasks` — parent IN_PROGRESS with no subtasks. Verify it handles gracefully (no transition).
- `TestCheckFeatureCompletion_MixedPhases` — parent with test-phase and impl-phase subtasks. All test done, some impl pending. Verify it correctly checks all subtasks regardless of phase.

### 4. processTestWriting

- `TestProcessTestWriting_AllTestSubtasksDone` — parent in TEST_WRITING with all test-phase subtasks DONE. Verify transition to TEST_REVIEW.
- `TestProcessTestWriting_TestSubtasksPending` — some test subtasks still in progress. Verify parent stays in TEST_WRITING.
- `TestProcessTestWriting_SchedulesTestSubtasks` — verify that test-phase subtasks in BACKLOG get scheduled.

### 5. checkContextUsage

This requires understanding the context monitoring system. Read the function carefully.

- `TestCheckContextUsage_NoRunningAgents` — no agents running, verify no-op (no error, no state changes)
- `TestCheckContextUsage_BelowThreshold` — agents running with context_used_pct below all thresholds, verify no action

For the threshold tests, you'll need to mock the agent context info. The function calls `getAgentContextInfos()` which queries the runner. If the runner is nil, test that it handles gracefully.

### 6. handlePaused

- `TestHandlePaused_StopsAssignedAgent` — task is PAUSED with an AssignedAgentID pointing to an agent with a tmux session. Since runner may be nil in test, verify the function's DB-level behavior.
- `TestHandlePaused_CascadesToSubtasks` — parent task PAUSED with subtasks that have assigned agents. Verify subtask agents are also marked for stopping.
- `TestHandlePaused_NoAgent` — task PAUSED with no assigned agent. Verify no-op.

### 7. doTick integration (lightweight)

- `TestDoTick_ProcessesAllStatuses` — set up tasks in multiple statuses (BACKLOG, PLANNING, IN_PROGRESS, MERGING, PAUSED). Run doTick once with runner=nil and merger=nil. Verify no panics on nil dependencies — the function should gracefully skip operations that need runner/merger.

Note: A full doTick integration test with real agents is out of scope. Focus on verifying the tick processes each status category and doesn't crash on minimal setup.

### 8. findCurrentGroup gaps

Check `scheduler_test.go` to see what's already covered. Add tests for any uncovered branches in the phase grouping logic, such as:
- Mixed subtask phases within a single group
- Empty subtask list
- All subtasks in same phase

## Test Helper

Reuse the existing `setupLifecycleTest` or `testOrchestrator` helpers. If you need a variant:

```go
func setupSchedulingTest(t *testing.T) (*Orchestrator, *gorm.DB, uuid.UUID) {
    t.Helper()
    db := lifecycleTestDB(t) // or testutil.NewTestDB(t)
    project := model.Project{ID: uuid.New(), Name: "test", BareRepoPath: "/tmp/test.git", DefaultBranch: "master"}
    db.Create(&project)
    orch := &Orchestrator{
        db:        db,
        projectID: project.ID,
        logger:    slog.Default(),
        testGate:  DefaultTestGateConfig(),
    }
    return orch, db, project.ID
}
```

## Conventions

- Package: `orchestrator` (same package for private method access)
- Use `testutil.NewTestDB(t)` or `lifecycleTestDB(t)` for databases
- Use `createLifecycleTask` for quick task creation
- Table-driven tests where multiple scenarios test the same function
- Build verification: `go test ./internal/orchestrator/ -cover -run TestProcess -run TestCheckFeature -run TestCheckContext -run TestHandlePaused -run TestDoTick -run TestFindCurrentGroup`
