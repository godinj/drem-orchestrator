#!/usr/bin/env node
import { spawnSync } from "node:child_process";

function requiredNumber(name, { integer = false, minimum = 0 } = {}) {
  const raw = process.env[name];
  const value = Number(raw);
  if (!raw || !Number.isFinite(value) || value < minimum || (integer && !Number.isInteger(value))) {
    console.error(`canvasbench opencode contract requires valid ${name}`);
    process.exit(64);
  }
  return value;
}

const baseURL = process.env.OPENAI_BASE_URL;
const apiKey = process.env.OPENAI_API_KEY;
if (!baseURL || !apiKey) {
  console.error("canvasbench opencode contract requires OPENAI_BASE_URL and OPENAI_API_KEY");
  process.exit(64);
}
const modelIndex = process.argv.indexOf("--model");
const modelRef = modelIndex >= 0 ? process.argv[modelIndex + 1] : "";
const slash = modelRef.indexOf("/");
if (slash <= 0 || slash === modelRef.length - 1) {
  console.error("canvasbench opencode contract requires --model provider/model");
  process.exit(64);
}
const provider = modelRef.slice(0, slash);
const model = modelRef.slice(slash + 1);
const temperature = requiredNumber("CANVASBENCH_TEMPERATURE");
const topP = requiredNumber("CANVASBENCH_TOP_P");
const topK = requiredNumber("CANVASBENCH_TOP_K", { integer: true });
const contextWindow = requiredNumber("CANVASBENCH_CONTEXT_WINDOW", { integer: true, minimum: 1 });
const maxTokens = requiredNumber("CANVASBENCH_MAX_OUTPUT_TOKENS", { integer: true, minimum: 1 });
const config = {
  provider: {
    [provider]: {
      npm: "@ai-sdk/openai-compatible",
      name: "CanvasBench OpenAI-compatible proxy",
      options: { baseURL, apiKey: "{env:OPENAI_API_KEY}" },
      models: { [model]: { name: model, limit: { context: contextWindow, output: maxTokens } } },
    },
  },
  agent: { build: { temperature, top_p: topP, top_k: topK } },
};
const result = spawnSync("/opt/harness/node_modules/.bin/opencode", process.argv.slice(2), {
  env: { ...process.env, OPENCODE_CONFIG_CONTENT: JSON.stringify(config) },
  stdio: "inherit",
});
if (result.error) {
  console.error("canvasbench opencode wrapper failed to start the pinned CLI");
  process.exit(70);
}
process.exit(result.status ?? 70);
