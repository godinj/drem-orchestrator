#!/usr/bin/env bash
set -euo pipefail

# Spawn a C-Suite temp worker.
#
# Usage: bash scripts/csuite-spawn-worker.sh <task-brief-file> [options]
#
# Assigns the next available worker ID (or uses the one provided),
# creates the worker directory structure, copies the task brief into
# the worker's inbox, launches a harness session (Claude Code or
# OpenCode) in tmux, and notifies Mike that the worker is active.
#
# Arguments:
#   task-brief-file   Path to the markdown task brief to place in the
#                     worker's inbox.
#
# Options:
#   --worker-id <id>  Override the auto-assigned worker ID (e.g. worker-042).
#   --harness <name>  Harness CLI to use: "claude" (default) or "opencode".
#   --model <model>   Model override (e.g. llamacpp/qwen35-27b-iq4xs-128k).
#                     For opencode this is required; for claude it is optional.
#   --dry-run         Print what would happen without creating anything.
#   --help            Show this help message.
#
# Prerequisites:
#   - tmux available on PATH
#   - claude or opencode available on PATH (depending on --harness)
#   - CSUITE_DIR set or defaulting to ~/.drem-csuite
#   - csuite-proto.sh available in the same directory as this script
#
# Environment:
#   CSUITE_DIR        Base directory for agent state (default: ~/.drem-csuite)
#   CSUITE_HARNESS    Harness CLI: "claude" or "opencode" (default: claude).
#                     Overridden by --harness flag.
#   CSUITE_MODEL      Model name for the harness. Overridden by --model flag.
#
# Examples:
#   # Auto-assign next worker ID (uses claude by default):
#   bash scripts/csuite-spawn-worker.sh /tmp/task-brief.md
#
#   # Specify a worker ID:
#   bash scripts/csuite-spawn-worker.sh /tmp/task-brief.md --worker-id worker-042
#
#   # Use OpenCode with a local model:
#   bash scripts/csuite-spawn-worker.sh /tmp/task-brief.md \
#       --harness opencode --model llamacpp/qwen35-27b-iq4xs-128k
#
#   # Same via environment variables:
#   CSUITE_HARNESS=opencode CSUITE_MODEL=llamacpp/qwen35-27b-iq4xs-128k \
#       bash scripts/csuite-spawn-worker.sh /tmp/task-brief.md

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

# Harness configuration: env vars provide defaults, flags override.
HARNESS="${CSUITE_HARNESS:-claude}"
MODEL="${CSUITE_MODEL:-}"

# ---------------------------------------------------------------------------
# Parse arguments
# ---------------------------------------------------------------------------

TASK_BRIEF=""
WORKER_ID_OVERRIDE=""

USAGE_LINE="Usage: bash scripts/csuite-spawn-worker.sh <task-brief-file> [--worker-id <id>] [--harness claude|opencode] [--model <model>]"

while [ $# -gt 0 ]; do
    case "$1" in
        --help|-h)
            echo "$USAGE_LINE"
            echo ""
            echo "Spawn a C-Suite temp worker with the given task brief."
            echo ""
            echo "Arguments:"
            echo "  task-brief-file     Path to the markdown task brief"
            echo ""
            echo "Options:"
            echo "  --worker-id <id>    Override auto-assigned worker ID (e.g. worker-042)"
            echo "  --harness <name>    Harness CLI: \"claude\" (default) or \"opencode\""
            echo "  --model <model>     Model override (e.g. llamacpp/qwen35-27b-iq4xs-128k)"
            echo "  --dry-run           Print actions without executing them"
            echo "  --help              Show this help message"
            echo ""
            echo "Environment variables:"
            echo "  CSUITE_HARNESS      Harness CLI (default: claude). Overridden by --harness."
            echo "  CSUITE_MODEL        Model name. Overridden by --model."
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
        --harness)
            if [ -z "${2:-}" ]; then
                echo "error: --harness requires a value (claude or opencode)" >&2
                exit 1
            fi
            HARNESS="$2"
            shift 2
            ;;
        --model)
            if [ -z "${2:-}" ]; then
                echo "error: --model requires a value" >&2
                exit 1
            fi
            MODEL="$2"
            shift 2
            ;;
        --dry-run)
            DRY_RUN=true
            shift
            ;;
        -*)
            echo "error: unknown option: $1" >&2
            echo "$USAGE_LINE" >&2
            exit 1
            ;;
        *)
            if [ -z "$TASK_BRIEF" ]; then
                TASK_BRIEF="$1"
            else
                echo "error: unexpected argument: $1" >&2
                echo "$USAGE_LINE" >&2
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
    echo "$USAGE_LINE" >&2
    exit 1
