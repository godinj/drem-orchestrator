# Agent: Orchestrator Completion Handler Tests

You are working on the `master` branch of drem-orchestrator, a Go-based agent orchestration system using GORM+SQLite, tmux, and git worktrees.
Your task is writing tests for the agent completion handler methods in `internal/orchestrator/orchestrator.go` that currently have 0% coverage.

## Context

Read these before starting:
- `CLAUDE.md` (project conventions, build/test commands)
- `internal/orchestrator/orchestrator.go` (lines 1042-1850 — all completion handler methods)
- `internal/orchestrator/lifecycle_test.go` (existing test patterns: `setupLifecycleTest`, `createLifecycleTask`, `makePlan`)
- `internal/orchestrator/orchestrator_test.go` (existing patterns: `testOrchestrator`, `initBareRepo`, `createFeatureWorktree`, `createAgentBranch`)
- `internal/orchestrator/failure_recovery_test.go` (existing failure handling test patterns)
- `internal/model/models.go` (Task, Agent structs and fields)
- `internal/model/enums.go` (TaskStatus, AgentType, AgentStatus enums)
- `internal/agent/runner.go` (Completion struct at line 33, Runner struct, DrainCompletions)
- `internal/agent/session.go` (SessionManager interface)
- `internal/agent/runner_mock_test.go` (mockSessionManager pattern for reference)
- `internal/testutil/testutil.go` (NewTestDB, SetupBareRepo, AddWorktree, CommitFile helpers)
- `internal/state/machine.go` (ValidTransitions map, TransitionTask function)

## Deliverables

### New file: `internal/orchestrator/completion_test.go`

Write table-driven tests in the `orchestrator` package (white-box, since target methods are unexported).

#### 1. TestProcessAgentResult_Routing

Tests the `processAgentResult(comp agent.Completion)` dispatcher (line 1042). Verify it:
- Looks up the Agent by ID from DB
- Looks up the associated Task via Agent.CurrentTaskID
- Routes to `onAgentCompleted` when agent ReturnCode == 0
- Routes to `onAgentFailed` when agent ReturnCode != 0
- Handles missing agent gracefully (agent not found in DB)
- Handles missing task gracefully (CurrentTaskID is nil or task deleted)
- Skips processing if task is in terminal state (DONE, FAILED, REJECTED)

Setup pattern: Create an Orchestrator via the existing `testOrchestrator` or `setupLifecycleTest` helper. Insert Agent and Task records directly into the DB. Call `processAgentResult` with a `agent.Completion{AgentID: ..., ReturnCode: ...}`.

#### 2. TestOnPlannerCompleted

Tests `onPlannerCompleted(ag *model.Agent, task *model.Task)` (line 1275). Verify it:
- Transitions task from PLANNING to PLAN_REVIEW on success
- Captures agent output and stores plan in task.Plan field
- Handles empty/invalid plan output (agent produced no parseable plan)
- Increments retry count and re-enters PLANNING if plan parse fails and retries remain (MaxPlannerRetries = 3)
- Fails task if max planner retries exhausted

Setup: Create task in PLANNING status with AssignedAgentID set. Create Agent record of type PLANNER with WorktreePath pointing to a real worktree (use `initBareRepo` + `createFeatureWorktree`). You'll need to mock or stub the agent output — check how `GetAgentOutput` resolves output (it reads from tmux pane or DB). If the orchestrator calls `runner.GetAgentOutput()`, you may need to set up the Runner's mock SessionManager to return canned output, or write the output to the agent's worktree path where the orchestrator reads it.

#### 3. TestOnAgentCompleted_Coder

Tests `onAgentCompleted(ag *model.Agent, task *model.Task)` (line 1065) for CODER agents. Verify it:
- Merges agent branch into feature worktree (calls merger.MergeAgentIntoFeature)
- On successful merge: marks agent DEAD, transitions subtask
- Calls `checkFeatureCompletion` to see if parent should advance
- Handles merge failure gracefully (task not failed, error logged)

Setup: Use `initBareRepo` to create a bare repo, create feature and agent worktrees, commit files to agent branch. Create parent task (IN_PROGRESS) and subtask (IN_PROGRESS) with agent assigned. The orchestrator's `merger` needs to be a real `merge.Orchestrator` pointing at the same repo, or you need to verify the merge path is exercised.

#### 4. TestOnAgentCompleted_EmptyWork

Tests `onAgentEmptyWork(ag *model.Agent, task *model.Task, agentOutput string)` (line 1847). Verify it:
- Detects when agent committed nothing (zero-diff)
- Retries up to MaxEmptyWorkRetries (2)
- Fails task after max retries exhausted
- If supervisor is configured (non-nil), calls supervisor for diagnosis

Setup: Create agent branch identical to feature (no new commits). Set up task with retry context.

#### 5. TestOnReviewerCompleted

Tests `onReviewerCompleted(ag *model.Agent, task *model.Task)` (line 1383). Verify it:
- Marks agent as DEAD
- Appropriate state handling for the parent task after review

#### 6. TestOnFixerCompleted

Tests `onFixerCompleted(ag *model.Agent, task *model.Task)` (line 1425). Verify it:
- On success: merges fixer work back, re-runs test gate or advances task
- On context exhaustion: escalates to human review
- Marks agent as DEAD

#### 7. TestOnAgentFailed

Tests `onAgentFailed(ag *model.Agent, task *model.Task)` (line 1649). Verify it:
- Marks agent as DEAD
- Increments task retry count via `incrementRetryCount`
- Retries if under retry limit (transitions back to schedulable state)
- Fails task if retry limit exceeded
- If supervisor configured: calls `FailureDiagnosisPrompt` for analysis
- Creates TaskEvent recording the failure

Setup: Create task in IN_PROGRESS, agent in WORKING. Test both retry and exhaustion paths.

## Test Infrastructure Notes

- Use `testutil.NewTestDB(t)` for isolated in-memory SQLite
- Use `testutil.SetupBareRepo(t)` + `testutil.AddWorktree` for real git operations
- Use `testutil.CommitFile(t, wt, filename, content, message)` to create commits on branches
- The existing `testOrchestrator(t, db, wtManager)` helper creates a minimal orchestrator — check if it provides a working Runner with mock SessionManager. If not, you may need to extend it or create a similar helper.
- For methods that call `runner.GetAgentOutput()`, you'll need the Runner's SessionManager to be a mock that returns canned pane output. Look at `internal/agent/runner_mock_test.go` for the `mockSessionManager` pattern — you may need to create a similar mock in the orchestrator test file, or construct the Runner with a mock.
- Insert model records directly via `db.Create(&model.Task{...})` and `db.Create(&model.Agent{...})`
- Assert state transitions by re-querying the DB: `db.First(&task, taskID)` and checking `task.Status`
- Assert TaskEvent creation: `db.Where("task_id = ?", taskID).Find(&events)` and check event types

## Conventions

- Package: `orchestrator` (same package, white-box tests)
- Table-driven tests with `t.Run` subtests
- Error wrapping with `fmt.Errorf("context: %w", err)`
- `gofmt` formatting
- Build verification: `go test ./internal/orchestrator/ -run TestProcessAgentResult -v && go test ./internal/orchestrator/ -run TestOnPlannerCompleted -v && go test ./internal/orchestrator/ -run TestOnAgentCompleted -v && go test ./internal/orchestrator/ -run TestOnAgentFailed -v`
- Final verification: `go test ./...` (all tests must pass)
