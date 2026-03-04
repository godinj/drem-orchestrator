#!/usr/bin/env bash
set -euo pipefail
echo "Installing Python dependencies..."
uv sync
echo "Installing UI dependencies..."
cd ui && npm install && cd ..
echo "Running migrations..."
uv run alembic upgrade head
echo "Done. Run: uv run uvicorn orchestrator.server:app --reload"
