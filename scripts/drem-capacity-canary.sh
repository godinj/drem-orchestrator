#!/usr/bin/env bash
set -euo pipefail

# Controlled capacity canary runner.
# Mutating scope is intentionally limited to dremctl create plus read-only polling.

DREM_ORCH_URL="${DREM_ORCH_URL:-http://127.0.0.1:8080}"
DREM_PROJECT="${DREM_PROJECT:-drem-orchestrator}"
if [ -n "${DREMCTL_BIN:-}" ]; then
    DREMCTL_BIN="$DREMCTL_BIN"
elif [ -x "./deploy/docker/context/dremctl" ]; then
    DREMCTL_BIN="./deploy/docker/context/dremctl"
else
    DREMCTL_BIN="dremctl"
fi

CANARY_COUNT="${DREM_CAPACITY_CANARY_COUNT:-1}"
CANARY_TIMEOUT="${DREM_CAPACITY_CANARY_TIMEOUT:-20m}"
CANARY_INTERVAL="${DREM_CAPACITY_CANARY_INTERVAL:-15s}"
TASK_LIMIT="${DREM_CAPACITY_CANARY_TASK_LIMIT:-100}"
EVENT_LIMIT="${DREM_CAPACITY_CANARY_EVENT_LIMIT:-200}"

usage() {
    cat <<'EOF'
usage: drem-capacity-canary.sh [--count N] [--timeout DURATION] [--interval DURATION]

Requires DREM_CAPACITY_CANARY_CONFIRM=yes before creating tasks.
Durations support Ns, Nm, Nh, or bare seconds.
EOF
}

while [ "$#" -gt 0 ]; do
    case "$1" in
        --count)
            CANARY_COUNT="$2"
            shift 2
            ;;
        --timeout)
            CANARY_TIMEOUT="$2"
            shift 2
            ;;
        --interval)
            CANARY_INTERVAL="$2"
            shift 2
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        *)
            echo "unknown argument: $1" >&2
            usage >&2
            exit 2
            ;;
    esac
done

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

duration_seconds() {
    local raw="$1" number unit
    case "$raw" in
        *[!0-9smh]*) return 1 ;;
    esac
    number="${raw%[smh]}"
    unit="${raw#$number}"
    [ -n "$number" ] || return 1
    case "$unit" in
        ""|s) printf '%s' "$number" ;;
        m) printf '%s' $((number * 60)) ;;
        h) printf '%s' $((number * 3600)) ;;
        *) return 1 ;;
    esac
}

extract_json_string_field() {
    local field="$1" json="$2"
    printf '%s' "$json" | tr -d '\n' | sed -n "s/.*\"${field}\"[[:space:]]*:[[:space:]]*\"\([^\"]*\)\".*/\1/p"
}

task_object_by_id() {
    local id="$1" json="$2"
    printf '%s' "$json" | tr -d '\n' | sed 's/}[[:space:]]*,[[:space:]]*{/}\
{/g' | sed -n "/\"id\"[[:space:]]*:[[:space:]]*\"${id}\"/{p;q;}"
}

task_field_by_id() {
    local id="$1" field="$2" json="$3" object
    object="$(task_object_by_id "$id" "$json")"
    extract_json_string_field "$field" "$object"
}

terminal_status() {
    case "$1" in
        done|failed|rejected) return 0 ;;
        *) return 1 ;;
    esac
}

emit_refusal_json() {
    printf '{'
    printf '"ok":false,'
    printf '"error":%s,' "$(json_string 'refusing to create canary tasks without DREM_CAPACITY_CANARY_CONFIRM=yes')"
    printf '"created_task_ids":[],'
    printf '"tasks":[]'
    printf '}\n'
}

