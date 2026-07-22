#!/usr/bin/env bash
set -euo pipefail

# Repository-free inference canary. It sends one constant prompt through the
# local tunnel/GQ path and never reads a checkout, creates a task, or mutates
# orchestration state.

ENDPOINT="${DREM_INFERENCE_CANARY_ENDPOINT:-http://127.0.0.1:18090/v1/chat/completions}"
MODEL="${DREM_INFERENCE_CANARY_MODEL:-}"
TIMEOUT="${DREM_INFERENCE_CANARY_TIMEOUT_SECONDS:-30}"
EXPECTED="DREM_INFERENCE_CANARY_OK"

json_string() {
    python3 -c 'import json,sys; print(json.dumps(sys.argv[1]))' "${1:-}"
}

emit_failure() {
    local stage="$1" message="$2"
    printf '{"ok":false,"stage":%s,"message":%s,"repository_data_sent":false}\n' \
        "$(json_string "$stage")" "$(json_string "$message")"
}

case "$TIMEOUT" in
    ''|*[!0-9]*) emit_failure configuration "timeout must be a positive integer"; exit 2 ;;
esac
if [ "$TIMEOUT" -lt 1 ]; then
    emit_failure configuration "timeout must be a positive integer"
    exit 2
fi

case "$ENDPOINT" in
    http://127.0.0.1:*|http://localhost:*|https://127.0.0.1:*|https://localhost:*) ;;
    *)
        if [ "${DREM_INFERENCE_CANARY_ALLOW_NON_LOOPBACK:-}" != "yes" ]; then
            emit_failure configuration "endpoint must be loopback unless DREM_INFERENCE_CANARY_ALLOW_NON_LOOPBACK=yes"
            exit 2
        fi
        ;;
esac

base="${ENDPOINT%/v1/chat/completions}"
models_url="${base}/v1/models"
curl_args=(-fsS --max-time "$TIMEOUT" -H 'Accept: application/json')
if [ -n "${DREM_INFERENCE_CANARY_BEARER_TOKEN:-}" ]; then
    curl_args+=(-H "Authorization: Bearer ${DREM_INFERENCE_CANARY_BEARER_TOKEN}")
fi

if [ -z "$MODEL" ]; then
    if ! models_response="$(curl "${curl_args[@]}" "$models_url" 2>/dev/null)"; then
        emit_failure transport "model discovery failed at the tunnel, GQ, or SGLang upstream"
        exit 1
    fi
    if ! MODEL="$(printf '%s' "$models_response" | python3 -c '
import json,sys
try:
    data=json.load(sys.stdin).get("data", [])
    model=next((str(item.get("id", "")).strip() for item in data if str(item.get("id", "")).strip()), "")
except Exception:
    model=""
print(model)
')" || [ -z "$MODEL" ]; then
        emit_failure protocol "model discovery returned no model id"
        exit 1
    fi
fi

payload="$(python3 -c '
import json,sys
print(json.dumps({
  "model": sys.argv[1],
  "messages": [{"role":"user","content":"Return exactly: DREM_INFERENCE_CANARY_OK"}],
  "temperature": 0,
  "max_tokens": 32,
}))
' "$MODEL")"

start_epoch="$(date +%s)"
if ! response="$(curl "${curl_args[@]}" -H 'Content-Type: application/json' -X POST --data "$payload" "$ENDPOINT" 2>/dev/null)"; then
    emit_failure transport "chat completion failed at the tunnel, GQ, or SGLang upstream"
    exit 1
fi
duration_seconds=$(( $(date +%s) - start_epoch ))

parsed="$(printf '%s' "$response" | python3 -c '
import hashlib,json,sys
try:
    raw=sys.stdin.read()
    obj=json.loads(raw)
    content=str(obj["choices"][0]["message"]["content"]).strip()
    request_id=str(obj.get("id", ""))
    digest=hashlib.sha256(raw.encode()).hexdigest()
    print(json.dumps({"content":content,"request_id":request_id,"response_sha256":digest}))
except Exception:
    print(json.dumps({"error":"invalid OpenAI-compatible response"}))
')"
parse_error="$(printf '%s' "$parsed" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("error", ""))')"
if [ -n "$parse_error" ]; then
    emit_failure protocol "$parse_error"
    exit 1
fi
content="$(printf '%s' "$parsed" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("content", ""))')"
if [ "$content" != "$EXPECTED" ]; then
    emit_failure semantic "model did not return the exact canary token"
    exit 1
fi

request_id="$(printf '%s' "$parsed" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("request_id", ""))')"
response_sha="$(printf '%s' "$parsed" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("response_sha256", ""))')"
printf '{"ok":true,"stage":"complete","endpoint":%s,"model":%s,"request_id":%s,"response_sha256":%s,"duration_seconds":%s,"repository_data_sent":false,"orchestration_state_mutated":false}\n' \
    "$(json_string "$ENDPOINT")" "$(json_string "$MODEL")" "$(json_string "$request_id")" \
    "$(json_string "$response_sha")" "$duration_seconds"
