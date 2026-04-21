#!/usr/bin/env bash
#
# csuite-entrypoint — Wave-2 PID-1-side script for the four C-Suite
# container images (mike, alex, ross, seth).
#
# Replaces deploy/docker/context/csuite-run.sh (which was the Wave-1
# "exec claude --print" entrypoint). Instead of launching a long-lived
# interactive claude process, this wrapper execs the csuite-persona
# headless poller (cmd/csuite-persona) which polls the persona's inbox
# and spawns `claude -p` once per message. See
# plans/csuite-persona-pivot.md and docs/containerization/install.md
# "C-Suite personas: the persona poller runtime" for the design.
#
# Environment contract (set by the per-project compose template; unchanged
# from csuite-run.sh):
#
#   CSUITE_AGENT      persona name: mike, alex, ross, or seth (required)
#   DREM_PROJECT      project name (informational; logged only)
#   DREM_ORCH_URL     orchestrator HTTP URL inside drem-net (unused by
#                     the poller itself; available to any claude -p
#                     invocation for tool-level HTTP calls)
#
# Subscription-only auth: this script NEVER sets CLAUDE_CODE_OAUTH_TOKEN,
# ANTHROPIC_API_KEY, or ANTHROPIC_AUTH_TOKEN. The claude CLI inside each
# invocation reads /home/drem/.claude/.credentials.json which the compose
# template bind-mounts read-only. See CLAUDE.md "Authentication:
# subscription-only".

set -euo pipefail

if [[ -z "${CSUITE_AGENT:-}" ]]; then
    echo "csuite-entrypoint: CSUITE_AGENT is not set" >&2
    exit 64
fi

case "${CSUITE_AGENT}" in
    mike|alex|ross|seth) ;;
    *)
        echo "csuite-entrypoint: unknown CSUITE_AGENT=${CSUITE_AGENT} (want mike|alex|ross|seth)" >&2
        exit 64
        ;;
esac

echo "csuite-entrypoint: starting persona=${CSUITE_AGENT} project=${DREM_PROJECT:-unset} orch=${DREM_ORCH_URL:-unset}"

# Defaults inside the container:
#   inbox:   /home/drem/.drem-csuite/${CSUITE_AGENT}/inbox
#   outbox:  /home/drem/.drem-csuite/${CSUITE_AGENT}/outbox
#   state:   /home/drem/.drem-csuite/${CSUITE_AGENT}/state.md
#   archive: /home/drem/.drem-csuite/${CSUITE_AGENT}/inbox/.archive
#   prompt:  /opt/csuite/prompts/${CSUITE_AGENT}.md
# The poller derives these from -persona when the corresponding flags
# are empty, so we only need to pass -persona here.
exec /usr/local/bin/csuite-persona -persona "${CSUITE_AGENT}"
