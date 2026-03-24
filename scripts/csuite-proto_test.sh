#!/usr/bin/env bash
set -euo pipefail

# Tests for csuite-proto.sh
# Usage: bash scripts/csuite-proto_test.sh
# Exit code: 0 if all tests pass, 1 if any fail.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# ---------------------------------------------------------------------------
# Test harness
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

assert_dir_exists() {
    if [ -d "$1" ]; then
        PASS=$((PASS + 1))
    else
        echo "  FAIL ($CURRENT_TEST): directory does not exist: $1"
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

assert_line_count() {
    local actual
    actual="$(echo "$1" | wc -l | tr -d ' ')"
    if [ "$actual" = "$2" ]; then
        PASS=$((PASS + 1))
    else
        echo "  FAIL ($CURRENT_TEST): expected $2 lines, got $actual"
        FAIL=$((FAIL + 1))
    fi
}

# ---------------------------------------------------------------------------
# Setup / teardown
# ---------------------------------------------------------------------------
TMPDIR_ROOT="$(mktemp -d)"
cleanup() { rm -rf "$TMPDIR_ROOT"; }
trap cleanup EXIT

# ---------------------------------------------------------------------------
# Tests
# ---------------------------------------------------------------------------

test_bootstrap() {
    CURRENT_TEST="test_bootstrap"
    local td="${TMPDIR_ROOT}/bootstrap"
    mkdir -p "$td"

    # Run bootstrap
    CSUITE_DIR="$td" bash "${SCRIPT_DIR}/csuite-bootstrap.sh" >/dev/null

    # Verify directories
    for agent in kyle mike alex ross seth; do
        assert_dir_exists "${td}/${agent}/inbox"
        assert_dir_exists "${td}/${agent}/inbox/archive"
        assert_dir_exists "${td}/${agent}/outbox"
        assert_file_exists "${td}/${agent}/state.md"
    done
    assert_dir_exists "${td}/temp-workers"

    # Idempotency: write something to a state.md, run again, verify not overwritten
    echo "existing content" > "${td}/kyle/state.md"
    CSUITE_DIR="$td" bash "${SCRIPT_DIR}/csuite-bootstrap.sh" >/dev/null
    local content
    content="$(cat "${td}/kyle/state.md")"
    assert_eq "$content" "existing content"

    echo "  $CURRENT_TEST: done"
}

test_send_creates_message() {
    CURRENT_TEST="test_send_creates_message"
    local td="${TMPDIR_ROOT}/send"
    CSUITE_DIR="$td" bash "${SCRIPT_DIR}/csuite-bootstrap.sh" >/dev/null

    export CSUITE_DIR="$td"
    source "${SCRIPT_DIR}/csuite-proto.sh"

    local result
    result="$(csuite_send mike alex "Test subject" high observation "This is the body.")"
    local rc=$?
    assert_ok "$rc"

    # File should exist
    assert_file_exists "$result"

    # Filename should match pattern: YYYYMMDD-HHMMSS-mike.md
    local fname
    fname="$(basename "$result")"
    if echo "$fname" | grep -qE '^[0-9]{8}-[0-9]{6}-mike\.md$'; then
        PASS=$((PASS + 1))
    else
        echo "  FAIL ($CURRENT_TEST): filename '$fname' does not match expected pattern"
        FAIL=$((FAIL + 1))
    fi

    # File should be in alex's inbox
    assert_contains "$result" "${td}/alex/inbox/"

    # Frontmatter fields
    local content
    content="$(cat "$result")"
    assert_contains "$content" "from: mike"
    assert_contains "$content" "to: alex"
    assert_contains "$content" "subject: \"Test subject\""
    assert_contains "$content" "priority: high"
    assert_contains "$content" "type: observation"

    # Body content
    assert_contains "$content" "This is the body."

    unset CSUITE_DIR
    echo "  $CURRENT_TEST: done"
}

test_inbox_lists_messages() {
    CURRENT_TEST="test_inbox_lists_messages"
    local td="${TMPDIR_ROOT}/inbox"
    CSUITE_DIR="$td" bash "${SCRIPT_DIR}/csuite-bootstrap.sh" >/dev/null

    export CSUITE_DIR="$td"
    source "${SCRIPT_DIR}/csuite-proto.sh"

    # Send 3 messages to kyle from different agents with slight time gaps
    csuite_send mike kyle "msg1" normal observation "body1" >/dev/null
    sleep 1
    csuite_send alex kyle "msg2" high request "body2" >/dev/null
    sleep 1
    csuite_send seth kyle "msg3" low report "body3" >/dev/null

    local listing
    listing="$(csuite_inbox kyle)"
    assert_line_count "$listing" "3"

    # Verify chronological order (first line should contain mike, last should contain seth)
    local first_line last_line
    first_line="$(echo "$listing" | head -1)"
    last_line="$(echo "$listing" | tail -1)"
    assert_contains "$first_line" "mike"
    assert_contains "$last_line" "seth"

    unset CSUITE_DIR
    echo "  $CURRENT_TEST: done"
}

test_archive_moves_message() {
    CURRENT_TEST="test_archive_moves_message"
    local td="${TMPDIR_ROOT}/archive"
    CSUITE_DIR="$td" bash "${SCRIPT_DIR}/csuite-bootstrap.sh" >/dev/null

    export CSUITE_DIR="$td"
    source "${SCRIPT_DIR}/csuite-proto.sh"

    local msgpath
    msgpath="$(csuite_send mike alex "archive test" normal observation "archive body")"
    local fname
    fname="$(basename "$msgpath")"

    # Archive it
    csuite_archive alex "$fname"
    local rc=$?
    assert_ok "$rc"

    # Should no longer be in inbox listing
    local listing
    listing="$(csuite_inbox alex)"
    if [ -z "$listing" ]; then
        PASS=$((PASS + 1))
    else
        if echo "$listing" | grep -qF "$fname"; then
            echo "  FAIL ($CURRENT_TEST): archived message still in inbox"
            FAIL=$((FAIL + 1))
        else
            PASS=$((PASS + 1))
        fi
    fi

    # Should exist in archive
    assert_file_exists "${td}/alex/inbox/archive/${fname}"

    unset CSUITE_DIR
    echo "  $CURRENT_TEST: done"
}

test_field_parsing() {
    CURRENT_TEST="test_field_parsing"
    local td="${TMPDIR_ROOT}/field"
    CSUITE_DIR="$td" bash "${SCRIPT_DIR}/csuite-bootstrap.sh" >/dev/null

    export CSUITE_DIR="$td"
    source "${SCRIPT_DIR}/csuite-proto.sh"

    # Create a message with known frontmatter
    local msgpath
    msgpath="$(csuite_send mike alex "Quoted subject value" high decision "field test body")"

    # Test field extraction
    local val

    val="$(csuite_field "$msgpath" from)"
    assert_eq "$val" "mike"

    val="$(csuite_field "$msgpath" to)"
    assert_eq "$val" "alex"

    val="$(csuite_field "$msgpath" subject)"
    assert_eq "$val" "Quoted subject value"

    val="$(csuite_field "$msgpath" priority)"
    assert_eq "$val" "high"

    val="$(csuite_field "$msgpath" type)"
    assert_eq "$val" "decision"

    # Timestamp should be a valid ISO 8601 format
    val="$(csuite_field "$msgpath" timestamp)"
    if echo "$val" | grep -qE '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$'; then
        PASS=$((PASS + 1))
    else
        echo "  FAIL ($CURRENT_TEST): timestamp '$val' is not valid ISO 8601"
        FAIL=$((FAIL + 1))
    fi

    # Test unquoted value (from, to, priority, type are all unquoted)
    val="$(csuite_field "$msgpath" from)"
    assert_eq "$val" "mike"

    unset CSUITE_DIR
    echo "  $CURRENT_TEST: done"
}

test_heartbeat_and_alive() {
    CURRENT_TEST="test_heartbeat_and_alive"
    local td="${TMPDIR_ROOT}/heartbeat"
    CSUITE_DIR="$td" bash "${SCRIPT_DIR}/csuite-bootstrap.sh" >/dev/null

    export CSUITE_DIR="$td"
    source "${SCRIPT_DIR}/csuite-proto.sh"

    # Write heartbeat
    csuite_heartbeat kyle
    local rc=$?
    assert_ok "$rc"

    # Should be alive with a 60-second window
    csuite_is_alive kyle 60
    rc=$?
    assert_ok "$rc"

    # Sleep 2 seconds, then check with a 1-second window — should be stale
    sleep 2

    csuite_is_alive kyle 1 || true
    rc=$?
    # We need to capture the actual exit code properly
    if csuite_is_alive kyle 1 2>/dev/null; then
        echo "  FAIL ($CURRENT_TEST): expected stale heartbeat (exit 1), got alive (exit 0)"
        FAIL=$((FAIL + 1))
    else
        PASS=$((PASS + 1))
    fi

    unset CSUITE_DIR
    echo "  $CURRENT_TEST: done"
}

test_create_worker() {
    CURRENT_TEST="test_create_worker"
    local td="${TMPDIR_ROOT}/worker"
    CSUITE_DIR="$td" bash "${SCRIPT_DIR}/csuite-bootstrap.sh" >/dev/null

    export CSUITE_DIR="$td"
    source "${SCRIPT_DIR}/csuite-proto.sh"

    local result
    result="$(csuite_create_worker worker-001)"
    local rc=$?
    assert_ok "$rc"

    assert_dir_exists "${td}/temp-workers/worker-001/inbox"
    assert_dir_exists "${td}/temp-workers/worker-001/inbox/archive"
    assert_dir_exists "${td}/temp-workers/worker-001/outbox"
    assert_file_exists "${td}/temp-workers/worker-001/state.md"

    unset CSUITE_DIR
    echo "  $CURRENT_TEST: done"
}

test_list_workers() {
    CURRENT_TEST="test_list_workers"
    local td="${TMPDIR_ROOT}/list_workers"
    CSUITE_DIR="$td" bash "${SCRIPT_DIR}/csuite-bootstrap.sh" >/dev/null

    export CSUITE_DIR="$td"
    source "${SCRIPT_DIR}/csuite-proto.sh"

    csuite_create_worker worker-001 >/dev/null
    csuite_create_worker worker-002 >/dev/null

    local listing
    listing="$(csuite_list_workers)"

    assert_line_count "$listing" "2"
    assert_contains "$listing" "worker-001"
    assert_contains "$listing" "worker-002"

    unset CSUITE_DIR
    echo "  $CURRENT_TEST: done"
}

# ---------------------------------------------------------------------------
# Run all tests
# ---------------------------------------------------------------------------
echo "Running csuite-proto tests..."
echo ""

test_bootstrap
test_send_creates_message
test_inbox_lists_messages
test_archive_moves_message
test_field_parsing
test_heartbeat_and_alive
test_create_worker
test_list_workers

echo ""
echo "Results: ${PASS} passed, ${FAIL} failed"

if [ "$FAIL" -gt 0 ]; then
    exit 1
fi
exit 0
