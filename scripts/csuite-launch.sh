#!/usr/bin/env bash
set -euo pipefail

# Launch Kyle (CEO) -- the C-Suite entry point.
#
# Usage: bash scripts/csuite-launch.sh [--all]
#
# Options:
#   --all    Write a "start everyone" message to Kyle's inbox before launch,
#            so Kyle starts all C-Suite agents immediately.
#
# Prerequisites:
#   - tmux available on PATH
#   - claude available on PATH
#   - drem-orchestrator repo at expected location
#
# This script:
#   1. Runs csuite-bootstrap.sh to ensure directory structure
#   2. Checks if Kyle is already running
#   3. If running, attaches to Kyle's session
#   4. If not running, starts Kyle and attaches

# ---------------------------------------------------------------------------
# Configuration
# ---------------------------------------------------------------------------

CSUITE_DIR="${CSUITE_DIR:-$HOME/.drem-csuite}"
TMUX_SOCKET="drem"
KYLE_SESSION="csuite-kyle"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
MASTER_WT="/home/godinj/git/drem-orchestrator.git/master"

# Use master worktree if it exists, otherwise fall back to repo dir
if [ -d "$MASTER_WT" ]; then
    WORK_DIR="$MASTER_WT"
else
    WORK_DIR="$REPO_DIR"
fi

KYLE_PROMPT="docs/csuite-agents/prompts/kyle.md"
START_ALL=false

# ---------------------------------------------------------------------------
# Parse arguments
# ---------------------------------------------------------------------------

for arg in "$@"; do
    case "$arg" in
        --all)
            START_ALL=true
            ;;
        --help|-h)
            echo "Usage: bash scripts/csuite-launch.sh [--all]"
            echo ""
            echo "Launch Kyle (CEO), the C-Suite agent team entry point."
            echo ""
            echo "Options:"
            echo "  --all    Tell Kyle to start all agents immediately"
            echo "  --help   Show this help message"
            exit 0
            ;;
        *)
            echo "error: unknown argument: $arg" >&2
            echo "Usage: bash scripts/csuite-launch.sh [--all]" >&2
            exit 1
            ;;
    esac
done

# ---------------------------------------------------------------------------
# Prerequisite checks
# ---------------------------------------------------------------------------

if ! command -v tmux &>/dev/null; then
    echo "error: tmux is not installed or not on PATH." >&2
    echo "Install tmux: https://github.com/tmux/tmux/wiki/Installing" >&2
    exit 1
fi

if ! command -v claude &>/dev/null; then
    echo "error: claude CLI is not installed or not on PATH." >&2
    echo "Install: https://docs.anthropic.com/en/docs/claude-code" >&2
    exit 1
fi

if [ ! -f "$WORK_DIR/$KYLE_PROMPT" ]; then
    echo "error: Kyle's prompt not found at $WORK_DIR/$KYLE_PROMPT" >&2
    echo "Ensure you are running this script from the drem-orchestrator repo." >&2
    exit 1
fi

# ---------------------------------------------------------------------------
# Bootstrap directory structure
# ---------------------------------------------------------------------------

BOOTSTRAP_SCRIPT="$SCRIPT_DIR/csuite-bootstrap.sh"
if [ -f "$BOOTSTRAP_SCRIPT" ]; then
    echo "Bootstrapping C-Suite directory structure..."
    bash "$BOOTSTRAP_SCRIPT"
else
    echo "warning: csuite-bootstrap.sh not found at $BOOTSTRAP_SCRIPT" >&2
    echo "Creating minimal directory structure..." >&2
    mkdir -p "$CSUITE_DIR/kyle/inbox/archive"
    mkdir -p "$CSUITE_DIR/kyle/outbox"
    [ -f "$CSUITE_DIR/kyle/state.md" ] || touch "$CSUITE_DIR/kyle/state.md"
fi

# ---------------------------------------------------------------------------
# Write "start everyone" message if --all flag is set
# ---------------------------------------------------------------------------

if [ "$START_ALL" = true ]; then
    TIMESTAMP=$(date -u +%Y%m%d-%H%M%S)
    TIMESTAMP_ISO=$(date -u +%Y-%m-%dT%H:%M:%SZ)
    MSG_FILE="$CSUITE_DIR/kyle/inbox/${TIMESTAMP}-operator-start-all.md"

    cat > "$MSG_FILE" << MSGEOF
---
from: operator
to: kyle
timestamp: ${TIMESTAMP_ISO}
subject: "Start all agents"
priority: high
type: request
---

Please start all C-Suite agents immediately. Launch them in order: Ross, Seth, Alex, Mike.
Report back with the status of each agent after startup.
MSGEOF

    echo "Wrote 'start everyone' directive to Kyle's inbox."
fi

# ---------------------------------------------------------------------------
# Check if Kyle is already running
# ---------------------------------------------------------------------------

if tmux -L "$TMUX_SOCKET" has-session -t "$KYLE_SESSION" 2>/dev/null; then
    echo "Kyle is already running. Attaching to session..."
    if [ -n "${TMUX:-}" ]; then
        exec tmux -L "$TMUX_SOCKET" switch-client -t "$KYLE_SESSION"
    else
        exec tmux -L "$TMUX_SOCKET" attach-session -t "$KYLE_SESSION"
    fi
fi

# ---------------------------------------------------------------------------
# Start Kyle's tmux session
# ---------------------------------------------------------------------------

echo "Starting Kyle (CEO)..."

INITIAL_PROMPT="You are Kyle, the CEO of the C-Suite agent team. Run your bootstrap sequence: read your state file, check agent status, read your inbox, and present a status briefing to the operator."

# Include restart context hint if it exists
if [ -f "$CSUITE_DIR/kyle/restart-context.md" ]; then
    INITIAL_PROMPT="You are Kyle, the CEO. You were restarted. Read your restart context at ~/.drem-csuite/kyle/restart-context.md first, then run your bootstrap sequence."
fi

tmux -L "$TMUX_SOCKET" new-session -d -s "$KYLE_SESSION" \
    "cd '$WORK_DIR' && claude \
        --system-prompt '$KYLE_PROMPT' \
        --allowedTools 'Bash(command),Read(file_path),Glob(pattern),Grep(pattern)' \
        -p '$INITIAL_PROMPT'"

echo "Kyle started in tmux session '$KYLE_SESSION' on socket '$TMUX_SOCKET'."
echo "Attaching..."

if [ -n "${TMUX:-}" ]; then
    exec tmux -L "$TMUX_SOCKET" switch-client -t "$KYLE_SESSION"
else
    exec tmux -L "$TMUX_SOCKET" attach-session -t "$KYLE_SESSION"
fi
