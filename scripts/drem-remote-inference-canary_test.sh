#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CANARY="${SCRIPT_DIR}/drem-remote-inference-canary.sh"
TEST_ROOT="$(mktemp -d)"
trap 'rm -rf "$TEST_ROOT"' EXIT
mkdir -p "$TEST_ROOT/bin"

cat > "$TEST_ROOT/bin/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
url="${*: -1}"
if [ "${FAKE_CURL_FAIL:-}" = "yes" ]; then
    exit 7
fi
case "$url" in
    */v1/models)
        printf '{"data":[{"id":"fake-sglang-model"}]}\n'
        ;;
    */v1/chat/completions)
        if [ "${FAKE_INVALID_RESPONSE:-}" = "yes" ]; then
            printf 'not-json\n'
            exit 0
        fi
        if [ "${DREM_INFERENCE_CANARY_PROFILE:-basic}" = "reviewer" ]; then
            content='{"coverage":"full","uncovered_criteria":[],"file_overlap_risk":"low","overlapping_pairs":[],"integration_gap":false,"tdd_assessment":{"test_coverage_adequate":true,"exceptions_justified":true,"issues":[]},"issues":[],"recommendation":"approve"}'
        else
            content="${FAKE_CANARY_CONTENT:-DREM_INFERENCE_CANARY_OK}"
        fi
        printf '{"id":"request-1","choices":[{"message":{"content":%s},"finish_reason":"stop"}],"usage":{"prompt_tokens":42,"completion_tokens":12}}\n' "$(python3 -c 'import json,sys; print(json.dumps(sys.argv[1]))' "$content")"
        ;;
    *) exit 2 ;;
esac
EOF
chmod +x "$TEST_ROOT/bin/curl"

pass=0
fail=0
check_contains() {
    if printf '%s' "$1" | grep -qF "$2"; then
        pass=$((pass + 1))
    else
        printf 'FAIL: expected %s in %s\n' "$2" "$1"
        fail=$((fail + 1))
    fi
}

out="$(PATH="$TEST_ROOT/bin:$PATH" bash "$CANARY")"
check_contains "$out" '"ok":true'
check_contains "$out" '"model":"fake-sglang-model"'
check_contains "$out" '"repository_data_sent":false'
check_contains "$out" '"orchestration_state_mutated":false'

out="$(PATH="$TEST_ROOT/bin:$PATH" DREM_INFERENCE_CANARY_PROFILE=reviewer bash "$CANARY")"
check_contains "$out" '"ok":true'
check_contains "$out" '"profile":"reviewer"'
check_contains "$out" '"finish_reason":"stop"'
check_contains "$out" '"prompt_tokens":42'

set +e
out="$(PATH="$TEST_ROOT/bin:$PATH" FAKE_CANARY_CONTENT=wrong bash "$CANARY")"
rc=$?
set -e
[ "$rc" -ne 0 ] && pass=$((pass + 1)) || fail=$((fail + 1))
check_contains "$out" '"stage":"semantic"'

set +e
out="$(PATH="$TEST_ROOT/bin:$PATH" FAKE_INVALID_RESPONSE=yes bash "$CANARY")"
rc=$?
set -e
[ "$rc" -ne 0 ] && pass=$((pass + 1)) || fail=$((fail + 1))
check_contains "$out" '"stage":"protocol"'

set +e
out="$(PATH="$TEST_ROOT/bin:$PATH" FAKE_CURL_FAIL=yes bash "$CANARY")"
rc=$?
set -e
[ "$rc" -ne 0 ] && pass=$((pass + 1)) || fail=$((fail + 1))
check_contains "$out" '"stage":"transport"'

set +e
out="$(PATH="$TEST_ROOT/bin:$PATH" DREM_INFERENCE_CANARY_ENDPOINT=http://remote.example/v1/chat/completions bash "$CANARY")"
rc=$?
set -e
[ "$rc" -eq 2 ] && pass=$((pass + 1)) || fail=$((fail + 1))
check_contains "$out" 'endpoint must be loopback'

printf '%s passed, %s failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
