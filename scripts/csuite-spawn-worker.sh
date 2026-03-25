#!/usr/bin/env bash
set -euo pipefail

# Spawn a C-Suite temp worker.
#
# Usage: bash scripts/csuite-spawn-worker.sh <task-brief-file> [--worker-id <id>]
#
# Assigns the next available worker ID (or uses the one provided),
# creates the worker directory structure, copies the task brief into
# the worker's inbox, launches a Claude Code session in tmux, and
# notifies Mike that the worker is active.
#
# Arguments:
#   task-brief-file   Path to the markdown task brief to place in the
#                     worker's inbox.
#
# Options:
#   --worker-id <id>  Override the auto-assigned worker ID (e.g. worker-042).
#   --dry-run         Print what would happen without creating anything.
#   --help            Show this help message.
#
# Prerequisites:
#   - tmux available on PATH
#   - claude available on PATH
#   - CSUITE_DIR set or defaulting to ~/.drem-csuite
#   - csuite-proto.sh available in the same directory as this script
#
# Environment:
#   CSUITE_DIR   Base directory for agent state (default: ~/.drem-csuite)
#
# Examples:
#   # Auto-assign next worker ID:
#   bash scripts/csuite-spawn-worker.sh /tmp/task-brief.md
#
#   # Specify a worker ID:
#   bash scripts/csuite-spawn-worker.sh /tmp/task-brief.md --worker-id worker-042

# ---------------------------------------------------------------------------
# Configuration
# ---------------------------------------------------------------------------

CSUITE_DIR="${CSUITE_DIR:-$HOME/.drem-csuite}"
TMUX_SOCKET="drem"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
MASTER_WT="/home/godinj/git/drem-orchestrator.git/master"

# Use master worktree if it exists, otherwise fall back to repo dir
if [ -d "$MASTER_WT" ]; then
    WORK_DIR="$MASTER_WT"
else
    WORK_DIR="$REPO_DIR"
fi

TEMP_WORKER_PROMPT="docs/csuite-agents/prompts/temp-worker.md"
DRY_RUN=false

# ---------------------------------------------------------------------------
# Parse arguments
# ---------------------------------------------------------------------------

TASK_BRIEF=""
WORKER_ID_OVERRIDE=""

while [ $# -gt 0 ]; do
    case "$1" in
        --help|-h)
            echo "Usage: bash scripts/csuite-spawn-worker.sh <task-brief-file> [--worker-id <id>]"
            echo ""
            echo "Spawn a C-Suite temp worker with the given task brief."
            echo ""
            echo "Arguments:"
            echo "  task-brief-file   Path to the markdown task brief"
            echo ""
            echo "Options:"
            echo "  --worker-id <id>  Override auto-assigned worker ID (e.g. worker-042)"
            echo "  --dry-run         Print actions without executing them"
            echo "  --help            Show this help message"
            exit 0
            ;;
        --worker-id)
            if [ -z "${2:-}" ]; then
                echo "error: --worker-id requires a value" >&2
                exit 1
            fi
            WORKER_ID_OVERRIDE="$2"
            shift 2
            ;;
        --dry-run)
            DRY_RUN=true
            shift
            ;;
        -*)
            echo "error: unknown option: $1" >&2
            echo "Usage: bash scripts/csuite-spawn-worker.sh <task-brief-file> [--worker-id <id>]" >&2
            exit 1
            ;;
        *)
            if [ -z "$TASK_BRIEF" ]; then
                TASK_BRIEF="$1"
            else
                echo "error: unexpected argument: $1" >&2
                echo "Usage: bash scripts/csuite-spawn-worker.sh <task-brief-file> [--worker-id <id>]" >&2
                exit 1
            fi
            shift
            ;;
    esac
done

# ---------------------------------------------------------------------------
# Validate inputs
# ---------------------------------------------------------------------------

if [ -z "$TASK_BRIEF" ]; then
    echo "error: task brief file is required" >&2
    echo "Usage: bash scripts/csuite-spawn-worker.sh <task-brief-file> [--worker-id <id>]" >&2
    exit 1
fi

if [ ! -f "$TASK_BRIEF" ]; then
    echo "error: task brief not found: $TASK_BRIEF" >&2
    exit 1
fi

if ! command -v tmux &>/dev/null; then
    echo "error: tmux is not installed or not on PATH." >&2
    exit 1
fi

if ! command -v claude &>/dev/null; then
    echo "error: claude CLI is not installed or not on PATH." >&2
    exit 1
fi

# ---------------------------------------------------------------------------
# Source protocol library
# ---------------------------------------------------------------------------

PROTO_SCRIPT="${SCRIPT_DIR}/csuite-proto.sh"
if [ ! -f "$PROTO_SCRIPT" ]; then
    echo "error: csuite-proto.sh not found at $PROTO_SCRIPT" >&2
    exit 1
fi

# shellcheck source=csuite-proto.sh
source "$PROTO_SCRIPT"

# ---------------------------------------------------------------------------
# Assign worker ID
# ---------------------------------------------------------------------------

