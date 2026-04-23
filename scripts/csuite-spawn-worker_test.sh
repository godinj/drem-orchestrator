#!/usr/bin/env bash
set -euo pipefail

# Tests for csuite-spawn-worker.sh
# Usage: bash scripts/csuite-spawn-worker_test.sh
# Exit code: 0 if all tests pass, 1 if any fail.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SPAWN_SCRIPT="${SCRIPT_DIR}/csuite-spawn-worker.sh"

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

assert_dir_exists() {
    if [ -d "$1" ]; then
        PASS=$((PASS + 1))
    else
        echo "  FAIL ($CURRENT_TEST): directory does not exist: $1"
        FAIL=$((FAIL + 1))
    fi
}

assert_contains() {
    if echo "$1" | grep -qF -- "$2"; then
        PASS=$((PASS + 1))
    else
        echo "  FAIL ($CURRENT_TEST): output does not contain '$2'"
        FAIL=$((FAIL + 1))
    fi
}

assert_not_contains() {
    if ! echo "$1" | grep -qF -- "$2"; then
        PASS=$((PASS + 1))
    else
        echo "  FAIL ($CURRENT_TEST): output should not contain '$2'"
        FAIL=$((FAIL + 1))
    fi
}

# ---------------------------------------------------------------------------
# Setup / teardown
# ---------------------------------------------------------------------------
TMPDIR_ROOT="$(mktemp -d)"
cleanup() { rm -rf "$TMPDIR_ROOT"; }
trap cleanup EXIT

# Create mock environment for spawn tests.
# Sets up CSUITE_DIR, mock bins for tmux/claude, and a task brief file.
# Outputs the mockbin path on stdout.
setup_spawn_env() {
    local td="$1"
    mkdir -p "$td"

    # Bootstrap agent directories
    CSUITE_DIR="$td" bash "${SCRIPT_DIR}/csuite-bootstrap.sh" >/dev/null

    # Create a sample task brief
    cat > "${td}/task-brief.md" <<'EOF'
## Task Brief: Test task

### Objective
This is a test task brief for spawn worker testing.

### Success Criteria
- Tests pass
EOF

    # Create mock bin directory
    local mockbin="${td}/.mockbin"
    mkdir -p "$mockbin"

    # Mock tmux — records commands to a log file instead of running them
    cat > "${mockbin}/tmux" <<'TMUX_EOF'
#!/usr/bin/env bash
# Log all tmux calls for verification
echo "$@" >> "${CSUITE_DIR}/.tmux-log"

case "${1:-}" in
    -L)
        # Handle: tmux -L drem has-session -t <name>
        if [ "${3:-}" = "has-session" ]; then
            # Check if we should simulate an existing session
            if [ -f "${CSUITE_DIR}/.simulate-existing-session" ]; then
                exit 0  # session exists
            fi
            exit 1  # session does not exist
        fi
        # Handle: tmux -L drem new-session ...
        if [ "${3:-}" = "new-session" ]; then
            exit 0
        fi
        ;;
esac
exit 0
TMUX_EOF
    chmod +x "${mockbin}/tmux"

    # Mock claude — just exit successfully
    cat > "${mockbin}/claude" <<'CLAUDE_EOF'
#!/usr/bin/env bash
exit 0
CLAUDE_EOF
    chmod +x "${mockbin}/claude"

    echo "$mockbin"
}

# Run the spawn script with mocked environment.
# Usage: run_spawn <csuite_dir> <mockbin> [args...]
run_spawn() {
    local csuite_dir="$1"
    local mockbin="$2"
    shift 2
    CSUITE_DIR="$csuite_dir" PATH="${mockbin}:${PATH}" bash "$SPAWN_SCRIPT" "$@" 2>&1
}

# Run the spawn script, capture exit code separately.
# Usage: run_spawn_rc <csuite_dir> <mockbin> [args...]
# Sets RC variable with the exit code.
run_spawn_rc() {
    local csuite_dir="$1"
    local mockbin="$2"
    shift 2
    RC=0
    CSUITE_DIR="$csuite_dir" PATH="${mockbin}:${PATH}" bash "$SPAWN_SCRIPT" "$@" 2>&1 || RC=$?
}

# ---------------------------------------------------------------------------
# Tests
# ---------------------------------------------------------------------------

