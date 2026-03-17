# Test Coverage Overhaul — Agent Prompts

## Prompt Index

| # | Name | Tier | Dependencies | Creates | Modifies |
|---|------|------|-------------|---------|----------|
| 01 | testutil | 1 | — | `internal/testutil/testutil.go` | worktree, merge, model, orchestrator test files |
| 02 | memory-tests | 1 | — | `internal/memory/memory_test.go` | — |
| 03 | prompt-tests | 1 | — | — | `internal/prompt/prompt_test.go` |
| 04 | merge-helper-tests | 1 | — | — | `internal/merge/merge.go`, `merge_test.go` |
| 05 | agent-helper-tests | 1 | — | — | `internal/agent/runner.go`, `runner_test.go` |
| 06 | agent-session-interface | 2 | 05 | `internal/agent/session.go`, `runner_mock_test.go` | `internal/agent/runner.go` |
| 07 | tui-submodel-tests | 2 | — | `internal/tui/detail_test.go`, `agents_test.go` | — |
| 08 | merge-integration-tests | 3 | 01, 04 | — | `internal/merge/merge_test.go` |
| 09 | orchestrator-lifecycle-tests | 3 | 01, 06 | `internal/orchestrator/lifecycle_test.go` | — |

## Execution Order

```
Tier 1 (parallel — no dependencies)
├── 01-testutil
├── 02-memory-tests
├── 03-prompt-tests
├── 04-merge-helper-tests
└── 05-agent-helper-tests

Tier 2 (after Tier 1 merges)
├── 06-agent-session-interface  (depends on 05)
└── 07-tui-submodel-tests       (no deps, but grouped with Tier 2 for ordering)

Tier 3 (after Tier 2 merges)
├── 08-merge-integration-tests       (depends on 01, 04)
└── 09-orchestrator-lifecycle-tests   (depends on 01, 06)
```

## Launch Commands

```bash
# Tier 1 (parallel)
claude --agent docs/test-coverage-overhaul/prompts/01-testutil.md
claude --agent docs/test-coverage-overhaul/prompts/02-memory-tests.md
claude --agent docs/test-coverage-overhaul/prompts/03-prompt-tests.md
claude --agent docs/test-coverage-overhaul/prompts/04-merge-helper-tests.md
claude --agent docs/test-coverage-overhaul/prompts/05-agent-helper-tests.md

# Tier 2 (after Tier 1 merges)
claude --agent docs/test-coverage-overhaul/prompts/06-agent-session-interface.md
claude --agent docs/test-coverage-overhaul/prompts/07-tui-submodel-tests.md

# Tier 3 (after Tier 2 merges)
claude --agent docs/test-coverage-overhaul/prompts/08-merge-integration-tests.md
claude --agent docs/test-coverage-overhaul/prompts/09-orchestrator-lifecycle-tests.md
```

## Expected Coverage Impact

| Package | Before | After Tier 1 | After Tier 2 | After Tier 3 |
|---------|--------|-------------|-------------|-------------|
| internal/memory | 0% | ~75% | ~75% | ~75% |
| internal/prompt | 42.4% | ~65% | ~65% | ~65% |
| internal/merge | 13.9% | ~25% | ~25% | ~55% |
| internal/agent | 4% | ~15% | ~45% | ~45% |
| internal/tui | 4.3% | 4.3% | ~25% | ~25% |
| internal/orchestrator | 38.2% | 38.2% | 38.2% | ~50% |
