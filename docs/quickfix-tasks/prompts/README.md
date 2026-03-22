# Quick Fix Task Category — Agent Prompts

## Prompt Index

| # | Name | Tier | Dependencies | Files Created | Files Modified |
|---|------|------|-------------|---------------|----------------|
| 01 | Task Model | 1 | — | `internal/model/enums_test.go` | `internal/model/enums.go`, `internal/model/models.go`, `internal/state/machine.go`, `internal/state/machine_test.go`, `internal/testutil/testutil.go` |
| 02 | Lifecycle Routing | 2 | 01 | `internal/orchestrator/quickfix_test.go` | `internal/orchestrator/task_processing.go`, `internal/orchestrator/orchestrator.go` |
| 03 | Classifier | 2 | 01 | `internal/orchestrator/classifier.go`, `internal/orchestrator/classifier_test.go` | — |
| 04 | TUI Quick Fix | 2 | 01 | — | `internal/tui/create.go`, `internal/tui/board.go`, `internal/tui/keyhandlers.go`, `internal/orchestrator/orchestrator.go` (CreateTask) |
| 05 | Bug Report Integration | 3 | 02, 03 | `internal/orchestrator/bugreport_classify_test.go` | `internal/orchestrator/orchestrator.go` (ingestBugReports) |

## Execution Order

```
Tier 1 (run first):
  01-task-model

Tier 2 (run in parallel after 01 merges):
  02-lifecycle-routing
  03-classifier
  04-tui-quickfix

Tier 3 (run after 02 + 03 merge):
  05-bugreport-integration
```

## Dependency Graph

```
01-task-model
    ├── 02-lifecycle-routing ──┐
    ├── 03-classifier ─────────┼── 05-bugreport-integration
    └── 04-tui-quickfix        │
```

## Launch Commands

```bash
# Tier 1
claude --agent docs/quickfix-tasks/prompts/01-task-model.md

# Tier 2 (parallel — after 01 merges)
claude --agent docs/quickfix-tasks/prompts/02-lifecycle-routing.md
claude --agent docs/quickfix-tasks/prompts/03-classifier.md
claude --agent docs/quickfix-tasks/prompts/04-tui-quickfix.md

# Tier 3 (after 02 + 03 merge)
claude --agent docs/quickfix-tasks/prompts/05-bugreport-integration.md
```

## Shared File Ownership

`internal/orchestrator/orchestrator.go` is modified by agents 02, 04, and 05. Each owns specific sections:

- **Agent 02**: `doTick` IN_PROGRESS loop, `transitionQuickFixToMerging` (new method)
- **Agent 04**: `CreateTask` signature (adds `quickFix bool` parameter)
- **Agent 05**: `ingestBugReports` modification, `classifyNewBugReports` (new method)
