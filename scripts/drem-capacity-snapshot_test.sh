#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SNAPSHOT_SCRIPT="${SCRIPT_DIR}/drem-capacity-snapshot.sh"

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
    if printf '%s' "$1" | grep -qF "$2"; then
        PASS=$((PASS + 1))
    else
        echo "  FAIL ($CURRENT_TEST): output does not contain '$2'"
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

    cat > "$dir/bin/curl" <<'CURL_EOF'
#!/usr/bin/env bash
set -euo pipefail
out=""
write_format=""
url=""
while [ "$#" -gt 0 ]; do
    case "$1" in
        -o)
            out="$2"
            shift 2
            ;;
        -w)
            write_format="$2"
            shift 2
            ;;
        -m)
            shift 2
            ;;
        -s|-S|-sS)
            shift
            ;;
        *)
            url="$1"
            shift
            ;;
    esac
done

code=200
body='{"ok":true}'
if [ "${FAKE_MODE:-healthy}" = "unhealthy" ] && printf '%s' "$url" | grep -qF '/planner/healthz'; then
    code=503
    body='planner down'
fi

case "$url" in
    */projects)
        body='[{"name":"canvas","language":"go","orch_url":"http://orch:8080","worker_count":3}]'
        ;;
    */planner/healthz|*/classifier/healthz|*/kyle/healthz)
        :
        ;;
    *)
        body='{"ok":true}'
        ;;
esac

if [ -n "$out" ]; then
    printf '%s' "$body" > "$out"
else
    printf '%s' "$body"
fi
if [ -n "$write_format" ]; then
    printf '%s' "$code"
fi
[ "$code" -ge 200 ] && [ "$code" -lt 300 ]
CURL_EOF
    chmod +x "$dir/bin/curl"

    cat > "$dir/bin/dremctl" <<'DREMCTL_EOF'
#!/usr/bin/env bash
set -euo pipefail
if [ "${FAKE_MODE:-healthy}" = "dremctl_down" ]; then
    echo "dremctl unavailable" >&2
    exit 17
fi
json=false
last=""
for arg in "$@"; do
    [ "$arg" = "--json" ] && json=true
    last="$arg"
done

if [ "$json" = true ] && [ "$last" = "status" ]; then
    cat <<'JSON'
{
  "projects": [{"name":"canvas","language":"go","orch_url":"http://orch:8080","worker_count":3}],
  "task_count": 7,
  "tasks_by_status": {"backlog":2,"in_progress":1,"done":4},
  "worker_count": 5,
  "workers_by_status": {"working":1,"dead":4},
  "recent_events": []
}
JSON
else
    echo "unexpected dremctl args: $*" >&2
    exit 2
fi
DREMCTL_EOF
    chmod +x "$dir/bin/dremctl"
}

run_snapshot() {
    local mode="$1" out_file="$2"
    set +e
    FAKE_MODE="$mode" \
    PATH="$TMPDIR_ROOT/bin:$PATH" \
    DREM_ORCH_URL="http://fake-orch" \
    DREM_PROJECT="canvas" \
    DREMCTL_BIN="dremctl" \
    DREM_PLANNER_HEALTH_URL="http://fake/planner/healthz" \
    DREM_CLASSIFIER_HEALTH_URL="http://fake/classifier/healthz" \
    DREM_KYLE_HEALTH_URL="http://fake/kyle/healthz" \
    bash "$SNAPSHOT_SCRIPT" > "$out_file" 2>"$out_file.err"
    local rc=$?
    set -e
    return "$rc"
}

setup_stubs "$TMPDIR_ROOT"

CURRENT_TEST="healthy snapshot"
healthy_out="$TMPDIR_ROOT/healthy.json"
if run_snapshot healthy "$healthy_out"; then rc=0; else rc=$?; fi
healthy_json="$(cat "$healthy_out")"
assert_ok "$rc"
assert_contains "$healthy_json" '"projects":{"ok":true'
assert_contains "$healthy_json" '"planner_health":{"ok":true'
assert_contains "$healthy_json" '"task_status_counts":{"backlog":2,"in_progress":1,"done":4}'
assert_contains "$healthy_json" '"historical_worker_count":5'
assert_contains "$healthy_json" '"live_worker_count":1'
assert_contains "$healthy_json" '"project_worker_count":3'

CURRENT_TEST="unhealthy planner"
unhealthy_out="$TMPDIR_ROOT/unhealthy.json"
if run_snapshot unhealthy "$unhealthy_out"; then rc=0; else rc=$?; fi
unhealthy_json="$(cat "$unhealthy_out")"
assert_fail "$rc"
assert_contains "$unhealthy_json" '"planner_health":{"ok":false'
assert_contains "$unhealthy_json" '"http_status":503'

CURRENT_TEST="unhealthy dremctl"
dremctl_out="$TMPDIR_ROOT/dremctl.json"
if run_snapshot dremctl_down "$dremctl_out"; then rc=0; else rc=$?; fi
dremctl_json="$(cat "$dremctl_out")"
assert_fail "$rc"
assert_contains "$dremctl_json" '"dremctl_status":{"ok":false'
assert_contains "$dremctl_json" 'dremctl unavailable'

echo "PASS: $PASS"
echo "FAIL: $FAIL"
[ "$FAIL" -eq 0 ]