main() {
    local timeout_seconds interval_seconds start_timestamp start_epoch deadline now
    local ids=() titles=() statuses=() workers=() movements=()
    local i title metadata_path description create_out task_id tasks_out events_out all_terminal
    local status assigned_worker saw_worker saw_movement overall_ok task_ok exit_code

    if [ "${DREM_CAPACITY_CANARY_CONFIRM:-}" != "yes" ]; then
        emit_refusal_json
        return 2
    fi
    case "$CANARY_COUNT" in
        ''|*[!0-9]*) echo "count must be a positive integer" >&2; return 2 ;;
    esac
    if [ "$CANARY_COUNT" -lt 1 ]; then
        echo "count must be a positive integer" >&2
        return 2
    fi
    timeout_seconds="$(duration_seconds "$CANARY_TIMEOUT")" || { echo "invalid timeout: $CANARY_TIMEOUT" >&2; return 2; }
    interval_seconds="$(duration_seconds "$CANARY_INTERVAL")" || { echo "invalid interval: $CANARY_INTERVAL" >&2; return 2; }

    start_timestamp="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    start_epoch="$(date -u +%s)"
    deadline=$((start_epoch + timeout_seconds))

    for i in $(seq 1 "$CANARY_COUNT"); do
        title="capacity-canary-${start_epoch}-${i}"
		metadata_path=".drem/capacity-canary-${start_epoch}-${i}.json"
		description="Tiny controlled capacity canary task ${i}/${CANARY_COUNT}. Do not search. Read README.md once, then create ${metadata_path} containing JSON with keys canary_title, exercised_path, and timestamp_utc. Set exercised_path to gq-sglang-direct. Do not run tests for this metadata-only artifact. Commit only that smallest repo-local artifact metadata change and complete normally."
        create_out="$("$DREMCTL_BIN" --orch-url "$DREM_ORCH_URL" --project "$DREM_PROJECT" --json create --title "$title" --description "$description")"
        task_id="$(extract_json_string_field id "$create_out")"
        if [ -z "$task_id" ]; then
            echo "dremctl create did not return a JSON id" >&2
            return 1
        fi
        ids+=("$task_id")
        titles+=("$title")
        statuses+=("created")
        workers+=("false")
        movements+=("false")
    done

    while :; do
        tasks_out="$("$DREMCTL_BIN" --orch-url "$DREM_ORCH_URL" --project "$DREM_PROJECT" --json tasks --limit "$TASK_LIMIT")"
        events_out="$("$DREMCTL_BIN" --orch-url "$DREM_ORCH_URL" --project "$DREM_PROJECT" --json events --since "$start_timestamp" --limit "$EVENT_LIMIT")"
        all_terminal=true

        for i in "${!ids[@]}"; do
            status="$(task_field_by_id "${ids[$i]}" status "$tasks_out")"
            assigned_worker="$(task_field_by_id "${ids[$i]}" assigned_worker "$tasks_out")"
            if [ -n "$status" ]; then
                statuses[$i]="$status"
            fi
            if [ -n "$assigned_worker" ]; then
                workers[$i]=true
            fi
            if printf '%s' "$events_out" | grep -qF "${ids[$i]}"; then
                movements[$i]=true
            fi
            if ! terminal_status "${statuses[$i]}"; then
                all_terminal=false
            fi
        done

        [ "$all_terminal" = true ] && break
        now="$(date -u +%s)"
        [ "$now" -ge "$deadline" ] && break
        sleep "$interval_seconds"
    done

    overall_ok=true
    printf '{'
    printf '"ok":'
    for i in "${!ids[@]}"; do
        task_ok=false
        if [ "${statuses[$i]}" = "done" ] && { [ "${workers[$i]}" = true ] || [ "${movements[$i]}" = true ]; }; then
            task_ok=true
        else
            overall_ok=false
        fi
    done
    printf '%s,' "$overall_ok"
    printf '"start_timestamp":%s,' "$(json_string "$start_timestamp")"
    printf '"project":%s,' "$(json_string "$DREM_PROJECT")"
    printf '"created_task_ids":['
    for i in "${!ids[@]}"; do
        [ "$i" -eq 0 ] || printf ','
        json_string "${ids[$i]}"
    done
    printf '],'
    printf '"tasks":['
    for i in "${!ids[@]}"; do
        [ "$i" -eq 0 ] || printf ','
        saw_worker=false
        saw_movement=false
        [ "${workers[$i]}" = true ] && saw_worker=true
        [ "${movements[$i]}" = true ] && saw_movement=true
        task_ok=false
        if [ "${statuses[$i]}" = "done" ] && { [ "$saw_worker" = true ] || [ "$saw_movement" = true ]; }; then
            task_ok=true
        fi
        printf '{"id":%s,"title":%s,"status":%s,"saw_worker_assignment":%s,"saw_fresh_event_movement":%s,"ok":%s}' \
            "$(json_string "${ids[$i]}")" \
            "$(json_string "${titles[$i]}")" \
            "$(json_string "${statuses[$i]}")" \
            "$saw_worker" \
            "$saw_movement" \
            "$task_ok"
    done
    printf ']'
    printf '}\n'

    exit_code=0
    [ "$overall_ok" = true ] || exit_code=1
    return "$exit_code"
}

main "$@"
