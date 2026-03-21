# Agent: Narrow Clarification Package Public Surface

You are working on the `master` branch of drem-orchestrator, a Go-based task orchestrator.
Your task is to reduce the exported symbol count of `internal/clarification/` by unexporting types and functions that are not referenced outside the package.

## Context

Read these before starting:
- `internal/clarification/` — all `.go` files (non-test)
- `docs/plan-clarification-prd.md` (design context)

The clarification package currently exports ~35 symbols for 472 LOC — an export ratio of 0.074. The goal is to narrow the public API to only what the orchestrator actually consumes.

**External consumers reference these clarification types** (found via grep):

From `internal/orchestrator/handlers.go`:
- `clarification.Assumption` — the assumption type (used in `planReviewData` struct)
- `clarification.ProcessAnswer` — process a user's clarification answer
- `clarification.ReplanContext` — generate replan context from completed session

From `internal/orchestrator/task_processing.go`:
- `clarification.Assumption` — parsed from plan JSON
- `clarification.Evaluate` — evaluate assumptions and decide if clarification needed

**These MUST stay exported: `Assumption`, `Evaluate`, `ProcessAnswer`, `ReplanContext`, plus the `Result` return type from `Evaluate`.**

## Deliverables

### Unexport internal-only types and functions

1. Get the current export count: `grep -c '^func [A-Z]\|^type [A-Z]\|^func (.*) [A-Z]' internal/clarification/*.go` (excluding test files).

2. Unexport everything NOT in the external consumer list above. Common candidates:
   - Internal session/round/qaPair types
   - Helper functions for deduplication, parsing, formatting
   - Any supervisor-related internal types
   - Internal evaluation logic types

3. Keep exported:
   - `Assumption` (struct type)
   - `Evaluate` (function)
   - `ProcessAnswer` (function)
   - `ReplanContext` (function)
   - Return types of the above functions (e.g., `Result` or equivalent)

4. Fix all references within the package (including test files) to use lowercase names.

### Scope Limitation

- Do NOT change any behavior — only symbol visibility
- Do NOT move code between packages
- Do NOT modify files outside `internal/clarification/`
- JSON struct tags need exported fields — check before unexporting struct fields

## Verification

```bash
# Measure export count reduction:
grep -c '^func [A-Z]\|^type [A-Z]\|^func (.*) [A-Z]' internal/clarification/*.go | grep -v _test

# Must compile:
go build ./...

# All tests must pass:
go test ./internal/clarification/... ./internal/orchestrator/...

# Constitution check must still pass:
bash scripts/check_constitution.sh
```