test_help_flag() {
    CURRENT_TEST="test_help_flag"
    local td="${TMPDIR_ROOT}/help"
    local mockbin
    mockbin="$(setup_spawn_env "$td")"

    local output
    output="$(run_spawn "$td" "$mockbin" --help)"
    local rc=$?
    assert_ok "$rc"
    assert_contains "$output" "Usage"
    assert_contains "$output" "task-brief-file"
    assert_contains "$output" "--worker-id"
    assert_contains "$output" "--dry-run"

    echo "  $CURRENT_TEST: done"
}

test_missing_task_brief_errors() {
    CURRENT_TEST="test_missing_task_brief_errors"
    local td="${TMPDIR_ROOT}/missing_brief"
    local mockbin
    mockbin="$(setup_spawn_env "$td")"

    run_spawn_rc "$td" "$mockbin"
    assert_fail "$RC"

    echo "  $CURRENT_TEST: done"
}

test_nonexistent_task_brief_errors() {
    CURRENT_TEST="test_nonexistent_task_brief_errors"
    local td="${TMPDIR_ROOT}/nofile"
    local mockbin
    mockbin="$(setup_spawn_env "$td")"

    local output
    run_spawn_rc "$td" "$mockbin" "/nonexistent/file.md"
    assert_fail "$RC"

    echo "  $CURRENT_TEST: done"
}

test_unknown_flag_errors() {
    CURRENT_TEST="test_unknown_flag_errors"
    local td="${TMPDIR_ROOT}/unknown_flag"
    local mockbin
    mockbin="$(setup_spawn_env "$td")"

    run_spawn_rc "$td" "$mockbin" --bogus
    assert_fail "$RC"

    echo "  $CURRENT_TEST: done"
}

test_auto_assigns_worker_id() {
    CURRENT_TEST="test_auto_assigns_worker_id"
    local td="${TMPDIR_ROOT}/auto_id"
    local mockbin
    mockbin="$(setup_spawn_env "$td")"

    local output
    output="$(run_spawn "$td" "$mockbin" "${td}/task-brief.md")"
    local rc=$?
    assert_ok "$rc"

    # First worker should be worker-001
    assert_contains "$output" "worker-001"
    assert_dir_exists "${td}/temp-workers/worker-001"

    echo "  $CURRENT_TEST: done"
}

test_auto_id_increments() {
    CURRENT_TEST="test_auto_id_increments"
    local td="${TMPDIR_ROOT}/auto_increment"
    local mockbin
    mockbin="$(setup_spawn_env "$td")"

    # Pre-create worker-001 and worker-002
    mkdir -p "${td}/temp-workers/worker-001"
    mkdir -p "${td}/temp-workers/worker-002"

    local output
    output="$(run_spawn "$td" "$mockbin" "${td}/task-brief.md")"
    local rc=$?
    assert_ok "$rc"

    # Next worker should be worker-003
    assert_contains "$output" "worker-003"
    assert_dir_exists "${td}/temp-workers/worker-003"

    echo "  $CURRENT_TEST: done"
}

test_manual_worker_id() {
    CURRENT_TEST="test_manual_worker_id"
    local td="${TMPDIR_ROOT}/manual_id"
    local mockbin
    mockbin="$(setup_spawn_env "$td")"

    local output
    output="$(run_spawn "$td" "$mockbin" "${td}/task-brief.md" --worker-id worker-042)"
    local rc=$?
    assert_ok "$rc"

    assert_contains "$output" "worker-042"
    assert_dir_exists "${td}/temp-workers/worker-042"

    echo "  $CURRENT_TEST: done"
}

test_creates_worker_directory_structure() {
    CURRENT_TEST="test_creates_worker_directory_structure"
    local td="${TMPDIR_ROOT}/dir_structure"
    local mockbin
    mockbin="$(setup_spawn_env "$td")"

    run_spawn "$td" "$mockbin" "${td}/task-brief.md" --worker-id worker-010 >/dev/null

    assert_dir_exists "${td}/temp-workers/worker-010/inbox"
    assert_dir_exists "${td}/temp-workers/worker-010/inbox/archive"
    assert_dir_exists "${td}/temp-workers/worker-010/outbox"
    assert_file_exists "${td}/temp-workers/worker-010/state.md"

    echo "  $CURRENT_TEST: done"
}

