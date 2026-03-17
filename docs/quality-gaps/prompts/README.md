# Quality Gaps — Agent Prompts

## Prompt Index

| # | Name | Tier | Dependencies | Creates | Modifies |
|---|------|------|-------------|---------|----------|
| 01 | merge-helper-tests | 1 | — | — | `internal/merge/merge_test.go` |
| 02 | prompt-helper-tests | 1 | — | — | `internal/prompt/prompt_test.go` |
| 03 | readme-recovery-docs | 1 | — | — | `README.md` |
| 04 | merge-integration-tests | 2 | 01 (same file) | — | `internal/merge/merge_test.go` |

## Execution Order

```
Tier 1 (parallel — no dependencies)
├── 01-merge-helper-tests
├── 02-prompt-helper-tests
└── 03-readme-recovery-docs

Tier 2 (after Tier 1 merges)
└── 04-merge-integration-tests  (depends on 01 — same file)
```

## Launch Commands

```bash
# Tier 1 (parallel)
claude --agent docs/quality-gaps/prompts/01-merge-helper-tests.md
claude --agent docs/quality-gaps/prompts/02-prompt-helper-tests.md
claude --agent docs/quality-gaps/prompts/03-readme-recovery-docs.md

# Tier 2 (after Tier 1 merges)
claude --agent docs/quality-gaps/prompts/04-merge-integration-tests.md
```

## Expected Coverage Impact

| Package | Before | After Tier 1 | After Tier 2 |
|---------|--------|-------------|-------------|
| internal/merge | 31.5% | ~40% | ~55% |
| internal/prompt | 73.5% | ~85% | ~85% |

## Documentation Impact

| Section | Before | After |
|---------|--------|-------|
| Self-Healing & Recovery | Missing | Added to README |
| State Reconciliation | Missing | Added to README |
| Troubleshooting | Missing | Added to README |
