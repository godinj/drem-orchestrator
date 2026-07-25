#!/bin/sh
set -eu
: "${OPENAI_API_BASE:?canvasbench mini contract requires OPENAI_API_BASE}"
: "${OPENAI_API_KEY:?canvasbench mini contract requires OPENAI_API_KEY}"
export OPENAI_BASE_URL="$OPENAI_API_BASE"
export MSWEA_CONFIGURED=true
export MSWEA_COST_TRACKING=ignore_errors
export MSWEA_GLOBAL_CONFIG_DIR=/tmp/mini-swe-agent
export MSWEA_SILENT_STARTUP=1
exec /usr/local/bin/mini-real "$@"
