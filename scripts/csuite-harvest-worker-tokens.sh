#!/usr/bin/env bash
set -euo pipefail

# Harvest token usage from Claude Code sessions for C-Suite temp workers.
#
# Discovers the Claude Code session JSONL files for each temp worker,
# parses per-turn usage data (input, output, cache read, cache creation),
# and upserts the aggregated totals into the csuite.db SQLite database.
#
# Usage:
#   csuite-harvest-worker-tokens.sh [options] [worker-id ...]
#
# Arguments:
#   worker-id         One or more worker IDs to harvest (e.g. worker-004).
#                     If omitted, harvests all active (non-archived) workers.
#
# Options:
#   --all             Include archived workers as well.
#   --session-id ID   Manually set the Claude session UUID for a single worker.
#   --dry-run         Print what would be harvested without writing to DB.
#   --quiet           Suppress progress output (only print errors).
#   --help            Show this help message.
#
# Session Discovery:
#   For each worker, the script discovers its Claude Code session in order:
#     1. Stored session_id file at <worker_dir>/session_id
#     2. Active tmux pane PID → ~/.claude/sessions/<pid>.json
#   If neither works, the worker is skipped with a warning.
#
# Database:
#   Creates/updates the temp_worker_tokens table in csuite.db with columns:
#     worker_id, session_id, input_tokens, output_tokens, cache_read_tokens,
#     cache_creation_tokens, total_cost_usd, model, turns, subagent_turns,
#     started_at, harvested_at, status
#
# Token Aggregation:
#   Sums usage across the main session JSONL and all subagent JSONL files
#   in the session's subagents/ directory.
#
# Cost Estimation (Claude Opus 4):
#   Input:          $15.00 / MTok
#   Output:         $75.00 / MTok
#   Cache Read:      $1.50 / MTok
#   Cache Creation: $18.75 / MTok
#
# Environment:
#   CSUITE_DIR     Base directory for agent state (default: ~/.drem-csuite)
#   CLAUDE_DIR     Claude Code config directory (default: ~/.claude)
#
# Examples:
#   # Harvest all active workers:
#   bash scripts/csuite-harvest-worker-tokens.sh
#
#   # Harvest a specific worker:
#   bash scripts/csuite-harvest-worker-tokens.sh worker-004
#
#   # Harvest an archived worker with known session ID:
#   bash scripts/csuite-harvest-worker-tokens.sh --session-id abc-123 worker-001
#
#   # Dry run showing what would happen:
#   bash scripts/csuite-harvest-worker-tokens.sh --dry-run --all

# ---------------------------------------------------------------------------
# Configuration
# ---------------------------------------------------------------------------

CSUITE_DIR="${CSUITE_DIR:-$HOME/.drem-csuite}"
CLAUDE_DIR="${CLAUDE_DIR:-$HOME/.claude}"
CSUITE_DB="${CSUITE_DIR}/csuite.db"
TMUX_SOCKET="drem"

# Claude Code stores per-project session data under this path.
# The project path component is the cwd with slashes replaced by dashes.
PROJECT_KEY="-home-godinj-git-drem-orchestrator-git-master"
SESSIONS_DIR="${CLAUDE_DIR}/projects/${PROJECT_KEY}"

# Cost per million tokens (Claude Opus 4)
COST_INPUT=15.00
COST_OUTPUT=75.00
COST_CACHE_READ=1.50
COST_CACHE_CREATION=18.75

# ---------------------------------------------------------------------------
# Parse arguments
# ---------------------------------------------------------------------------

INCLUDE_ARCHIVED=false
DRY_RUN=false
QUIET=false
MANUAL_SESSION_ID=""
WORKER_IDS=()

while [ $# -gt 0 ]; do
    case "$1" in
        --help|-h)
            awk 'NR>1 && /^#/{sub(/^# ?/,""); print; next} NR>2{exit}' "$0"
            exit 0
            ;;
        --all)
            INCLUDE_ARCHIVED=true
            shift
            ;;
        --session-id)
            MANUAL_SESSION_ID="$2"
            shift 2
            ;;
        --dry-run)
            DRY_RUN=true
            shift
            ;;
        --quiet|-q)
            QUIET=true
            shift
            ;;
        -*)
            echo "error: unknown option: $1" >&2
            exit 1
            ;;
        *)
            WORKER_IDS+=("$1")
            shift
            ;;
    esac
