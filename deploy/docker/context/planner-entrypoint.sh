#!/usr/bin/env bash
#
# planner-entrypoint — PID-1-side script (under tini) for the drem-planner
# container. Parses the orchestrator-supplied argv, clones the feature
# branch out of /bare into /work, invokes the claude CLI in headless mode
# against the cloned worktree, and exits with the CLI's exit code so
# dispatchPlan can route on the typed exit-code table.
#
# Environment contract (set by the spawner in Spec.Env):
#
#   ANTHROPIC_API_KEY    required; forwarded from the orch container's env
#   DREM_TASK_ID         informational; echoed in log prefixes
#
# Required flags (see plans/warm-direct-planner.md §7):
#
#   --task-id     <uuid>        task identifier (echoed in logs)
#   --branch      <ref>         feature branch to clone from /bare
#   --prompt-file <path>        absolute path inside the container to the
#                               planner prompt rendered by orch
#   --model       <tag>         Anthropic model id (e.g. claude-opus-4-6)
#
# Optional flags:
#
#   --effort      <low|medium|high>   effort / reasoning knob
#   --bare-repo   <path>              override default /bare mount point
#
# Exit codes (must match dispatchPlan's exitCodeForPlan table):
#
#   0   success; plan.json written to /work/plan.json
#   1   claude CLI error (auth, network, flag-parse); orch will retry
#   2   flag parse error / precondition missing; orch fails task
#

set -uo pipefail

log() {
    printf '[%s] planner-entrypoint: %s\n' "$(date -u +%FT%TZ)" "$*"
}

die() {
    log "fatal: $*"
    exit 2
}

TASK_ID=""
BRANCH=""
PROMPT_FILE=""
MODEL=""
EFFORT=""
BARE_REPO="/bare"

while [[ $# -gt 0 ]]; do
    case "$1" in
        --task-id)     TASK_ID="${2:-}";    shift 2 ;;
        --branch)      BRANCH="${2:-}";     shift 2 ;;
        --prompt-file) PROMPT_FILE="${2:-}"; shift 2 ;;
        --model)       MODEL="${2:-}";      shift 2 ;;
        --effort)      EFFORT="${2:-}";     shift 2 ;;
        --bare-repo)   BARE_REPO="${2:-}";  shift 2 ;;
        *) die "unknown flag: $1" ;;
    esac
done

[[ -n "${TASK_ID}"     ]] || die "--task-id is required"
[[ -n "${BRANCH}"      ]] || die "--branch is required"
[[ -n "${PROMPT_FILE}" ]] || die "--prompt-file is required"
[[ -n "${MODEL}"       ]] || die "--model is required"

[[ -n "${ANTHROPIC_API_KEY:-}" ]] || die "ANTHROPIC_API_KEY env var is unset"
[[ -d "${BARE_REPO}"   ]] || die "bare repo not mounted at ${BARE_REPO}"
[[ -r "${PROMPT_FILE}" ]] || die "prompt file not readable: ${PROMPT_FILE}"

WORK_DIR="/work"
if [[ -e "${WORK_DIR}/.git" ]]; then
    die "workspace ${WORK_DIR} already has a .git; refusing to clobber"
fi

log "cloning branch '${BRANCH}' from ${BARE_REPO} into ${WORK_DIR}"
git clone --branch "${BRANCH}" "${BARE_REPO}" "${WORK_DIR}" \
    || { log "git clone failed"; exit 1; }

cd "${WORK_DIR}" || { log "cannot cd into ${WORK_DIR}"; exit 1; }

# Build claude argv. --dangerously-skip-permissions is required for
# headless / unattended runs; --model pins the Anthropic model id; --effort
# controls reasoning intensity. The prompt is piped via stdin so argv
# stays short and the shell does not need to escape prompt content.
claude_args=(--dangerously-skip-permissions --model "${MODEL}")
if [[ -n "${EFFORT}" ]]; then
    claude_args+=(--effort "${EFFORT}")
fi

log "invoking claude (task=${TASK_ID} model=${MODEL} effort=${EFFORT:-default})"
claude "${claude_args[@]}" < "${PROMPT_FILE}"
CLAUDE_RC=$?

if [[ ${CLAUDE_RC} -ne 0 ]]; then
    log "claude CLI exited non-zero (rc=${CLAUDE_RC})"
    exit 1
fi

if [[ ! -s "${WORK_DIR}/plan.json" ]]; then
    log "claude exited 0 but no plan.json produced; treating as failure"
    exit 1
fi

log "plan.json written; exiting 0"
exit 0
