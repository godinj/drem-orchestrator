#!/usr/bin/env bash
# Claude Code Notification hook for C-Suite agents.
#
# Checks the agent's inbox for unarchived messages. If any exist,
# outputs a prompt telling the agent to process them.
#
# Requires CSUITE_AGENT to be set in the environment (e.g., "kyle").
# Silently exits if CSUITE_AGENT is not set, so this hook is a no-op
# for non-C-Suite sessions (like the operator's own session).

AGENT="${CSUITE_AGENT:-}"
[ -z "$AGENT" ] && exit 0

CSUITE_DIR="${CSUITE_DIR:-$HOME/.drem-csuite}"
INBOX="${CSUITE_DIR}/${AGENT}/inbox"

[ -d "$INBOX" ] || exit 0

count=$(find "$INBOX" -maxdepth 1 -name '*.md' -type f 2>/dev/null | wc -l)

if [ "$count" -gt 0 ]; then
    echo "You have ${count} unread message(s) in your inbox. Read and process them now."
fi
