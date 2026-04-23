#!/usr/bin/env bash
#
# csuite-run — PID-1-side script for the three C-Suite container images
# (Mike, Alex, Seth).
#
# Each persona image (deploy/docker/csuite-{mike,alex,seth}.Dockerfile)
# layers on top of drem-csuite-base and sets the CSUITE_AGENT environment
# variable. This script reads CSUITE_AGENT, resolves the matching prompt
# under /opt/csuite/prompts/, and execs the Claude CLI against it.
#
# Warm-container behaviour: the Claude CLI runs as a long-lived process
# talking to the orchestrator at $DREM_ORCH_URL. When the orchestrator
# signals a new turn (via csuite-watcher or a follow-up subprocess), the
# CLI picks it up over its existing session. If the agent ever exits the
# container restart policy (unless-stopped in the per-project compose)
# brings it back — we fall through to exec so tini sees the exit code
# cleanly.
#
# Environment contract (set by the per-project compose template):
#
#   CSUITE_AGENT      persona name: mike, alex, or seth (required)
#   DREM_PROJECT      project name (informational; logged only)
#   DREM_ORCH_URL     orchestrator HTTP URL inside drem-net
#
# Non-goals:
#   - This script does not spawn drem-watchdog; C-Suite agents are purely
#     orchestration-level (they read state, emit messages). They never
#     push commits, so the bare repo is not mounted.
#   - No retry/backoff loop: restart policy is compose's job.

set -euo pipefail

if [[ -z "${CSUITE_AGENT:-}" ]]; then
    echo "csuite-run: CSUITE_AGENT is not set" >&2
    exit 64
fi

case "${CSUITE_AGENT}" in
    mike|alex|seth) ;;
    *)
        echo "csuite-run: unknown CSUITE_AGENT=${CSUITE_AGENT} (want mike|alex|seth)" >&2
        exit 64
        ;;
esac

prompt="/opt/csuite/prompts/${CSUITE_AGENT}.md"
if [[ ! -f "${prompt}" ]]; then
    echo "csuite-run: prompt file ${prompt} is missing" >&2
    exit 65
fi

echo "csuite-run: starting persona=${CSUITE_AGENT} project=${DREM_PROJECT:-unset} orch=${DREM_ORCH_URL:-unset}"

# The Claude CLI ignores unknown env vars, so DREM_* variables propagate
# through to any sub-tool invocations the agent triggers (git, gh, etc.).
# --print makes the CLI non-interactive; --system-prompt-file loads the
# persona file. Standard input is kept open so csuite-watcher can push
# new turn prompts to the running process.
exec claude --print --system-prompt-file "${prompt}"
