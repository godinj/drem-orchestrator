#!/bin/sh
set -eu
: "${OPENAI_BASE_URL:?canvasbench qwen contract requires OPENAI_BASE_URL}"
: "${OPENAI_API_KEY:?canvasbench qwen contract requires OPENAI_API_KEY}"
mkdir -p "$HOME/.qwen"
node <<'NODE'
const { writeFileSync } = require("node:fs");
function number(name, integer = false, minimum = 0) {
  const raw = process.env[name];
  const value = Number(raw);
  if (!raw || !Number.isFinite(value) || value < minimum || (integer && !Number.isInteger(value))) {
    console.error(`canvasbench qwen contract requires valid ${name}`);
    process.exit(64);
  }
  return value;
}
const preserve = process.env.CANVASBENCH_PRESERVE_THINKING;
if (preserve !== "true" && preserve !== "false") {
  console.error("canvasbench qwen contract requires CANVASBENCH_PRESERVE_THINKING");
  process.exit(64);
}
const settings = {
  model: {
    generationConfig: {
      contextWindowSize: number("CANVASBENCH_CONTEXT_WINDOW", true, 1),
      samplingParams: {
        temperature: number("CANVASBENCH_TEMPERATURE"),
        top_p: number("CANVASBENCH_TOP_P"),
        max_tokens: number("CANVASBENCH_MAX_OUTPUT_TOKENS", true, 1),
      },
      extra_body: {
        top_k: number("CANVASBENCH_TOP_K", true),
        seed: number("CANVASBENCH_SEED", true),
        chat_template_kwargs: { preserve_thinking: preserve === "true" },
      },
    },
  },
};
writeFileSync(`${process.env.HOME}/.qwen/settings.json`, JSON.stringify(settings), { mode: 0o600 });
NODE
exec /opt/harness/node_modules/.bin/qwen "$@"
