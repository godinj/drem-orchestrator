#!/usr/bin/env bash
set -euo pipefail

# Constitution enforcement script
# Checks each enforceable rule from ARCHITECTURE.md and reports pass/fail.
# Run from the repo root: bash scripts/check_constitution.sh

ERRORS=0
PASSES=0
TOTAL=0

pass() {
    echo "PASS: $1"
    PASSES=$((PASSES + 1))
    TOTAL=$((TOTAL + 1))
}

fail() {
    echo "FAIL: $1"
    ERRORS=$((ERRORS + 1))
    TOTAL=$((TOTAL + 1))
}

warn() {
    echo "WARN: $1"
}

# ──────────────────────────────────────────────────────────────────────
# Check 1: gofmt compliance (Formatting > gofmt compliance: 100%)
# All .go files must pass gofmt -l with no output.
# ──────────────────────────────────────────────────────────────────────
drifted=$(gofmt -l ./internal/ ./cmd/ 2>/dev/null || true)
if [ -n "$drifted" ]; then
    fail "gofmt drift in:"
    echo "$drifted"
else
    pass "gofmt compliance"
fi

# ──────────────────────────────────────────────────────────────────────
# Check 2: File length ceiling — 800 lines, non-test
# (Structural Limits > File length ceiling: 800 lines)
# Grandfathered: orchestrator/orchestrator.go (WARN, not FAIL)
# ──────────────────────────────────────────────────────────────────────
oversized=$(find internal/ cmd/ -name '*.go' ! -name '*_test.go' -exec wc -l {} + | \
    awk '$1 > 800 && !/total$/ {print}')
if [ -n "$oversized" ]; then
    non_grandfathered=$(echo "$oversized" | grep -v 'orchestrator/orchestrator.go' || true)
    if [ -n "$non_grandfathered" ]; then
        fail "Files exceeding 800 lines:"
        echo "$non_grandfathered"
    else
        pass "File length ceiling (non-grandfathered)"
    fi
    grandfathered=$(echo "$oversized" | grep 'orchestrator/orchestrator.go' || true)
    if [ -n "$grandfathered" ]; then
        warn "Grandfathered file (must shrink, not grow):"
        echo "$grandfathered"
    fi
else
    pass "File length ceiling"
fi

# ──────────────────────────────────────────────────────────────────────
# Check 3: Function count ceiling — 20 exported functions per file
# (Structural Limits > Function count ceiling: 20 exported functions per file)
# Grandfathered: orchestrator/orchestrator.go (WARN, not FAIL)
# ──────────────────────────────────────────────────────────────────────
func_violations=""
while IFS= read -r gofile; do
    count=$(grep -c '^func ' "$gofile" 2>/dev/null) || count=0
    if [ "$count" -gt 20 ]; then
        func_violations="${func_violations}${count} ${gofile}"$'\n'
    fi
done < <(find internal/ cmd/ -name '*.go' ! -name '*_test.go' 2>/dev/null)

if [ -n "$func_violations" ]; then
    non_grandfathered_funcs=$(echo "$func_violations" | grep -v 'orchestrator/orchestrator.go' || true)
    if [ -n "$non_grandfathered_funcs" ]; then
        fail "Files exceeding 20 exported functions:"
        echo "$non_grandfathered_funcs"
    else
        pass "Function count ceiling (non-grandfathered)"
    fi
    grandfathered_funcs=$(echo "$func_violations" | grep 'orchestrator/orchestrator.go' || true)
    if [ -n "$grandfathered_funcs" ]; then
        warn "Grandfathered file (must shrink, not grow):"
        echo "$grandfathered_funcs"
    fi
else
    pass "Function count ceiling"
fi