fi

if [ ! -f "$TASK_BRIEF" ]; then
    echo "error: task brief not found: $TASK_BRIEF" >&2
    exit 1
fi

# Validate harness value.
case "$HARNESS" in
    claude|opencode) ;;
    *)
        echo "error: unsupported harness '$HARNESS'. Use 'claude' or 'opencode'." >&2
        exit 1
        ;;
esac

if ! command -v tmux &>/dev/null; then
    echo "error: tmux is not installed or not on PATH." >&2
    exit 1
fi

if ! command -v "$HARNESS" &>/dev/null; then
    echo "error: $HARNESS CLI is not installed or not on PATH." >&2
    exit 1
fi

# ---------------------------------------------------------------------------
# Enforce global temp worker cap (operator directive: max 5)
# ---------------------------------------------------------------------------

MAX_TEMP_WORKERS=5
ACTIVE_WORKERS=$(tmux -L "$TMUX_SOCKET" list-sessions 2>/dev/null | grep -c "csuite-worker" || true)
if [ "$ACTIVE_WORKERS" -ge "$MAX_TEMP_WORKERS" ] && [ "$DRY_RUN" = false ]; then
    echo "error: $ACTIVE_WORKERS temp workers already running (cap: $MAX_TEMP_WORKERS). Queue this request." >&2
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
    echo "  Harness:      $HARNESS"
    echo "  Model:        ${MODEL:-<default>}"
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
harness: ${HARNESS}
model: ${MODEL:-default}
task: "$(head -1 "$TASK_BRIEF" | sed 's/^#* *//' | head -c 80)"
spawned: $(date -u +%Y-%m-%dT%H:%M:%SZ)
---
STATE_EOF

# ---------------------------------------------------------------------------
# Launch worker tmux session
# ---------------------------------------------------------------------------

echo "Launching ${WORKER_ID} in tmux session '${SESSION_NAME}' (harness: ${HARNESS})..."

INITIAL_PROMPT="You are ${WORKER_ID}. Read your task brief at ~/.drem-csuite/temp-workers/${WORKER_ID}/inbox/ and begin."

# Determine system prompt path (used by Claude; OpenCode reads it differently).
SYSTEM_PROMPT=""
if [ -f "$WORK_DIR/$TEMP_WORKER_PROMPT" ]; then
    SYSTEM_PROMPT="$TEMP_WORKER_PROMPT"
fi

# Build the harness command depending on the selected provider.
# Mirrors the invocation patterns in internal/agent/runner.go and
# internal/model/agentconfig.go.
if [ "$HARNESS" = "opencode" ]; then
    # ---------------------------------------------------------------
    # OpenCode path
    #
    # Invocation pattern (from internal/agent/process.go):
    #   opencode run [--model <m>] --format json --agent build --dir <cwd> <prompt>
    #
    # The system prompt is prepended to the initial prompt since OpenCode
    # takes a single positional prompt argument rather than a --system-prompt
    # flag.
    # ---------------------------------------------------------------
    # Build the combined prompt: system prompt (if available) + initial prompt.
    # OpenCode takes a single positional prompt, so we read the system prompt
    # file at script time and concatenate it with the initial task prompt.
    if [ -n "$SYSTEM_PROMPT" ] && [ -f "$WORK_DIR/$SYSTEM_PROMPT" ]; then
        SYSTEM_PROMPT_CONTENT="$(cat "$WORK_DIR/$SYSTEM_PROMPT")"
        COMBINED_PROMPT="${SYSTEM_PROMPT_CONTENT}

${INITIAL_PROMPT}"
    else
        COMBINED_PROMPT="$INITIAL_PROMPT"
    fi

    # Write the combined prompt to a file so we avoid shell quoting issues
    # inside the tmux command string.
    OC_PROMPT_FILE="${WORKER_DIR}/opencode-prompt.md"
    printf '%s\n' "$COMBINED_PROMPT" > "$OC_PROMPT_FILE"

    # Build a self-contained shell script for tmux to execute, avoiding
    # embedded-quote problems with variable interpolation.
    OC_LAUNCH_SCRIPT="${WORKER_DIR}/launch.sh"
    cat > "$OC_LAUNCH_SCRIPT" <<LAUNCH_EOF
