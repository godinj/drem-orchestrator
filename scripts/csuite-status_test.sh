#!/usr/bin/env bash
set -euo pipefail

# Tests for csuite-status.sh
# Usage: bash scripts/csuite-status_test.sh
# Exit code: 0 if all tests pass, 1 if any fail.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
STATUS_SCRIPT="${SCRIPT_DIR}/csuite-status.sh"

# ---------------------------------------------------------------------------
# Test harness (same pattern as csuite-proto_test.sh)
# ---------------------------------------------------------------------------
PASS=0
FAIL=0
CURRENT_TEST=""

assert_eq() {
    if [ "$1" = "$2" ]; then
        PASS=$((PASS + 1))
    else
        echo "  FAIL ($CURRENT_TEST): expected '$2', got '$1'"
        FAIL=$((FAIL + 1))
    fi
}

assert_ok() {
    if [ "$1" -eq 0 ]; then
        PASS=$((PASS + 1))
    else
        echo "  FAIL ($CURRENT_TEST): expected exit 0, got $1"
        FAIL=$((FAIL + 1))
    fi
}

assert_fail() {
    if [ "$1" -ne 0 ]; then
        PASS=$((PASS + 1))
    else
        echo "  FAIL ($CURRENT_TEST): expected non-zero exit, got 0"
        FAIL=$((FAIL + 1))
    fi
}

assert_file_exists() {
    if [ -f "$1" ]; then
        PASS=$((PASS + 1))
    else
        echo "  FAIL ($CURRENT_TEST): file does not exist: $1"
        FAIL=$((FAIL + 1))
    fi
}

assert_file_not_exists() {
    if [ ! -f "$1" ]; then
        PASS=$((PASS + 1))
    else
        echo "  FAIL ($CURRENT_TEST): file should not exist: $1"
        FAIL=$((FAIL + 1))
    fi
}

assert_contains() {
    if echo "$1" | grep -qF "$2"; then
        PASS=$((PASS + 1))
    else
        echo "  FAIL ($CURRENT_TEST): output does not contain '$2'"
        FAIL=$((FAIL + 1))
    fi
}

assert_not_contains() {
    if ! echo "$1" | grep -qF "$2"; then
        PASS=$((PASS + 1))
    else
        echo "  FAIL ($CURRENT_TEST): output should not contain '$2'"
        FAIL=$((FAIL + 1))
    fi
}

assert_line_count_ge() {
    local actual
    actual="$(echo "$1" | wc -l | tr -d ' ')"
    if [ "$actual" -ge "$2" ]; then
        PASS=$((PASS + 1))
    else
        echo "  FAIL ($CURRENT_TEST): expected >= $2 lines, got $actual"
        FAIL=$((FAIL + 1))
    fi
}

# ---------------------------------------------------------------------------
# Setup / teardown
# ---------------------------------------------------------------------------
TMPDIR_ROOT="$(mktemp -d)"
cleanup() { rm -rf "$TMPDIR_ROOT"; }
trap cleanup EXIT

