# OpenCode Provider Integration — Agent Prompts

Implements per-role provider selection so agents can use OpenCode (local LLM via Ollama) instead of Claude Code.

Source: `opencode-plan.md`

## Prompt Index

| # | Name | Tier | Dependencies | Files Modified |
|---|------|------|-------------|----------------|
| 01 | Model & Config | 1 | — | `internal/model/agentconfig.go`, `cmd/drem/config.go`, `cmd/drem/config_profiles.go`, `internal/model/models.go` |
| 02 | OpenCode Process | 1 | — | `internal/agent/process.go` |
| 03 | Runner Integration | 2 | 01, 02 | `internal/agent/runner.go`, `internal/agent/hook.go`, `internal/agent/monitor.go`, `cmd/drem/main.go` |

## Dependency Graph

```
01-model-config ────┐
                    ├──► 03-runner-integration
02-opencode-process ┘
```

## Execution

```bash
# Tier 1 (parallel)
claude --agent docs/opencode-provider/prompts/01-model-config.md
claude --agent docs/opencode-provider/prompts/02-opencode-process.md

# Tier 2 (after Tier 1 merges)
claude --agent docs/opencode-provider/prompts/03-runner-integration.md
```