test_copies_task_brief_to_inbox() {
    CURRENT_TEST="test_copies_task_brief_to_inbox"
    local td="${TMPDIR_ROOT}/copy_brief"
    local mockbin
    mockbin="$(setup_spawn_env "$td")"

    run_spawn "$td" "$mockbin" "${td}/task-brief.md" --worker-id worker-010 >/dev/null

    assert_file_exists "${td}/temp-workers/worker-010/inbox/task-brief.md"

    # Content should match the original
    local original copied
    original="$(cat "${td}/task-brief.md")"
    copied="$(cat "${td}/temp-workers/worker-010/inbox/task-brief.md")"
    assert_eq "$copied" "$original"

    echo "  $CURRENT_TEST: done"
}

test_state_file_has_metadata() {
    CURRENT_TEST="test_state_file_has_metadata"
    local td="${TMPDIR_ROOT}/state_meta"
    local mockbin
    mockbin="$(setup_spawn_env "$td")"

    run_spawn "$td" "$mockbin" "${td}/task-brief.md" --worker-id worker-010 >/dev/null

    local state
    state="$(cat "${td}/temp-workers/worker-010/state.md")"
    assert_contains "$state" "requester: mike"
    assert_contains "$state" "status: active"
    assert_contains "$state" "spawned:"

    echo "  $CURRENT_TEST: done"
}

test_tmux_session_launched() {
    CURRENT_TEST="test_tmux_session_launched"
    local td="${TMPDIR_ROOT}/tmux_launch"
    local mockbin
    mockbin="$(setup_spawn_env "$td")"

    run_spawn "$td" "$mockbin" "${td}/task-brief.md" --worker-id worker-010 >/dev/null

    # Check tmux log for new-session call
    assert_file_exists "${td}/.tmux-log"
    local tmux_log
    tmux_log="$(cat "${td}/.tmux-log")"
    assert_contains "$tmux_log" "new-session"
    assert_contains "$tmux_log" "csuite-worker-010"

    echo "  $CURRENT_TEST: done"
}

test_existing_session_blocked() {
    CURRENT_TEST="test_existing_session_blocked"
    local td="${TMPDIR_ROOT}/existing_session"
    local mockbin
    mockbin="$(setup_spawn_env "$td")"

    # Signal mock tmux to report that the session already exists
    touch "${td}/.simulate-existing-session"

    run_spawn_rc "$td" "$mockbin" "${td}/task-brief.md" --worker-id worker-010
    assert_fail "$RC"

    echo "  $CURRENT_TEST: done"
}

test_dry_run_no_side_effects() {
    CURRENT_TEST="test_dry_run_no_side_effects"
    local td="${TMPDIR_ROOT}/dry_run"
    local mockbin
    mockbin="$(setup_spawn_env "$td")"

    local output
    output="$(run_spawn "$td" "$mockbin" "${td}/task-brief.md" --dry-run)"
    local rc=$?
    assert_ok "$rc"

    assert_contains "$output" "DRY RUN"
    assert_contains "$output" "worker-001"

    # No worker directory should have been created
    if [ -d "${td}/temp-workers/worker-001" ]; then
        echo "  FAIL ($CURRENT_TEST): worker directory created during dry run"
        FAIL=$((FAIL + 1))
    else
        PASS=$((PASS + 1))
    fi

    # No tmux log
    if [ -f "${td}/.tmux-log" ]; then
        echo "  FAIL ($CURRENT_TEST): tmux called during dry run"
        FAIL=$((FAIL + 1))
    else
        PASS=$((PASS + 1))
    fi

    echo "  $CURRENT_TEST: done"
}

test_worker_id_flag_requires_value() {
    CURRENT_TEST="test_worker_id_flag_requires_value"
    local td="${TMPDIR_ROOT}/id_no_value"
    local mockbin
    mockbin="$(setup_spawn_env "$td")"

    run_spawn_rc "$td" "$mockbin" "${td}/task-brief.md" --worker-id
    assert_fail "$RC"

    echo "  $CURRENT_TEST: done"
}

# ---------------------------------------------------------------------------
# Integration test: end-to-end spawn workflow
# ---------------------------------------------------------------------------

