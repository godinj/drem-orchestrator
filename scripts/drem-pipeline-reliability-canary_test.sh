#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CANARY_SCRIPT="${SCRIPT_DIR}/drem-pipeline-reliability-canary.sh"

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
title=""
for arg in "$@"; do
    case "$prev" in
        --orch-url|--project|--actor|--description|--limit)
            prev=""
            continue
            ;;
        --title)
            title="$arg"
            prev=""
            continue
            ;;
    esac
    case "$arg" in
        --orch-url|--project|--actor|--title|--description|--limit|--json)
            prev="$arg"
            ;;
        create|tasks)
            cmd="$arg"
            break
            ;;
    esac
done

case "$cmd" in
    create)
        if printf '%s' "$*" | grep -qF 'quickfix'; then
            id="aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
        elif printf '%s' "$*" | grep -qF 'failure'; then
            id="cccccccc-cccc-4ccc-8ccc-cccccccccccc"
        else
            id="bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
        fi
        printf '{"id":"%s","title":"%s","status":"classifying"}\n' "$id" "$title"
        ;;
    tasks)
        if [ "${FAKE_CANARY_MODE:-done}" = "failed" ]; then
            cat <<'JSON'
[
  {"id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","title":"pipeline-canary-quickfix","status":"done","latest_failure_summary":""},
  {"id":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","title":"pipeline-canary-standard","status":"failed","latest_failure_summary":"go test failed in canary fixture"},
  {"id":"cccccccc-cccc-4ccc-8ccc-cccccccccccc","title":"pipeline-canary-failure","status":"failed","latest_failure_summary":"intentional failure canary"}
]
JSON
        else
            cat <<'JSON'
[
  {"id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","title":"pipeline-canary-quickfix","status":"done","latest_failure_summary":""},
  {"id":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","title":"pipeline-canary-standard","status":"done","latest_failure_summary":""},
  {"id":"cccccccc-cccc-4ccc-8ccc-cccccccccccc","title":"pipeline-canary-failure","status":"done","latest_failure_summary":""}
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

    cat > "$dir/bin/curl" <<'CURL_EOF'
#!/usr/bin/env bash
set -euo pipefail
log_file="${CURL_LOG:?CURL_LOG required}"
url=""
while [ "$#" -gt 0 ]; do
    case "$1" in
        -m)
            shift 2
            ;;
        -f|-s|-S|-fsS)
            shift
            ;;
        *)
            url="$1"
            shift
            ;;
    esac
done
printf '%s\n' "$url" >> "$log_file"
case "$url" in
    */healthz)
        printf 'ok\n'
        ;;
    */world/summary)
        printf 'Kyle world summary: OK\n'
        ;;
    *)
        echo "unexpected curl url: $url" >&2
        exit 2
        ;;
esac
CURL_EOF
    chmod +x "$dir/bin/curl"
}

run_canary() {
    local mode="$1" confirm="$2" failure_confirm="$3" out_file="$4" dremctl_log="$5" curl_log="$6"
    set +e
    FAKE_CANARY_MODE="$mode" \
    PATH="$TMPDIR_ROOT/bin:$PATH" \
    DREM_ORCH_URL="http://fake-orch" \
    DREM_PROJECT="canvas" \
	DREM_ACTOR="canary:pipeline:test" \
    DREM_KYLE_URL="http://fake-kyle" \
    DREMCTL_BIN="dremctl" \
    DREMCTL_LOG="$dremctl_log" \
    CURL_LOG="$curl_log" \
    DREM_PIPELINE_CANARY_CONFIRM="$confirm" \
    DREM_PIPELINE_CANARY_FAILURE_CONFIRM="$failure_confirm" \
    DREM_PIPELINE_CANARY_TIMEOUT="1s" \
    DREM_PIPELINE_CANARY_INTERVAL="0s" \
    bash "$CANARY_SCRIPT" > "$out_file" 2>"$out_file.err"
    local rc=$?
    set -e
    return "$rc"
}

setup_stubs "$TMPDIR_ROOT"

