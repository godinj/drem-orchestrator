#!/usr/bin/env node
import crypto from "node:crypto";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { spawnSync } from "node:child_process";

const baseURL = (process.env.OPENAI_BASE_URL || "").replace(/\/+$/, "");
const apiKey = process.env.OPENAI_API_KEY || "";
if (!baseURL || !apiKey) throw new Error("canvasbench cline contract requires OPENAI_BASE_URL and OPENAI_API_KEY");
const parsed = new URL(baseURL);
if (!["http:", "https:"].includes(parsed.protocol) || parsed.pathname.replace(/\/+$/, "") !== "/v1") {
  throw new Error("canvasbench cline contract requires a valid OpenAI /v1 base URL");
}

const state = fs.mkdtempSync(path.join(os.tmpdir(), "canvasbench-cline-"));
try {
  const settings = path.join(state, "settings");
  fs.mkdirSync(settings, { mode: 0o700 });
  const provider = {
    provider: "openai-compatible",
    model: process.env.CANVASBENCH_ADAPTER_MODEL || "",
    apiKey,
    baseUrl: baseURL,
    protocol: "openai-chat",
    client: "openai-compatible",
    contextWindow: Number(process.env.CANVASBENCH_CONTEXT_WINDOW || 32768),
    maxTokens: Number(process.env.CANVASBENCH_MAX_OUTPUT_TOKENS || 4096),
    capabilities: ["streaming", "tools"],
  };
  const providers = {
    version: 1,
    lastUsedProvider: "openai-compatible",
    providers: {
      "openai-compatible": {
        settings: provider,
        updatedAt: "2026-01-01T00:00:00.000Z",
        tokenSource: "manual",
      },
    },
  };
  const providerPath = path.join(settings, "providers.json");
  fs.writeFileSync(providerPath, `${JSON.stringify(providers)}\n`, { mode: 0o600 });
  const args = process.argv.slice(2);
  const completed = spawnSync("/opt/harness/cline-real", args, {
    env: { ...process.env, CLINE_DATA_DIR: state, CLINE_PROVIDER_SETTINGS_PATH: providerPath },
    encoding: "utf8",
    maxBuffer: 32 * 1024 * 1024,
  });
  if (completed.error) throw completed.error;
  const exitCode = completed.status ?? 1;
  const captured = `${completed.stdout || ""}${completed.stderr || ""}`.trim();
  const output = captured || `cline exited ${exitCode} without output`;
  const session = crypto.createHash("sha256").update(args.join("\0")).digest("hex").slice(0, 24);
  process.stdout.write(`${JSON.stringify({
    schema: "canvasbench.cli-wrapper.v1",
    harness: "cline",
    session_id: `cline-${session}`,
    output,
    exit_code: exitCode,
  })}\n`);
  process.exitCode = exitCode;
} finally {
  fs.rmSync(state, { recursive: true, force: true });
}
