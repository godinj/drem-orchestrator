# Agent: Context Monitoring & Escalation Tests

You are working on the `master` branch of drem-orchestrator, a Go-based agent orchestration system using GORM+SQLite, tmux, and git worktrees.
Your task is writing tests for context window monitoring and escalation logic in `internal/orchestrator/orchestrator.go`.

## Context

Read these before starting:
- `CLAUDE.md` (project conventions, build/test commands)
- `internal/orchestrator/orchestrator.go`:
  - `checkContextUsage()` (line 3675) — monitors all agents' context window usage
  - `getAgentContextInfos()` (line 3638) — collects context usage for all active agents
  - `handleAgentContextExhausted(subtask, ag, pct)` (line 3911) — handles context exhaustion
  - `escalateFixerToHuman(subtask, ag, pct)` (line 3860) — marks task for human review
  - `spawnFixerForTestFailure(subtask, ag)` (line 3776) — spawns fixer agent
  - `handleTestWritingFailure(subtask, ag)` (line 3930) — handles test writing agent failures
  - `getTaskPhase(task)` (line 3761) — returns TDD phase
  - Orchestrator struct fields: `contextWarnPct`, `contextStopPct`, `contextFixerPct` (line 90)
- `internal/agent/runner.go`:
  - `GetContextUsage(agentID)` (line 640) — returns `*ctxmon.Usage` for running agent
  - `GetRunningAgents()` (line 447) — returns copy of RunningAgent slice
  - `RunningAgent.ContextUsage` field (line 45) — `*ctxmon.Usage`
- `internal/ctxmon/` — context monitoring types (read for Usage struct definition)
- `internal/model/models.go` (Task, Agent structs — especially Agent.Config JSONField for context_used_pct)
- `internal/model/enums.go` (TaskStatus, AgentStatus enums)
- `internal/orchestrator/orchestrator_test.go` (existing patterns)
- `internal/orchestrator/lifecycle_test.go` (existing patterns)
- `internal/testutil/testutil.go` (NewTestDB, SetupBareRepo helpers)

## Dependencies

This agent's tests benefit from patterns established in Agents 01-04. If those test files exist, read them for reusable helpers and mock patterns. Otherwise, create your own infrastructure.

## Deliverables

### New file: `internal/orchestrator/context_escalation_test.go`

Write tests in the `orchestrator` package (white-box tests).

#### 1. TestCheckContextUsage_BelowThreshold

Tests `checkContextUsage()` when all agents are below the warning threshold. Verify it:
- Queries all running agents via Runner
- No action taken when all context percentages are below `contextWarnPct`
- Tasks remain in their current state

Setup: Create orchestrator with `contextWarnPct=80, contextStopPct=95, contextFixerPct=85`. Create running agents with context usage at 50%, 70%. Verify no state changes after calling checkContextUsage.

#### 2. TestCheckContextUsage_WarnThreshold

Tests `checkContextUsage()` when an agent exceeds the warning threshold but is below stop threshold. Verify it:
- Detects agents at or above `contextWarnPct`
- Logs a warning or emits an event (check what the method does at the warn level)
- Does NOT stop the agent or fail the task

Setup: Create agent with context usage at 82% (above 80% warn, below 95% stop).

#### 3. TestCheckContextUsage_StopThreshold

Tests `checkContextUsage()` when an agent exceeds the stop threshold. Verify it:
- Detects agents at or above `contextStopPct`
- Triggers `handleAgentContextExhausted` for the affected task
- The agent should be stopped or the task escalated

Setup: Create agent with context usage at 96% (above 95% stop).

#### 4. TestHandleAgentContextExhausted

Tests `handleAgentContextExhausted(subtask, ag, pct)` (line 3911). Verify it:
- For CODER agents: stops the agent, merges any partial work, schedules a new agent with fresh context
- For FIXER agents at or above `contextFixerPct`: escalates to human via `escalateFixerToHuman`
- For FIXER agents below `contextFixerPct`: spawns a replacement fixer
- Handles the case where no work was committed (empty agent branch)
- Creates appropriate TaskEvents

