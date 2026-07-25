#!/usr/bin/env node
import crypto from "node:crypto";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { spawnSync } from "node:child_process";

const baseURL = (process.env.OPENAI_BASE_URL || "").replace(/\/+$/, "");
const apiKey = process.env.OPENAI_API_KEY || "";
if (!baseURL || !apiKey) throw new Error("canvasbench Continue contract requires OPENAI_BASE_URL and OPENAI_API_KEY");
const parsed = new URL(baseURL);
if (!["http:", "https:"].includes(parsed.protocol) || parsed.pathname.replace(/\/+$/, "") !== "/v1") {
  throw new Error("canvasbench Continue contract requires a valid OpenAI /v1 base URL");
}

const state = fs.mkdtempSync(path.join(os.tmpdir(), "canvasbench-continue-"));
try {
  const configPath = path.join(state, "config.yaml");
  const config = {
    name: "CanvasBench",
    version: "1.0.0",
    schema: "v1",
    models: [{
      name: "canvasbench",
      provider: "openai",
      model: process.env.CANVASBENCH_ADAPTER_MODEL || "",
      apiBase: baseURL,
      apiKey: "${{ secrets.OPENAI_API_KEY }}",
      contextLength: Number(process.env.CANVASBENCH_CONTEXT_WINDOW || 32768),
      defaultCompletionOptions: {
        maxTokens: Number(process.env.CANVASBENCH_MAX_OUTPUT_TOKENS || 4096),
        temperature: Number(process.env.CANVASBENCH_TEMPERATURE || 0),
        topP: Number(process.env.CANVASBENCH_TOP_P || 1),
      },
      capabilities: ["tool_use"],
      roles: ["chat", "edit", "apply"],
    }],
  };
  fs.writeFileSync(configPath, `${JSON.stringify(config)}\n`, { mode: 0o600 });
  const originalArgs = process.argv.slice(2);
  const args = originalArgs.map((value) => value === "__CANVASBENCH_CONFIG__" ? configPath : value);
  const completed = spawnSync(process.execPath, ["/opt/harness/cn-real.js", ...args], {
    env: process.env,
    encoding: "utf8",
    maxBuffer: 32 * 1024 * 1024,
  });
  if (completed.error) throw completed.error;
  const exitCode = completed.status ?? 1;
  const captured = `${completed.stdout || ""}${completed.stderr || ""}`.trim();
  const output = captured || `Continue exited ${exitCode} without output`;
  const session = crypto.createHash("sha256").update(originalArgs.join("\0")).digest("hex").slice(0, 24);
  process.stdout.write(`${JSON.stringify({
    schema: "canvasbench.cli-wrapper.v1",
    harness: "continue",
    session_id: `continue-${session}`,
    output,
    exit_code: exitCode,
  })}\n`);
  process.exitCode = exitCode;
} finally {
  fs.rmSync(state, { recursive: true, force: true });
}
