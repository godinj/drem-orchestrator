#!/usr/bin/env bash
set -euo pipefail

# Creates the C-Suite agent communication directory structure.
# Usage: bash scripts/csuite-bootstrap.sh
#
# Idempotent — safe to run multiple times. Existing state.md files
# are never overwritten.

CSUITE_DIR="${CSUITE_DIR:-$HOME/.drem-csuite}"

AGENTS=(kyle mike alex seth)

created_dirs=0
created_files=0

for agent in "${AGENTS[@]}"; do
    for dir in "${CSUITE_DIR}/${agent}/inbox/archive" "${CSUITE_DIR}/${agent}/outbox"; do
        if [ ! -d "$dir" ]; then
            mkdir -p "$dir"
            created_dirs=$((created_dirs + 1))
        fi
    done
    state_file="${CSUITE_DIR}/${agent}/state.md"
    if [ ! -f "$state_file" ]; then
        touch "$state_file"
        created_files=$((created_files + 1))
    fi
done

# Create temp-workers parent directory
if [ ! -d "${CSUITE_DIR}/temp-workers" ]; then
    mkdir -p "${CSUITE_DIR}/temp-workers"
    created_dirs=$((created_dirs + 1))
fi

# NOTE: csuite-inbox-watch (fsnotify-based inbox watcher) has been disabled.
# It used tmux send-keys to inject notifications into agent TUI sessions,
# which interrupts any operator interaction in those sessions.
# Agents now discover new messages by polling their inbox directories.
#
# Kill any stale inbox watchers that may still be running.
for agent in "${AGENTS[@]}"; do
    pidfile="${CSUITE_DIR}/${agent}/inbox-watch.pid"
    if [ -f "$pidfile" ]; then
        kill "$(cat "$pidfile")" 2>/dev/null || true
        rm -f "$pidfile"
    fi
done

echo "C-Suite bootstrap complete: ${CSUITE_DIR}"
echo "  Agents: ${AGENTS[*]}"
echo "  Directories created: ${created_dirs}"
echo "  State files created: ${created_files}"
echo "  Temp-workers dir: ${CSUITE_DIR}/temp-workers"

exit 0