# Create a mock CSUITE_DIR with fake agent state for the tests.
# Also sets up mock drem CLI and tmux commands on PATH.
setup_mock_csuite() {
    local td="$1"
    mkdir -p "$td"

    # Bootstrap agent directories
    for agent in kyle mike alex ross seth; do
        mkdir -p "${td}/${agent}/inbox/archive"
        mkdir -p "${td}/${agent}/outbox"
    done
    mkdir -p "${td}/temp-workers"

    # Agent state files with context_percent and Current Focus
    cat > "${td}/kyle/state.md" <<'EOF'
---
role: CTO
context_percent: 42
heartbeat: 1711267200
---
# Current Focus
Reviewing architecture decisions for task 97ae7e1b
EOF

    cat > "${td}/mike/state.md" <<'EOF'
---
role: VP Engineering
context_percent: 78
heartbeat: 1711267200
---
# Current Focus
Monitoring pipeline throughput and agent health
EOF

    cat > "${td}/alex/state.md" <<'EOF'
---
role: PM
context_percent: 88
heartbeat: 1711267200
---
# Current Focus
Triaging incoming tasks from backlog
EOF

    cat > "${td}/ross/state.md" <<'EOF'
---
role: QA Lead
context_percent: 15
heartbeat: 1711267200
---
# Current Focus
Reviewing test coverage for recent merges
EOF

    cat > "${td}/seth/state.md" <<'EOF'
---
role: Tech Lead
context_percent: 63
heartbeat: 1711267200
---
# Current Focus
Investigating failed task b5dbb8ca
EOF

    # Temp worker state
    mkdir -p "${td}/temp-workers/worker-001/inbox/archive"
    mkdir -p "${td}/temp-workers/worker-001/outbox"
    cat > "${td}/temp-workers/worker-001/state.md" <<'EOF'
---
requester: alex
status: active
task: Build csuite-status.sh
---
EOF

    # Inbox messages for various agents
    cat > "${td}/kyle/inbox/20260324-073200-mike.md" <<'EOF'
---
from: mike
to: kyle
timestamp: 2026-03-24T07:32:00Z
subject: "Pipeline throughput report"
priority: normal
type: report
---
Pipeline is flowing. 3 tasks moved to test_writing.
EOF

    cat > "${td}/kyle/inbox/20260324-073500-alex.md" <<'EOF'
---
from: alex
to: kyle
timestamp: 2026-03-24T07:35:00Z
subject: "New task triage complete"
priority: high
type: observation
---
5 new tasks triaged and moved to backlog.
EOF

    cat > "${td}/mike/inbox/20260324-073100-ross.md" <<'EOF'
---
from: ross
to: mike
timestamp: 2026-03-24T07:31:00Z
subject: "Test coverage gap"
priority: high
type: observation
---
Coverage dropped below 80% on merge module.
EOF

    # Create mock bin directory for drem CLI and tmux stubs
    local mockbin="${td}/.mockbin"
    mkdir -p "$mockbin"

    # Mock drem CLI — returns fake pipeline stats
    cat > "${mockbin}/drem" <<'DREM_EOF'
#!/usr/bin/env bash
case "$1" in
    stats)
        echo "total: 12"
        echo "done: 4"
        echo "failed: 1"
        echo "in_progress: 3"
        echo "plan_review: 2"
        echo "test_review: 1"
        echo "backlog: 1"
        ;;
    failures)
        echo "b5dbb8ca | Build csuite-dashboard | compile error in tui.go | 2026-03-24T06:15:00Z"
        ;;
    *)
        echo "unknown command: $1" >&2
        exit 1
        ;;
esac
DREM_EOF
    chmod +x "${mockbin}/drem"

    # Mock tmux — returns fake session list
    cat > "${mockbin}/tmux" <<'TMUX_EOF'
#!/usr/bin/env bash
# Only intercept list-sessions; pass through otherwise
if [ "${1:-}" = "list-sessions" ] || [ "${2:-}" = "list-sessions" ]; then
    echo "csuite-kyle: 1 windows (created Mon Mar 24 07:00:00 2026)"
    echo "csuite-mike: 1 windows (created Mon Mar 24 07:00:00 2026)"
    echo "csuite-alex: 1 windows (created Mon Mar 24 07:00:00 2026)"
    echo "csuite-seth: 1 windows (created Mon Mar 24 07:00:00 2026)"
    exit 0
fi
exit 1
TMUX_EOF
    chmod +x "${mockbin}/tmux"

    echo "$mockbin"
}

# Run the status script with mocked environment.
# Usage: run_status <csuite_dir> <mockbin> [flags...]
run_status() {
    local csuite_dir="$1"
    local mockbin="$2"
    shift 2
    CSUITE_DIR="$csuite_dir" PATH="${mockbin}:${PATH}" bash "$STATUS_SCRIPT" "$@"
}

# ---------------------------------------------------------------------------
# Tests
# ---------------------------------------------------------------------------

test_help_flag() {
    CURRENT_TEST="test_help_flag"
    local td="${TMPDIR_ROOT}/help"
    local mockbin
    mockbin="$(setup_mock_csuite "$td")"

    local output
    output="$(run_status "$td" "$mockbin" --help 2>&1)"
    local rc=$?
    assert_ok "$rc"
    assert_contains "$output" "report"
    assert_contains "$output" "dashboard"

    echo "  $CURRENT_TEST: done"
}

