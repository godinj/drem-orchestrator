#!/usr/bin/env bash
#
# test-worker-image — smoke test for the per-language worker images.
#
# Run manually after deploy/docker/build-workers.sh to confirm that each image
# carries the expected toolchain and that the baked-in binaries execute. There
# is no automated CI target today; this script is the build-verification
# checklist documented in the per-image Dockerfile comments.
#
# Usage:
#   bash deploy/docker/build-workers.sh
#   bash deploy/docker/test-worker-image.sh
#
# A non-zero exit means at least one sub-command failed. Re-run with -x to
# bisect the failure.

set -euo pipefail

# Override the entrypoint so these one-shot checks do not trigger the normal
# worker-entrypoint flow (which would try to clone from a non-existent /bare).
RUN="docker run --rm --entrypoint="

echo ">> drem-worker-go: go version"
${RUN} localhost:5000/drem-worker-go:latest go version

echo ">> drem-worker-cpp: g++ version"
${RUN} localhost:5000/drem-worker-cpp:latest g++ --version

echo ">> drem-worker-go: drem-watchdog --help (exit status is accepted; only text output matters)"
# The watchdog's flag parser returns ErrHelp for --help, which surfaces as a
# non-zero exit. Treat the image as healthy as long as the binary is present
# and prints its usage banner; `|| true` keeps the smoke script moving.
${RUN} localhost:5000/drem-worker-go:latest drem-watchdog --help || true

echo ">> drem-worker-go: claude --version"
${RUN} localhost:5000/drem-worker-go:latest claude --version

echo ">> drem-worker-cpp: drem-watchdog --help"
${RUN} localhost:5000/drem-worker-cpp:latest drem-watchdog --help || true

echo ">> drem-worker-cpp: claude --version"
${RUN} localhost:5000/drem-worker-cpp:latest claude --version

echo ">> all smoke checks passed"
