# Agent: Reduce Orchestrator Internal Imports Below Baseline

You are working on the `master` branch of drem-orchestrator, a Go-based task orchestrator.
Your task is to reduce `internal/orchestrator/`'s internal import count from 37 to <= 35.

## Context

Read these before starting:
- `ARCHITECTURE.md` (Package import ceiling rule — 6 max, orchestrator grandfathered at 35)
- `.drem/constraints.toml` (the `Internal import ceiling` constraint with orchestrator exception at baseline_count = 35)
- All non-test `.go` files in `internal/orchestrator/`

The orchestrator currently imports 13 distinct internal packages across its source files (non-test). The constraint counts individual import lines across ALL files in the directory, not unique packages. The current count is 37 matches (where the baseline is 35).

Run this to see the full picture:
```bash
grep -rn '"github.com/godinj/drem-orchestrator/internal/' internal/orchestrator/*.go | grep -v _test.go
```

## Deliverables

### Reduce import count by at least 2

Strategies (pick whichever is cleanest):

1. **Consolidate duplicate imports**: If the same internal package is imported in multiple files but only used lightly in one of them, refactor to eliminate the light usage (move the call to the file that already imports it).

2. **Push logic to dependencies**: If a file imports a package only to call one function, consider moving that call to a helper in the package that already owns the relationship.

3. **Remove unnecessary imports**: Check if any import is unused or could be replaced by an existing dependency.

Important: The constraint counts `grep` matches of `".*internal/` across non-test files in the directory. Each `import "github.com/godinj/drem-orchestrator/internal/foo"` line counts as 1. Reducing the total from 37 to 35 means eliminating 2 import lines.

### Scope Limitation

- Do NOT change any external behavior
- Do NOT add new packages
- Do NOT move large amounts of code — prefer minimal, targeted changes
- Keep test files working (test files can import whatever they need — only non-test imports are counted)

## Verification

```bash
# Must be <= 35:
grep -rn '".*internal/' internal/orchestrator/*.go | grep -v _test.go | wc -l

# Constitution check must pass:
bash scripts/check_constitution.sh

# All tests must pass:
go test ./internal/orchestrator/...
```