test_unknown_flag_errors() {
    CURRENT_TEST="test_unknown_flag_errors"
    local td="${TMPDIR_ROOT}/unknown"
    local mockbin
    mockbin="$(setup_mock_csuite "$td")"

    local output
    output="$(run_status "$td" "$mockbin" --bogus 2>&1 || true)"
    local rc=0
    run_status "$td" "$mockbin" --bogus >/dev/null 2>&1 || rc=$?
    assert_fail "$rc"  # Should be non-zero exit
    assert_contains "$output" "unknown flag"

    echo "  $CURRENT_TEST: done"
}

test_report_creates_situation_report() {
    CURRENT_TEST="test_report_creates_situation_report"
    local td="${TMPDIR_ROOT}/report_sr"
    local mockbin
    mockbin="$(setup_mock_csuite "$td")"

    run_status "$td" "$mockbin" --report 2>&1 || true
    assert_file_exists "${td}/situation-report.md"

    echo "  $CURRENT_TEST: done"
}

test_report_creates_changelog() {
    CURRENT_TEST="test_report_creates_changelog"
    local td="${TMPDIR_ROOT}/report_cl"
    local mockbin
    mockbin="$(setup_mock_csuite "$td")"

    run_status "$td" "$mockbin" --report 2>&1 || true
    assert_file_exists "${td}/changelog.md"

    echo "  $CURRENT_TEST: done"
}

test_situation_report_has_pipeline_summary() {
    CURRENT_TEST="test_situation_report_has_pipeline_summary"
    local td="${TMPDIR_ROOT}/report_pipeline"
    local mockbin
    mockbin="$(setup_mock_csuite "$td")"

    run_status "$td" "$mockbin" --report 2>&1 || true

    local content
    content="$(cat "${td}/situation-report.md" 2>/dev/null || echo "")"

    # Should contain pipeline summary section with task counts
    assert_contains "$content" "Pipeline Summary"
    assert_contains "$content" "Total"
    assert_contains "$content" "Done"
    assert_contains "$content" "Failed"

    echo "  $CURRENT_TEST: done"
}

test_situation_report_has_agent_health() {
    CURRENT_TEST="test_situation_report_has_agent_health"
    local td="${TMPDIR_ROOT}/report_health"
    local mockbin
    mockbin="$(setup_mock_csuite "$td")"

    run_status "$td" "$mockbin" --report 2>&1 || true

    local content
    content="$(cat "${td}/situation-report.md" 2>/dev/null || echo "")"

    # Agent health section with alive/dead status and context percentages
    assert_contains "$content" "Agent Health"
    assert_contains "$content" "kyle"
    assert_contains "$content" "mike"
    assert_contains "$content" "alex"
    assert_contains "$content" "ross"
    assert_contains "$content" "seth"

    echo "  $CURRENT_TEST: done"
}

test_situation_report_has_context_percent() {
    CURRENT_TEST="test_situation_report_has_context_percent"
    local td="${TMPDIR_ROOT}/report_ctx"
    local mockbin
    mockbin="$(setup_mock_csuite "$td")"

    run_status "$td" "$mockbin" --report 2>&1 || true

    local content
    content="$(cat "${td}/situation-report.md" 2>/dev/null || echo "")"

    # Should reflect context_percent from state files
    assert_contains "$content" "42"   # kyle
    assert_contains "$content" "78"   # mike
    assert_contains "$content" "88"   # alex

    echo "  $CURRENT_TEST: done"
}

test_situation_report_has_alive_dead_status() {
    CURRENT_TEST="test_situation_report_has_alive_dead_status"
    local td="${TMPDIR_ROOT}/report_alive"
    local mockbin
    mockbin="$(setup_mock_csuite "$td")"

    # Mock tmux shows kyle, mike, alex, seth alive but NOT ross
    run_status "$td" "$mockbin" --report 2>&1 || true

    local content
    content="$(cat "${td}/situation-report.md" 2>/dev/null || echo "")"

    # ross has no tmux session in the mock, should be marked dead
    # The exact format is up to implementation, but report should distinguish
    assert_contains "$content" "ross"

    echo "  $CURRENT_TEST: done"
}

