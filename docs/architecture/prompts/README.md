# Architecture Compliance Prompts

Agent prompts to bring drem-orchestrator into alignment with `ARCHITECTURE.md`.

## Prompt Summary

| # | Name | Tier | Dependencies | Creates | Modifies |
|---|------|------|-------------|---------|----------|
| 01 | gofmt-compliance | 1 | — | — | 13 files (formatting only) |
| 02 | gorm-hooks-consolidation | 1 | — | — | model/models.go, db/db.go |
| 03 | magic-numbers | 1 | — | — | orchestrator.go, runner.go, memory.go, merge.go, tmux.go |
| 04 | test-helper-consolidation | 1 | — | — | testutil/testutil.go + 9 test files |
| 05 | check-constitution | 1 | — | scripts/check_constitution.sh | — |
| 06 | extract-reconciliation-and-testing | 2 | 01-05 | reconcile.go, test_execution.go | orchestrator.go |
| 07 | extract-results-and-processing | 3 | 06 | agent_results.go, task_processing.go | orchestrator.go |

## Execution Order

```
Tier 1 (parallel)     Tier 2              Tier 3
┌──────────────┐
│ 01-gofmt     │
│ 02-gorm      │
│ 03-constants │──────▶ 06-extract ──────▶ 07-extract
│ 04-testutil  │        reconcile +        results +
│ 05-script    │        test execution     task processing
└──────────────┘
```

### Tier 1 (parallel — no dependencies)

```bash
claude --agent docs/architecture/prompts/01-gofmt-compliance.md
claude --agent docs/architecture/prompts/02-gorm-hooks-consolidation.md
claude --agent docs/architecture/prompts/03-magic-numbers.md
claude --agent docs/architecture/prompts/04-test-helper-consolidation.md
claude --agent docs/architecture/prompts/05-check-constitution.md
```

### Tier 2 (after all Tier 1 branches merge)

```bash
claude --agent docs/architecture/prompts/06-extract-reconciliation-and-testing.md
```

### Tier 3 (after Tier 2 merges)

```bash
claude --agent docs/architecture/prompts/07-extract-results-and-processing.md
```

## Expected Outcome

| Metric | Before | After |
|--------|--------|-------|
| orchestrator.go lines | 4,567 | ~1,300 |
| orchestrator.go functions | 84 | ~25 |
| gofmt violations | 13 | 0 |
| BeforeCreate hooks | 6 | 0 (1 callback) |
| Test helper duplicates | 12+ | 0 |
| Magic number instances | ~30 | 0 |
| Enforcement script | none | scripts/check_constitution.sh |
