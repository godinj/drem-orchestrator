#!/usr/bin/env bash
set -euo pipefail

# Recurring DREM pipeline reliability canary runner.
# Safe default: emit a plan only unless DREM_PIPELINE_CANARY_CONFIRM=yes.

DREM_ORCH_URL="${DREM_ORCH_URL:-http://127.0.0.1:8080}"
DREM_PROJECT="${DREM_PROJECT:-drem-orchestrator}"
DREM_ACTOR="${DREM_ACTOR:-}"
DREM_KYLE_URL="${DREM_KYLE_URL:-http://127.0.0.1:8095}"
if [ -n "${DREMCTL_BIN:-}" ]; then
    DREMCTL_BIN="$DREMCTL_BIN"
elif [ -x "./deploy/docker/context/dremctl" ]; then
    DREMCTL_BIN="./deploy/docker/context/dremctl"
else
    DREMCTL_BIN="dremctl"
fi

CANARY_MODE="${DREM_PIPELINE_CANARY_MODE:-all}"
CANARY_TIMEOUT="${DREM_PIPELINE_CANARY_TIMEOUT:-20m}"
CANARY_INTERVAL="${DREM_PIPELINE_CANARY_INTERVAL:-15s}"
TASK_LIMIT="${DREM_PIPELINE_CANARY_TASK_LIMIT:-100}"
KYLE_TIMEOUT="${DREM_PIPELINE_CANARY_KYLE_TIMEOUT:-5}"

usage() {
    cat <<'EOF'
usage: drem-pipeline-reliability-canary.sh [--mode MODE] [--timeout DURATION] [--interval DURATION]

Modes: all, quickfix, standard, kyle, failure
Safe default: prints a JSON plan unless DREM_PIPELINE_CANARY_CONFIRM=yes.
Mutating confirmed modes also require a stable, specific DREM_ACTOR.
The failure canary is a non-mutating placeholder unless both confirmations are set:
  DREM_PIPELINE_CANARY_CONFIRM=yes
  DREM_PIPELINE_CANARY_FAILURE_CONFIRM=yes

Durations support Ns, Nm, Nh, or bare seconds.
EOF
}

while [ "$#" -gt 0 ]; do
    case "$1" in
        --mode)
            CANARY_MODE="$2"
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
        done|failed|rejected|cancelled) return 0 ;;
        *) return 1 ;;
    esac
}

include_mode() {
    local mode="$1"
    case "$CANARY_MODE" in
        all) return 0 ;;
        "$mode") return 0 ;;
        *) return 1 ;;
    esac
}

quickfix_title() { printf 'pipeline-canary-%s-quickfix' "$RUN_ID"; }
standard_title() { printf 'pipeline-canary-%s-standard' "$RUN_ID"; }
failure_title() { printf 'pipeline-canary-%s-failure-placeholder' "$RUN_ID"; }

quickfix_description() {
    local path=".drem/pipeline-canary-${RUN_ID}-quickfix.json"
    printf 'Quickfix metadata reliability canary. Do not search broadly. Read README.md once, then create %s containing JSON keys run_id, canary_kind, timestamp_utc, and note. Set canary_kind to quickfix_metadata. Do not run tests for this metadata-only artifact. Commit only that smallest repo-local artifact metadata change and complete normally.' "$path"
}

standard_description() {
    local doc_path="docs/pipeline-reliability-standard-canary-${RUN_ID}.md"
    printf 'Standard parent happy-path reliability canary. This must exercise the standard code+test pipeline, not a metadata-only shortcut. Add a tiny isolated type in internal/model with a unique valid Go identifier suffix derived from run id %s by keeping digits only. The type should have fields Title, ExercisedPath, and TimestampUTC plus a Validate() error method that fails when any field is empty. Add focused internal/model unit tests for valid and invalid cases. Add %s explaining this is a recurring reliability canary for classifier, planner, plan_review, test_writing, test_review, implementation subtasks, testing_ready, merger, and Kyle visibility. Keep the change minimal and isolated to internal/model and that docs file. Run go test ./internal/model.' "$RUN_ID" "$doc_path"
}