#!/usr/bin/env bash
cd "$WORK_DIR"
export CSUITE_AGENT="$WORKER_ID"
exec opencode run \\
    --model "$MODEL" \\
    "\$(cat "$OC_PROMPT_FILE")"
LAUNCH_EOF
    chmod +x "$OC_LAUNCH_SCRIPT"

    tmux -L "$TMUX_SOCKET" new-session -d -s "$SESSION_NAME" "$OC_LAUNCH_SCRIPT"
else
    # ---------------------------------------------------------------
    # Claude Code path (default, backward compatible)
    #
    # Invocation pattern:
    #   claude [--model <m>] [--system-prompt <path>] --dangerously-skip-permissions <prompt>
    # ---------------------------------------------------------------
    CLAUDE_ARGS=""
    if [ -n "$MODEL" ]; then
        CLAUDE_ARGS="--model '$MODEL'"
    fi

    if [ -n "$SYSTEM_PROMPT" ]; then
        tmux -L "$TMUX_SOCKET" new-session -d -s "$SESSION_NAME" \
            "cd '$WORK_DIR' && CSUITE_AGENT='$WORKER_ID' claude \
                $CLAUDE_ARGS \
                --system-prompt '$SYSTEM_PROMPT' \
                --dangerously-skip-permissions \
                '$INITIAL_PROMPT'"
    else
        tmux -L "$TMUX_SOCKET" new-session -d -s "$SESSION_NAME" \
            "cd '$WORK_DIR' && CSUITE_AGENT='$WORKER_ID' claude \
                $CLAUDE_ARGS \
                --dangerously-skip-permissions \
                '$INITIAL_PROMPT'"
    fi
fi

echo "Worker ${WORKER_ID} started in tmux session '${SESSION_NAME}' (harness: ${HARNESS})."

# ---------------------------------------------------------------------------
# Record session ID for tracking
# ---------------------------------------------------------------------------

if [ "$HARNESS" = "claude" ]; then
    # Wait briefly for Claude to initialize and register its session file,
    # then capture the session UUID from ~/.claude/sessions/<pane_pid>.json.
    (
        sleep 3
        PANE_PID=$(tmux -L "$TMUX_SOCKET" list-panes -t "$SESSION_NAME" -F '#{pane_pid}' 2>/dev/null | head -1)
        if [ -n "$PANE_PID" ] && [ -f "${HOME}/.claude/sessions/${PANE_PID}.json" ]; then
            SESSION_UUID=$(jq -r '.sessionId' "${HOME}/.claude/sessions/${PANE_PID}.json" 2>/dev/null)
            if [ -n "$SESSION_UUID" ] && [ "$SESSION_UUID" != "null" ]; then
                echo "$SESSION_UUID" > "${WORKER_DIR}/session_id"
                echo "Recorded Claude session ID: ${SESSION_UUID}"
            fi
        fi
    ) &
else
    # OpenCode writes metadata to .opencode/ in the working directory.
    # Record the PID for monitoring.
    (
        sleep 2
        PANE_PID=$(tmux -L "$TMUX_SOCKET" list-panes -t "$SESSION_NAME" -F '#{pane_pid}' 2>/dev/null | head -1)
        if [ -n "$PANE_PID" ]; then
            echo "$PANE_PID" > "${WORKER_DIR}/session_id"
            echo "Recorded OpenCode pane PID: ${PANE_PID}"
        fi
    ) &
fi

# ---------------------------------------------------------------------------
# Output summary
# ---------------------------------------------------------------------------

echo ""
echo "=== Spawn Summary ==="
echo "  Worker ID:    ${WORKER_ID}"
echo "  Harness:      ${HARNESS}"
echo "  Model:        ${MODEL:-<default>}"
echo "  Directory:    ${WORKER_DIR}"
echo "  Session:      ${SESSION_NAME} (tmux -L ${TMUX_SOCKET})"
echo "  Task brief:   ${BRIEF_FILENAME}"
echo "  Attach:       tmux -L ${TMUX_SOCKET} attach -t ${SESSION_NAME}"
