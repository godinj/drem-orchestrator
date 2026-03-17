# Agent: Failure Recovery & Automated TESTING_READY Gate

You are working on the `master` branch of Drem Orchestrator, a Go-based multi-agent orchestrator that manages Claude Code agents via tmux in git bare repos with worktrees.
Your task is to implement graduated failure recovery: context-aware fixer escalation for implementation agents, test-writing failure recovery, integration fixer at TESTING_READY, and the automated TESTING_READY gate.

## Context

Read these before starting:
- `docs/tdd-enforcement/prd-tdd-enforcement.md` (sections 4.6.1 through 4.6.4, 4.4.5, Phase 5)
- `internal/orchestrator/orchestrator.go` — read these sections:
  - `checkContextUsage` (search for it) — current context window enforcement
  - `processAgentResult` (around line 955) — agent completion handling
  - `verifyTestsBeforeMerge` (added by Agent 09) — test gate
  - `doTick` step 5 for IN_PROGRESS processing
  - The TESTING_READY handling in doTick and `HandleTestPassed`/`HandleTestFailed`
- `internal/ctxmon/ctxmon.go` (Usage struct, context monitoring types)
- `internal/agent/runner.go` (`SpawnAgent`, `StopAgent`, `RunningAgent` struct)
- `cmd/drem/config.go` (Config struct)
- `internal/model/enums.go` (all status constants)
- `internal/model/models.go` (Task struct with `Phase`, `NeedsHumanReview` fields, Agent struct)

Key facts:
- `checkContextUsage()` currently checks two thresholds: `contextWarnPct` (75%) and `contextStopPct` (90%)
- At `contextStopPct`, the current behavior is: stop agent, fail task
- For TDD, we need an intermediate 85% threshold that spawns a fixer instead of failing
- The fixer agent is an existing agent type (`model.AgentFixer`) with `fixerInstructions` in prompt.go
- `SpawnAgent` creates a worktree and launches a tmux session

## Dependencies

This agent depends on Agents 06 (test-writing flow) and 09 (per-agent test gate). The test gate, test command discovery, phase-aware scheduling, and `processTestWriting` must exist.

## Deliverables

### 1. Modify `cmd/drem/config.go`

Add the fixer threshold:

```go
type Config struct {
    // ... existing fields ...
    ContextFixerPercent int `toml:"context_fixer_percent"`
}
```

Default: `ContextFixerPercent: 85`.

### 2. Modify `internal/orchestrator/orchestrator.go`

**a) Add `contextFixerPct` field to Orchestrator and wire through `New()`:**

```go
type Orchestrator struct {
    // ... existing ...
    contextFixerPct int
}
```

**b) Rewrite `checkContextUsage` for role-aware escalation (§4.6.1, §4.6.2):**

The current logic:
```
>= contextStopPct OR compaction → stop agent, fail task
>= contextWarnPct → log warning
```

The new logic distinguishes by agent role and subtask phase:

```go
func (o *Orchestrator) checkContextUsage() {
    for _, ra := range o.runner.GetRunningAgents() {
        usage := ra.ContextUsage
        if usage == nil {
            continue
        }

        // Load the agent's current subtask to check phase
        var subtask model.Task
        // ... load by ra.Agent.CurrentTaskID ...

        pct := usage.UsedPercent

        if usage.CompactionTriggered || pct >= o.contextStopPct {
            // Hard stop for ALL agents at contextStopPct (90%)
            o.runner.StopAgent(ra.Agent.ID)
            o.handleAgentContextExhausted(&subtask, &ra.Agent, pct)
            continue
        }

        if pct >= o.contextFixerPct {
            // 85% threshold: role-aware escalation
            if subtask.Phase == "implementation" || subtask.Phase == "integration" {
                // Implementation agent struggling with tests → spawn fixer
                o.runner.StopAgent(ra.Agent.ID)
                o.spawnFixerForTestFailure(&subtask, &ra.Agent)
                continue
            }
            if subtask.Phase == "test" {
                // Test-writing agent at 85% — no fixer, just let it finish
                // or hit contextStopPct. Test-writing recovery is different (§4.6.3)
                o.logger.Warn("test-writing agent at high context usage",
                    "agent_id", ra.Agent.ID, "pct", pct)
                continue
            }
        }

        if ra.Agent.AgentType == model.AgentFixer && pct >= 80 {
            // Fixer agents at 80% → stop and escalate to human
            o.runner.StopAgent(ra.Agent.ID)
            o.escalateFixerToHuman(&subtask, &ra.Agent, pct)
            continue
        }

        if pct >= o.contextWarnPct {
            o.logger.Info("agent context window warning",
                "agent_id", ra.Agent.ID, "pct", pct)
            o.emit("context_window_warning", map[string]any{
                "agent_id": ra.Agent.ID, "used_pct": pct,
            })
        }
    }
}
```

