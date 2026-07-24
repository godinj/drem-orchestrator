#!/usr/bin/env bash
set -euo pipefail

# Repository-free inference canary. It sends one constant prompt through the
# local tunnel/GQ path and never reads a checkout, creates a task, or mutates
# orchestration state.

ENDPOINT="${DREM_INFERENCE_CANARY_ENDPOINT:-http://127.0.0.1:18090/v1/chat/completions}"
MODEL="${DREM_INFERENCE_CANARY_MODEL:-}"
TIMEOUT="${DREM_INFERENCE_CANARY_TIMEOUT_SECONDS:-30}"
PROFILE="${DREM_INFERENCE_CANARY_PROFILE:-basic}"
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
case "$PROFILE" in
    basic|reviewer) ;;
    *) emit_failure configuration "profile must be basic or reviewer"; exit 2 ;;
esac

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
model,profile=sys.argv[1:3]
if profile == "reviewer":
  system="""You are a software plan reviewer. Return ONLY one JSON object with exactly this shape: {"coverage":"full|partial|none","uncovered_criteria":[],"file_overlap_risk":"low|medium|high","overlapping_pairs":[],"integration_gap":false,"tdd_assessment":{"test_coverage_adequate":true,"exceptions_justified":true,"issues":[]},"issues":[],"recommendation":"approve|revise|reject"}. Use only the lowercase enum values shown. Use revise for a salvageable plan and reject only for fundamental redesign. Any actionable issue requires revise or reject. Do not emit markdown or prose."""
  user="""Canvas task: Divide audio events at transients. Criteria: stable interior splits, reverse/stretch mapping, one undo transaction, command registration, and focused tests. Scope: AudioClip.h, AudioClipTransientSlicing.cpp, ActionAudioProcesses.cpp, ActionCoordinator.cpp, keymap, manifests, integration test. Plan: test transient detection and mapping; implement model API; test action registration; wire action and keymap; run native and Computer Use verification."""
  messages=[{"role":"system","content":system},{"role":"user","content":user}]
  max_tokens=1024
else:
  messages=[{"role":"user","content":"Return exactly: DREM_INFERENCE_CANARY_OK"}]
  max_tokens=32
payload={
  "model": model,
  "messages": messages,
  "temperature": 0,
  "max_tokens": max_tokens,
  "chat_template_kwargs": {"enable_thinking": False},
}
if profile == "reviewer":
  payload["response_format"]={"type":"json_object"}
print(json.dumps(payload))
' "$MODEL" "$PROFILE")"

start_epoch="$(date +%s)"
if ! response="$(curl "${curl_args[@]}" -H 'Content-Type: application/json' -H 'X-GQ-Caller: reviewer-canary' -H 'X-GQ-Priority: high' -X POST --data "$payload" "$ENDPOINT" 2>/dev/null)"; then
    emit_failure transport "chat completion failed at the tunnel, GQ, or SGLang upstream"
    exit 1
fi
duration_seconds=$(( $(date +%s) - start_epoch ))

parsed="$(printf '%s' "$response" | python3 -c '
import hashlib,json,sys
try:
    raw=sys.stdin.read()
    obj=json.loads(raw)
    choice=obj["choices"][0]
    raw_content=choice["message"].get("content")
    content="" if raw_content is None else str(raw_content).strip()
    finish_reason=str(choice.get("finish_reason", ""))
    usage=obj.get("usage", {})
    prompt_tokens=int(usage.get("prompt_tokens", 0) or 0)
    completion_tokens=int(usage.get("completion_tokens", 0) or 0)
    review_valid=False
    try:
        review=json.loads(content)
        review_valid=(review.get("recommendation") in {"approve","revise","reject"}
          and review.get("coverage") in {"full","partial","none"}
          and isinstance(review.get("issues"), list))
    except Exception:
        pass
    request_id=str(obj.get("id", ""))
    digest=hashlib.sha256(raw.encode()).hexdigest()
    print(json.dumps({"content":content,"request_id":request_id,"response_sha256":digest,
      "finish_reason":finish_reason,"prompt_tokens":prompt_tokens,
      "completion_tokens":completion_tokens,"review_valid":review_valid}))
except Exception:
    print(json.dumps({"error":"invalid OpenAI-compatible response"}))
')"
parse_error="$(printf '%s' "$parsed" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("error", ""))')"
if [ -n "$parse_error" ]; then
    emit_failure protocol "$parse_error"
    exit 1
fi
content="$(printf '%s' "$parsed" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("content", ""))')"
finish_reason="$(printf '%s' "$parsed" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("finish_reason", ""))')"
prompt_tokens="$(printf '%s' "$parsed" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("prompt_tokens", 0))')"
completion_tokens="$(printf '%s' "$parsed" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("completion_tokens", 0))')"
if [ "$PROFILE" = "reviewer" ]; then
    review_valid="$(printf '%s' "$parsed" | python3 -c 'import json,sys; print("yes" if json.load(sys.stdin).get("review_valid") else "no")')"
    if [ "$finish_reason" != "stop" ] || [ "$review_valid" != "yes" ] || [ "$prompt_tokens" -lt 1 ] || [ "$completion_tokens" -lt 1 ]; then
        emit_failure semantic "reviewer did not return a complete measured review JSON object"
        exit 1
    fi
elif [ "$content" != "$EXPECTED" ]; then
    emit_failure semantic "model did not return the exact canary token"
    exit 1
fi

request_id="$(printf '%s' "$parsed" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("request_id", ""))')"
response_sha="$(printf '%s' "$parsed" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("response_sha256", ""))')"
printf '{"ok":true,"stage":"complete","profile":%s,"endpoint":%s,"model":%s,"request_id":%s,"response_sha256":%s,"finish_reason":%s,"prompt_tokens":%s,"completion_tokens":%s,"duration_seconds":%s,"repository_data_sent":false,"orchestration_state_mutated":false}\n' \
    "$(json_string "$PROFILE")" "$(json_string "$ENDPOINT")" "$(json_string "$MODEL")" "$(json_string "$request_id")" \
    "$(json_string "$response_sha")" "$(json_string "$finish_reason")" "$prompt_tokens" "$completion_tokens" "$duration_seconds"
