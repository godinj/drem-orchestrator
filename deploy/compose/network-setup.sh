#!/usr/bin/env bash
# One-time idempotent setup for the shared drem-net Docker network.
#
# The global compose (deploy/compose/global.yml) and every per-project compose
# attach to `drem-net` as an external network. Run this script once on a fresh
# host before `docker compose -f deploy/compose/global.yml up -d`.
#
# Usage: bash deploy/compose/network-setup.sh
set -euo pipefail
docker network inspect drem-net >/dev/null 2>&1 || docker network create drem-net
