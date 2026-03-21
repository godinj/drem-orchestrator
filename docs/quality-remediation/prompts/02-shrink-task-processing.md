# Agent: Shrink task_processing.go Below 800 Lines

You are working on the `master` branch of drem-orchestrator, a Go-based task orchestrator.
Your task is to extract functions from `task_processing.go` (808 lines) to bring it under the 800-line ceiling.

## Context

Read these before starting:
- `ARCHITECTURE.md` (File length ceiling rule — 800 lines max for non-test `.go` files)
- `internal/orchestrator/task_processing.go` (the file to shrink)
- `internal/orchestrator/agent_merge.go` (existing extraction — merge-related logic already lives here)

## Deliverables

### Extract merge execution logic

#### 1. New file: `internal/orchestrator/merge_execution.go`

Extract `executeMerge` (lines 643-772, ~130 lines) into a new file `merge_execution.go` in the same package. This function handles the MERGING state, supervisor-powered conflict analysis, build failure diagnosis, and fixer spawning — a cohesive unit.

The new file must:
- Be in `package orchestrator`
- Contain `func (o *Orchestrator) executeMerge(task *model.Task) error`
- Include the necessary imports (only those used by the extracted function)
- NOT add any new exported symbols

After extraction, `task_processing.go` should be ~678 lines (808 - 130).

### Scope Limitation

- Do NOT refactor the extracted function — move it verbatim
- Do NOT rename anything
- Do NOT change any logic
- Do NOT touch any other files except `task_processing.go` (removing the function) and the new `merge_execution.go` (adding it)

## Verification

```bash
# task_processing.go must be <= 800 lines:
wc -l internal/orchestrator/task_processing.go

# New file must be under 800 lines:
wc -l internal/orchestrator/merge_execution.go

# Constitution check must pass:
bash scripts/check_constitution.sh

# All tests must pass:
go test ./internal/orchestrator/...
```