failure_description() {
    printf 'Future failure visibility canary placeholder for run_id %s. Intentionally asks for a visible, bounded failure signal. This task must not be filed unless DREM_PIPELINE_CANARY_FAILURE_CONFIRM=yes is set.' "$RUN_ID"
}

emit_plan_json() {
    printf '{'
    printf '"ok":true,'
    printf '"dry_run":true,'
    printf '"run_id":%s,' "$(json_string "$RUN_ID")"
    printf '"project":%s,' "$(json_string "$DREM_PROJECT")"
    printf '"message":%s,' "$(json_string 'set DREM_PIPELINE_CANARY_CONFIRM=yes to create canary tasks')"
    printf '"planned_canaries":['
    local first=true
    if include_mode quickfix; then
        [ "$first" = true ] || printf ','
        first=false
        printf '{"mode":"quickfix","mutating":true,"title":%s}' "$(json_string "$(quickfix_title)")"
    fi
    if include_mode standard; then
        [ "$first" = true ] || printf ','
        first=false
        printf '{"mode":"standard","mutating":true,"title":%s}' "$(json_string "$(standard_title)")"
    fi
    if include_mode kyle; then
        [ "$first" = true ] || printf ','
        first=false
        printf '{"mode":"kyle","mutating":false,"summary_url":%s,"health_url":%s}' \
            "$(json_string "${DREM_KYLE_URL%/}/world/summary")" \
            "$(json_string "${DREM_KYLE_URL%/}/healthz")"
    fi
    if include_mode failure; then
        [ "$first" = true ] || printf ','
        first=false
        printf '{"mode":"failure","mutating":false,"enabled":false,"title":%s}' "$(json_string "$(failure_title)")"
    fi
    printf '],'
    printf '"task_ids":[],'
    printf '"tasks":[],'
    printf '"kyle_health":"not_checked"'
    printf '}\n'
}

create_task() {
    local title="$1" description="$2" create_out task_id
    create_out="$("$DREMCTL_BIN" --orch-url "$DREM_ORCH_URL" --project "$DREM_PROJECT" --actor "$DREM_ACTOR" --json create --title "$title" --description "$description")"
    task_id="$(extract_json_string_field id "$create_out")"
    if [ -z "$task_id" ]; then
        echo "dremctl create did not return a JSON id" >&2
        return 1
    fi
    ids+=("$task_id")
    titles+=("$title")
    modes+=("$3")
    starts+=("$(date -u +%s)")
    statuses+=("created")
    failure_summaries+=("")
}

check_kyle() {
    local health_url summary_url health_body summary_body
    health_url="${DREM_KYLE_URL%/}/healthz"
    summary_url="${DREM_KYLE_URL%/}/world/summary"
    if ! health_body="$(curl -fsS -m "$KYLE_TIMEOUT" "$health_url" 2>&1)"; then
        kyle_health="unhealthy"
        kyle_summary="$health_body"
        return 0
    fi
    if ! summary_body="$(curl -fsS -m "$KYLE_TIMEOUT" "$summary_url" 2>&1)"; then
        kyle_health="summary_unavailable"
        kyle_summary="$summary_body"
        return 0
    fi
    kyle_health="ok"
    kyle_summary="$summary_body"
}

poll_tasks() {
    local deadline now tasks_out status summary i
    deadline=$((START_EPOCH + TIMEOUT_SECONDS))
    while :; do
        tasks_out="$("$DREMCTL_BIN" --orch-url "$DREM_ORCH_URL" --project "$DREM_PROJECT" --json tasks --limit "$TASK_LIMIT")"
        all_terminal=true
        for i in "${!ids[@]}"; do
            status="$(task_field_by_id "${ids[$i]}" status "$tasks_out")"
            summary="$(task_field_by_id "${ids[$i]}" latest_failure_summary "$tasks_out")"
            [ -n "$status" ] && statuses[$i]="$status"
            [ -n "$summary" ] && failure_summaries[$i]="$summary"
            if ! terminal_status "${statuses[$i]}"; then
                all_terminal=false
            fi
        done
        [ "$all_terminal" = true ] && break
        now="$(date -u +%s)"
        [ "$now" -ge "$deadline" ] && break
        sleep "$INTERVAL_SECONDS"
    done
}

