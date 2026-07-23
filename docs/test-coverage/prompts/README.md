# Test Coverage Prompts

Agent prompts for improving drem-orchestrator test coverage before a major architectural refactor.

## Prompt Index

| # | Name | Tier | Dependencies | New Test File | Target Methods |
|---|------|------|-------------|---------------|----------------|
| 01 | Agent Completion Handlers | 1 | none | `internal/orchestrator/completion_test.go` | processAgentResult, onAgentCompleted, onPlannerCompleted, onCoderCompleted, onReviewerCompleted, onFixerCompleted, onAgentFailed, onAgentEmptyWork |
| 02 | Reconciliation and lifecycle recovery | 1 | none | existing focused reconciliation/lifecycle tests | Reconcile recovery-only contract, typed terminal replay, fail-closed inventory gaps, orphan cleanup |
| 03 | Supervisor Execution | 1 | none | `internal/supervisor/evaluate_test.go` | Evaluate, EvaluateJSON, WriteJournalEntry, prompt generation functions |
| 04 | Merge Execution & Pause | 1 | none | `internal/orchestrator/merge_pause_test.go` | executeMerge, handlePaused, processBacklog, processPlanning, DeleteSubtask |
| 05 | Tick Loop & Lifecycle | 2 | 01-04 | `internal/orchestrator/tick_integration_test.go` | doTick, Run, full BACKLOG→DONE lifecycle, failure-retry lifecycle |
| 06 | Context & Escalation | 2 | 01-04 | `internal/orchestrator/context_escalation_test.go` | checkContextUsage, handleAgentContextExhausted, escalateFixerToHuman, spawnFixerForTestFailure, handleTestWritingFailure |

## Execution Order

```
Tier 1 (parallel — no dependencies between them):
  01-agent-completion-handlers.md
  02-reconciliation.md
  03-supervisor-execution.md
  04-merge-pause.md

Tier 2 (after all Tier 1 branches are merged):
  05-tick-lifecycle.md
  06-context-escalation.md
```

## Launch Commands

```bash
# Tier 1 (parallel)
claude --agent docs/test-coverage/prompts/01-agent-completion-handlers.md
claude --agent docs/test-coverage/prompts/02-reconciliation.md
claude --agent docs/test-coverage/prompts/03-supervisor-execution.md
claude --agent docs/test-coverage/prompts/04-merge-pause.md

# Tier 2 (after Tier 1 merges)
claude --agent docs/test-coverage/prompts/05-tick-lifecycle.md
claude --agent docs/test-coverage/prompts/06-context-escalation.md
```

## Coverage Targets

| Package | Current | Target |
|---------|---------|--------|
| `internal/orchestrator` | 44.5% | 75%+ |
| `internal/supervisor` | 37.3% | 80%+ |

## Verification

After all agents complete:

```bash
go test ./... -cover
```
