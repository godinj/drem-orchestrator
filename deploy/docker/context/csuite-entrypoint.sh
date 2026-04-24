#!/usr/bin/env bash
#
# csuite-entrypoint — Wave-2 PID-1-side script for the four C-Suite
# container images (mike, alex, seth, kyle).
#
# Replaces deploy/docker/context/csuite-run.sh (which was the Wave-1
# interactive-model entrypoint). Instead of launching a long-lived
# interactive model process, this wrapper execs the csuite-persona
# headless poller (cmd/csuite-persona) which polls the persona's inbox
# and spawns `codex exec` once per message. See
# plans/csuite-persona-pivot.md and docs/containerization/install.md
# "C-Suite personas: the persona poller runtime" for the design.
#
# Environment contract (set by the per-project compose template; unchanged
# from csuite-run.sh):
#
#   CSUITE_AGENT      persona name: mike, alex, seth, or kyle (required)
#   DREM_PROJECT      project name (informational; logged only)
#   DREM_ORCH_URL     orchestrator HTTP URL inside drem-net (unused by
#                     the poller itself; available to any claude -p
#                     invocation for tool-level HTTP calls)
#
# Auth is supplied by bind-mounted CLI state; the compose template mounts
# /home/drem/.codex/auth.json for Codex and keeps the legacy Claude
# credential mount for rollback compatibility.

set -euo pipefail

if [[ -z "${CSUITE_AGENT:-}" ]]; then
    echo "csuite-entrypoint: CSUITE_AGENT is not set" >&2
    exit 64
fi

case "${CSUITE_AGENT}" in
    mike|alex|seth|kyle) ;;
    *)
        echo "csuite-entrypoint: unknown CSUITE_AGENT=${CSUITE_AGENT} (want mike|alex|seth|kyle)" >&2
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
#
# DREM_CLAUDE_TIMEOUT (optional, retained for compatibility) overrides the poller's default 5-minute
# per-invocation timeout. Accepts any Go duration string (e.g. "30m",
# "1h", "90m"). Unset falls back to persona.DefaultClaudeTimeout
# (5 min). Motivation: CTO-synthesis tasks routinely exceed 5 min of
# model wall-clock; 5 min bounds a single transactional reply but
# is too tight for deep analysis passes. Per-persona tuning is done by
# setting DREM_CLAUDE_TIMEOUT in the project compose template
# (internal/projects/templates/project-compose.yml.tmpl).
if [[ -n "${DREM_CLAUDE_TIMEOUT:-}" ]]; then
    echo "csuite-entrypoint: claude-timeout=${DREM_CLAUDE_TIMEOUT} (overriding default)"
    exec /usr/local/bin/csuite-persona \
        -persona "${CSUITE_AGENT}" \
        -claude-timeout "${DREM_CLAUDE_TIMEOUT}"
fi

exec /usr/local/bin/csuite-persona -persona "${CSUITE_AGENT}"