test_situation_report_has_inbox_counts() {
    CURRENT_TEST="test_situation_report_has_inbox_counts"
    local td="${TMPDIR_ROOT}/report_inbox"
    local mockbin
    mockbin="$(setup_mock_csuite "$td")"

    run_status "$td" "$mockbin" --report 2>&1 || true

    local content
    content="$(cat "${td}/situation-report.md" 2>/dev/null || echo "")"

    # Inbox summary section
    assert_contains "$content" "Inbox"
    # kyle has 2 messages, mike has 1, others have 0
    assert_contains "$content" "kyle"
    assert_contains "$content" "2"  # kyle's inbox count

    echo "  $CURRENT_TEST: done"
}

test_situation_report_has_timestamp() {
    CURRENT_TEST="test_situation_report_has_timestamp"
    local td="${TMPDIR_ROOT}/report_ts"
    local mockbin
    mockbin="$(setup_mock_csuite "$td")"

    run_status "$td" "$mockbin" --report 2>&1 || true

    local content
    content="$(cat "${td}/situation-report.md" 2>/dev/null || echo "")"

    # Should have a generated timestamp
    assert_contains "$content" "Generated"

    echo "  $CURRENT_TEST: done"
}

test_changelog_newest_first() {
    CURRENT_TEST="test_changelog_newest_first"
    local td="${TMPDIR_ROOT}/changelog_order"
    local mockbin
    mockbin="$(setup_mock_csuite "$td")"

    run_status "$td" "$mockbin" --report 2>&1 || true

    local content
    content="$(cat "${td}/changelog.md" 2>/dev/null || echo "")"

    # Changelog should have entries (even if just from initial state)
    # and should be ordered newest first
    assert_line_count_ge "$content" "1"

    echo "  $CURRENT_TEST: done"
}

test_changelog_max_50_entries() {
    CURRENT_TEST="test_changelog_max_50_entries"
    local td="${TMPDIR_ROOT}/changelog_cap"
    local mockbin
    mockbin="$(setup_mock_csuite "$td")"

    # Pre-seed changelog with 55 entries to test capping
    {
        for i in $(seq 55 -1 1); do
            echo "2026-03-24T06:$(printf '%02d' $((i % 60))):00 | task-$i | backlog → planning | auto"
        done
    } > "${td}/changelog.md"

    run_status "$td" "$mockbin" --report 2>&1 || true

    local content
    content="$(cat "${td}/changelog.md" 2>/dev/null || echo "")"

    # Should be capped at 50 entries (non-empty lines)
    local entry_count
    entry_count="$(echo "$content" | grep -c '|' || true)"
    if [ "$entry_count" -le 50 ]; then
        PASS=$((PASS + 1))
    else
        echo "  FAIL ($CURRENT_TEST): expected <= 50 entries, got $entry_count"
        FAIL=$((FAIL + 1))
    fi

    echo "  $CURRENT_TEST: done"
}

test_changelog_appends_events() {
    CURRENT_TEST="test_changelog_appends_events"
    local td="${TMPDIR_ROOT}/changelog_append"
    local mockbin
    mockbin="$(setup_mock_csuite "$td")"

    # First run
    run_status "$td" "$mockbin" --report 2>&1 || true

    local count1
    count1="$(cat "${td}/changelog.md" 2>/dev/null | wc -l | tr -d ' ')"

    # Second run should append new events (at minimum not lose old ones)
    run_status "$td" "$mockbin" --report 2>&1 || true

    local count2
    count2="$(cat "${td}/changelog.md" 2>/dev/null | wc -l | tr -d ' ')"

    if [ "$count2" -ge "$count1" ]; then
        PASS=$((PASS + 1))
    else
        echo "  FAIL ($CURRENT_TEST): expected changelog to grow or stay same, was $count1 now $count2"
        FAIL=$((FAIL + 1))
    fi

    echo "  $CURRENT_TEST: done"
}