done

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

log() {
    [ "$QUIET" = true ] && return
    echo "[harvest] $*"
}

warn() {
    echo "[harvest] WARNING: $*" >&2
}

# ---------------------------------------------------------------------------
# Ensure dependencies
# ---------------------------------------------------------------------------

if ! command -v jq &>/dev/null; then
    echo "error: jq is required but not found on PATH" >&2
    exit 1
fi

if ! command -v sqlite3 &>/dev/null; then
    echo "error: sqlite3 is required but not found on PATH" >&2
    exit 1
fi

# ---------------------------------------------------------------------------
# Create / migrate database table
# ---------------------------------------------------------------------------

init_db() {
    sqlite3 "$CSUITE_DB" <<'SQL'
PRAGMA journal_mode=WAL;
PRAGMA busy_timeout=5000;
CREATE TABLE IF NOT EXISTS temp_worker_tokens (
    worker_id          TEXT PRIMARY KEY,
    session_id         TEXT,
    input_tokens       INTEGER DEFAULT 0,
    output_tokens      INTEGER DEFAULT 0,
    cache_read_tokens  INTEGER DEFAULT 0,
    cache_creation_tokens INTEGER DEFAULT 0,
    total_cost_usd     REAL DEFAULT 0.0,
    model              TEXT DEFAULT '',
    turns              INTEGER DEFAULT 0,
    subagent_turns     INTEGER DEFAULT 0,
    started_at         TEXT,
    harvested_at       TEXT,
    status             TEXT DEFAULT 'unknown'
);
SQL
}

# ---------------------------------------------------------------------------
# Session discovery
# ---------------------------------------------------------------------------

# Discover session ID for a worker via stored file.
discover_stored_session() {
    local worker_dir="$1"
    local session_file="${worker_dir}/session_id"
    if [ -f "$session_file" ]; then
        cat "$session_file" | tr -d '[:space:]'
        return 0
    fi
    return 1
}

# Discover session ID for a worker via active tmux pane PID.
discover_tmux_session() {
    local worker_id="$1"
    local session_name="csuite-${worker_id}"

    # Get the pane PID from tmux
    local pane_pid
    pane_pid=$(tmux -L "$TMUX_SOCKET" list-panes -t "$session_name" -F '#{pane_pid}' 2>/dev/null | head -1) || return 1

    if [ -z "$pane_pid" ]; then
        return 1
    fi

    # Look up session file by PID
    local session_file="${CLAUDE_DIR}/sessions/${pane_pid}.json"
    if [ -f "$session_file" ]; then
        jq -r '.sessionId' "$session_file" 2>/dev/null
        return 0
    fi

    return 1
}

# Discover session ID using all available methods.
discover_session() {
    local worker_id="$1"
    local worker_dir="$2"

    # Method 1: Manual override
    if [ -n "$MANUAL_SESSION_ID" ]; then
        echo "$MANUAL_SESSION_ID"
        return 0
    fi

    # Method 2: Stored session ID
    local session_id
    session_id=$(discover_stored_session "$worker_dir") && {
        echo "$session_id"
        return 0
    }

    # Method 3: Active tmux session
    session_id=$(discover_tmux_session "$worker_id") && {
        # Store it for future use
        echo "$session_id" > "${worker_dir}/session_id"
        echo "$session_id"
        return 0
    }

    return 1
}

# ---------------------------------------------------------------------------
# Token aggregation
# ---------------------------------------------------------------------------