**c) Add `spawnFixerForTestFailure` (§4.6.1):**

```go
// spawnFixerForTestFailure stops an implementation agent that's struggling
// and spawns a fixer agent with the test failure context.
func (o *Orchestrator) spawnFixerForTestFailure(subtask *model.Task, ag *model.Agent) error
```

Logic:
1. Get the last test result from the agent: `ag.Config["last_test_result"]`
2. Get the agent's diff: run `git diff <base>...HEAD` in the agent's worktree
3. Identify the failing test files from the test result output
4. Build a fixer prompt:
   ```
   Fix the code to pass these tests. Do NOT modify the tests.

   ## Test Failure Output
   <last test result output>

   ## Agent's Changes (diff)
   <git diff>

   ## Failing Test Files
   <list of test files>
   ```
5. Spawn a fixer agent on the same worktree/branch
6. Update subtask context: `subtask.Context["fixer_spawned"] = true`

**d) Add `escalateFixerToHuman`:**

```go
// escalateFixerToHuman stops a fixer agent at its context limit and
// marks the task for human review.
func (o *Orchestrator) escalateFixerToHuman(subtask *model.Task, ag *model.Agent, pct int) error
```

Logic:
1. Stop the fixer
2. Write a diagnostic summary to the subtask context
3. Set `NeedsHumanReview = true` on the parent task
4. Transition the subtask to FAILED
5. Emit `"fixer_escalated_to_human"` event

**e) Add `handleAgentContextExhausted`:**

Refactor the existing hard-stop logic into this method. For test-writing agents, apply §4.6.3 recovery:

```go
func (o *Orchestrator) handleAgentContextExhausted(subtask *model.Task, ag *model.Agent, pct int) error {
    if subtask.Phase == "test" {
        return o.handleTestWritingFailure(subtask, ag)
    }
    // Default: fail the task
    return o.failTask(subtask, fmt.Sprintf("agent exhausted context window (%d%%)", pct))
}
```

**f) Add `handleTestWritingFailure` (§4.6.3):**

```go
// handleTestWritingFailure handles a test-writing agent that exhausted its
// context. If compilable test files exist, treat as partial success. Otherwise,
// retry once, then escalate to human.
func (o *Orchestrator) handleTestWritingFailure(subtask *model.Task, ag *model.Agent) error
```

Logic:
1. Inspect the agent's worktree for test files
2. Run the compilation command on any found test files
3. If compilable test files exist:
   - Mark subtask as DONE
   - Log warning: "test-writing agent stopped early, partial tests"
   - The human catches quality issues at TEST_REVIEW
4. If no compilable test files:
   - Check if this is already a retry (`subtask.Context["test_writing_retry"]`)
   - If not a retry: create a new test-writing subtask with error context, mark this one FAILED
   - If already a retry: mark parent task FAILED with diagnostic "Unable to generate compilable tests for subtask N"

**g) Add `processTestingReady` — automated gate (§4.4.5, Phase 5):**

