# Agent: Narrow Constraints Package Public Surface

You are working on the `master` branch of drem-orchestrator, a Go-based task orchestrator.
Your task is to reduce the exported symbol count of `internal/constraints/` by unexporting types and functions that are not referenced outside the package.

## Context

Read these before starting:
- `internal/constraints/` — all `.go` files (non-test), including the `depth/` subpackage
- `ARCHITECTURE.md` (Interfaces & Coupling section)

The constraints package currently exports ~126 symbols for 1100 LOC — an export ratio of 0.115 (shallowest core module). The goal is to hide internal plumbing behind a narrower API.

**External consumers reference these constraints types** (found via grep):

From `internal/orchestrator/`:
- `constraints.LoadConfig` — load `.drem/constraints.toml`
- `constraints.Evaluate` — run all constraints
- `constraints.EvaluateFiles` — run constraints scoped to changed files
- `constraints.FormatReport` — format a report as string
- `constraints.Config` — the config struct (passed to `ValidatePlanConstraints`)
- `constraints.LinesException` — used in `plan_validation.go` `hasLinesException()` helper

From `internal/prompt/`:
- `constraints.LoadConfig` — load constraints for context files

**These MUST stay exported.**

## Deliverables

### Unexport internal-only types and functions

1. Get the current export count: `grep -c '^func [A-Z]\|^type [A-Z]\|^func (.*) [A-Z]' internal/constraints/*.go` (excluding test files).

2. Unexport these categories (verify each is not referenced externally first):
   - **Helper functions**: `GlobFiles`, `MatchDoublestarGlob`, `IsGitIgnored`, `EvalMaxLines`, and similar internal evaluation helpers
   - **Individual constraint types**: `CommandConstraint`, `MaxLinesConstraint`, `MaxMatchesConstraint`, `NoMatchConstraint`, `DepthConstraint` — if they are only instantiated within the package during config parsing
   - **Internal result types**: Any intermediate types used only within evaluation

3. Keep exported:
   - `Config`, `LoadConfig`
   - `Evaluate`, `EvaluateFiles`
   - `Report`, `Result`, `FormatReport`
   - `LinesException` (used by orchestrator's plan_validation.go)
   - Anything else referenced by external packages

4. Fix all references within the package (including test files) to use lowercase names.

### Scope Limitation

- Do NOT change any behavior — only symbol visibility
- Do NOT move code between packages
- Do NOT modify files outside `internal/constraints/` (and `internal/constraints/depth/`)
- TOML struct tags need exported fields — check before unexporting config struct fields

## Verification

```bash
# Measure export count reduction:
grep -c '^func [A-Z]\|^type [A-Z]\|^func (.*) [A-Z]' internal/constraints/*.go | grep -v _test

# Must compile:
go build ./...

# All tests must pass:
go test ./internal/constraints/... ./internal/orchestrator/... ./internal/prompt/...

# Constitution check must still pass:
bash scripts/check_constitution.sh
```
