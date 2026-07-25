#!/bin/sh
set -eu
: "${OPENAI_BASE_URL:?canvasbench qwen contract requires OPENAI_BASE_URL}"
: "${OPENAI_API_KEY:?canvasbench qwen contract requires OPENAI_API_KEY}"
exec /opt/harness/node_modules/.bin/qwen "$@"