if [ -n "$WORKER_ID_OVERRIDE" ]; then
    WORKER_ID="$WORKER_ID_OVERRIDE"
else
    LAST_ID=$(ls -d "${CSUITE_DIR}/temp-workers/worker-"* 2>/dev/null \
        | sed 's/.*worker-//' | sort -n | tail -1 || true)
    NEXT_NUM=$(( ${LAST_ID:-0} + 1 ))
    WORKER_ID="worker-$(printf '%03d' "$NEXT_NUM")"
fi

WORKER_DIR="${CSUITE_DIR}/temp-workers/${WORKER_ID}"
SESSION_NAME="csuite-${WORKER_ID}"

# ---------------------------------------------------------------------------
# Dry-run mode
# ---------------------------------------------------------------------------

if [ "$DRY_RUN" = true ]; then
    echo "DRY RUN — would perform these actions:"
    echo "  Worker ID:    $WORKER_ID"
    echo "  Worker dir:   $WORKER_DIR"
    echo "  Task brief:   $TASK_BRIEF → ${WORKER_DIR}/inbox/$(basename "$TASK_BRIEF")"
    echo "  Tmux session: $SESSION_NAME (socket: $TMUX_SOCKET)"
    echo "  Notify:       mike via csuite_send"
    exit 0
fi

# ---------------------------------------------------------------------------
# Check for existing active worker session
# ---------------------------------------------------------------------------

if tmux -L "$TMUX_SOCKET" has-session -t "$SESSION_NAME" 2>/dev/null; then
    echo "error: worker session '$SESSION_NAME' is already running." >&2
    echo "Cannot spawn a new worker with ID '$WORKER_ID' while its session is active." >&2
    exit 1
fi

# ---------------------------------------------------------------------------
# Create worker directory
# ---------------------------------------------------------------------------

echo "Creating worker directory for ${WORKER_ID}..."
csuite_create_worker "$WORKER_ID" >/dev/null

# ---------------------------------------------------------------------------
# Copy task brief to worker's inbox
# ---------------------------------------------------------------------------

BRIEF_FILENAME="$(basename "$TASK_BRIEF")"
cp "$TASK_BRIEF" "${WORKER_DIR}/inbox/${BRIEF_FILENAME}"
echo "Copied task brief to ${WORKER_DIR}/inbox/${BRIEF_FILENAME}"

# ---------------------------------------------------------------------------
# Initialize worker state file
# ---------------------------------------------------------------------------

cat > "${WORKER_DIR}/state.md" <<STATE_EOF
---
requester: mike
status: active
task: "$(head -1 "$TASK_BRIEF" | sed 's/^#* *//' | head -c 80)"
spawned: $(date -u +%Y-%m-%dT%H:%M:%SZ)
---
STATE_EOF

# ---------------------------------------------------------------------------
# Launch worker tmux session
# ---------------------------------------------------------------------------

echo "Launching ${WORKER_ID} in tmux session '${SESSION_NAME}'..."

INITIAL_PROMPT="You are ${WORKER_ID}. Read your task brief at ~/.drem-csuite/temp-workers/${WORKER_ID}/inbox/ and begin."

# Determine system prompt path
SYSTEM_PROMPT=""
if [ -f "$WORK_DIR/$TEMP_WORKER_PROMPT" ]; then
    SYSTEM_PROMPT="$TEMP_WORKER_PROMPT"
fi

if [ -n "$SYSTEM_PROMPT" ]; then
    tmux -L "$TMUX_SOCKET" new-session -d -s "$SESSION_NAME" \
        "cd '$WORK_DIR' && CSUITE_AGENT='$WORKER_ID' claude \
            --system-prompt '$SYSTEM_PROMPT' \
            --dangerously-skip-permissions \
            '$INITIAL_PROMPT'"
else
    tmux -L "$TMUX_SOCKET" new-session -d -s "$SESSION_NAME" \
        "cd '$WORK_DIR' && CSUITE_AGENT='$WORKER_ID' claude \
            --dangerously-skip-permissions \
            '$INITIAL_PROMPT'"
fi

echo "Worker ${WORKER_ID} started in tmux session '${SESSION_NAME}'."

# ---------------------------------------------------------------------------
# Notify Mike
# ---------------------------------------------------------------------------

csuite_send ross mike "Worker ${WORKER_ID} launched" normal report \
    "Temp worker ${WORKER_ID} has been launched with task brief '${BRIEF_FILENAME}'. Session: ${SESSION_NAME}." >/dev/null

echo "Notified Mike that ${WORKER_ID} is active."

# ---------------------------------------------------------------------------
# Output summary
# ---------------------------------------------------------------------------

echo ""
echo "=== Spawn Summary ==="
echo "  Worker ID:    ${WORKER_ID}"
echo "  Directory:    ${WORKER_DIR}"
echo "  Session:      ${SESSION_NAME} (tmux -L ${TMUX_SOCKET})"
echo "  Task brief:   ${BRIEF_FILENAME}"
echo "  Attach:       tmux -L ${TMUX_SOCKET} attach -t ${SESSION_NAME}"
