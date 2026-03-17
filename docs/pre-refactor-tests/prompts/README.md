# Pre-Refactor Test Coverage Prompts

Agent prompts to raise test coverage before a major architectural refactor.

**Current**: 42.8% overall (303 tests)
**Target**: ~65%+ overall, critical packages at 70%+

## Prompt Index

| # | Name | Tier | Deps | Package | Coverage Target | Files Created | Files Modified |
|---|------|------|------|---------|-----------------|---------------|----------------|
| 01 | model-coverage | 1 | none | `internal/model/` | 56% → 80%+ | `json_test.go` | `models_test.go`, `enums_test.go` |
| 02 | supervisor-coverage | 1 | none | `internal/supervisor/` | 37% → 70%+ | — | `supervisor_test.go` |
| 03 | prompt-coverage | 1 | none | `internal/prompt/` | 54% → 70%+ | — | `prompt_test.go` |
| 04 | taskimport-coverage | 1 | none | `internal/taskimport/` | 58% → 75%+ | `import_test.go` | — |
| 05 | orchestrator-lifecycle | 1 | none | `internal/orchestrator/` | covers core loop | `agent_result_test.go` | — |
| 06 | orchestrator-reconcile | 1 | none | `internal/orchestrator/` | covers data repair | `reconcile_test.go` | — |
| 07 | orchestrator-scheduling | 1 | none | `internal/orchestrator/` | covers scheduling | `scheduling_test.go` | — |

## Execution Order

All prompts are Tier 1 — they add tests for existing code and have no cross-dependencies. Run all in parallel:

```bash
# Tier 1 (all parallel)
/swarm docs/pre-refactor-tests/prompts
```

Or individually:

```bash
claude --agent docs/pre-refactor-tests/prompts/01-model-coverage.md
claude --agent docs/pre-refactor-tests/prompts/02-supervisor-coverage.md
claude --agent docs/pre-refactor-tests/prompts/03-prompt-coverage.md
claude --agent docs/pre-refactor-tests/prompts/04-taskimport-coverage.md
claude --agent docs/pre-refactor-tests/prompts/05-orchestrator-lifecycle.md
claude --agent docs/pre-refactor-tests/prompts/06-orchestrator-reconcile.md
claude --agent docs/pre-refactor-tests/prompts/07-orchestrator-scheduling.md
```

## Verification

After all agents complete:

```bash
go test ./... -cover
```

Expected outcome: overall coverage rises from ~42.8% to ~65%+, with all critical packages at 70%+.
