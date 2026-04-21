#!/usr/bin/env bash
#
# Build and push the C-Suite and orchestrator container images.
#
# Phase 3 of the containerization effort (docs/prd-containerization.md
# §Images). One-command entry point for refreshing:
#   - localhost:5000/drem-csuite-base:latest
#   - localhost:5000/drem-csuite-{mike,alex,ross,seth}:latest
#   - localhost:5000/drem-csuite-watcher:latest
#   - localhost:5000/drem-orch:latest          (production)
#   - localhost:5000/drem-orch-dev:latest      (development)
#
# Usage:
#   bash deploy/docker/build-csuite.sh
#
# Prerequisites:
#   - docker with buildkit (Docker 20.10+)
#   - The local registry running at localhost:5000 (see
#     deploy/compose/global.yml).
#   - A Go toolchain on the host matching the repo's go.mod directive
#     (the orch and csuite-watcher images build Go binaries, but via
#     multi-stage Docker — the host toolchain is not strictly required).
#
# The persona prompts under docs/csuite-agents/prompts/ are staged into
# deploy/docker/context/csuite-prompts/ so the csuite-base Dockerfile
# can COPY them with a stable path.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

cd "${REPO_ROOT}"

# ---- stage persona prompts into the build context ------------------------
PROMPT_SRC="docs/csuite-agents/prompts"
PROMPT_DST="deploy/docker/context/csuite-prompts"
echo ">> staging C-Suite prompts -> ${PROMPT_DST}"
rm -rf "${PROMPT_DST}"
mkdir -p "${PROMPT_DST}"
for persona in mike alex ross seth; do
    if [[ ! -f "${PROMPT_SRC}/${persona}.md" ]]; then
        echo "error: ${PROMPT_SRC}/${persona}.md is missing" >&2
        exit 1
    fi
    cp "${PROMPT_SRC}/${persona}.md" "${PROMPT_DST}/${persona}.md"
done

# csuite-run.sh is the PID-1 script baked into drem-csuite-base. It must
# already exist in the build context (checked into git).
if [[ ! -f deploy/docker/context/csuite-run.sh ]]; then
    echo "error: deploy/docker/context/csuite-run.sh is missing" >&2
    exit 1
fi

# csuite-entrypoint.sh is the Wave-2 entrypoint wrapper that execs the
# csuite-persona poller with the right flags based on $CSUITE_AGENT.
# Also checked into the repo.
if [[ ! -f deploy/docker/context/csuite-entrypoint.sh ]]; then
    echo "error: deploy/docker/context/csuite-entrypoint.sh is missing" >&2
    exit 1
fi

# Pre-build the csuite-persona Go binary into the build context so the
# csuite-base Dockerfile can COPY it like worker-base does with
# drem-watchdog. CGO disabled → static-ish binary that runs cleanly on
# debian:bookworm-slim with no libc surprises. See
# plans/csuite-persona-pivot.md for the rationale.
echo ">> building csuite-persona -> deploy/docker/context/csuite-persona"
CGO_ENABLED=0 GOOS=linux go build \
    -trimpath -ldflags="-s -w" \
    -o deploy/docker/context/csuite-persona \
    ./cmd/csuite-persona

# ---- build images --------------------------------------------------------
cd deploy/docker

echo ">> building localhost:5000/drem-csuite-base:latest"
docker build -t localhost:5000/drem-csuite-base:latest \
    -f csuite-base.Dockerfile context/

for agent in mike alex ross seth; do
    echo ">> building localhost:5000/drem-csuite-${agent}:latest"
    docker build -t "localhost:5000/drem-csuite-${agent}:latest" \
        -f "csuite-${agent}.Dockerfile" context/
done

# Repo-root context for the Go multi-stage builds.
cd "${REPO_ROOT}"

echo ">> building localhost:5000/drem-csuite-watcher:latest"
docker build -t localhost:5000/drem-csuite-watcher:latest \
    -f deploy/docker/csuite-watcher.Dockerfile .

echo ">> building localhost:5000/drem-orch:latest"
docker build -t localhost:5000/drem-orch:latest \
    -f deploy/docker/orch.Dockerfile .

echo ">> building localhost:5000/drem-orch-dev:latest"
docker build -t localhost:5000/drem-orch-dev:latest \
    -f deploy/docker/orch-dev.Dockerfile .

# ---- push ----------------------------------------------------------------
echo ">> pushing images to localhost:5000"
for img in \
    drem-csuite-base \
    drem-csuite-mike \
    drem-csuite-alex \
    drem-csuite-ross \
    drem-csuite-seth \
    drem-csuite-watcher \
    drem-orch \
    drem-orch-dev ; do
    docker push "localhost:5000/${img}:latest"
done

echo ">> done"
