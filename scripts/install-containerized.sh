#!/usr/bin/env bash
#
# install-containerized.sh — Bring a fresh host from zero to a running
# drem-orchestrator containerized stack.
#
# Source of truth: docs/containerization/install.md. Keep this script and
# that document in sync; every fenced command block in the doc maps to a
# phase here.
#
# Usage:
#   bash scripts/install-containerized.sh [--subset|--full] [--skip-build]
#
#   --subset        Stop after Step 5a (registry + spawner + kyle + proxy).
#                   No GPU required. Skips SGLang+GQ build and full up.
#   --full          (default) Build everything including sglang+gq and
#                   bring up the full global stack. Requires GPU +
#                   nvidia-container-toolkit + $HOME/sglang-models/<subdir>.
#   --skip-build    Skip image builds; assume registry already populated.
#                   Useful when re-running just the compose up phases.
#
# The script is idempotent: reruns push the same tags, re-apply the
# docker network, and re-up the compose stack without tearing down.

set -euo pipefail

# ---------- args -----------------------------------------------------------
MODE="full"
SKIP_BUILD=0
for arg in "$@"; do
  case "$arg" in
    --subset)     MODE="subset" ;;
    --full)       MODE="full" ;;
    --skip-build) SKIP_BUILD=1 ;;
    -h|--help)
      sed -n '2,22p' "$0"; exit 0 ;;
    *)
      echo "unknown flag: $arg" >&2; exit 2 ;;
  esac
done

# ---------- paths ----------------------------------------------------------
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
cd "${REPO_ROOT}"

REGISTRY="localhost:5000"
GLOBAL_COMPOSE="deploy/compose/global.yml"

log() { printf '\n>>> %s\n' "$*"; }
fail() { printf '!!! %s\n' "$*" >&2; exit 1; }

# ---------- Step 1: docker perms ------------------------------------------
log "Step 1 — docker group membership"
if ! docker info >/dev/null 2>&1; then
  fail "docker daemon not reachable as $(whoami). Run: sudo usermod -aG docker \$USER && re-login (or re-run this script under sudo -E)."
fi

# ---------- Step 2: external network --------------------------------------
log "Step 2 — external network drem-net"
if ! docker network inspect drem-net >/dev/null 2>&1; then
  bash deploy/compose/network-setup.sh
else
  echo "    drem-net already exists"
fi

# ---------- Step 3: registry (required before any push) -------------------
log "Step 3 — registry"
docker compose -f "${GLOBAL_COMPOSE}" up -d registry
# Wait up to 30 s for the registry to accept connections.
for _ in $(seq 1 30); do
  if curl -sf "http://127.0.0.1:5000/v2/" >/dev/null; then break; fi
  sleep 1
done
curl -sf "http://127.0.0.1:5000/v2/" >/dev/null \
  || fail "registry did not come up on 127.0.0.1:5000"

# ---------- Step 4: build + push images -----------------------------------
build_push() {
  local dockerfile="$1" image="$2"
  log "    build+push ${image}"
  docker build -f "${dockerfile}" -t "${REGISTRY}/${image}:latest" .
  docker push "${REGISTRY}/${image}:latest"
}

if [ "${SKIP_BUILD}" -eq 0 ]; then
  log "Step 4a — smoke image (docker-query-proxy)"
  build_push deploy/docker/docker-query-proxy.Dockerfile drem-docker-query-proxy

  log "Step 4b — remaining Go infra images"
  for img in spawner agentmon kyle merger; do
    build_push "deploy/docker/${img}.Dockerfile" "drem-${img}"
  done

  log "Step 4c — C-Suite + orchestrator images"
  bash deploy/docker/build-csuite.sh

  log "Step 4d — worker images"
  bash deploy/docker/build-workers.sh

  if [ "${MODE}" = "full" ]; then
    log "Step 4e — sglang + gq (GPU stack)"
    build_push deploy/docker/sglang.Dockerfile drem-sglang
    build_push deploy/docker/gq.Dockerfile     drem-gq
  fi
else
  log "Step 4 — skipped (--skip-build)"
fi

log "registry catalog:"
curl -s http://127.0.0.1:5000/v2/_catalog | python3 -m json.tool 2>/dev/null \
  || curl -s http://127.0.0.1:5000/v2/_catalog

# ---------- Step 5: bring up the global stack -----------------------------
if [ "${MODE}" = "subset" ]; then
  log "Step 5a — global stack (subset, no GPU)"
  docker compose -f "${GLOBAL_COMPOSE}" up -d \
    registry spawner kyle docker-query-proxy
else
  log "Step 5b — global stack (full)"
  # Verify GPU prereqs before attempting sglang.
  if ! docker info --format '{{json .Runtimes}}' | grep -q nvidia; then
    fail "nvidia runtime not registered in docker. Install nvidia-container-toolkit and retry, or rerun with --subset."
  fi
  : "${SGLANG_MODEL_DIR:=${HOME}/sglang-models}"
  : "${SGLANG_MODEL_SUBDIR:=gemma4-26b}"
  if [ ! -d "${SGLANG_MODEL_DIR}/${SGLANG_MODEL_SUBDIR}" ]; then
    fail "model weights not found at ${SGLANG_MODEL_DIR}/${SGLANG_MODEL_SUBDIR}"
  fi
  docker compose -f "${GLOBAL_COMPOSE}" up -d
fi

log "global compose status:"
docker compose -f "${GLOBAL_COMPOSE}" ps

cat <<EOF

Next steps (not automated — they depend on host-specific inputs):

  drem project register --name drem-orchestrator \\
    --bare /path/to/drem-orchestrator.git --language go
  docker compose -f ~/.drem/projects/drem-orchestrator/compose.yml up -d

See docs/containerization/install.md §6–§8 for verification and teardown.
EOF