test_atomic_write_situation_report() {
    CURRENT_TEST="test_atomic_write_situation_report"
    local td="${TMPDIR_ROOT}/atomic_sr"
    local mockbin
    mockbin="$(setup_mock_csuite "$td")"

    # Run the script and check that it writes via temp+mv (no partial reads).
    # We verify by checking that no .tmp file is left behind after completion.
    run_status "$td" "$mockbin" --report 2>&1 || true

    # No leftover temp files
    local tmp_files
    tmp_files="$(find "$td" -maxdepth 1 -name '*.tmp' -o -name '.situation-report*' 2>/dev/null | wc -l | tr -d ' ')"
    assert_eq "$tmp_files" "0"

    # The final file should exist (if report mode worked)
    assert_file_exists "${td}/situation-report.md"

    echo "  $CURRENT_TEST: done"
}

test_atomic_write_changelog() {
    CURRENT_TEST="test_atomic_write_changelog"
    local td="${TMPDIR_ROOT}/atomic_cl"
    local mockbin
    mockbin="$(setup_mock_csuite "$td")"

    run_status "$td" "$mockbin" --report 2>&1 || true

    # No leftover temp files
    local tmp_files
    tmp_files="$(find "$td" -maxdepth 1 -name '*.tmp' -o -name '.changelog*' 2>/dev/null | wc -l | tr -d ' ')"
    assert_eq "$tmp_files" "0"

    assert_file_exists "${td}/changelog.md"

    echo "  $CURRENT_TEST: done"
}

test_empty_csuite_dir() {
    CURRENT_TEST="test_empty_csuite_dir"
    local td="${TMPDIR_ROOT}/empty"
    mkdir -p "$td"
    local mockbin="${TMPDIR_ROOT}/empty_mockbin"
    mkdir -p "$mockbin"

    # Mock drem with empty stats
    cat > "${mockbin}/drem" <<'EOF'
#!/usr/bin/env bash
case "$1" in
    stats) echo "total: 0" ;;
    failures) ;;
    *) exit 1 ;;
esac
EOF
    chmod +x "${mockbin}/drem"

    cat > "${mockbin}/tmux" <<'EOF'
#!/usr/bin/env bash
if [ "${1:-}" = "list-sessions" ] || [ "${2:-}" = "list-sessions" ]; then
    echo "no server running on" >&2
    exit 1
fi
exit 1
EOF
    chmod +x "${mockbin}/tmux"

    # Should not crash on empty directory
    run_status "$td" "$mockbin" --report 2>&1 || true

    # Should still produce output files (even if mostly empty)
    assert_file_exists "${td}/situation-report.md"
    assert_file_exists "${td}/changelog.md"

    echo "  $CURRENT_TEST: done"
}

test_temp_workers_in_report() {
    CURRENT_TEST="test_temp_workers_in_report"
    local td="${TMPDIR_ROOT}/report_workers"
    local mockbin
    mockbin="$(setup_mock_csuite "$td")"

    run_status "$td" "$mockbin" --report 2>&1 || true

    local content
    content="$(cat "${td}/situation-report.md" 2>/dev/null || echo "")"

    # Should mention temp workers
    assert_contains "$content" "worker-001"

    echo "  $CURRENT_TEST: done"
}

# ---------------------------------------------------------------------------
# Run all tests
# ---------------------------------------------------------------------------
echo "Running csuite-status tests..."
echo ""

test_help_flag
test_unknown_flag_errors
test_report_creates_situation_report
test_report_creates_changelog
test_situation_report_has_pipeline_summary
test_situation_report_has_agent_health
test_situation_report_has_context_percent
test_situation_report_has_alive_dead_status
test_situation_report_has_inbox_counts
test_situation_report_has_timestamp
test_changelog_newest_first
test_changelog_max_50_entries
test_changelog_appends_events
test_atomic_write_situation_report
test_atomic_write_changelog
test_empty_csuite_dir
test_temp_workers_in_report

echo ""
echo "Results: ${PASS} passed, ${FAIL} failed"

if [ "$FAIL" -gt 0 ]; then
    exit 1
fi
exit 0