# Aggregate tokens from a JSONL file. Outputs JSON with totals.
aggregate_jsonl() {
    local jsonl_file="$1"
    [ -f "$jsonl_file" ] || { echo '{"input_tokens":0,"output_tokens":0,"cache_read":0,"cache_creation":0,"turns":0}'; return; }

    jq -s '
        [.[] | select(.type == "assistant" and .message.usage) | .message.usage] |
        {
            input_tokens: (map(.input_tokens // 0) | add // 0),
            output_tokens: (map(.output_tokens // 0) | add // 0),
            cache_read: (map(.cache_read_input_tokens // 0) | add // 0),
            cache_creation: (map(.cache_creation_input_tokens // 0) | add // 0),
            turns: length
        }
    ' "$jsonl_file" 2>/dev/null || echo '{"input_tokens":0,"output_tokens":0,"cache_read":0,"cache_creation":0,"turns":0}'
}

# Get model name from the first assistant message.
get_model() {
    local jsonl_file="$1"
    [ -f "$jsonl_file" ] || { echo "unknown"; return; }
    jq -r '[.[] | select(.type == "assistant" and .message.model)] | .[0].message.model // "unknown"' <(jq -s '.' "$jsonl_file" 2>/dev/null) 2>/dev/null || echo "unknown"
}

# Aggregate tokens from a session (main + subagents).
# Outputs a single JSON object with combined totals.
harvest_session_tokens() {
    local session_id="$1"
    local main_jsonl="${SESSIONS_DIR}/${session_id}.jsonl"

    if [ ! -f "$main_jsonl" ]; then
        warn "JSONL not found: $main_jsonl"
        echo '{"input_tokens":0,"output_tokens":0,"cache_read":0,"cache_creation":0,"turns":0,"subagent_turns":0,"model":"unknown"}'
        return 1
    fi

    # Main session
    local main_result
    main_result=$(aggregate_jsonl "$main_jsonl")

    local model
    model=$(get_model "$main_jsonl")

    # Subagents
    local sub_dir="${SESSIONS_DIR}/${session_id}/subagents"
    local sub_input=0 sub_output=0 sub_cache_read=0 sub_cache_creation=0 sub_turns=0

    if [ -d "$sub_dir" ]; then
        for sub_jsonl in "$sub_dir"/*.jsonl; do
            [ -f "$sub_jsonl" ] || continue
            # Skip .meta.json files
            [[ "$sub_jsonl" == *.meta.json ]] && continue

            local sub_result
            sub_result=$(aggregate_jsonl "$sub_jsonl")

            sub_input=$(( sub_input + $(echo "$sub_result" | jq '.input_tokens') ))
            sub_output=$(( sub_output + $(echo "$sub_result" | jq '.output_tokens') ))
            sub_cache_read=$(( sub_cache_read + $(echo "$sub_result" | jq '.cache_read') ))
            sub_cache_creation=$(( sub_cache_creation + $(echo "$sub_result" | jq '.cache_creation') ))
            sub_turns=$(( sub_turns + $(echo "$sub_result" | jq '.turns') ))
        done
    fi

    # Combine
    local main_input main_output main_cache_read main_cache_creation main_turns
    main_input=$(echo "$main_result" | jq '.input_tokens')
    main_output=$(echo "$main_result" | jq '.output_tokens')
    main_cache_read=$(echo "$main_result" | jq '.cache_read')
    main_cache_creation=$(echo "$main_result" | jq '.cache_creation')
    main_turns=$(echo "$main_result" | jq '.turns')

    jq -n \
        --argjson input "$(( main_input + sub_input ))" \
        --argjson output "$(( main_output + sub_output ))" \
        --argjson cache_read "$(( main_cache_read + sub_cache_read ))" \
        --argjson cache_creation "$(( main_cache_creation + sub_cache_creation ))" \
        --argjson turns "$main_turns" \
        --argjson subagent_turns "$sub_turns" \
        --arg model "$model" \
        '{
            input_tokens: $input,
            output_tokens: $output,
            cache_read: $cache_read,
            cache_creation: $cache_creation,
            turns: $turns,
            subagent_turns: $subagent_turns,
            model: $model
        }'
}

# ---------------------------------------------------------------------------
# Cost calculation
# ---------------------------------------------------------------------------

calc_cost() {
    local input="$1" output="$2" cache_read="$3" cache_creation="$4"
    # Cost = (input * COST_INPUT + output * COST_OUTPUT + cache_read * COST_CACHE_READ + cache_creation * COST_CACHE_CREATION) / 1_000_000
    awk -v i="$input" -v o="$output" -v cr="$cache_read" -v cc="$cache_creation" \
        -v ci="$COST_INPUT" -v co="$COST_OUTPUT" -v ccr="$COST_CACHE_READ" -v ccc="$COST_CACHE_CREATION" \
        'BEGIN { printf "%.4f", (i*ci + o*co + cr*ccr + cc*ccc) / 1000000 }'
}

# ---------------------------------------------------------------------------
# Upsert to database
# ---------------------------------------------------------------------------

upsert_tokens() {
    local worker_id="$1"
    local session_id="$2"
    local tokens_json="$3"
    local status="$4"

    local input output cache_read cache_creation turns subagent_turns model cost started_at
    input=$(echo "$tokens_json" | jq '.input_tokens')
    output=$(echo "$tokens_json" | jq '.output_tokens')
    cache_read=$(echo "$tokens_json" | jq '.cache_read')
    cache_creation=$(echo "$tokens_json" | jq '.cache_creation')
    turns=$(echo "$tokens_json" | jq '.turns')
    subagent_turns=$(echo "$tokens_json" | jq '.subagent_turns')
    model=$(echo "$tokens_json" | jq -r '.model')
    cost=$(calc_cost "$input" "$output" "$cache_read" "$cache_creation")

    # Try to get started_at from session metadata
    started_at=""
    for f in "${CLAUDE_DIR}/sessions/"*.json; do
        [ -f "$f" ] || continue
        local sid
        sid=$(jq -r '.sessionId' "$f" 2>/dev/null)
        if [ "$sid" = "$session_id" ]; then
            local epoch_ms
            epoch_ms=$(jq -r '.startedAt' "$f" 2>/dev/null)
            if [ -n "$epoch_ms" ] && [ "$epoch_ms" != "null" ]; then
                started_at=$(date -u -d @"$(( epoch_ms / 1000 ))" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || true)
            fi
            break
        fi
    done

    # Fall back to worker state.md spawned field
    if [ -z "$started_at" ]; then
        local state_file
        for dir in "${CSUITE_DIR}/temp-workers/${worker_id}" "${CSUITE_DIR}/temp-workers/archive/${worker_id}"; do
            if [ -f "${dir}/state.md" ]; then
                state_file="${dir}/state.md"
                break
            fi
        done
        if [ -n "${state_file:-}" ]; then
            started_at=$(sed -n '/^---$/,/^---$/p' "$state_file" 2>/dev/null \
                | grep '^spawned:' | head -1 \
                | sed 's/^spawned:[[:space:]]*//')
        fi
    fi

    local harvested_at
    harvested_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)

    if [ "$DRY_RUN" = true ]; then
        log "DRY RUN: would upsert $worker_id:"
        log "  session:    $session_id"
        log "  input:      $input"
        log "  output:     $output"
        log "  cache_read: $cache_read"
        log "  cache_create: $cache_creation"
        log "  cost:       \$$cost"
        log "  model:      $model"
        log "  turns:      $turns (+$subagent_turns subagent)"
        log "  started:    ${started_at:-unknown}"
        log "  status:     $status"
        return
    fi

    sqlite3 "$CSUITE_DB" <<SQL
INSERT INTO temp_worker_tokens (
    worker_id, session_id, input_tokens, output_tokens,
    cache_read_tokens, cache_creation_tokens, total_cost_usd,
    model, turns, subagent_turns, started_at, harvested_at, status
) VALUES (
    '${worker_id}', '${session_id}', ${input}, ${output},
    ${cache_read}, ${cache_creation}, ${cost},
    '${model}', ${turns}, ${subagent_turns},
    '${started_at:-}', '${harvested_at}', '${status}'
)
ON CONFLICT(worker_id) DO UPDATE SET
    session_id = '${session_id}',
    input_tokens = ${input},
    output_tokens = ${output},
    cache_read_tokens = ${cache_read},
    cache_creation_tokens = ${cache_creation},
    total_cost_usd = ${cost},
    model = '${model}',
    turns = ${turns},
    subagent_turns = ${subagent_turns},
    started_at = COALESCE(NULLIF('${started_at:-}', ''), started_at),
    harvested_at = '${harvested_at}',
    status = '${status}';
SQL
}

# ---------------------------------------------------------------------------
# Build worker list
# ---------------------------------------------------------------------------

build_worker_list() {
    if [ ${#WORKER_IDS[@]} -gt 0 ]; then
        printf '%s\n' "${WORKER_IDS[@]}"
        return
    fi

    # Active workers
    local workers_dir="${CSUITE_DIR}/temp-workers"
    if [ -d "$workers_dir" ]; then
        for wdir in "$workers_dir"/worker-*/; do
            [ -d "$wdir" ] || continue
            basename "$wdir"
        done
    fi

    # Archived workers (if --all)
    if [ "$INCLUDE_ARCHIVED" = true ] && [ -d "${workers_dir}/archive" ]; then
        for wdir in "${workers_dir}/archive"/worker-*/; do
            [ -d "$wdir" ] || continue
            basename "$wdir"
        done
    fi
}

# Find the worker directory (active or archived).
find_worker_dir() {
    local worker_id="$1"
    local active="${CSUITE_DIR}/temp-workers/${worker_id}"
    local archived="${CSUITE_DIR}/temp-workers/archive/${worker_id}"

    if [ -d "$active" ]; then
        echo "$active"
    elif [ -d "$archived" ]; then
        echo "$archived"
    else
        return 1
    fi
}

# Check if worker is currently running in tmux.
is_worker_running() {
    local worker_id="$1"
    tmux -L "$TMUX_SOCKET" has-session -t "csuite-${worker_id}" 2>/dev/null
}

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

main() {
    init_db

    local workers
    workers=$(build_worker_list | sort -u)

    if [ -z "$workers" ]; then
        log "No workers found to harvest."
        exit 0
    fi

    local total=0 harvested=0 skipped=0

    while IFS= read -r worker_id; do
        [ -z "$worker_id" ] && continue
        total=$(( total + 1 ))

        local worker_dir
        worker_dir=$(find_worker_dir "$worker_id") || {
            warn "Directory not found for $worker_id — skipping"
            skipped=$(( skipped + 1 ))
            continue
        }

        log "Harvesting $worker_id..."

        # Discover session
        local session_id
        session_id=$(discover_session "$worker_id" "$worker_dir") || {
            warn "Cannot discover session for $worker_id — skipping"
            skipped=$(( skipped + 1 ))
            continue
        }

        log "  Session: $session_id"

        # Harvest tokens
        local tokens_json
        tokens_json=$(harvest_session_tokens "$session_id") || {
            warn "Failed to parse tokens for $worker_id (session $session_id)"
            skipped=$(( skipped + 1 ))
            continue
        }

        # Determine status
        local status="completed"
        is_worker_running "$worker_id" && status="running"

        # Upsert
        upsert_tokens "$worker_id" "$session_id" "$tokens_json" "$status"
        harvested=$(( harvested + 1 ))

        local cost
        cost=$(calc_cost \
            "$(echo "$tokens_json" | jq '.input_tokens')" \
            "$(echo "$tokens_json" | jq '.output_tokens')" \
            "$(echo "$tokens_json" | jq '.cache_read')" \
            "$(echo "$tokens_json" | jq '.cache_creation')")

        log "  Tokens: in=$(echo "$tokens_json" | jq '.input_tokens') out=$(echo "$tokens_json" | jq '.output_tokens') cache_read=$(echo "$tokens_json" | jq '.cache_read') cache_create=$(echo "$tokens_json" | jq '.cache_creation')"
        log "  Cost: \$$cost  |  Turns: $(echo "$tokens_json" | jq '.turns') (+$(echo "$tokens_json" | jq '.subagent_turns') subagent)"
    done <<< "$workers"

    log ""
    log "Done: $harvested/$total harvested ($skipped skipped)"

    # Print summary table if not quiet
    if [ "$QUIET" = false ] && [ "$DRY_RUN" = false ] && [ "$harvested" -gt 0 ]; then
        echo ""
        echo "=== Token Usage Summary ==="
        sqlite3 -header -column "$CSUITE_DB" \
            "SELECT worker_id, input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens, printf('\$%.2f', total_cost_usd) as cost, turns, subagent_turns, status FROM temp_worker_tokens ORDER BY worker_id;"
    fi
}

main
