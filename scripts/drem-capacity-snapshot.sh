#!/usr/bin/env bash
set -euo pipefail

# Read-only capacity snapshot for Drem operations.
# Performs GET-only health/status checks and dremctl status --json.

DREM_ORCH_URL="${DREM_ORCH_URL:-http://127.0.0.1:8080}"
DREM_PROJECT="${DREM_PROJECT:-drem-orchestrator}"
if [ -n "${DREMCTL_BIN:-}" ]; then
    DREMCTL_BIN="$DREMCTL_BIN"
elif [ -x "./deploy/docker/context/dremctl" ]; then
    DREMCTL_BIN="./deploy/docker/context/dremctl"
else
    DREMCTL_BIN="dremctl"
fi
CURL_TIMEOUT="${DREM_CAPACITY_CURL_TIMEOUT:-5}"

DREM_KYLE_URL="${DREM_KYLE_URL:-http://127.0.0.1:8095}"
DREM_KYLE_HEALTH_URL="${DREM_KYLE_HEALTH_URL:-${DREM_KYLE_URL%/}/healthz}"
DREM_PLANNER_HEALTH_URL="${DREM_PLANNER_HEALTH_URL:-}"
DREM_CLASSIFIER_HEALTH_URL="${DREM_CLASSIFIER_HEALTH_URL:-}"
DREM_PLANNER_CONTAINER="${DREM_PLANNER_CONTAINER:-drem-planner}"
DREM_CLASSIFIER_CONTAINER="${DREM_CLASSIFIER_CONTAINER:-drem-classifier}"

json_escape() {
    local s="${1:-}"
    s="${s//\\/\\\\}"
    s="${s//\"/\\\"}"
    s="${s//$'\n'/\\n}"
    s="${s//$'\r'/}"
    s="${s//$'\t'/\\t}"
    printf '%s' "$s"
}

json_string() {
    printf '"%s"' "$(json_escape "${1:-}")"
}

extract_object_field() {
    local field="$1" json="$2"
    printf '%s' "$json" | tr -d '\n' | sed -n "s/.*\"${field}\"[[:space:]]*:[[:space:]]*\({[^}]*}\).*/\1/p"
}

extract_number_field() {
    local field="$1" json="$2"
    printf '%s' "$json" | tr -d '\n' | sed -n "s/.*\"${field}\"[[:space:]]*:[[:space:]]*\([0-9][0-9]*\).*/\1/p"
}

extract_first_worker_count() {
    printf '%s' "$1" | tr -d '\n' | sed -n 's/.*"worker_count"[[:space:]]*:[[:space:]]*\([0-9][0-9]*\).*/\1/p'
}

extract_project_worker_count() {
    local json="$1" compact project_re count
    compact="$(printf '%s' "$json" | tr -d '\n')"
    project_re="$(printf '%s' "$DREM_PROJECT" | sed 's/[.[\*^$()+?{}|\\]/\\&/g; s/\//\\\//g')"
    count="$(printf '%s' "$compact" | sed -n "s/.*{[^}]*\"name\"[[:space:]]*:[[:space:]]*\"${project_re}\"[^}]*\"worker_count\"[[:space:]]*:[[:space:]]*\([0-9][0-9]*\).*/\1/p")"
    if [ -z "$count" ]; then
        count="$(extract_first_worker_count "$json")"
    fi
    printf '%s' "$count"
}

extract_status_count() {
    local status="$1" json="$2"
    printf '%s' "$json" | sed -n "s/.*\"${status}\"[[:space:]]*:[[:space:]]*\([0-9][0-9]*\).*/\1/p"
}

sum_live_workers() {
    local json="$1" count total=0
    for status in working running active; do
        count="$(extract_status_count "$status" "$json")"
        if [ -n "$count" ]; then
            total=$((total + count))
        fi
    done
    printf '%s' "$total"
}

endpoint_json() {
    local name="$1" kind="$2" url="$3" container="${4:-}"
    local body_file err_file body err http_status rc ok
    body_file="$(mktemp)"
    err_file="$(mktemp)"
    body=""
    err=""
    http_status=""
    rc=0
    ok=false

    if [ "$kind" = "curl" ]; then
        if http_status="$(curl -sS -m "$CURL_TIMEOUT" -o "$body_file" -w '%{http_code}' "$url" 2>"$err_file")"; then
            rc=0
        else
            rc=$?
        fi
        body="$(cat "$body_file" 2>/dev/null || true)"
        err="$(cat "$err_file" 2>/dev/null || true)"
        if [ "$rc" -eq 0 ] && [ "$http_status" -ge 200 ] 2>/dev/null && [ "$http_status" -lt 300 ] 2>/dev/null; then
            ok=true
        fi
    else
        if body="$(docker exec "$container" wget -qO- http://localhost:8090/healthz 2>"$err_file")"; then
            rc=0
            ok=true
        else
            rc=$?
            err="$(cat "$err_file" 2>/dev/null || true)"
        fi
        url="docker://${container}/healthz"
    fi

    rm -f "$body_file" "$err_file"
    printf '"%s":{"ok":%s,"url":%s,"http_status":%s,"exit_code":%s,"error":%s}' \
        "$name" \
        "$ok" \
        "$(json_string "$url")" \
        "${http_status:-null}" \
        "$rc" \
        "$(json_string "$err")"
    [ "$ok" = true ]
}

