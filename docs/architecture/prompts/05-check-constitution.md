# Agent: Create Constitution Enforcement Script

You are working on the `master` branch of Drem Orchestrator, a Go-based agent orchestration system.
Your task is to create `scripts/check_constitution.sh` — a script that enforces the rules in `ARCHITECTURE.md`.

## Context

Read these before starting:
- `ARCHITECTURE.md` (all sections — each rule with a compliance test becomes a check)

## Deliverables

### New file: `scripts/check_constitution.sh`

Create a bash script that runs each enforceable check from `ARCHITECTURE.md` and reports pass/fail. The script must:

1. Exit with code 0 if all checks pass, non-zero otherwise
2. Print clear pass/fail messages for each check
3. Be runnable from the repo root: `bash scripts/check_constitution.sh`

#### Checks to implement

```bash
#!/usr/bin/env bash
set -euo pipefail

ERRORS=0

# Check 1: gofmt compliance
# All .go files must pass gofmt -l with no output
drifted=$(gofmt -l ./internal/ ./cmd/ 2>/dev/null)
if [ -n "$drifted" ]; then
    echo "FAIL: gofmt drift in:"
    echo "$drifted"
    ERRORS=$((ERRORS + 1))
else
    echo "PASS: gofmt compliance"
fi

# Check 2: File length ceiling (800 lines, non-test)
# Grandfathered: orchestrator.go (report but don't fail)
oversized=$(find internal/ cmd/ -name '*.go' ! -name '*_test.go' -exec wc -l {} + | \
    awk '$1 > 800 && !/total$/ {print}')
if [ -n "$oversized" ]; then
    # Filter out grandfathered files for error count
    non_grandfathered=$(echo "$oversized" | grep -v 'orchestrator/orchestrator.go' || true)
    if [ -n "$non_grandfathered" ]; then
        echo "FAIL: Files exceeding 800 lines:"
        echo "$non_grandfathered"
        ERRORS=$((ERRORS + 1))
    fi
    grandfathered=$(echo "$oversized" | grep 'orchestrator/orchestrator.go' || true)
    if [ -n "$grandfathered" ]; then
        echo "WARN: Grandfathered file (must shrink, not grow):"
        echo "$grandfathered"
    fi
else
    echo "PASS: File length ceiling"
fi

# Check 3: No duplicate GORM BeforeCreate hooks
hook_count=$(grep -c 'func.*BeforeCreate' internal/model/models.go 2>/dev/null || echo 0)
if [ "$hook_count" -gt 1 ]; then
    echo "FAIL: $hook_count BeforeCreate hooks in models.go (should be 0 or 1)"
    ERRORS=$((ERRORS + 1))
else
    echo "PASS: GORM hook consolidation"
fi

# Check 4: No DB init outside testutil in test files
db_dupes=$(grep -rn 'gorm.Open(sqlite' internal/ --include='*_test.go' | grep -v testutil/ || true)
if [ -n "$db_dupes" ]; then
    echo "FAIL: DB initialization outside testutil:"
    echo "$db_dupes"
    ERRORS=$((ERRORS + 1))
else
    echo "PASS: DB init consolidated in testutil"
fi

# Check 5: No git test helpers outside testutil
git_dupes=$(grep -rn 'func setupBareRepo\|func initBareRepo\|func addWorktree\|func commitFile' \
    internal/ --include='*_test.go' | grep -v testutil/ || true)
if [ -n "$git_dupes" ]; then
    echo "FAIL: Git test helpers outside testutil:"
    echo "$git_dupes"
    ERRORS=$((ERRORS + 1))
else
    echo "PASS: Git helpers consolidated in testutil"
fi

# Check 6: No test factory functions outside testutil
factory_dupes=$(grep -rn 'func createTest\|func mockTestDB\|func lifecycleTestDB' \
    internal/ --include='*_test.go' | grep -v testutil/ || true)
if [ -n "$factory_dupes" ]; then
    echo "FAIL: Test factory functions outside testutil:"
    echo "$factory_dupes"
    ERRORS=$((ERRORS + 1))
else
    echo "PASS: Test factories consolidated in testutil"
fi

# Check 7: go vet passes
if ! go vet ./... 2>/dev/null; then
    echo "FAIL: go vet found issues"
    ERRORS=$((ERRORS + 1))
else
    echo "PASS: go vet clean"
fi
```

The above is a starting template — refine it as needed. Key requirements:

- Each check maps to a specific rule in `ARCHITECTURE.md`
- Grandfathered files are reported as `WARN`, not `FAIL`
- The final summary prints total errors and exits with appropriate code
- Add a summary footer:
  ```
  ──────────────────────────
  N checks passed, M failed
  ```

### Make it executable

```bash
chmod +x scripts/check_constitution.sh
```

## Scope Limitation

- Do NOT modify any source code or test code.
- Do NOT modify `ARCHITECTURE.md`.
- The script should be self-contained (no external dependencies beyond bash, grep, awk, wc, gofmt, go).

## Verification

```bash
# The script must run without syntax errors
bash scripts/check_constitution.sh

# It should report current violations (FAIL) for rules not yet fixed
# and PASS for rules already satisfied
```

## Conventions

- Use `set -euo pipefail` at the top
- Use `ERRORS` counter pattern (increment on each failure)
- Exit 0 on success, 1 on failure
- Build verification: `bash scripts/check_constitution.sh`
