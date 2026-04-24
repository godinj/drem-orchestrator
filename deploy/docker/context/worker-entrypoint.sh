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
#   4. Exec the configured harness (claude, opencode, or codex) so the harness
#      becomes PID 1 from tini's perspective and receives signals directly.
#
# Environment contract (set by the spawner in Spec.Env):
#
#   DREM_BRANCH          feature branch to clone and push to (required)
#   DREM_AGENT_ID        agent identifier used in watchdog heartbeats (required)
#   DREM_AGENT_HARNESS   "claude" (default), "opencode", or "codex"
#   DREM_TEST_CMD        optional test command the watchdog will run on cadence
#   DREM_PROMPT_PATH     absolute path (inside the container) to the prompt the
#                        spawner wrote before starting the container
#   DREM_WATCHDOG_INTERVAL       optional; passed to drem-watchdog --interval
#   DREM_WATCHDOG_TEST_INTERVAL  optional; passed to drem-watchdog --test-interval
#   DREM_REMOTE_URL      optional override; defaults to /bare
#   DREM_MODEL           optional model override forwarded to the harness
#
# The script deliberately avoids set -e around the harness exec so that a
# harness that exits non-zero still allows tini to report the exit code
# cleanly instead of being masked by shell error handling.

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
    claude|opencode|codex) ;;
    *) die "unsupported DREM_AGENT_HARNESS='${HARNESS}' (expected 'claude', 'opencode', or 'codex')" ;;
esac

BARE_REPO="${DREM_REMOTE_URL:-/bare}"
WORK_DIR="${HOME:-/home/drem}/work"

if [[ ! -d "${BARE_REPO}" ]]; then
    die "bare repo not mounted at ${BARE_REPO}"
fi

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

# Forward SIGTERM/SIGINT to the watchdog before exiting so the final
# commit-and-push attempt has a chance to run. tini will reap the watchdog
# process once this shell exits.
trap 'kill -TERM "${WATCHDOG_PID}" 2>/dev/null || true' TERM INT

# ---------- harness exec ---------------------------------------------------
# The harness becomes the foreground process; its exit code is what the
# orchestrator sees via Docker inspect. The watchdog remains a background
# child of this shell until the trap above forwards the signal.
PROMPT_PATH="${DREM_PROMPT_PATH:-}"

case "${HARNESS}" in
    claude)
        claude_args=(--dangerously-skip-permissions)
        if [[ -n "${DREM_MODEL:-}" ]]; then
            claude_args+=(--model "${DREM_MODEL}")
        fi
        if [[ -n "${PROMPT_PATH}" && -f "${PROMPT_PATH}" ]]; then
            log "execing claude with prompt from ${PROMPT_PATH}"
            # shellcheck disable=SC2094
            exec claude "${claude_args[@]}" "$(cat "${PROMPT_PATH}")"
        fi
        log "execing claude in interactive mode (no DREM_PROMPT_PATH set)"
        exec claude "${claude_args[@]}"
        ;;

    opencode)
        oc_args=(run --format json --agent build --dir "${WORK_DIR}")
        if [[ -n "${DREM_MODEL:-}" ]]; then
            oc_args+=(--model "${DREM_MODEL}")
        fi
        if [[ -n "${PROMPT_PATH}" && -f "${PROMPT_PATH}" ]]; then
            log "execing opencode with prompt from ${PROMPT_PATH}"
            # shellcheck disable=SC2094
            exec opencode "${oc_args[@]}" "$(cat "${PROMPT_PATH}")"
        fi
        die "opencode requires DREM_PROMPT_PATH (non-interactive harness)"
        ;;

    codex)
        codex_args=(exec --json --sandbox danger-full-access --ask-for-approval never --cd "${WORK_DIR}")
        if [[ -n "${DREM_MODEL:-}" ]]; then
            codex_args+=(--model "${DREM_MODEL}")
        fi
        if [[ -n "${DREM_EFFORT:-}" ]]; then
            codex_args+=(-c "model_reasoning_effort=\"${DREM_EFFORT}\"")
        fi
        if [[ -n "${PROMPT_PATH}" && -f "${PROMPT_PATH}" ]]; then
            log "execing codex with prompt from ${PROMPT_PATH}"
            exec codex "${codex_args[@]}" - < "${PROMPT_PATH}"
        fi
        die "codex requires DREM_PROMPT_PATH (non-interactive harness)"
        ;;
esac
