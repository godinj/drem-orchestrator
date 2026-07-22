#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CANARY_SCRIPT="${SCRIPT_DIR}/drem-capacity-canary.sh"

PASS=0
FAIL=0
CURRENT_TEST=""

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

assert_contains() {
    if printf '%s' "$1" | grep -qF -- "$2"; then
        PASS=$((PASS + 1))
    else
        echo "  FAIL ($CURRENT_TEST): output does not contain '$2'"
        echo "  output: $1"
        FAIL=$((FAIL + 1))
    fi
}

assert_not_contains() {
    if ! printf '%s' "$1" | grep -qF -- "$2"; then
        PASS=$((PASS + 1))
    else
        echo "  FAIL ($CURRENT_TEST): output should not contain '$2'"
        echo "  output: $1"
        FAIL=$((FAIL + 1))
    fi
}

TMPDIR_ROOT="$(mktemp -d)"
cleanup() { rm -rf "$TMPDIR_ROOT"; }
trap cleanup EXIT

setup_stubs() {
    local dir="$1"
    mkdir -p "$dir/bin"

    cat > "$dir/bin/dremctl" <<'DREMCTL_EOF'
#!/usr/bin/env bash
set -euo pipefail
log_file="${DREMCTL_LOG:?DREMCTL_LOG required}"
printf '%s\n' "$*" >> "$log_file"

for arg in "$@"; do
    case "$arg" in
        approve|reject|pass|fail|retry|archive|restart)
            echo "forbidden dremctl command: $arg" >&2
            exit 99
            ;;
    esac
done

cmd=""
prev=""
for arg in "$@"; do
    case "$prev" in
        --orch-url|--project|--actor|--title|--description|--limit|--since)
            prev=""
            continue
            ;;
    esac
    case "$arg" in
        --orch-url|--project|--actor|--title|--description|--limit|--since|--json)
            prev="$arg"
            ;;
        create|tasks|events)
            cmd="$arg"
            break
            ;;
    esac
done

case "$cmd" in
    create)
        cat <<'JSON'
{
  "id": "11111111-1111-4111-8111-111111111111",
  "title": "capacity-canary",
  "status": "backlog",
  "created_at": "2026-04-30T10:00:00Z",
  "updated_at": "2026-04-30T10:00:00Z",
  "assigned_worker": ""
}
JSON
        ;;
    tasks)
        if [ "${FAKE_CANARY_MODE:-done}" = "timeout" ]; then
            cat <<'JSON'
[
  {"id":"11111111-1111-4111-8111-111111111111","title":"capacity-canary","status":"backlog","created_at":"2026-04-30T10:00:00Z","updated_at":"2026-04-30T10:00:00Z","assigned_worker":""}
]
JSON
        else
            cat <<'JSON'
[
  {"id":"11111111-1111-4111-8111-111111111111","title":"capacity-canary","status":"done","created_at":"2026-04-30T10:00:00Z","updated_at":"2026-04-30T10:01:00Z","assigned_worker":"worker-1"}
]
JSON
        fi
        ;;
    events)
        if [ "${FAKE_CANARY_MODE:-done}" = "timeout" ]; then
            printf '[]\n'
        else
            cat <<'JSON'
[
  {"timestamp":"2026-04-30T10:00:30Z","type":"task.status","payload":{"task_id":"11111111-1111-4111-8111-111111111111","from":"in_progress","to":"done"}}
]
JSON
        fi
        ;;
    *)
        echo "unexpected dremctl args: $*" >&2
        exit 2
        ;;
esac
DREMCTL_EOF
    chmod +x "$dir/bin/dremctl"
}

run_canary() {
    local mode="$1" confirm="$2" out_file="$3" log_file="$4"
    set +e
    FAKE_CANARY_MODE="$mode" \
    PATH="$TMPDIR_ROOT/bin:$PATH" \
    DREM_ORCH_URL="http://fake-orch" \
    DREM_PROJECT="canvas" \
	DREM_ACTOR="canary:capacity:test" \
    DREMCTL_BIN="dremctl" \
    DREMCTL_LOG="$log_file" \
    DREM_CAPACITY_CANARY_CONFIRM="$confirm" \
    DREM_CAPACITY_CANARY_TIMEOUT="1s" \
    DREM_CAPACITY_CANARY_INTERVAL="0s" \
    bash "$CANARY_SCRIPT" > "$out_file" 2>"$out_file.err"
    local rc=$?
    set -e
    return "$rc"
}

setup_stubs "$TMPDIR_ROOT"

CURRENT_TEST="refusal without confirmation"
refusal_out="$TMPDIR_ROOT/refusal.json"
refusal_log="$TMPDIR_ROOT/refusal.log"
if run_canary done no "$refusal_out" "$refusal_log"; then rc=0; else rc=$?; fi
refusal_json="$(cat "$refusal_out")"
assert_fail "$rc"
assert_contains "$refusal_json" '"ok":false'
assert_contains "$refusal_json" 'DREM_CAPACITY_CANARY_CONFIRM=yes'
if [ -s "$refusal_log" ]; then
    echo "  FAIL ($CURRENT_TEST): dremctl should not be called without confirmation"
    FAIL=$((FAIL + 1))
else
    PASS=$((PASS + 1))
fi

CURRENT_TEST="successful create terminal done"
done_out="$TMPDIR_ROOT/done.json"
done_log="$TMPDIR_ROOT/done.log"
if run_canary done yes "$done_out" "$done_log"; then rc=0; else rc=$?; fi
done_json="$(cat "$done_out")"
done_calls="$(cat "$done_log")"
assert_ok "$rc"
assert_contains "$done_json" '"ok":true'
assert_contains "$done_json" '"created_task_ids":["11111111-1111-4111-8111-111111111111"]'
assert_contains "$done_json" '"status":"done"'
assert_contains "$done_json" '"saw_worker_assignment":true'
assert_contains "$done_json" '"saw_fresh_event_movement":true'
assert_contains "$done_calls" '--json create --title'
assert_contains "$done_calls" '--json tasks --limit'
assert_contains "$done_calls" '--json events --since'
assert_not_contains "$done_calls" ' approve '
assert_not_contains "$done_calls" ' reject '
assert_not_contains "$done_calls" ' pass '
assert_not_contains "$done_calls" ' fail '
assert_not_contains "$done_calls" ' retry '
assert_not_contains "$done_calls" ' archive '

CURRENT_TEST="timeout no movement failure"
timeout_out="$TMPDIR_ROOT/timeout.json"
timeout_log="$TMPDIR_ROOT/timeout.log"
if run_canary timeout yes "$timeout_out" "$timeout_log"; then rc=0; else rc=$?; fi
timeout_json="$(cat "$timeout_out")"
assert_fail "$rc"
assert_contains "$timeout_json" '"ok":false'
assert_contains "$timeout_json" '"status":"backlog"'
assert_contains "$timeout_json" '"saw_worker_assignment":false'
assert_contains "$timeout_json" '"saw_fresh_event_movement":false'

echo "PASS: $PASS"
echo "FAIL: $FAIL"
[ "$FAIL" -eq 0 ]
