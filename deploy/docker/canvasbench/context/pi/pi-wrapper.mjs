#!/usr/bin/env node
import { mkdirSync, writeFileSync } from "node:fs";
import { spawnSync } from "node:child_process";
import { join } from "node:path";

const baseUrl = process.env.OPENAI_BASE_URL;
const apiKey = process.env.OPENAI_API_KEY;
if (!baseUrl || !apiKey) {
  console.error("canvasbench pi contract requires OPENAI_BASE_URL and OPENAI_API_KEY");
  process.exit(64);
}
const modelIndex = process.argv.indexOf("--model");
const modelRef = modelIndex >= 0 ? process.argv[modelIndex + 1] : "";
const slash = modelRef.indexOf("/");
if (slash <= 0 || slash === modelRef.length - 1) {
  console.error("canvasbench pi contract requires --model provider/model");
  process.exit(64);
}
const provider = modelRef.slice(0, slash);
const model = modelRef.slice(slash + 1);
const configDir = process.env.PI_CODING_AGENT_DIR || join(process.env.HOME, ".pi", "agent");
mkdirSync(configDir, { recursive: true, mode: 0o700 });
writeFileSync(join(configDir, "models.json"), JSON.stringify({
  providers: {
    [provider]: {
      baseUrl,
      apiKey: "$OPENAI_API_KEY",
      api: "openai-completions",
      models: [{
        id: model, name: model, reasoning: false, input: ["text"],
        cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
        contextWindow: 131072, maxTokens: 8192,
      }],
    },
  },
}), { mode: 0o600 });
const result = spawnSync("/opt/harness/node_modules/.bin/pi", process.argv.slice(2), {
  env: { ...process.env, PI_CODING_AGENT_DIR: configDir }, stdio: "inherit",
});
if (result.error) {
  console.error("canvasbench pi wrapper failed to start the pinned CLI");
  process.exit(70);
}
process.exit(result.status ?? 70);