# ──────────────────────────────────────────────────────────────────────
# Check 4: Package import ceiling — 6 internal imports
# (Structural Limits > Package import ceiling: 6 internal imports)
# Grandfathered: orchestrator (WARN, not FAIL)
# ──────────────────────────────────────────────────────────────────────
import_violations=""
for pkg_dir in internal/*/; do
    pkg_name=$(basename "$pkg_dir")
    # Count distinct internal/ imports across all non-test .go files in the package
    non_test_files=$(find "$pkg_dir" -maxdepth 1 -name '*.go' ! -name '*_test.go' 2>/dev/null)
    if [ -z "$non_test_files" ]; then
        continue
    fi
    raw_imports=$(echo "$non_test_files" | xargs grep -h '".*internal/' 2>/dev/null || true)
    if [ -z "$raw_imports" ]; then
        import_count=0
    else
        import_count=$(echo "$raw_imports" | \
            sed 's/.*internal\//internal\//' | sed 's/".*//' | \
            sort -u | wc -l)
    fi
    if [ "$import_count" -gt 6 ]; then
        import_violations="${import_violations}${pkg_name}: ${import_count} internal imports"$'\n'
    fi
done

if [ -n "$import_violations" ]; then
    non_grandfathered_imports=$(echo "$import_violations" | grep -v '^orchestrator:' || true)
    if [ -n "$non_grandfathered_imports" ]; then
        fail "Packages exceeding 6 internal imports:"
        echo "$non_grandfathered_imports"
    else
        pass "Package import ceiling (non-grandfathered)"
    fi
    grandfathered_imports=$(echo "$import_violations" | grep '^orchestrator:' || true)
    if [ -n "$grandfathered_imports" ]; then
        warn "Grandfathered package (must not add more):"
        echo "$grandfathered_imports"
    fi
else
    pass "Package import ceiling"
fi

# ──────────────────────────────────────────────────────────────────────
# Check 5: No duplicate GORM BeforeCreate hooks
# (Models > No duplicate GORM hooks)
# ──────────────────────────────────────────────────────────────────────
hook_count=$(grep -c 'func.*BeforeCreate' internal/model/models.go 2>/dev/null) || hook_count=0
if [ "$hook_count" -gt 1 ]; then
    fail "$hook_count BeforeCreate hooks in models.go (should be 0 or 1)"
else
    pass "GORM hook consolidation"
fi

# ──────────────────────────────────────────────────────────────────────
# Check 6: No DB init outside testutil in test files
# (Duplication > testutil is the single source for test infrastructure)
# ──────────────────────────────────────────────────────────────────────
db_dupes=$(grep -rn 'gorm.Open(sqlite' internal/ --include='*_test.go' | grep -v testutil/ || true)
if [ -n "$db_dupes" ]; then
    fail "DB initialization outside testutil:"
    echo "$db_dupes"
else
    pass "DB init consolidated in testutil"
fi

# ──────────────────────────────────────────────────────────────────────
# Check 7: No git test helpers outside testutil
# (Duplication > Search before creating / testutil is the single source)
# ──────────────────────────────────────────────────────────────────────
git_dupes=$(grep -rn 'func setupBareRepo\|func initBareRepo\|func addWorktree\|func commitFile' \
    internal/ --include='*_test.go' | grep -v testutil/ || true)
if [ -n "$git_dupes" ]; then
    fail "Git test helpers outside testutil:"
    echo "$git_dupes"
else
    pass "Git helpers consolidated in testutil"
fi

# ──────────────────────────────────────────────────────────────────────
# Check 8: No test factory functions outside testutil
# (Test Infrastructure > Test factory functions in testutil)
# ──────────────────────────────────────────────────────────────────────
factory_dupes=$(grep -rn 'func createTest\|func newTest\|func mockTestDB\|func lifecycleTestDB' \
    internal/ --include='*_test.go' | grep -v testutil/ || true)
if [ -n "$factory_dupes" ]; then
    fail "Test factory functions outside testutil:"
    echo "$factory_dupes"
else
    pass "Test factories consolidated in testutil"
fi

# ──────────────────────────────────────────────────────────────────────
# Check 9: go vet passes
# (Standard Go quality check, implied by Formatting rules)
# ──────────────────────────────────────────────────────────────────────
if ! go vet ./... 2>/dev/null; then
    fail "go vet found issues"
else
    pass "go vet clean"
fi

# ──────────────────────────────────────────────────────────────────────
# Summary
# ──────────────────────────────────────────────────────────────────────
echo ""
echo "──────────────────────────"
echo "$PASSES checks passed, $ERRORS failed"

if [ "$ERRORS" -gt 0 ]; then
    exit 1
fi
exit 0