```go
// processTestingReady runs the automated test gate at TESTING_READY.
// Spawns a reviewer agent that runs the full test suite. If tests pass,
// transitions to MERGING. If tests fail, spawns a fixer agent.
func (o *Orchestrator) processTestingReady(parent *model.Task) error
```

Logic:
1. Check if a reviewer/fixer is already running for this task (avoid re-spawning)
2. If no agent running:
   - Run the full test suite on the integration branch
   - If tests pass → transition to MERGING
   - If tests fail → spawn a fixer agent on the integration worktree with:
     - Full test failure output
     - The diff between feature branch and main
     - Instruction: "Fix the integration failures. Prefer fixing implementation code over modifying tests."
3. If a fixer agent completes:
   - Re-run the full test suite
   - If pass → MERGING
   - If fail → set `NeedsHumanReview = true`, keep in TESTING_READY, emit event

**h) Update `doTick`** to call processTestingReady:

Add after the IN_PROGRESS processing (step 5):

```go
// 5b. Process TESTING_READY parent tasks (automated gate).
var testingReadyTasks []model.Task
if err := o.db.Where("project_id = ? AND status = ? AND parent_task_id IS NULL",
    o.projectID, model.StatusTestingReady).Find(&testingReadyTasks).Error; err != nil {
    o.logger.Error("doTick: query testing_ready tasks", "error", err)
} else {
    for i := range testingReadyTasks {
        if err := o.processTestingReady(&testingReadyTasks[i]); err != nil {
            o.logger.Error("doTick: processTestingReady", "task_id", testingReadyTasks[i].ID, "error", err)
        }
    }
}
```

**i) Update `HandleTestPassed` and `HandleTestFailed`** — these existing methods handled the manual TESTING_READY gate. Now that TESTING_READY is automated:
- `HandleTestPassed` can remain as a manual override (human forces merge)
- `HandleTestFailed` should transition to IN_PROGRESS (for re-implementation) instead of PLANNING

### 3. Modify `cmd/drem/main.go`

Wire `ContextFixerPercent` through to `orchestrator.New()`.

### 4. Add tests

**`internal/orchestrator/failure_recovery_test.go`** (new file):

- **checkContextUsage — impl agent at 85%**: Fixer spawned, agent stopped
- **checkContextUsage — impl agent at 74%**: No action (below fixer threshold)
- **checkContextUsage — test agent at 85%**: Warning only, no fixer (test agents don't get fixers)
- **checkContextUsage — fixer agent at 80%**: Fixer stopped, human escalation
- **checkContextUsage — any agent at 90%**: Hard stop, task failed
- **checkContextUsage — compaction triggered**: Hard stop regardless of percentage
- **handleTestWritingFailure — compilable tests exist**: Subtask marked DONE
- **handleTestWritingFailure — no compilable tests, first attempt**: Retry subtask created
- **handleTestWritingFailure — no compilable tests, retry**: Parent FAILED
- **processTestingReady — tests pass**: Transitions to MERGING
- **processTestingReady — tests fail**: Fixer spawned on integration branch
- **processTestingReady — fixer succeeds**: Tests re-run, pass → MERGING
- **processTestingReady — fixer hits 80%**: Human review flagged, NeedsHumanReview set

## Scope Limitation

ONLY modify:
- `internal/orchestrator/orchestrator.go`
- `cmd/drem/config.go`
- `cmd/drem/main.go`
- New test files in `internal/orchestrator/`

Do NOT modify: `internal/model/`, `internal/state/`, `internal/prompt/`, `internal/tui/`, `internal/merge/`, `internal/ctxmon/`, `internal/agent/`.

## Conventions

- Go 1.22+ with standard library
- `gofmt` for formatting
- Exported functions have doc comments
- Error wrapping: `fmt.Errorf("context: %w", err)`
- Table-driven tests
- Build verification: `go build ./... && go test ./...`
