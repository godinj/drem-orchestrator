# Agent: Extract Agent Results and Task Processing from orchestrator.go

> Historical extraction prompt. Any reference below to idle-file completion or
> duplicate reconciliation paths is superseded by Phase 4. See
> `plans/orchestration-meta-analysis.md`.

You are working on the `master` branch of Drem Orchestrator, a Go-based agent orchestration system.
Your task is to extract two groups of methods from `internal/orchestrator/orchestrator.go` into their own files within the same package.

## Context

Read these before starting:
- `ARCHITECTURE.md` (Structural Limits section — file length ceiling, function count ceiling)
- `internal/orchestrator/orchestrator.go` (the full file — understand remaining methods after prior extractions)
- `internal/orchestrator/reconcile.go` (prior extraction — follow the same pattern)
- `internal/orchestrator/test_execution.go` (prior extraction — follow the same pattern)
- `internal/orchestrator/plan_validation.go` (established extraction pattern)

## Background

After Agent 06 extracted reconciliation and test execution, `orchestrator.go` should be ~3,100 lines. This agent extracts ~1,860 more lines, bringing it down to ~1,300 lines of core scheduling logic.

## Dependencies

This agent depends on Agent 06 (extract-reconciliation-and-testing). The reconciliation and test execution methods must already be in their own files. If they're not, extract them first following Agent 06's instructions before proceeding.

## Deliverables

### New file: `internal/orchestrator/agent_results.go`

Move these functions (~790 lines) from `orchestrator.go`:

**Core result routing:**
1. `func (o *Orchestrator) processAgentResult(...)` — dispatches to type-specific handlers
2. `func (o *Orchestrator) onAgentCompleted(...)` — handles successful agent completion (~210 lines)
3. `func (o *Orchestrator) onPlannerCompleted(...)` — handles planner agent results
4. `func (o *Orchestrator) onReviewerCompleted(...)` — handles reviewer agent results
5. `func (o *Orchestrator) onFixerCompleted(...)` — handles fixer agent results

**Failure handling:**
6. `func (o *Orchestrator) onAgentFailed(...)` — handles agent failure (~198 lines)
7. `func (o *Orchestrator) onAgentEmptyWork(...)` — handles agents that report no work done

**Spawning helpers (only if exclusively called by the above):**
8. `func (o *Orchestrator) SpawnReviewerSession(...)` — spawns a reviewer agent
9. `func (o *Orchestrator) SpawnFixerSession(...)` — spawns a fixer agent
10. `func (o *Orchestrator) spawnDiagnosticAgent(...)` — spawns a diagnostic agent

**Context and failure escalation (only if exclusively called by the above):**
11. `func (o *Orchestrator) spawnFixerForTestFailure(...)` — spawns fixer for test failures
12. `func (o *Orchestrator) escalateFixerToHuman(...)` — escalates to human review
13. `func (o *Orchestrator) handleAgentContextExhausted(...)` — handles context window exhaustion
14. `func (o *Orchestrator) handleTestWritingFailure(...)` — handles test writing failures

For items 8-14: check whether each function is called ONLY from methods in this group. If a function is also called from methods staying in orchestrator.go (e.g. `doTick`, `HandlePlanApproved`), leave it in orchestrator.go.

### New file: `internal/orchestrator/task_processing.go`

Move these functions (~1,072 lines) from `orchestrator.go`:

**Per-status processors:**
1. `func (o *Orchestrator) processBacklog(...)` — processes backlog tasks
2. `func (o *Orchestrator) processPlanning(...)` — processes planning tasks
3. `func (o *Orchestrator) scheduleSubtasks(...)` — schedules subtasks for execution (~219 lines)
4. `func (o *Orchestrator) checkFeatureCompletion(...)` — checks if all subtasks are done
5. `func (o *Orchestrator) findCurrentGroup(...)` — finds the current execution group

**Merge execution:**
6. `func (o *Orchestrator) executeMerge(...)` — executes the merge pipeline
7. `func (o *Orchestrator) handlePaused(...)` — handles paused tasks

