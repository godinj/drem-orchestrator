#!/usr/bin/env bash
# C-Suite disk communication protocol library.
# Source this file: source scripts/csuite-proto.sh
#
# Requires: CSUITE_DIR (defaults to ~/.drem-csuite)

CSUITE_DIR="${CSUITE_DIR:-$HOME/.drem-csuite}"

# ---------------------------------------------------------------------------
# Internal helpers
# ---------------------------------------------------------------------------

# Validate that CSUITE_DIR exists.
_csuite_check_dir() {
    if [ ! -d "$CSUITE_DIR" ]; then
        echo "error: CSUITE_DIR does not exist: $CSUITE_DIR" >&2
        return 1
    fi
}

# Validate that an agent directory exists under CSUITE_DIR.
# Usage: _csuite_check_agent <agent>
_csuite_check_agent() {
    local agent="$1"
    _csuite_check_dir || return 1
    if [ ! -d "${CSUITE_DIR}/${agent}" ]; then
        echo "error: agent directory does not exist: ${CSUITE_DIR}/${agent}" >&2
        return 1
    fi
}

# Notify an agent that a message arrived.
# Usage: _csuite_notify <to> <from> <subject> <priority>
#
# Previously this used tmux send-keys to inject a one-liner into the
# recipient's Claude Code session.  That approach interrupts any operator
# interaction happening in the TUI, so it has been removed.
#
# Agents discover new messages by polling their inbox directory on their
# own loop cadence.  No push notification is needed.
_csuite_notify() {
    # No-op.  Agents poll their inboxes; no tmux send-keys notification.
    return 0
}

# ---------------------------------------------------------------------------
# Public API
# ---------------------------------------------------------------------------

# Send a message to an agent's inbox.
# Usage: csuite_send <from> <to> <subject> <priority> <type> <body>
#   from      — sender agent name
#   to        — recipient agent name
#   subject   — message subject (string)
#   priority  — low | normal | high | critical
#   type      — observation | request | report | decision
#   body      — message body (markdown string)
#
# Returns: the path to the created message file on stdout.
csuite_send() {
    local from="$1"
    local to="$2"
    local subject="$3"
    local priority="$4"
    local type="$5"
    local body="$6"

    _csuite_check_agent "$to" || return 1

    local ts_file
    ts_file="$(date -u +%Y%m%d-%H%M%S)"
    local ts_yaml
    ts_yaml="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

    local filename="${ts_file}-${from}.md"
    local filepath="${CSUITE_DIR}/${to}/inbox/${filename}"

    cat > "$filepath" <<MSGEOF
---
from: ${from}
to: ${to}
timestamp: ${ts_yaml}
subject: "${subject}"
priority: ${priority}
type: ${type}
---

${body}
MSGEOF

    # Push-notify the recipient if their session is running
    _csuite_notify "$to" "$from" "$subject" "$priority"

    echo "$filepath"
}

# List unprocessed messages in an agent's inbox.
# Usage: csuite_inbox <agent>
# Outputs one filename per line, sorted chronologically (oldest first).
csuite_inbox() {
    local agent="$1"
    _csuite_check_agent "$agent" || return 1

    local inbox="${CSUITE_DIR}/${agent}/inbox"
    find "$inbox" -maxdepth 1 -type f -name '*.md' | sort
}

# Read a single message from an agent's inbox.
# Usage: csuite_read <agent> <filename>
# Outputs the full message content to stdout.
csuite_read() {
    local agent="$1"
    local filename="$2"
    _csuite_check_agent "$agent" || return 1

    local filepath="${CSUITE_DIR}/${agent}/inbox/${filename}"
    if [ ! -f "$filepath" ]; then
        echo "error: message not found: $filepath" >&2
        return 1
    fi
    cat "$filepath"
}

