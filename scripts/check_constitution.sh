#!/usr/bin/env bash
set -euo pipefail

# Constitution enforcement via the Go constraint runner.
# This script is a thin wrapper around the constraints package.
# The actual rules are defined in .drem/constraints.toml.
#
# Usage: bash scripts/check_constitution.sh

cd "$(git rev-parse --show-toplevel 2>/dev/null || pwd)"

go run ./cmd/check-constraints/...
