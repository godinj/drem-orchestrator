# Agent: Extract Reconciliation and Test Execution from orchestrator.go

You are working on the `master` branch of Drem Orchestrator, a Go-based agent orchestration system.
Your task is to extract two groups of methods from `internal/orchestrator/orchestrator.go` into their own files within the same package.

## Context

Read these before starting:
- `ARCHITECTURE.md` (Structural Limits section — file length ceiling, function count ceiling)
- `internal/orchestrator/orchestrator.go` (the entire file — understand the Orchestrator struct and all methods)
- `internal/orchestrator/plan_validation.go` (example of a successful prior extraction — follow this pattern)
- `internal/orchestrator/scheduler.go` (another example of same-package extraction)

## Background

`orchestrator.go` is 4,567 lines with 84 functions. It is grandfathered under the architecture constitution but must shrink. This agent extracts ~1,400 lines into 2 new files.

## Dependencies

This agent depends on Tier 1 agents (01-05). If magic number constants from Agent 03 are present, use them. If not, use the original literals — do not block on missing constants.

## Deliverables

### New file: `internal/orchestrator/reconcile.go`

Move these 6 functions (~486 lines) from `orchestrator.go`:

1. `func (o *Orchestrator) Reconcile()` — entry point, calls the 5 sub-reconcilers
2. `func (o *Orchestrator) reconcileStaleSubtasks()` — finds subtasks stuck too long
3. `func (o *Orchestrator) reconcileOrphanedSubtasks()` — finds subtasks with dead agents
4. `func (o *Orchestrator) reconcileEmptyFeatures()` — finds feature tasks with no active work
5. `func (o *Orchestrator) reconcileOrphanWorktrees()` — finds worktrees without matching tasks
6. `func (o *Orchestrator) reconcileStuckAgents()` — finds agents that stopped heartbeating

The file header:
```go
package orchestrator
```

Add necessary imports. All methods are on `*Orchestrator` so they have full access to the struct fields. No signature changes needed.

### New file: `internal/orchestrator/test_execution.go`

Move these 8 functions (~912 lines) from `orchestrator.go`:

1. `func (o *Orchestrator) processTestWriting(...)` — drives the test-writing agent flow
2. `func (o *Orchestrator) processTestingReady(...)` — runs test suite when all test agents complete
3. `func (o *Orchestrator) runTestSuite(...)` — executes `go test` and captures output
4. `func (o *Orchestrator) getTestCommand(...)` — determines the test command for a task
5. `func (o *Orchestrator) verifyTestsBeforeMerge(...)` — final test run before merge
6. `func (o *Orchestrator) verifyCompilationBeforeMerge(...)` — compilation check before merge
7. `func (o *Orchestrator) scopeTestsForSubtask(...)` — scopes test files for a subtask
8. `func (o *Orchestrator) checkForCompilableTests(...)` — checks if test files compile

Also move any helper functions that are ONLY called by the above methods and not by anything else in orchestrator.go. Read the call graph carefully.

Additionally, move these related functions if they are only used by test execution:
- `func (o *Orchestrator) extractTestFiles(...)` — parses test file paths from plans
- `func (o *Orchestrator) storeTestResult(...)` — persists test run results
- `func (o *Orchestrator) runCommandWithTimeout(...)` — runs a shell command with timeout

Check each one: if it's called from methods staying in orchestrator.go, leave it there. If it's only called from test execution methods, move it.

## Extraction Process

For each function group:

1. **Identify the exact line range** in orchestrator.go (use `grep -n '^func'` to find boundaries)
2. **Create the new file** with `package orchestrator` header
3. **Copy the functions** to the new file (preserve exact formatting and comments)
4. **Delete the functions** from orchestrator.go
5. **Add imports** to the new file — only import what the moved functions actually use
6. **Remove unused imports** from orchestrator.go if any were only used by the moved functions
7. **Verify compilation**: `go build ./internal/orchestrator/`

## Scope Limitation

- Do NOT modify any function signatures, logic, or behavior.
- Do NOT rename anything.
- Do NOT refactor or restructure code — this is a pure file-split operation.
- Do NOT move functions that are called by methods staying in orchestrator.go.
- Do NOT modify test files — existing tests should pass unchanged since all functions remain in the same package.

## Verification

```bash
# orchestrator.go must be smaller than before (should lose ~1,400 lines)
wc -l internal/orchestrator/orchestrator.go
# Expected: ~3,100 lines (down from 4,567)

# New files must exist
ls -la internal/orchestrator/reconcile.go internal/orchestrator/test_execution.go

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