Setup: Create subtasks with agents at various context levels. Test each agent type path.

#### 5. TestEscalateFixerToHuman

Tests `escalateFixerToHuman(subtask, ag, pct)` (line 3860). Verify it:
- Sets `task.NeedsHumanReview = true` in DB
- Marks the agent as DEAD
- Records the escalation reason (including context percentage)
- Creates a TaskEvent
- Does not transition the task to a different status (stays for human intervention)

Setup: Create subtask in IN_PROGRESS with a FIXER agent. Call escalateFixerToHuman. Verify DB state.

#### 6. TestSpawnFixerForTestFailure

Tests `spawnFixerForTestFailure(subtask, ag)` (line 3776). Verify it:
- Stops the current agent
- Spawns a new FIXER agent for the same task
- The fixer gets the test failure output as context
- Handles spawn failure gracefully (Runner at capacity)

Setup: Create subtask with a CODER agent that failed tests. The agent should have output available. Mock Runner to track spawn calls. Call spawnFixerForTestFailure.

#### 7. TestHandleTestWritingFailure

Tests `handleTestWritingFailure(subtask, ag)` (line 3930). Verify it:
- Handles test writing agent that failed or exhausted context
- Retries if under retry limit
- Fails the subtask if retry limit exceeded
- Marks the agent as DEAD

Setup: Create subtask in TEST_WRITING phase with assigned agent. Test both retry and exhaustion paths.

#### 8. TestGetTaskPhase

Tests `getTaskPhase(task)` (line 3761). Verify it:
- Returns "test" for tasks in test-writing phase
- Returns "implementation" for coder tasks
- Returns correct phase from task.Context field
- Returns empty string for tasks with no phase

Setup: Create tasks with various Context JSONField values containing "phase" key.

#### 9. TestContextThresholdEdgeCases

Tests boundary conditions for context thresholds. Verify:
- Agent at exactly `contextWarnPct` (e.g., 80%) — triggers warning
- Agent at exactly `contextStopPct` (e.g., 95%) — triggers stop
- Agent at exactly `contextFixerPct` (e.g., 85%) — triggers escalation for fixers
- Agent at 0% — no action
- Agent at 100% — triggers stop

Setup: Table-driven test with percentages at each boundary.

## Test Infrastructure Notes

- Use `testutil.NewTestDB(t)` for isolated in-memory SQLite
- For context usage simulation, you need to control what `Runner.GetContextUsage(agentID)` and `Runner.GetRunningAgents()` return. Options:
  - Construct a real Runner with mock SessionManager, then manually inject RunningAgent entries with ContextUsage set
  - The Runner's `running` map is protected by mutex but is a private field — since tests are in the `orchestrator` package (not `agent`), you may need to use Runner's public API to set up state
  - Alternative: if the orchestrator reads context from the DB (Agent.Config field has `context_used_pct`), set that directly
  - Read `checkContextUsage()` carefully to understand WHERE it gets context data from (Runner.GetRunningAgents? Runner.GetContextUsage? DB?)
- For agent spawn/stop verification: mock SessionManager tracking calls
- For escalation tests: verify DB state (NeedsHumanReview, agent Status, TaskEvents)
- The `ctxmon.Usage` struct — read `internal/ctxmon/` to understand its fields (likely includes Pct int or similar)

## Conventions

- Package: `orchestrator` (same package, white-box tests)
- Table-driven tests with `t.Run` subtests
- Error wrapping with `fmt.Errorf("context: %w", err)`
- `gofmt` formatting
- Build verification: `go test ./internal/orchestrator/ -run TestCheckContext -v && go test ./internal/orchestrator/ -run TestHandleAgent -v && go test ./internal/orchestrator/ -run TestEscalateFixer -v`
- Final verification: `go test ./...` (all tests must pass)
