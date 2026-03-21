# Agent Bug Reports — Prompt Execution Plan

Source PRD: `plans/agent-bug-reports-prd.md`

## Prompts

| # | Name | Tier | Dependencies | New Files | Modified Files |
|---|------|------|-------------|-----------|----------------|
| 01 | Models & Enums | 1 | — | `internal/model/bugreport.go`, `internal/model/bugreport_enums.go`, `internal/model/bugreport_test.go` | `internal/db/db.go` |
| 02 | Prompts & Journal Swap | 1 | — | `internal/prompt/prompt_test.go` (new tests) | `internal/prompt/prompt.go`, `internal/supervisor/journal.go` (delete), `internal/supervisor/supervisor_test.go`, `internal/orchestrator/orchestrator.go` |
| 03 | Bug Report Service | 2 | 01 | `internal/bugreport/service.go`, `internal/bugreport/ingest.go`, `internal/bugreport/service_test.go` | — |
| 04 | Orchestrator Integration | 3 | 03 | — | `internal/orchestrator/orchestrator.go`, `cmd/drem/main.go`, `.gitignore` |
| 05 | TUI Bug Report Screen | 3 | 03 | `internal/tui/bugreports.go`, `internal/tui/bugreport_actions.go` | `internal/tui/app.go`, `internal/tui/keyhandlers.go`, `internal/tui/styles.go` |
| 06 | Documentation | 4 | 01–05 | `docs/agent-bug-reports/walkthrough.md` | `README.md` |

## Execution Order

```
Tier 1 (parallel — no dependencies):
  01-models-enums
  02-prompts-journal-swap

Tier 2 (after Tier 1 merges):
  03-bugreport-service

Tier 3 (after Tier 2 merges, parallel):
  04-orchestrator-integration
  05-tui-bugreport-screen

Tier 4 (after Tier 3 merges):
  06-documentation
```

## Dependency Graph

```
01-models-enums ──────┐
                      ├──→ 03-bugreport-service ──┬──→ 04-orchestrator-integration ──┐
02-prompts-journal ───┘                            │                                  ├──→ 06-documentation
                                                   └──→ 05-tui-bugreport-screen ─────┘
```

## Launch Commands

```bash
# Tier 1 (parallel)
claude --agent docs/agent-bug-reports/prompts/01-models-enums.md
claude --agent docs/agent-bug-reports/prompts/02-prompts-journal-swap.md

# Tier 2 (after Tier 1 merges)
claude --agent docs/agent-bug-reports/prompts/03-bugreport-service.md

# Tier 3 (parallel, after Tier 2 merges)
claude --agent docs/agent-bug-reports/prompts/04-orchestrator-integration.md
claude --agent docs/agent-bug-reports/prompts/05-tui-bugreport-screen.md

# Tier 4 (after Tier 3 merges)
claude --agent docs/agent-bug-reports/prompts/06-documentation.md
```
