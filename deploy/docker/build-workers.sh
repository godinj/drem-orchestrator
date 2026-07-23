#!/usr/bin/env bash
#
# Build and push the per-language worker container images.
#
# Phase 2 of the containerization effort (docs/prd-containerization.md §Images).
# This script is the one-command entry point for refreshing every worker image
# after a change to the watchdog, the entrypoint, or any Dockerfile under
# deploy/docker/.
#
# The worker Go binaries are built first and dropped into the shared Docker
# build context so worker-base can COPY them without depending on a multi-stage
# Go build — keeps the base image small and lets us reuse it for language images
# that don't have Go installed.
#
# Usage:
#   bash deploy/docker/build-workers.sh
#
# Prerequisites:
#   - docker with buildkit (Docker 20.10+)
#   - The local registry running at localhost:5000 (see deploy/compose/global.yml)
#   - A Go toolchain on the host matching the repo's go.mod directive
#
# After a successful run, verify with deploy/docker/test-worker-image.sh.

set -euo pipefail

# Resolve the repo root regardless of the caller's cwd so the script is safe
# to invoke from anywhere (e.g. deploy/docker/ or the repo root).
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

cd "${REPO_ROOT}"

SOURCE_REVISION="${DREM_SOURCE_REVISION:-$(git rev-parse HEAD)}"
SOURCE_STATE="${DREM_SOURCE_STATE:-${SOURCE_REVISION}}"
LABEL_ARGS=(
    --label "org.opencontainers.image.revision=${SOURCE_REVISION}"
    --label "io.drem.source-state=${SOURCE_STATE}"
)

# Build the watchdog with CGO disabled so it runs on a debian:bookworm-slim
# base without a libc mismatch. Static-ish build keeps the image layer simple.
echo ">> building drem-watchdog -> deploy/docker/context/drem-watchdog"
mkdir -p deploy/docker/context
CGO_ENABLED=0 GOOS=linux go build \
    -trimpath -ldflags="-s -w" \
    -o deploy/docker/context/drem-watchdog \
    ./cmd/drem-watchdog

echo ">> building drem-direct-agent -> deploy/docker/context/drem-direct-agent"
CGO_ENABLED=0 GOOS=linux go build \
    -trimpath -ldflags="-s -w" \
    -o deploy/docker/context/drem-direct-agent \
    ./cmd/drem-direct-agent

# worker-entrypoint.sh must live in the build context so worker-base can
# COPY it into /usr/local/bin. The canonical source file is checked into
# the repo at deploy/docker/context/worker-entrypoint.sh — nothing to do here
# beyond confirming it exists before we kick off the docker build.
if [[ ! -f deploy/docker/context/worker-entrypoint.sh ]]; then
    echo "error: deploy/docker/context/worker-entrypoint.sh is missing" >&2
    exit 1
fi

cd deploy/docker

echo ">> building localhost:5000/drem-worker-base:latest"
docker build "${LABEL_ARGS[@]}" \
    -t "localhost:5000/drem-worker-base:${SOURCE_STATE}" \
    -t localhost:5000/drem-worker-base:latest \
    -f worker-base.Dockerfile context/

# Push the base BEFORE building derived images so `--pull` on the
# derived builds can fetch the freshly-pushed base from the registry.
# Without this sequence, derived-image FROM layer cache is not
# invalidated when only the base changes: docker keyed the cached
# `FROM localhost:5000/drem-worker-base:latest` layer against the
# OLD base image ID, and a rebuild of base alone leaves the cache
# entry intact. This bit the 2026-04-20 worker-subscription-auth
# rollout when the `/home/drem/.claude` mkdir added to worker-base
# did not propagate to worker-go until a manual `--no-cache` rebuild.
echo ">> pushing base so derived --pull builds see fresh digest"
docker push localhost:5000/drem-worker-base:latest
docker push "localhost:5000/drem-worker-base:${SOURCE_STATE}"

# --pull forces docker to re-fetch the base image from the registry at
# build time, invalidating any stale FROM layer cache. Combined with
# the push above, this guarantees derived images build against the
# newly-pushed base digest.
echo ">> building localhost:5000/drem-worker-go:latest"
docker build --pull "${LABEL_ARGS[@]}" \
    -t "localhost:5000/drem-worker-go:${SOURCE_STATE}" \
    -t localhost:5000/drem-worker-go:latest \
    -f worker-go.Dockerfile context/

echo ">> building localhost:5000/drem-worker-cpp:latest"
docker build --pull "${LABEL_ARGS[@]}" \
    -t "localhost:5000/drem-worker-cpp:${SOURCE_STATE}" \
    -t localhost:5000/drem-worker-cpp:latest \
    -f worker-cpp.Dockerfile context/

echo ">> pushing derived images to localhost:5000"
docker push localhost:5000/drem-worker-go:latest
docker push localhost:5000/drem-worker-cpp:latest
docker push "localhost:5000/drem-worker-go:${SOURCE_STATE}"
docker push "localhost:5000/drem-worker-cpp:${SOURCE_STATE}"

echo ">> done"
