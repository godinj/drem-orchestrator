# Plan Clarification Loop — Agent Prompts

Generated from [`docs/plan-clarification-prd.md`](../plan-clarification-prd.md).

## Prompt Summary

| # | Name | Tier | Dependencies | Files Created | Files Modified |
|---|------|------|-------------|---------------|----------------|
| 01 | Model & State Machine | 1 | — | — | `internal/model/enums.go`, `internal/state/machine.go`, `internal/state/machine_test.go` |
| 02 | Clarification Package | 1 | — | `internal/clarification/clarification.go`, `internal/clarification/clarification_test.go` | — |
| 03 | Prompt Updates | 1 | — | — | `internal/prompt/prompt.go`, `internal/supervisor/prompts.go` |
| 04 | Orchestrator Integration | 2 | 01, 02, 03 | — | `internal/orchestrator/handlers.go`, `internal/orchestrator/task_processing.go`, `internal/orchestrator/orchestrator.go` |
| 05 | TUI Integration | 2 | 01, 04 | — | `internal/tui/detail.go`, `internal/tui/model.go` |

## Execution Order

```
Tier 1 (parallel — no dependencies between them):
  01-model-state-machine
  02-clarification-package
  03-prompt-updates

Tier 2 (after all Tier 1 agents complete and merge):
  04-orchestrator-integration
  05-tui-integration
```

## Launch Commands

```bash
# Tier 1 (parallel)
claude --agent docs/plan-clarification/prompts/01-model-state-machine.md
claude --agent docs/plan-clarification/prompts/02-clarification-package.md
claude --agent docs/plan-clarification/prompts/03-prompt-updates.md

# Tier 2 (after Tier 1 merges)
claude --agent docs/plan-clarification/prompts/04-orchestrator-integration.md
claude --agent docs/plan-clarification/prompts/05-tui-integration.md
```