test_e2e_spawn_workflow() {
    CURRENT_TEST="test_e2e_spawn_workflow"
    local td="${TMPDIR_ROOT}/e2e"
    local mockbin
    mockbin="$(setup_spawn_env "$td")"

    # Write a realistic task brief
    cat > "${td}/e2e-brief.md" <<'BRIEF_EOF'
## Task Brief: Implement widget feature

### Objective
Build the widget feature for the dashboard.

### Subtasks
1. Write widget component
2. Add tests
3. Update docs

### Success Criteria
- All tests pass
- Documentation updated
BRIEF_EOF

    # Step 1: Spawn with auto-assigned ID
    local output
    output="$(run_spawn "$td" "$mockbin" "${td}/e2e-brief.md")"
    local rc=$?
    assert_ok "$rc"

    # Step 2: Verify worker ID is worker-001 (first spawn)
    assert_contains "$output" "worker-001"

    # Step 3: Verify full directory structure
    assert_dir_exists "${td}/temp-workers/worker-001/inbox"
    assert_dir_exists "${td}/temp-workers/worker-001/inbox/archive"
    assert_dir_exists "${td}/temp-workers/worker-001/outbox"
    assert_file_exists "${td}/temp-workers/worker-001/state.md"

    # Step 4: Verify task brief was copied to inbox
    assert_file_exists "${td}/temp-workers/worker-001/inbox/e2e-brief.md"
    local brief_content
    brief_content="$(cat "${td}/temp-workers/worker-001/inbox/e2e-brief.md")"
    assert_contains "$brief_content" "Implement widget feature"
    assert_contains "$brief_content" "Write widget component"

    # Step 5: Verify state file has correct metadata
    local state_content
    state_content="$(cat "${td}/temp-workers/worker-001/state.md")"
    assert_contains "$state_content" "requester: mike"
    assert_contains "$state_content" "status: active"
    assert_contains "$state_content" "spawned:"
    assert_contains "$state_content" "Implement widget feature"

    # Step 6: Verify tmux new-session was called with correct session name
    assert_file_exists "${td}/.tmux-log"
    local tmux_log
    tmux_log="$(cat "${td}/.tmux-log")"
    assert_contains "$tmux_log" "new-session"
    assert_contains "$tmux_log" "csuite-worker-001"

    # Step 7: Spawn a second worker — should get worker-002
    local output2
    output2="$(run_spawn "$td" "$mockbin" "${td}/e2e-brief.md")"
    rc=$?
    assert_ok "$rc"
    assert_contains "$output2" "worker-002"
    assert_dir_exists "${td}/temp-workers/worker-002"

    # Step 8: Verify both workers are listed via csuite_list_workers
    export CSUITE_DIR="$td"
    source "${SCRIPT_DIR}/csuite-proto.sh"
    local workers
    workers="$(csuite_list_workers)"
    assert_contains "$workers" "worker-001"
    assert_contains "$workers" "worker-002"
    unset CSUITE_DIR

    # Step 9: Verify csuite-status.sh can see the workers (if available)
    if [ -f "${SCRIPT_DIR}/csuite-status.sh" ]; then
        local status_output
        status_output="$(CSUITE_DIR="$td" PATH="${mockbin}:${PATH}" bash "${SCRIPT_DIR}/csuite-status.sh" --report 2>&1 || true)"
        local report
        report="$(cat "${td}/situation-report.md" 2>/dev/null || echo "")"
        assert_contains "$report" "worker-001"
        assert_contains "$report" "worker-002"
    fi

    echo "  $CURRENT_TEST: done"
}

# ---------------------------------------------------------------------------
# Run all tests
# ---------------------------------------------------------------------------
echo "Running csuite-spawn-worker tests..."
echo ""

test_help_flag
test_missing_task_brief_errors
test_nonexistent_task_brief_errors
test_unknown_flag_errors
test_auto_assigns_worker_id
test_auto_id_increments
test_manual_worker_id
test_creates_worker_directory_structure
test_copies_task_brief_to_inbox
test_state_file_has_metadata
test_tmux_session_launched
test_existing_session_blocked
test_dry_run_no_side_effects
test_worker_id_flag_requires_value
test_e2e_spawn_workflow

echo ""
echo "Results: ${PASS} passed, ${FAIL} failed"

if [ "$FAIL" -gt 0 ]; then
    exit 1
fi
exit 0
