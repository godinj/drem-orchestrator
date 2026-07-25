#!/bin/sh
set -eu
: "${OPENAI_API_BASE:?canvasbench mini contract requires OPENAI_API_BASE}"
: "${OPENAI_API_KEY:?canvasbench mini contract requires OPENAI_API_KEY}"
export OPENAI_BASE_URL="$OPENAI_API_BASE"
exec /usr/local/bin/mini-real "$@"
