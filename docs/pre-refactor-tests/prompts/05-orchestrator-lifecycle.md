# Agent: Orchestrator Core Lifecycle Test Coverage

You are working on the `master` branch of Drem Orchestrator, a Go multi-agent development orchestrator.
Your task is pre-refactor test hardening: add tests for the orchestrator's core agent completion and merge flows.

## Context

Read these before starting:
- `internal/orchestrator/orchestrator.go` (full file — focus on processAgentResult, onAgentCompleted, onPlannerCompleted, onAgentFailed, executeMerge, onAgentEmptyWork, onReviewerCompleted, onFixerCompleted)
- `internal/orchestrator/orchestrator_test.go` (existing test helpers: testOrchestrator, initBareRepo, createFeatureWorktree, createAgentBranch)
- `internal/orchestrator/lifecycle_test.go` (existing lifecycle test patterns: setupLifecycleTest, lifecycleTestDB, createLifecycleTask, makePlan)
- `internal/orchestrator/failure_recovery_test.go` (existing failure tests — study patterns)
- `internal/agent/runner.go` (Runner struct and Completion type — understand the interface)
- `internal/agent/runner_mock_test.go` (mockSessionManager pattern for testing without tmux)
- `internal/model/models.go` (Task, Agent, TaskEvent models)
- `internal/model/enums.go` (TaskStatus and AgentType constants)
- `internal/state/state.go` (Transition function for state machine validation)
- `internal/merge/merge.go` (merge.Orchestrator interface — MergeFeatureIntoMain)
- `internal/testutil/testutil.go` (NewTestDB, SetupBareRepo, AddWorktree, CommitFile)

## Important Design Notes

The Orchestrator struct requires these dependencies:
- `db *gorm.DB` — use testutil.NewTestDB
- `runner *agent.Runner` — for lifecycle tests that don't spawn real agents, set to nil and test the DB/state logic only
- `worktree *worktree.Manager` — use real worktree.Manager with test bare repo
- `merger *merge.Orchestrator` — can be nil for tests not exercising merge
- `memory *memory.Manager` — can be nil
- `supervisor *supervisor.Supervisor` — nil disables LLM calls

The `agent.Completion` struct is: `{AgentID uuid.UUID, ExitCode int}`.

Many functions like `processAgentResult`, `onAgentCompleted`, `onAgentFailed` are private methods. Test them through the public surface or by calling them via a test helper that accesses the orchestrator.

Since these are private methods, you'll need to write tests in `package orchestrator` (not `orchestrator_test`). Add tests to a new file `internal/orchestrator/agent_result_test.go`.

## Deliverables

### New file: `internal/orchestrator/agent_result_test.go`

### 1. processAgentResult routing

Test that processAgentResult correctly routes based on exit code:

- `TestProcessAgentResult_SuccessRouting` — create task + agent (status=working) in DB, call processAgentResult with ExitCode=0. Verify agent status is updated. Use a real bare repo with a worktree so the agent has a valid branch.
- `TestProcessAgentResult_FailureRouting` — same setup, ExitCode=1. Verify the failure path is taken (task gets error context or retries).
- `TestProcessAgentResult_UnknownAgent` — call with an AgentID not in DB. Verify graceful error (no panic).

### 2. onPlannerCompleted

This reads `plan.json` from the agent's worktree. Set up:
1. Create bare repo with `testutil.SetupBareRepo`
2. Add worktree, write a valid `plan.json` file, commit it
3. Create DB records (project, task in `planning` status, agent with `agent_type=planner`)

Tests:
- `TestOnPlannerCompleted_ValidPlan` — write valid plan.json with 2 subtasks, call the function, verify:
  - Task status transitions to `plan_review`
  - Task.Plan field is populated in DB
  - A TaskEvent with type "status_changed" is created
- `TestOnPlannerCompleted_InvalidPlan` — write plan.json with invalid JSON, verify task fails or retries
- `TestOnPlannerCompleted_MissingPlan` — no plan.json in worktree, verify appropriate error handling

Valid plan.json format:
```json
{
  "subtasks": [
    {"title": "Sub A", "description": "Do A", "agent_type": "coder", "estimated_files": ["a.go"]},
    {"title": "Sub B", "description": "Do B", "agent_type": "coder", "estimated_files": ["b.go"]}
  ]
}
```

### 3. onAgentFailed

- `TestOnAgentFailed_FirstFailure` — agent fails on first attempt (no retries yet), verify task context gets error info
- `TestOnAgentFailed_MaxRetries` — set retry_count in task.Context to max, verify task transitions to `failed`
- `TestOnAgentFailed_WithSupervisorNil` — supervisor is nil, verify fallback behavior (no LLM diagnosis)

### 4. onReviewerCompleted and onFixerCompleted

- `TestOnReviewerCompleted` — create reviewer agent + task, write `review.json` to worktree, call function, verify review stored in task context
- `TestOnFixerCompleted` — create fixer agent + task, call function, verify agent status set to idle

### 5. executeMerge

- `TestExecuteMerge_Success` — set up bare repo with feature branch that has commits ahead of main. Create merger (use real merge.Orchestrator with the test repo). Call executeMerge, verify task transitions to `done`.
- `TestExecuteMerge_NoMerger` — merger is nil, verify error handling

### 6. Helper utilities

- `TestIncrementRetryCount` — call incrementRetryCount on task with no context, verify returns 1. Call again, verify returns 2.
- `TestExtractTestFiles` — create a worktree with test files changed vs base, verify the function returns them
- `TestTaskFeatureName` — verify slugification: task with title "Add Auth Module" produces feature name containing "add-auth-module"

## Conventions

- Package: `orchestrator` (same package, not `_test` — needed to access private methods)
- Use `testutil.NewTestDB(t)` for isolated databases
- Use `testutil.SetupBareRepo(t)` for git operations
- Create Project record first for FK constraints
- Use `t.Helper()` in all helper functions
- Build verification: `go test ./internal/orchestrator/ -cover -run TestProcessAgentResult -run TestOnPlanner -run TestOnAgent -run TestOnReviewer -run TestOnFixer -run TestExecuteMerge -run TestIncrement -run TestExtract -run TestTaskFeature`
