#!/usr/bin/env bash
# csuite-status.sh — unified status script with dashboard and report modes.
# Usage: csuite-status.sh [--report|--dashboard|--both|--help]
#
# Modes:
#   --report     Write situation-report.md and changelog.md to CSUITE_DIR.
#                situation-report.md: pipeline summary, agent health table,
#                inbox counts, temp workers, and recent failures.
#                changelog.md: rolling last 50 events (newest first).
#
#   --dashboard  Render ANSI-colored terminal dashboard to stdout.
#                Shows agent status table (alive/dead, context%, heartbeat
#                freshness, current focus), active temp workers, last 10
#                cross-agent messages, and pipeline snapshot with failures.
#
#   --both       Run both modes (default).
#
#   --help       Show this help message.
#
# Alert Thresholds (dashboard mode):
#   Context >75%   yellow warning
#   Context >85%   red critical
#   Heartbeat stale >5min    yellow
#   Heartbeat stale >15min   red
#
# Data Sources:
#   Agent status     tmux -L drem list-sessions
#   Agent state      $CSUITE_DIR/*/state.md (context_percent, heartbeat, focus)
#   Temp workers     $CSUITE_DIR/temp-workers/worker-*/state.md
#   Pipeline stats   drem stats
#   Failures         drem failures --since=24h
#   Messages         $CSUITE_DIR/*/inbox/*.md (YAML frontmatter)
#
# Recommended Usage:
#   Dashboard (auto-refresh every 5s):
#     watch -n5 -c scripts/csuite-status.sh --dashboard
#
#   Report loop (write files every 2 minutes):
#     while true; do scripts/csuite-status.sh --report; sleep 120; done
#
#   Both modes on demand:
#     scripts/csuite-status.sh --both
#
# Environment:
#   CSUITE_DIR   Base directory for agent state (default: ~/.drem-csuite)
#
set -euo pipefail

usage() {
    awk 'NR>1 && /^#/{sub(/^# ?/,""); print; next} NR>2{exit}' "$0"
}

CSUITE_DIR="${CSUITE_DIR:-$HOME/.drem-csuite}"
AGENTS=(kyle mike alex ross seth)

# Alert thresholds
CTX_WARN=75
CTX_CRIT=85
HB_WARN=300   # 5 minutes
HB_CRIT=900   # 15 minutes

# ANSI colors
RED='\033[0;31m'
YELLOW='\033[0;33m'
GREEN='\033[0;32m'
BOLD='\033[1m'
RESET='\033[0m'

# ---------------------------------------------------------------------------
# Data collection
# ---------------------------------------------------------------------------

_field() {
    local file="$1" key="$2"
    sed -n '/^---$/,/^---$/p' "$file" 2>/dev/null \
        | grep "^${key}:" | head -1 \
        | sed "s/^${key}:[[:space:]]*//" \
        | sed 's/^"\(.*\)"$/\1/' | sed "s/^'\(.*\)'$/\1/"
}

_tmux_sessions() {
    tmux -L drem list-sessions 2>/dev/null | sed 's/:.*//' || true
}

_is_alive() {
    echo "$TMUX_SESSIONS" | grep -qF "csuite-${1}"
}

_inbox_count() {
    local inbox="${CSUITE_DIR}/${1}/inbox"
    if [ -d "$inbox" ]; then
        find "$inbox" -maxdepth 1 -type f -name '*.md' 2>/dev/null | wc -l | tr -d ' '
    else
        echo "0"
    fi
}

_context_pct() {
    local state="${CSUITE_DIR}/${1}/state.md"
    [ -f "$state" ] && _field "$state" "context_percent" || echo "?"
}

_heartbeat() {
    local state="${CSUITE_DIR}/${1}/state.md"
    [ -f "$state" ] && _field "$state" "heartbeat" || echo ""
}

_current_focus() {
    local state="${CSUITE_DIR}/${1}/state.md"
    [ -f "$state" ] && sed -n '/^# Current Focus$/{ n; p; }' "$state" || echo ""
}

# ---------------------------------------------------------------------------
# Report mode
# ---------------------------------------------------------------------------