# Archive a processed message (move from inbox to inbox/archive).
# Usage: csuite_archive <agent> <filename>
csuite_archive() {
    local agent="$1"
    local filename="$2"
    _csuite_check_agent "$agent" || return 1

    local src="${CSUITE_DIR}/${agent}/inbox/${filename}"
    local dst="${CSUITE_DIR}/${agent}/inbox/archive/${filename}"

    if [ ! -f "$src" ]; then
        echo "error: message not found: $src" >&2
        return 1
    fi
    if [ ! -d "${CSUITE_DIR}/${agent}/inbox/archive" ]; then
        mkdir -p "${CSUITE_DIR}/${agent}/inbox/archive"
    fi
    mv "$src" "$dst"
}

# Parse YAML frontmatter field from a message file.
# Usage: csuite_field <filepath> <field>
# Outputs the value (quotes stripped) to stdout.
csuite_field() {
    local filepath="$1"
    local field="$2"

    if [ ! -f "$filepath" ]; then
        echo "error: file not found: $filepath" >&2
        return 1
    fi

    # Extract lines between the first --- and the second ---.
    local value
    value="$(sed -n '/^---$/,/^---$/p' "$filepath" \
        | grep "^${field}:" \
        | head -1 \
        | sed "s/^${field}:[[:space:]]*//" \
        | sed 's/^"\(.*\)"$/\1/' \
        | sed "s/^'\(.*\)'$/\1/")"

    if [ -z "$value" ]; then
        return 1
    fi
    echo "$value"
}

# Update an agent's state file with a heartbeat timestamp.
# Usage: csuite_heartbeat <agent>
# Writes/updates a "heartbeat:" line in the agent's state.md.
csuite_heartbeat() {
    local agent="$1"
    _csuite_check_agent "$agent" || return 1

    local state_file="${CSUITE_DIR}/${agent}/state.md"
    local ts
    ts="$(date -u +%s)"

    if [ ! -f "$state_file" ]; then
        touch "$state_file"
    fi

    if grep -q '^heartbeat:' "$state_file"; then
        sed -i "s/^heartbeat:.*$/heartbeat: ${ts}/" "$state_file"
    else
        echo "heartbeat: ${ts}" >> "$state_file"
    fi
}

# Check if an agent's heartbeat is fresh (updated within N seconds).
# Usage: csuite_is_alive <agent> <max_age_seconds>
# Returns 0 if alive (fresh), 1 if stale or missing.
csuite_is_alive() {
    local agent="$1"
    local max_age="$2"
    _csuite_check_agent "$agent" || return 1

    local state_file="${CSUITE_DIR}/${agent}/state.md"
    if [ ! -f "$state_file" ]; then
        return 1
    fi

    local hb
    hb="$(grep '^heartbeat:' "$state_file" | head -1 | sed 's/^heartbeat:[[:space:]]*//')"
    if [ -z "$hb" ]; then
        return 1
    fi

    local now
    now="$(date -u +%s)"
    local age=$(( now - hb ))

    if [ "$age" -le "$max_age" ]; then
        return 0
    else
        return 1
    fi
}

# Create a temp worker directory structure.
# Usage: csuite_create_worker <worker_id>
#   worker_id — e.g. "worker-001"
# Creates inbox/, inbox/archive/, outbox/, and state.md under temp-workers/<worker_id>.
csuite_create_worker() {
    local worker_id="$1"
    _csuite_check_dir || return 1

    local base="${CSUITE_DIR}/temp-workers/${worker_id}"
    mkdir -p "${base}/inbox/archive"
    mkdir -p "${base}/outbox"
    if [ ! -f "${base}/state.md" ]; then
        touch "${base}/state.md"
    fi
    echo "$base"
}

# List all temp worker directories.
# Usage: csuite_list_workers
# Outputs one worker directory name per line.
csuite_list_workers() {
    _csuite_check_dir || return 1

    local workers_dir="${CSUITE_DIR}/temp-workers"
    if [ ! -d "$workers_dir" ]; then
        return 0
    fi
    find "$workers_dir" -mindepth 1 -maxdepth 1 -type d | sort | while read -r dir; do
        basename "$dir"
    done
}
