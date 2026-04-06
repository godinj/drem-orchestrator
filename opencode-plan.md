# Plan: OpenCode Provider Integration

## Context

The drem-orchestrator currently only spawns Claude Code subprocesses for all agent roles. OpenCode (https://github.com/anomalyco/opencode, v1.3.16) is installed locally and configured with Ollama serving Qwen3-Coder-30B-A3B (IQ4_XS quant, Q8_0 KV cache) on an RTX 3090.

The goal is to allow per-role provider selection so roles like `coder` can use OpenCode (free local model) while `planner` stays on Claude Opus.

This enables cost savings and A/B testing between Claude and local/alternative models via the existing profile system.

## OpenCode CLI Contract

Validated against OpenCode v1.3.16 installed at `~/.opencode/bin/opencode`.

### Non-interactive invocation

```bash
opencode run --model ollama/qwen3-coder-iq4xs-128k \
             --variant minimal \
             --format json \
             --agent build \
             --dir /path/to/worktree \
             "the prompt text"
```

Key flags for `opencode run`:

| Flag | Purpose |
|------|---------|
| `--model` / `-m` | Model in `provider/model` format |
| `--variant` | Provider-specific reasoning effort (e.g. `high`, `max`, `minimal`) |
| `--format` | `default` (formatted text) or `json` (JSONL event stream) |
| `--agent` | Agent mode: `build` (full access), `plan` (read-only) |
| `--dir` | Working directory |
| `--file` / `-f` | Attach file(s) to message |
| `--continue` / `-c` | Continue last session |
| `--session` / `-s` | Resume specific session |
| `--thinking` | Show thinking blocks in output |

Prompt is passed as **positional arguments** (not stdin).

### JSON event stream (`--format json`)

Stdout emits newline-delimited JSON events. Observed event types:

**`step_start`** — agent begins a reasoning step
```json
{
  "type": "step_start",
  "timestamp": 1775451567836,
  "sessionID": "ses_...",
  "part": {
    "id": "prt_...",
    "messageID": "msg_...",
    "sessionID": "ses_...",
    "type": "step-start"
  }
}
```

**`text`** — text output chunk
```json
{
  "type": "text",
  "timestamp": 1775451567880,
  "sessionID": "ses_...",
  "part": {
    "id": "prt_...",
    "messageID": "msg_...",
    "sessionID": "ses_...",
    "type": "text",
    "text": "Hello!",
    "time": {"start": 1775451567879, "end": 1775451567879}
  }
}
```

**`step_finish`** — step completed, includes token counts
```json
{
  "type": "step_finish",
  "timestamp": 1775451567884,
  "sessionID": "ses_...",
  "part": {
    "id": "prt_...",
    "reason": "stop",
    "messageID": "msg_...",
    "sessionID": "ses_...",
    "type": "step-finish",
    "tokens": {
      "total": 12103,
      "input": 12100,
      "output": 3,
      "reasoning": 0,
      "cache": {"write": 0, "read": 0}
    },
    "cost": 0
  }
}
```

Additional event types (tool calls, errors) likely exist but have not been captured yet. The `step_finish` event is the critical one — it carries token counts and the stop reason.

### Differences from Claude Code

| Concern | Claude Code | OpenCode |
|---------|-------------|----------|
| Prompt delivery | stdin pipe (`-p -`) | Positional args |
| Output stream | stdout JSONL | stdout JSONL (`--format json`) |
| Output destination | Also writes `.claude/agent-output.log` | stdout only — must be captured by process layer |
| Permission bypass | `--dangerously-skip-permissions` | Not needed — `opencode run` auto-approves in non-interactive mode |
| Settings/hooks | `.claude/settings.json` + hooks | Not applicable |
| Context monitoring | Reads `.claude/usage` file | Parse `step_finish` events from stdout stream |
| Compaction signal | `.claude/compaction` file | No equivalent — not supported |
| Exit logging | Custom Stop hook writes `exit-log.jsonl` | Parse final `step_finish` from captured stdout |

## Local Model Configuration

### Hardware

- **GPU:** NVIDIA RTX 3090 (24 GB VRAM)
- **Model:** Qwen3-Coder-30B-A3B — MoE architecture (30.5B total, ~3.3B active per token)
- **Quant:** IQ4_XS (16.4 GB weights)
- **KV cache:** Q8_0 (negligible quality loss, no speed penalty vs FP16)
- **Inference:** Ollama v0.14.2 with flash attention + CUDA graphs

### Available Ollama models

| Model name | Context | Recommended use |
|------------|---------|-----------------|
| `qwen3-coder-iq4xs-128k` | 128K | **Default — full context** |
| `qwen3-coder-iq4xs-96k` | 96K | Fallback if 128K causes issues under load |
| `qwen3-coder-iq4xs-64k` | 64K | Conservative fallback |
| `qwen3-coder-iq4xs-48k` | 48K | Minimum viable; also the max for 2-agent parallel |

All variants tested and passing on the RTX 3090 with desktop running (no headless mode needed).

### VRAM budget and parallelism constraints

Ollama can serve multiple concurrent requests to the same model via `OLLAMA_NUM_PARALLEL=N`. Each parallel slot allocates its own KV cache upfront.

**Single agent (current default, `OLLAMA_NUM_PARALLEL=1`):**

| Component | VRAM |
|-----------|------|
| Weights (IQ4_XS) | ~16.4 GB |
| KV cache (Q8_0, 128K) | ~6 GB |
| Batch + overhead | ~1.5 GB |
| **Total** | **~23.4 GB** — fits in 24 GB |

**Two parallel agents (`OLLAMA_NUM_PARALLEL=2`):**

| Context per agent | KV cache × 2 | + Weights | Total | Fits? |
|-------------------|---------------|-----------|-------|-------|
| 128K | ~12 GB | 16.4 GB | ~29.9 GB | **No** — OOM |
| 96K | ~9 GB | 16.4 GB | ~26.9 GB | **No** — OOM |
| 64K | ~6 GB | 16.4 GB | ~23.9 GB | **Barely** — no headroom |
| 48K | ~4.6 GB | 16.4 GB | ~22.5 GB | **Yes** |

**Conclusion:** On current hardware (single RTX 3090, 24 GB), parallel local agents require dropping to 48K context. For MVP, run one local agent at a time with 128K context. If parallel local agents are needed later:

1. Set `OLLAMA_NUM_PARALLEL=2` in `/etc/systemd/system/ollama.service.d/performance.conf`
2. Switch the model reference in `drem.toml` to `ollama/qwen3-coder-iq4xs-48k`
3. Run `sudo systemctl daemon-reload && sudo systemctl restart ollama`

Alternatively, adding a second GPU or upgrading to a 48 GB card removes this constraint entirely.

### Performance expectations (single agent, 128K, RTX 3090)

| Metric | Estimate |
|--------|----------|
| Prefill (tok/s) | ~500–700 |
| Generation (tok/s) | ~50–70 |
| Usable context | 128K tokens |

### Ollama configuration reference

Model definitions live in `~/git/model-tuning/`. The active Ollama service config is at `/etc/systemd/system/ollama.service.d/performance.conf`:

```ini
[Service]
Environment="OLLAMA_FLASH_ATTENTION=1"
Environment="OLLAMA_KV_CACHE_TYPE=q8_0"
Environment="GGML_CUDA_GRAPH_OPT=1"
Environment="OLLAMA_HOST=0.0.0.0"
```

To add parallel support, append: `Environment="OLLAMA_NUM_PARALLEL=2"`

## Approach

Add a `provider` field to per-role config that flows through the existing config → model → process → runner pipeline. Default is `"claude"` for full backwards compatibility.

## Changes

### 1. Model layer — `csuite-chat-tui/internal/model/agentconfig.go`

- Add `ProviderType` string type with constants `ProviderClaude = "claude"`, `ProviderOpenCode = "opencode"`
- Add `Provider ProviderType` field to `AgentCLIConfig`
- Add `EffectiveProvider()` method (returns `ProviderClaude` when empty)
- Split `CLIArgs()` into provider-specific branches:
  - Claude: `["--model", X, "--effort", Y]` (unchanged)
  - OpenCode: `["--model", X, "--variant", Y, "--format", "json", "--agent", "build"]`

### 2. Config layer — `csuite-chat-tui/cmd/drem/config.go`

- Add `Provider string` to `AgentConfig` struct (TOML: `provider`)
- Add `OpenCodeBin string` to `Config` struct (TOML: `opencode_bin`, default: `"opencode"`)
- Update `ForAgentType()` to pass `Provider` through to `AgentCLIConfig`
- Update `DefaultConfig()` with `OpenCodeBin: "opencode"`

### 3. Profile layer — `csuite-chat-tui/cmd/drem/config_profiles.go`

- Add `Provider string` to `ProfileConfig` role entries
- Add provider override in `ForAgentTypeWithProfile()`:
  ```
  if override.Provider != "" { base.Provider = override.Provider }
  ```

### 4. Process layer — `csuite-chat-tui/internal/agent/process.go`

- Add `StartOpenCodeProcess()` function alongside `StartAgentProcess()`:
  - Command: `opencode run [--model X] [--variant Y] [--format json] [--agent build] --dir <cwd> <prompt>`
  - Prompt passed as positional arg (not stdin pipe)
  - Stdout captured to `.opencode/agent-output.jsonl` (we create this — OpenCode doesn't write its own log file)
  - Stdout also tee'd to an in-memory scanner for real-time event parsing
  - Same `AgentProcess` return type and lifecycle
  - No `--dangerously-skip-permissions` (not needed — `opencode run` auto-approves)

### 5. Runner layer — `csuite-chat-tui/internal/agent/runner.go`

- Add `openCodeBin string` field to `Runner`
- Update `NewRunner` signature: add `openCodeBin` parameter
- Add `Provider model.ProviderType` field to `RunningAgent`
- Split `startAgent()` into provider dispatch:
  - `startClaudeAgent()` — existing body (writes `.claude/settings.json`, hooks, status scripts, exit-log script)
  - `startOpenCodeAgent()` — minimal setup: write prompt to file for reference, write agent metadata, call `StartOpenCodeProcess`, launch monitors
  - OpenCode agents skip: settings.json, hooks, status line script, exit-log script, compaction signal
- Update `monitorAgent()` to branch exit-info enrichment by provider:
  - Claude: existing `enrichCompletion` (reads `exit-log.jsonl`)
  - OpenCode: new `enrichOpenCodeCompletion` (parses last `step_finish` from captured stdout log)

### 6. OpenCode exit info — `csuite-chat-tui/internal/agent/hook.go`

- Add `enrichOpenCodeCompletion(comp *Completion, logPath string)`:
  - Read the captured `.opencode/agent-output.jsonl` file
  - Find last `step_finish` event line
  - Extract `reason` into `ExitInfo.ExitReason`
  - Extract `tokens` into `ExitInfo` (map to existing fields)
  - `cost` field available but will be 0 for local models

### 7. Context monitoring — `csuite-chat-tui/internal/agent/monitor.go`

- In `contextMonitorLoop`, check `ra.Provider`:
  - Claude: existing path (reads usage file, transcript fallback, compaction signal)
  - OpenCode: tail the captured `.opencode/agent-output.jsonl` for `step_finish` events, accumulate token counts into `ctxmon.Usage`
  - No compaction detection for OpenCode (no equivalent mechanism)
- Activity monitoring (`agentmon.Monitor`) works for both — it scans git state and file modification times, not Claude-specific output

### 8. DB model — `csuite-chat-tui/internal/model/models.go`

- Add `Provider string` field to `Agent` struct (column: `provider`, default: `""`)
- Empty string means Claude (backwards compatible — all existing rows are Claude agents)
- Populated at spawn time alongside `ModelID` and `Effort`

### 9. Wiring — `csuite-chat-tui/cmd/drem/main.go`

- Update `NewRunner` call at line ~192 to pass `cfg.OpenCodeBin`

## Files to modify

1. `csuite-chat-tui/internal/model/agentconfig.go` — Provider type + field + CLIArgs split
2. `csuite-chat-tui/cmd/drem/config.go` — Provider on AgentConfig, OpenCodeBin on Config
3. `csuite-chat-tui/cmd/drem/config_profiles.go` — Provider override in profile resolution
4. `csuite-chat-tui/internal/agent/process.go` — StartOpenCodeProcess
5. `csuite-chat-tui/internal/agent/runner.go` — openCodeBin field, startOpenCodeAgent, RunningAgent.Provider
6. `csuite-chat-tui/internal/agent/hook.go` — enrichOpenCodeCompletion
7. `csuite-chat-tui/internal/agent/monitor.go` — Provider-aware context monitoring
8. `csuite-chat-tui/internal/model/models.go` — Provider field on Agent
9. `csuite-chat-tui/cmd/drem/main.go` — Wire OpenCodeBin

## Deferred (post-MVP)

- OpenCode support in Supervisor (currently synchronous Claude-only calls)
- Interactive supervisor sessions via OpenCode
- OpenCode idle detection (no hook equivalent; rely on process exit)
- Compaction detection for OpenCode
- Cost estimation for non-Anthropic models
- Parallel local agent support (requires OLLAMA_NUM_PARALLEL + reduced context; see VRAM budget section)
- Additional event types in the JSON stream (tool calls, errors) — capture and surface in dashboard

## Verification

1. `go vet ./...` and `go test ./...` pass
2. Existing `drem.toml` (no `provider` fields) works identically — full backwards compat
3. Add `provider = "opencode"` to one role in `drem.toml`, verify:
   - OpenCode subprocess spawns with correct args
   - `.opencode/agent-output.jsonl` captures the JSONL event stream
   - `step_finish` events are parsed for token counts and exit reason
   - Token counts appear in dashboard/DB
   - Exit info is captured on completion
4. Verify OpenCode agent does NOT write `.claude/settings.json`, hooks, or status scripts
5. Test with local model: `provider = "opencode"`, `model = "ollama/qwen3-coder-iq4xs-128k"`
6. Test provider in profile override: confirm profile-level `provider` overrides base config
