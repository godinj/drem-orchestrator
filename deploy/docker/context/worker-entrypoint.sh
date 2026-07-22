#!/usr/bin/env bash
#
# worker-entrypoint — PID-1-side script that turns a freshly-spawned worker
# container into a running agent session.
#
# Invoked as CMD under tini (see worker-base.Dockerfile). Responsibilities:
#
#   1. Clone the feature branch out of the bare repo mounted at /bare into
#      the container-local workspace (/home/drem/work). The bare repo is
#      the single source of truth (PRD §Container filesystem model).
#   2. Re-point origin at /bare so that git push from inside the watchdog
#      lands back in the bare repo. /bare is mounted read-write for workers
#      precisely because the watchdog needs to push (PRD §Lifecycle and
#      recovery, user stories 17, 18).
#   3. Spawn drem-watchdog in the background; it auto-commits and pushes
#      every --interval and immediately after any --test-cmd returns 0.
#   4. Run the configured harness (claude, opencode, codex, or the direct
#      agent), preserve its exit code, then synchronously stop the watchdog so
#      its final commit-and-push completes before the container exits.
#
# Environment contract (set by the spawner in Spec.Env):
#
#   DREM_BRANCH          feature branch to clone and push to (required)
#   DREM_AGENT_ID        agent identifier used in watchdog heartbeats (required)
#   DREM_AGENT_HARNESS   "claude" (default), "opencode", "codex", or
#                        "sglang-direct"
#   DREM_TEST_CMD        optional test command the watchdog will run on cadence
#   DREM_PROMPT_PATH     absolute path (inside the container) to the prompt the
#                        spawner wrote before starting the container
#   DREM_WATCHDOG_INTERVAL       optional; passed to drem-watchdog --interval
#   DREM_WATCHDOG_TEST_INTERVAL  optional; passed to drem-watchdog --test-interval
#   DREM_REMOTE_URL      optional override; defaults to /bare
#   DREM_MODEL           optional model override forwarded to the harness
#
# The script deliberately avoids set -e around the harness so that its exit
# code is preserved even when watchdog finalization also needs to run.

set -uo pipefail

# ---------- helpers --------------------------------------------------------
log() {
    # Structured-ish prefix so agentmon's extractor can attribute lines to
    # this container by scanning for "worker-entrypoint:".
    printf '[%s] worker-entrypoint: %s\n' "$(date -u +%FT%TZ)" "$*"
}

die() {
    log "fatal: $*"
    exit 1
}

require_env() {
    local name="$1"
    if [[ -z "${!name:-}" ]]; then
        die "required env var $name is unset"
    fi
}

# ---------- validate inputs ------------------------------------------------
require_env DREM_BRANCH
require_env DREM_AGENT_ID

HARNESS="${DREM_AGENT_HARNESS:-claude}"
case "${HARNESS}" in
    claude|opencode|codex|sglang-direct) ;;
    *) die "unsupported DREM_AGENT_HARNESS='${HARNESS}' (expected 'claude', 'opencode', 'codex', or 'sglang-direct')" ;;
esac

BARE_REPO="${DREM_REMOTE_URL:-/bare}"
WORK_DIR="${HOME:-/home/drem}/work"

if [[ ! -d "${BARE_REPO}" ]]; then
    die "bare repo not mounted at ${BARE_REPO}"
fi

# Fail before cloning or spending model tokens if the worker cannot create the
# ref lock that git push will need later. This catches host bind-mount ownership
# drift such as refs/heads/feature being root-owned inside /bare.
ref_dir="${BARE_REPO}/refs/heads/$(dirname "${DREM_BRANCH}")"
ref_lock="${BARE_REPO}/refs/heads/${DREM_BRANCH}.drem-preflight-lock.$$"
mkdir -p "${ref_dir}" || die "bare repo refs not writable for ${DREM_BRANCH}"
: >"${ref_lock}" || die "bare repo ref lock not writable for ${DREM_BRANCH}"
rm -f "${ref_lock}" || die "bare repo ref lock cleanup failed for ${DREM_BRANCH}"

# ---------- clone + remote wiring -----------------------------------------
# A fresh container always starts with an empty workspace, but the image's
# WORKDIR is /home/drem/work; guard against it having pre-existing state
# (e.g. during `docker run --entrypoint` debugging) by failing loudly.
if [[ -e "${WORK_DIR}/.git" ]]; then
    log "workspace ${WORK_DIR} already has a .git; refusing to clobber"
    die "refusing to overwrite existing workspace"
fi

log "cloning branch '${DREM_BRANCH}' from ${BARE_REPO} into ${WORK_DIR}"
git clone --branch "${DREM_BRANCH}" "${BARE_REPO}" "${WORK_DIR}" \
    || die "git clone failed"

cd "${WORK_DIR}" || die "cannot cd into ${WORK_DIR}"

# Default identity for the commits the watchdog makes. The agent itself may
# override these via `git config` during its session; we only set them if
# unset so operator overrides via env stick.
git config user.name  "${DREM_GIT_USER_NAME:-drem-worker}"
git config user.email "${DREM_GIT_USER_EMAIL:-drem-worker@localhost}"

# origin is already pointing at ${BARE_REPO} from clone, but re-assert in case
# the spawner mutated /bare location mid-flight (e.g. via a symlink rebind).
git remote set-url origin "${BARE_REPO}"

