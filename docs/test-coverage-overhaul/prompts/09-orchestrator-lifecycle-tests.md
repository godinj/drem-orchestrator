# Agent: Orchestrator Lifecycle State Transition Tests

You are working on the `master` branch of drem-orchestrator, a Go TUI tool that orchestrates Claude Code agents via tmux.
Your task is to add end-to-end lifecycle tests for the orchestrator's state transitions, raising coverage from 38.2% toward ~50%.

## Context

Read these before starting:
- `CLAUDE.md` (build commands, conventions)
- `docs/test-coverage-overhaul/prd-test-coverage.md` (Phase 3b section)
- `internal/orchestrator/orchestrator.go` (source — ~4,567 LOC, the main orchestrator)
- `internal/orchestrator/orchestrator_test.go` (existing tests and helpers)
- `internal/orchestrator/failure_recovery_test.go` (existing test patterns with newTestOrch)
- `internal/state/machine.go` (state machine — valid transitions)
- `internal/model/models.go` (Task, Agent, Project models)
- `internal/model/enums.go` (TaskStatus, AgentType enums)

**Already tested (do NOT duplicate):**
- Test-writing phase workflows (~1,563 LOC of tests)
- Test gate checking (595 LOC)
- Test review flows (514 LOC)
- Failure recovery / supervisor actions (938 LOC)
- Plan parsing (300 LOC)
- Plan validation (595 LOC)
- Scheduler (420 LOC)
- Basic task CRUD: CreateTask, AddComment, DeleteComment, PauseTask, ResumeTask, RetryTask

**NOT tested — target these:**
- Plan rejection flow (`HandlePlanRejected` → re-planning)
- `DeletePlanStep` — plan JSON manipulation
- `SpawnReviewerSession` — reviewer agent spawning
- `SpawnFixerSession` — fixer agent spawning
- Full lifecycle transitions that aren't covered by the focused phase tests

## Dependencies

This agent depends on:
- Agent 01 (testutil) for `testutil.NewTestDB()` and git helpers
- Agent 06 (agent-session-interface) for `SessionManager` interface in agent package

Read the existing test infrastructure in `orchestrator_test.go` and `failure_recovery_test.go` carefully. They have helper functions like `testOrchestrator()`, `newTestOrch()`, and `createProject()` that you should reuse or extend.

## Deliverables

### New files

#### 1. `internal/orchestrator/lifecycle_test.go` (~300–400 LOC)

Test orchestrator public API methods and state transitions that aren't covered elsewhere.

**Test helper (reuse existing or create):**

```go
// setupLifecycleTest creates an orchestrator with mock dependencies suitable
// for testing state transitions without real tmux/git.
func setupLifecycleTest(t *testing.T) (*Orchestrator, *gorm.DB)
```

Read the existing `testOrchestrator()` and `newTestOrch()` helpers. Use the same pattern — if the orchestrator now accepts a `SessionManager` interface (from Agent 06), pass a mock. If not, pass a real tmux.Manager wrapped in a skip guard.

**Test functions:**

```go
func TestHandlePlanRejected(t *testing.T)
```
- Create task in PLAN_REVIEW status with a plan
- Call `HandlePlanRejected(taskID)` with rejection feedback
- Verify task transitions back to PLANNING status
- Verify the rejection feedback is stored (as comment or in task fields)
- Verify the old plan is preserved or cleared (check actual behavior)

```go
func TestHandlePlanRejected_WrongStatus(t *testing.T)
```
- Create task in IN_PROGRESS status (not PLAN_REVIEW)
- Call `HandlePlanRejected` → verify error (invalid state transition)

```go
func TestDeletePlanStep(t *testing.T)
```
- Create task with a 3-step plan stored as JSON
- Call `DeletePlanStep(taskID, 1)` → verify step at index 1 is removed
- Verify remaining steps are still intact (indices shifted)
- Verify task is saved to DB with updated plan

```go
func TestDeletePlanStep_OutOfBounds(t *testing.T)
```
- Create task with 2-step plan
- Call `DeletePlanStep(taskID, 5)` → verify error (index out of range)

```go
func TestDeletePlanStep_NoPlan(t *testing.T)
```
- Create task with no plan (nil or empty)
- Call `DeletePlanStep` → verify error

```go
func TestSpawnReviewerSession(t *testing.T)
```
- Create task in appropriate status with completed subtasks
- Call `SpawnReviewerSession(taskID)` → verify reviewer agent created
- Verify agent record in DB with correct type (AgentTypeReviewer or similar)
- Note: may need mock SessionManager or real tmux with skip guard

```go
func TestSpawnFixerSession(t *testing.T)
```
- Create task with a failure/error state
- Call `SpawnFixerSession(taskID)` → verify fixer agent created
- Verify agent record in DB with correct type

```go
func TestPauseResumeLifecycle(t *testing.T)
```
- Create task in IN_PROGRESS status
- PauseTask → verify PAUSED status
- ResumeTask → verify back to IN_PROGRESS
- PauseTask on already-paused → verify idempotent or error (check behavior)

```go
func TestTaskLifecycle_BacklogToPlanning(t *testing.T)
```
- Create task in BACKLOG status
- Read `processBacklog()` to understand what triggers the transition
- If it's triggered by the tick loop, call the relevant public method
- Verify task transitions to PLANNING and a planner agent is spawned

```go
func TestTaskLifecycle_PlanApprovalToTDD(t *testing.T)
```
- Create task in PLAN_REVIEW with a valid plan
- Call `HandlePlanApproved(taskID)` → verify transition to TEST_WRITING (TDD flow)
- Verify the task's TDD phase fields are set correctly

```go
func TestTaskLifecycle_TestReviewApproval(t *testing.T)
```
- Create task in TEST_REVIEW status with test files
- Call `HandleTestReviewApproved(taskID)` → verify transition to IN_PROGRESS
- Verify subtask creation if the plan has multiple steps

```go
func TestTaskLifecycle_TestReviewRejection(t *testing.T)
```
- Create task in TEST_REVIEW status
- Call `HandleTestReviewRejected(taskID)` with feedback → verify transition back to TEST_WRITING
- Verify feedback stored

## Scope Limitation

- Only create/modify files in `internal/orchestrator/`
- Do NOT modify `orchestrator.go` — test the existing API
- Reuse existing test helpers (`testOrchestrator`, `newTestOrch`, `createProject`) where possible
- If a method requires agent spawning and you don't have a mock, use a skip guard: `if testing.Short() { t.Skip("requires tmux") }`
- Focus on testing the PUBLIC API methods, not private functions

## Verification

```bash
go test ./internal/orchestrator/ -v -cover -timeout 120s
```

All existing and new tests must pass. Coverage should increase from 38.2% toward ~50%.

## Conventions

- `gofmt` for formatting
- Table-driven tests where applicable
- `t.Helper()` on test helpers
- Follow patterns from existing orchestrator test files
- Error wrapping with `fmt.Errorf("context: %w", err)`
