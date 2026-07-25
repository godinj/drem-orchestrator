#!/bin/sh
set -eu
: "${OPENAI_API_BASE:?canvasbench mini contract requires OPENAI_API_BASE}"
: "${OPENAI_API_KEY:?canvasbench mini contract requires OPENAI_API_KEY}"
export OPENAI_BASE_URL="$OPENAI_API_BASE"
export MSWEA_CONFIGURED=true
export MSWEA_COST_TRACKING=ignore_errors
export MSWEA_GLOBAL_CONFIG_DIR=/tmp/mini-swe-agent
export MSWEA_SILENT_STARTUP=1
: "${CANVASBENCH_TEMPERATURE:?canvasbench mini contract requires CANVASBENCH_TEMPERATURE}"
: "${CANVASBENCH_TOP_P:?canvasbench mini contract requires CANVASBENCH_TOP_P}"
: "${CANVASBENCH_TOP_K:?canvasbench mini contract requires CANVASBENCH_TOP_K}"
: "${CANVASBENCH_MAX_OUTPUT_TOKENS:?canvasbench mini contract requires CANVASBENCH_MAX_OUTPUT_TOKENS}"
: "${CANVASBENCH_SEED:?canvasbench mini contract requires CANVASBENCH_SEED}"
exec /usr/local/bin/mini-real "$@" \
  -c mini.yaml \
  -c "model.model_kwargs.temperature=$CANVASBENCH_TEMPERATURE" \
  -c "model.model_kwargs.top_p=$CANVASBENCH_TOP_P" \
  -c "model.model_kwargs.top_k=$CANVASBENCH_TOP_K" \
  -c "model.model_kwargs.max_tokens=$CANVASBENCH_MAX_OUTPUT_TOKENS" \
  -c "model.model_kwargs.seed=$CANVASBENCH_SEED"