# ---------- watchdog -------------------------------------------------------
# Build up the argv for the watchdog. Empty args are omitted so the Go flag
# parser does not see "--test-cmd=" as a zero-length command.
watchdog_args=(
    --repo "${WORK_DIR}"
    --branch "${DREM_BRANCH}"
    --remote origin
    --agent-id "${DREM_AGENT_ID}"
)
if [[ -n "${DREM_WATCHDOG_INTERVAL:-}" ]]; then
    watchdog_args+=(--interval "${DREM_WATCHDOG_INTERVAL}")
fi
if [[ -n "${DREM_TEST_CMD:-}" ]]; then
    watchdog_args+=(--test-cmd "${DREM_TEST_CMD}")
fi
if [[ -n "${DREM_WATCHDOG_TEST_INTERVAL:-}" ]]; then
    watchdog_args+=(--test-interval "${DREM_WATCHDOG_TEST_INTERVAL}")
fi

log "starting drem-watchdog (agent=${DREM_AGENT_ID} branch=${DREM_BRANCH})"
/usr/local/bin/drem-watchdog "${watchdog_args[@]}" &
WATCHDOG_PID=$!
HARNESS_PID=""

# Keep the entrypoint shell alive as the supervisor. A fast harness can finish
# before the watchdog's first interval; synchronously terminating and waiting
# for the watchdog is what guarantees its bounded final flush runs.
stop_watchdog() {
    if [[ -n "${WATCHDOG_PID:-}" ]]; then
        kill -TERM "${WATCHDOG_PID}" 2>/dev/null || true
        wait "${WATCHDOG_PID}" 2>/dev/null
        WATCHDOG_EXIT=$?
        WATCHDOG_PID=""
        if [[ "${WATCHDOG_EXIT}" -ne 0 ]]; then
            log "warning: drem-watchdog exited with status ${WATCHDOG_EXIT}"
        fi
    fi
}

forward_termination() {
    if [[ -n "${HARNESS_PID:-}" ]]; then
        kill -TERM "${HARNESS_PID}" 2>/dev/null || true
    fi
    if [[ -n "${WATCHDOG_PID:-}" ]]; then
        kill -TERM "${WATCHDOG_PID}" 2>/dev/null || true
    fi
}

run_harness() {
    # Bash may attach /dev/null to an asynchronous command's stdin when job
    # control is disabled. Apply the prompt redirection to the command that is
    # actually backgrounded, rather than to this supervising function call.
    if [[ -n "${HARNESS_STDIN_PATH:-}" ]]; then
        "$@" < "${HARNESS_STDIN_PATH}" &
    else
        "$@" &
    fi
    HARNESS_PID=$!
    wait "${HARNESS_PID}"
    HARNESS_EXIT=$?
    HARNESS_PID=""
    log "harness exited with status ${HARNESS_EXIT}; finalizing watchdog"
    stop_watchdog
    exit "${HARNESS_EXIT}"
}

trap forward_termination TERM INT
trap stop_watchdog EXIT

# ---------- harness supervision -------------------------------------------
# The harness is a child of this shell. run_harness preserves its exit code
# while ensuring the watchdog has completed its final flush first.
PROMPT_PATH="${DREM_PROMPT_PATH:-}"

case "${HARNESS}" in
    claude)
        claude_args=(--dangerously-skip-permissions)
        if [[ -n "${DREM_MODEL:-}" ]]; then
            claude_args+=(--model "${DREM_MODEL}")
        fi
        if [[ -n "${PROMPT_PATH}" && -f "${PROMPT_PATH}" ]]; then
            log "running claude with prompt from ${PROMPT_PATH}"
            # shellcheck disable=SC2094
            run_harness claude "${claude_args[@]}" "$(cat "${PROMPT_PATH}")"
        fi
        log "running claude in interactive mode (no DREM_PROMPT_PATH set)"
        run_harness claude "${claude_args[@]}"
        ;;

    opencode)
        oc_args=(run --format json --agent build --dir "${WORK_DIR}")
        if [[ -n "${DREM_MODEL:-}" ]]; then
            oc_args+=(--model "${DREM_MODEL}")
        fi
        if [[ -n "${PROMPT_PATH}" && -f "${PROMPT_PATH}" ]]; then
            log "running opencode with prompt from ${PROMPT_PATH}"
            # shellcheck disable=SC2094
            run_harness opencode "${oc_args[@]}" "$(cat "${PROMPT_PATH}")"
        fi
        die "opencode requires DREM_PROMPT_PATH (non-interactive harness)"
        ;;

    codex)
        codex_args=(exec --json --dangerously-bypass-approvals-and-sandbox --cd "${WORK_DIR}")
        if [[ -n "${DREM_MODEL:-}" ]]; then
            codex_args+=(--model "${DREM_MODEL}")
        fi
        if [[ -n "${DREM_EFFORT:-}" ]]; then
            codex_args+=(-c "model_reasoning_effort=\"${DREM_EFFORT}\"")
        fi
        if [[ -n "${PROMPT_PATH}" && -f "${PROMPT_PATH}" ]]; then
            log "running codex with prompt from ${PROMPT_PATH}"
            HARNESS_STDIN_PATH="${PROMPT_PATH}" run_harness codex "${codex_args[@]}" -
        fi
        die "codex requires DREM_PROMPT_PATH (non-interactive harness)"
        ;;

    sglang-direct)
        if [[ -n "${PROMPT_PATH}" && -f "${PROMPT_PATH}" ]]; then
            log "running sglang-direct with prompt from ${PROMPT_PATH}"
            run_harness drem-direct-agent --role "${DREM_AGENT:-coder}" --prompt "${PROMPT_PATH}" --workdir "${WORK_DIR}"
        fi
        die "sglang-direct requires DREM_PROMPT_PATH (non-interactive harness)"
        ;;
esac