**TUI action handlers:**
8. `func (o *Orchestrator) HandlePlanApproved(...)` — handles plan approval from TUI
9. `func (o *Orchestrator) HandlePlanRejected(...)` — handles plan rejection from TUI
10. `func (o *Orchestrator) HandleTestPassed(...)` — handles test pass from TUI
11. `func (o *Orchestrator) HandleTestFailed(...)` — handles test fail from TUI
12. `func (o *Orchestrator) HandleTestReviewApproved(...)` — handles test review approval
13. `func (o *Orchestrator) HandleTestReviewRejected(...)` — handles test review rejection

**Utility helpers (only if exclusively called by the above):**
14. `func (o *Orchestrator) resolveFeatureWorktree(...)` — resolves worktree path for a feature
15. `func (o *Orchestrator) resolveIntegrationWorktree(...)` — resolves integration worktree
16. `func (o *Orchestrator) IntegrationWorktreePath(...)` — returns integration worktree path
17. `func (o *Orchestrator) isWorkAlreadyMerged(...)` — checks if work is already merged

For items 14-17: same rule — only move if exclusively called from methods in this group.

## Extraction Process

For each function group:

1. **Identify the exact line range** in orchestrator.go (use `grep -n '^func'` to find boundaries)
2. **Trace the call graph** — for each function, grep for its name to find all callers. Only move it if ALL callers are also being moved (or are already in another extracted file).
3. **Create the new file** with `package orchestrator` header
4. **Copy the functions** to the new file (preserve exact formatting and comments)
5. **Delete the functions** from orchestrator.go
6. **Add imports** to the new file — only import what the moved functions actually use
7. **Remove unused imports** from orchestrator.go
8. **Verify compilation**: `go build ./internal/orchestrator/`

## What Should Remain in orchestrator.go

After both extractions (Agent 06 + this agent), orchestrator.go should contain only:

- The `Orchestrator` struct definition and `New()` constructor
- `Run()` and `doTick()` — the main scheduling loop
- `SetTestGateConfig()` — configuration
- `ReapOrphanedSessions()` — startup cleanup
- `recoverStuckAgents()` — if called from doTick
- TUI utility methods: `AddComment`, `DeleteComment`, `DeletePlanStep`, `DeleteSubtask`, `GetComments`, `PauseTask`, `ResumeTask`, `RetryTask`, `CreateTask`
- `SpawnSupervisorSession`, `journalDir`, `logSupervisorAction`
- `emit`, `failTask`, `incrementRetryCount`
- `getAgentContextInfos`, `checkContextUsage`, `getTaskPhase`

These are the core scheduling primitives and simple CRUD operations. Target: ~1,300 lines.

## Scope Limitation

- Do NOT modify any function signatures, logic, or behavior.
- Do NOT rename anything.
- Do NOT refactor or restructure code — this is a pure file-split operation.
- Do NOT move functions that are called by methods staying in orchestrator.go.
- Do NOT modify test files.
- If the call graph is ambiguous (a function is called from both a staying and moving method), leave it in orchestrator.go.

## Verification

```bash
# orchestrator.go must be ~1,300 lines
wc -l internal/orchestrator/orchestrator.go
# Expected: ~1,200-1,400 lines

# New files must exist
ls -la internal/orchestrator/agent_results.go internal/orchestrator/task_processing.go

# All 4 extracted files + orchestrator.go should roughly total the original 4,567
wc -l internal/orchestrator/orchestrator.go \
     internal/orchestrator/reconcile.go \
     internal/orchestrator/test_execution.go \
     internal/orchestrator/agent_results.go \
     internal/orchestrator/task_processing.go

# All tests must pass unchanged
go test ./internal/orchestrator/...

# Full test suite
go test ./...
```

## Conventions

- Same package (`package orchestrator`) — no interface changes needed
- Preserve existing doc comments on all moved functions
- Import ordering: stdlib, then external, then internal
- Build verification: `go test ./...`
