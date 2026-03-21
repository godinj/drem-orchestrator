# Agent: Narrow Supervisor Package Public Surface

You are working on the `master` branch of drem-orchestrator, a Go-based task orchestrator.
Your task is to reduce the exported symbol count of `internal/supervisor/` by unexporting types and functions that are not referenced outside the package.

## Context

Read these before starting:
- `internal/supervisor/` — all `.go` files (non-test)
- `ARCHITECTURE.md` (Interfaces & Coupling section)

The supervisor package currently exports ~61 symbols (types, functions, methods) for 799 LOC — an export ratio of 0.076. The goal is to unexport internal types that are only used within the package, reducing the public API to what external consumers actually need.

**External consumers reference these supervisor types** (found via grep across the codebase):

From `internal/orchestrator/`:
- `supervisor.Supervisor` (the main type)
- `supervisor.AssumptionCrossCheck` and `supervisor.AssumptionCrossCheckPrompt`
- `supervisor.BuildFailureDiagnosis` and `supervisor.BuildFailurePrompt`
- `supervisor.MergeConflictAnalysis` and `supervisor.MergeConflictPrompt`
- `supervisor.FailureDiagnosis` and `supervisor.FailureDiagnosisPrompt`
- `supervisor.PlanDepthReview` and `supervisor.PlanDepthReviewPrompt`
- `supervisor.DepthConstraintDiagnosis` and `supervisor.DepthConstraintDiagnosisPrompt`
- `supervisor.JournalEntry` and `supervisor.WriteJournalEntry`
- `supervisor.SubtaskInfo` and `supervisor.OnDemandPrompt` / `supervisor.OnDemandOpts`

From `internal/testutil/`:
- `supervisor.New` (constructor)

**These MUST stay exported.**

## Deliverables

### Unexport internal-only types and functions

1. Run `grep -c '^func [A-Z]\|^type [A-Z]\|^func (.*) [A-Z]' internal/supervisor/*.go` (excluding test files) to get the current export count.

2. For each exported type and function in the supervisor package, check whether it appears in the external consumer list above. If it does NOT, unexport it (lowercase the first letter).

3. Common candidates for unexporting:
   - Helper functions only called within the package
   - Struct types only used as intermediate data within the package
   - Any `FeedbackIntegration`, `MissedAssumption`, `DepthViolation` types — check if referenced externally first
   - Internal prompt-building helper functions

4. After unexporting, fix all references within the package (including test files) to use the new lowercase names.

### Scope Limitation

- Do NOT change any behavior — only symbol visibility
- Do NOT move code between packages
- Do NOT create new packages
- Do NOT modify files outside `internal/supervisor/`
- Struct fields accessed via JSON tags can stay exported (Go JSON needs exported fields)

## Verification

```bash
# Measure export count reduction:
grep -c '^func [A-Z]\|^type [A-Z]\|^func (.*) [A-Z]' internal/supervisor/*.go | grep -v _test

# Must compile:
go build ./...

# All tests must pass:
go test ./...

# Constitution check must still pass:
bash scripts/check_constitution.sh
```