CURRENT_TEST="dry run plan without confirmation"
plan_out="$TMPDIR_ROOT/plan.json"
plan_dremctl_log="$TMPDIR_ROOT/plan-dremctl.log"
plan_curl_log="$TMPDIR_ROOT/plan-curl.log"
if run_canary done no no "$plan_out" "$plan_dremctl_log" "$plan_curl_log"; then rc=0; else rc=$?; fi
plan_json="$(cat "$plan_out")"
assert_ok "$rc"
assert_contains "$plan_json" '"dry_run":true'
assert_contains "$plan_json" '"run_id":'
assert_contains "$plan_json" '"mode":"quickfix"'
assert_contains "$plan_json" '"mode":"standard"'
assert_contains "$plan_json" '"mode":"kyle"'
assert_contains "$plan_json" '"mode":"failure"'
assert_contains "$plan_json" 'DREM_PIPELINE_CANARY_CONFIRM=yes'
if [ -s "$plan_dremctl_log" ] || [ -s "$plan_curl_log" ]; then
    echo "  FAIL ($CURRENT_TEST): dry run should not call dremctl or curl"
    FAIL=$((FAIL + 1))
else
    PASS=$((PASS + 1))
fi

CURRENT_TEST="confirmed happy report"
done_out="$TMPDIR_ROOT/done.json"
done_dremctl_log="$TMPDIR_ROOT/done-dremctl.log"
done_curl_log="$TMPDIR_ROOT/done-curl.log"
if run_canary done yes no "$done_out" "$done_dremctl_log" "$done_curl_log"; then rc=0; else rc=$?; fi
done_json="$(cat "$done_out")"
done_calls="$(cat "$done_dremctl_log")"
curl_calls="$(cat "$done_curl_log")"
assert_ok "$rc"
assert_contains "$done_json" '"ok":true'
assert_contains "$done_json" '"dry_run":false'
assert_contains "$done_json" '"task_ids":["aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"]'
assert_contains "$done_json" '"final_state":"done"'
assert_contains "$done_json" '"duration_seconds":'
assert_contains "$done_json" '"kyle_health":"ok"'
assert_contains "$done_json" 'Kyle world summary: OK'
assert_contains "$done_json" '"failure_canary":{"enabled":false,"mutating":false'
assert_contains "$done_calls" '--json create --title'
assert_contains "$done_calls" '--json tasks --limit'
assert_contains "$curl_calls" 'http://fake-kyle/healthz'
assert_contains "$curl_calls" 'http://fake-kyle/world/summary'
assert_not_contains "$done_calls" ' approve '
assert_not_contains "$done_calls" ' retry '
assert_not_contains "$done_calls" ' archive '
assert_not_contains "$done_calls" 'failure-placeholder'

CURRENT_TEST="failure summary visible"
failed_out="$TMPDIR_ROOT/failed.json"
failed_dremctl_log="$TMPDIR_ROOT/failed-dremctl.log"
failed_curl_log="$TMPDIR_ROOT/failed-curl.log"
if run_canary failed yes no "$failed_out" "$failed_dremctl_log" "$failed_curl_log"; then rc=0; else rc=$?; fi
failed_json="$(cat "$failed_out")"
assert_fail "$rc"
assert_contains "$failed_json" '"ok":false'
assert_contains "$failed_json" '"final_state":"failed"'
assert_contains "$failed_json" '"latest_failure_summary":"go test failed in canary fixture"'

CURRENT_TEST="failure canary needs extra confirmation"
failure_plan_out="$TMPDIR_ROOT/failure-plan.json"
failure_plan_dremctl_log="$TMPDIR_ROOT/failure-plan-dremctl.log"
failure_plan_curl_log="$TMPDIR_ROOT/failure-plan-curl.log"
set +e
PATH="$TMPDIR_ROOT/bin:$PATH" \
DREM_ORCH_URL="http://fake-orch" \
DREM_PROJECT="canvas" \
DREM_KYLE_URL="http://fake-kyle" \
DREMCTL_BIN="dremctl" \
DREMCTL_LOG="$failure_plan_dremctl_log" \
CURL_LOG="$failure_plan_curl_log" \
DREM_PIPELINE_CANARY_CONFIRM="yes" \
DREM_PIPELINE_CANARY_TIMEOUT="1s" \
DREM_PIPELINE_CANARY_INTERVAL="0s" \
bash "$CANARY_SCRIPT" --mode failure > "$failure_plan_out" 2>"$failure_plan_out.err"
rc=$?
set -e
failure_plan_json="$(cat "$failure_plan_out")"
assert_ok "$rc"
assert_contains "$failure_plan_json" '"failure_canary":{"enabled":false,"mutating":false'
if [ -s "$failure_plan_dremctl_log" ]; then
    echo "  FAIL ($CURRENT_TEST): failure placeholder should not call dremctl without extra confirmation"
    FAIL=$((FAIL + 1))
else
    PASS=$((PASS + 1))
fi

echo "PASS: $PASS"
echo "FAIL: $FAIL"
[ "$FAIL" -eq 0 ]