emit_report_json() {
    local end_epoch overall_ok i task_ok duration
    end_epoch="$(date -u +%s)"
    overall_ok=true
    for i in "${!ids[@]}"; do
        task_ok=false
        if [ "${statuses[$i]}" = "done" ]; then
            task_ok=true
        else
            overall_ok=false
        fi
    done
    if include_mode kyle && [ "$kyle_health" != "ok" ]; then
        overall_ok=false
    fi

    printf '{'
    printf '"ok":%s,' "$overall_ok"
    printf '"dry_run":false,'
    printf '"run_id":%s,' "$(json_string "$RUN_ID")"
    printf '"project":%s,' "$(json_string "$DREM_PROJECT")"
    printf '"task_ids":['
    for i in "${!ids[@]}"; do
        [ "$i" -eq 0 ] || printf ','
        json_string "${ids[$i]}"
    done
    printf '],'
    printf '"tasks":['
    for i in "${!ids[@]}"; do
        [ "$i" -eq 0 ] || printf ','
        duration=$((end_epoch - starts[$i]))
        printf '{"mode":%s,"id":%s,"title":%s,"final_state":%s,"duration_seconds":%s,"latest_failure_summary":%s}' \
            "$(json_string "${modes[$i]}")" \
            "$(json_string "${ids[$i]}")" \
            "$(json_string "${titles[$i]}")" \
            "$(json_string "${statuses[$i]}")" \
            "$duration" \
            "$(json_string "${failure_summaries[$i]}")"
    done
    printf '],'
    printf '"failure_canary":{'
    if include_mode failure; then
        printf '"enabled":%s,"mutating":%s,"title":%s' \
            "$failure_canary_enabled" \
            "$failure_canary_enabled" \
            "$(json_string "$(failure_title)")"
    else
        printf '"enabled":false,"mutating":false'
    fi
    printf '},'
    printf '"kyle_health":%s,' "$(json_string "$kyle_health")"
    printf '"kyle_summary":%s' "$(json_string "$kyle_summary")"
    printf '}\n'

    [ "$overall_ok" = true ]
}

main() {
    TIMEOUT_SECONDS="$(duration_seconds "$CANARY_TIMEOUT")" || { echo "invalid timeout: $CANARY_TIMEOUT" >&2; return 2; }
    INTERVAL_SECONDS="$(duration_seconds "$CANARY_INTERVAL")" || { echo "invalid interval: $CANARY_INTERVAL" >&2; return 2; }
    START_EPOCH="$(date -u +%s)"
    RUN_ID="$(date -u +%Y%m%dT%H%M%SZ)-$$"
    ids=()
    titles=()
    modes=()
    starts=()
    statuses=()
    failure_summaries=()
    kyle_health="not_checked"
    kyle_summary=""
    failure_canary_enabled=false

    case "$CANARY_MODE" in
        all|quickfix|standard|kyle|failure) ;;
        *) echo "invalid mode: $CANARY_MODE" >&2; return 2 ;;
    esac

    if [ "${DREM_PIPELINE_CANARY_CONFIRM:-}" != "yes" ]; then
        emit_plan_json
        return 0
    fi
    if { include_mode quickfix || include_mode standard || { include_mode failure && [ "${DREM_PIPELINE_CANARY_FAILURE_CONFIRM:-}" = "yes" ]; }; } && [ -z "$DREM_ACTOR" ]; then
        echo "DREM_ACTOR is required for mutating pipeline canaries" >&2
        return 2
    fi

    if include_mode quickfix; then
        create_task "$(quickfix_title)" "$(quickfix_description)" quickfix
    fi
    if include_mode standard; then
        create_task "$(standard_title)" "$(standard_description)" standard
    fi
    if include_mode failure && [ "${DREM_PIPELINE_CANARY_FAILURE_CONFIRM:-}" = "yes" ]; then
        failure_canary_enabled=true
        create_task "$(failure_title)" "$(failure_description)" failure
    fi
    if [ "${#ids[@]}" -gt 0 ]; then
        poll_tasks
    fi
    if include_mode kyle; then
        check_kyle
    fi
    emit_report_json
}

main "$@"