do_report() {
    TMUX_SESSIONS="$(_tmux_sessions)"
    local stats failures now_ts
    stats="$(drem stats 2>/dev/null || true)"
    failures="$(drem failures --since=24h 2>/dev/null || true)"
    now_ts="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

    # --- situation-report.md via temp file ---
    local tmpsr
    tmpsr="$(mktemp "${CSUITE_DIR}/.tmp.XXXXXX")"

    {
        echo "# Situation Report"
        echo ""
        echo "Generated: ${now_ts}"
        echo ""
        echo "## Pipeline Summary"
        echo ""
        if [ -n "$stats" ]; then
            while IFS='' read -r line; do
                local key val label
                key="${line%%:*}"
                val="${line#*: }"
                label="$(echo "$key" | awk '{print toupper(substr($0,1,1)) substr($0,2)}')"
                echo "- ${label}: ${val}"
            done <<< "$stats"
        else
            echo "No pipeline data available."
        fi
        echo ""
        echo "## Recent Failures"
        echo ""
        if [ -n "$failures" ]; then
            while IFS='' read -r line; do
                echo "- ${line}"
            done <<< "$failures"
        else
            echo "None."
        fi
        echo ""
        echo "## Agent Health"
        echo ""
        echo "| Agent | Status | Context% | Inbox | Focus |"
        echo "|-------|--------|----------|-------|-------|"
        for agent in "${AGENTS[@]}"; do
            [ -d "${CSUITE_DIR}/${agent}" ] || continue
            local status="dead"
            _is_alive "$agent" && status="alive"
            local ctx inbox focus
            ctx="$(_context_pct "$agent")"
            inbox="$(_inbox_count "$agent")"
            focus="$(_current_focus "$agent")"
            echo "| ${agent} | ${status} | ${ctx}% | ${inbox} | ${focus} |"
        done
        echo ""
        echo "## Inbox Summary"
        echo ""
        for agent in "${AGENTS[@]}"; do
            [ -d "${CSUITE_DIR}/${agent}" ] || continue
            echo "- ${agent}: $(_inbox_count "$agent")"
        done
        echo ""
        echo "## Temp Workers"
        echo ""
        local workers_dir="${CSUITE_DIR}/temp-workers"
        local found_workers=0
        if [ -d "$workers_dir" ]; then
            for wdir in "$workers_dir"/*/; do
                [ -d "$wdir" ] || continue
                found_workers=1
                local wid wreq wstatus wtask
                wid="$(basename "$wdir")"
                if [ -f "${wdir}/state.md" ]; then
                    wreq="$(_field "${wdir}/state.md" "requester" 2>/dev/null || echo "?")"
                    wstatus="$(_field "${wdir}/state.md" "status" 2>/dev/null || echo "?")"
                    wtask="$(_field "${wdir}/state.md" "task" 2>/dev/null || echo "?")"
                else
                    wreq="?" wstatus="?" wtask="?"
                fi
                echo "- ${wid}: requester=${wreq}, status=${wstatus}, task=${wtask}"
            done
        fi
        [ "$found_workers" -eq 0 ] && echo "None."
    } > "$tmpsr"

    mv "$tmpsr" "${CSUITE_DIR}/situation-report.md"

    # --- changelog.md ---
    local tmpcl
    tmpcl="$(mktemp "${CSUITE_DIR}/.tmp.XXXXXX")"

    # Generate new events from current state
    local new_events=""
    for agent in "${AGENTS[@]}"; do
        [ -d "${CSUITE_DIR}/${agent}" ] || continue
        local status="dead"
        _is_alive "$agent" && status="alive"
        local ctx
        ctx="$(_context_pct "$agent")"
        new_events="${new_events}${now_ts} | ${agent} | status=${status}, ctx=${ctx}% | auto
"
    done

    # Prepend new events to existing, cap at 50 entries with |
    {
        if [ -n "$new_events" ]; then
            echo -n "$new_events"
        fi
        if [ -f "${CSUITE_DIR}/changelog.md" ]; then
            cat "${CSUITE_DIR}/changelog.md"
        fi
    } | { grep '|' || true; } | head -50 > "$tmpcl"

    mv "$tmpcl" "${CSUITE_DIR}/changelog.md"
}

# ---------------------------------------------------------------------------
# Dashboard mode
# ---------------------------------------------------------------------------

do_dashboard() {
    TMUX_SESSIONS="$(_tmux_sessions)"
    local stats failures now_ts now_epoch
    stats="$(drem stats 2>/dev/null || true)"
    failures="$(drem failures --since=24h 2>/dev/null || true)"
    now_ts="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    now_epoch="$(date -u +%s)"

    printf "${BOLD}=== C-Suite Dashboard ===${RESET}  %s\n\n" "$now_ts"

    printf "${BOLD}%-8s %-8s %-10s %-12s %s${RESET}\n" "Agent" "Status" "Context%" "Heartbeat" "Focus"
    printf '%.0s-' {1..78}; echo
    for agent in "${AGENTS[@]}"; do
        [ -d "${CSUITE_DIR}/${agent}" ] || continue
        local status_color status_text="dead"
        if _is_alive "$agent"; then
            status_color="${GREEN}"; status_text="alive"
        else
            status_color="${RED}"
        fi

        local ctx ctx_color="${RESET}"
        ctx="$(_context_pct "$agent")"
        if [ "$ctx" != "?" ]; then
            [ "$ctx" -ge "$CTX_CRIT" ] 2>/dev/null && ctx_color="${RED}"
            [ "$ctx" -ge "$CTX_WARN" ] 2>/dev/null && [ "$ctx" -lt "$CTX_CRIT" ] 2>/dev/null && ctx_color="${YELLOW}"
        fi

        local hb hb_text hb_color="${RESET}"
        hb="$(_heartbeat "$agent")"
        if [ -n "$hb" ] && [ "$hb" != "?" ]; then
            local age=$(( now_epoch - hb ))
            hb_text="${age}s ago"
            if [ "$age" -ge "$HB_CRIT" ]; then hb_color="${RED}"
            elif [ "$age" -ge "$HB_WARN" ]; then hb_color="${YELLOW}"
            else hb_color="${GREEN}"; fi
        else
            hb_text="unknown"
        fi

        local focus
        focus="$(_current_focus "$agent")"
        printf "%-8s ${status_color}%-8s${RESET} ${ctx_color}%-10s${RESET} ${hb_color}%-12s${RESET} %s\n" \
            "$agent" "$status_text" "${ctx}%" "$hb_text" "$focus"
    done
    echo

    printf "${BOLD}Active Temp Workers${RESET}\n"
    local workers_dir="${CSUITE_DIR}/temp-workers" found=0
    if [ -d "$workers_dir" ]; then
        for wdir in "$workers_dir"/*/; do
            [ -d "$wdir" ] || continue
            found=1
            local wid wreq wstatus wtask
            wid="$(basename "$wdir")"
            if [ -f "${wdir}/state.md" ]; then
                wreq="$(_field "${wdir}/state.md" "requester" 2>/dev/null || echo "?")"
                wstatus="$(_field "${wdir}/state.md" "status" 2>/dev/null || echo "?")"
                wtask="$(_field "${wdir}/state.md" "task" 2>/dev/null || echo "?")"
            else
                wreq="?" wstatus="?" wtask="?"
            fi
            printf "  %-14s %-10s %-10s %s\n" "$wid" "$wreq" "$wstatus" "$wtask"
        done
    fi
    [ "$found" -eq 0 ] && echo "  None."
    echo

    # Token usage from harvest data (if available)
    if sqlite3 "$CSUITE_DIR/csuite.db" "SELECT 1 FROM temp_worker_tokens LIMIT 1" &>/dev/null 2>&1; then
        printf "${BOLD}Temp Worker Token Usage${RESET}\n"
        printf "  ${BOLD}%-14s %10s %10s %12s %8s${RESET}\n" "Worker" "Input" "Output" "Cache Read" "Cost"
        printf '  %s\n' "$(printf '%.0s-' {1..56})"
        sqlite3 "$CSUITE_DIR/csuite.db" \
            "SELECT worker_id, input_tokens, output_tokens, cache_read_tokens, printf('\$%.2f', total_cost_usd) FROM temp_worker_tokens ORDER BY worker_id;" \
            2>/dev/null | while IFS='|' read -r wid inp outp cread cost; do
            printf "  %-14s %10s %10s %12s %8s\n" "$wid" "$inp" "$outp" "$cread" "$cost"
        done
        local total_cost
        total_cost=$(sqlite3 "$CSUITE_DIR/csuite.db" "SELECT printf('\$%.2f', COALESCE(SUM(total_cost_usd), 0)) FROM temp_worker_tokens;" 2>/dev/null || echo "\$0.00")
        printf '  %s\n' "$(printf '%.0s-' {1..56})"
        printf "  ${BOLD}%-14s %10s %10s %12s %8s${RESET}\n" "TOTAL" "" "" "" "$total_cost"
        echo
    fi

    printf "${BOLD}Recent Messages (last 10)${RESET}\n"
    local all_msgs=""
    for agent in "${AGENTS[@]}"; do
        local inbox="${CSUITE_DIR}/${agent}/inbox"
        [ -d "$inbox" ] || continue
        for msg in "$inbox"/*.md; do
            [ -f "$msg" ] || continue
            local mfrom mto msubj mts
            mfrom="$(_field "$msg" "from" 2>/dev/null || echo "?")"
            mto="$(_field "$msg" "to" 2>/dev/null || echo "?")"
            msubj="$(_field "$msg" "subject" 2>/dev/null || echo "?")"
            mts="$(_field "$msg" "timestamp" 2>/dev/null || echo "?")"
            all_msgs+="${mts}|${mfrom}>${mto}|${msubj}"$'\n'
        done
    done
    if [ -n "$all_msgs" ]; then
        echo "$all_msgs" | sort -r | head -10 | while IFS='|' read -r ts route subj; do
            [ -z "$ts" ] && continue
            printf "  %s  %-16s %s\n" "$ts" "$route" "$subj"
        done
    else
        echo "  No messages."
    fi
    echo

    printf "${BOLD}Pipeline Snapshot${RESET}\n"
    if [ -n "$stats" ]; then
        while IFS='' read -r line; do echo "  $line"; done <<< "$stats"
    else
        echo "  No pipeline data."
    fi
    if [ -n "$failures" ]; then
        echo
        printf "${BOLD}Recent Failures${RESET}\n"
        while IFS='' read -r line; do printf "  ${RED}%s${RESET}\n" "$line"; done <<< "$failures"
    fi
}

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

MODE="${1:---both}"

case "$MODE" in
    --help)    usage; exit 0 ;;
    --report)  do_report ;;
    --dashboard) do_dashboard ;;
    --both)    do_report; do_dashboard ;;
    *)
        echo "error: unknown flag: $1" >&2
        usage >&2
        exit 2
        ;;
esac