main() {
    local timestamp projects_url projects_result planner_result classifier_result kyle_result
    local required_failed=0 projects_ok planner_ok classifier_ok kyle_ok dremctl_ok
    local status_out_file status_err_file status_rc dremctl_status dremctl_error
    local tasks_by_status workers_by_status historical_worker_count live_worker_count project_worker_count

    timestamp="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    projects_url="${DREM_ORCH_URL%/}/projects"

    projects_result="$(endpoint_json projects curl "$projects_url")" && projects_ok=true || projects_ok=false
    [ "$projects_ok" = true ] || required_failed=1

    if [ -n "$DREM_PLANNER_HEALTH_URL" ]; then
        planner_result="$(endpoint_json planner_health curl "$DREM_PLANNER_HEALTH_URL")" && planner_ok=true || planner_ok=false
    else
        planner_result="$(endpoint_json planner_health docker '' "$DREM_PLANNER_CONTAINER")" && planner_ok=true || planner_ok=false
    fi
    [ "$planner_ok" = true ] || required_failed=1

    if [ -n "$DREM_CLASSIFIER_HEALTH_URL" ]; then
        classifier_result="$(endpoint_json classifier_health curl "$DREM_CLASSIFIER_HEALTH_URL")" && classifier_ok=true || classifier_ok=false
    else
        classifier_result="$(endpoint_json classifier_health docker '' "$DREM_CLASSIFIER_CONTAINER")" && classifier_ok=true || classifier_ok=false
    fi
    [ "$classifier_ok" = true ] || required_failed=1

    kyle_result="$(endpoint_json kyle_health curl "$DREM_KYLE_HEALTH_URL")" && kyle_ok=true || kyle_ok=false
    [ "$kyle_ok" = true ] || required_failed=1

    status_out_file="$(mktemp)"
    status_err_file="$(mktemp)"
    status_rc=0
    dremctl_ok=false
    if "$DREMCTL_BIN" --orch-url "$DREM_ORCH_URL" --project "$DREM_PROJECT" --json status >"$status_out_file" 2>"$status_err_file"; then
        dremctl_ok=true
    else
        status_rc=$?
    fi
    dremctl_status="$(cat "$status_out_file" 2>/dev/null || true)"
    dremctl_error="$(cat "$status_err_file" 2>/dev/null || true)"
    rm -f "$status_out_file" "$status_err_file"
    [ "$dremctl_ok" = true ] || required_failed=1

    tasks_by_status="$(extract_object_field tasks_by_status "$dremctl_status")"
    workers_by_status="$(extract_object_field workers_by_status "$dremctl_status")"
    historical_worker_count="$(extract_number_field historical_worker_count "$dremctl_status")"
    if [ -z "$historical_worker_count" ]; then
        historical_worker_count="$(extract_number_field worker_count "$dremctl_status")"
    fi
    project_worker_count="$(extract_project_worker_count "$(curl -sS -m "$CURL_TIMEOUT" "$projects_url" 2>/dev/null || true)")"
    if [ -n "$workers_by_status" ]; then
        live_worker_count="$(sum_live_workers "$workers_by_status")"
    else
        live_worker_count=""
    fi

    printf '{'
    printf '"timestamp":%s,' "$(json_string "$timestamp")"
    printf '"project":%s,' "$(json_string "$DREM_PROJECT")"
    printf '"checks":{%s,%s,%s,%s,' "$projects_result" "$planner_result" "$classifier_result" "$kyle_result"
    printf '"dremctl_status":{"ok":%s,"exit_code":%s,"error":%s}' "$dremctl_ok" "$status_rc" "$(json_string "$dremctl_error")"
    printf '},'
    printf '"task_status_counts":%s,' "${tasks_by_status:-null}"
    printf '"worker_status_counts":%s,' "${workers_by_status:-null}"
    printf '"historical_worker_count":%s,' "${historical_worker_count:-null}"
    printf '"live_worker_count":%s,' "${live_worker_count:-null}"
    printf '"project_worker_count":%s' "${project_worker_count:-null}"
    printf '}\n'

    return "$required_failed"
}

main "$@"
