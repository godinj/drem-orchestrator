# Quality Remediation Prompts

15 agent prompts addressing constitution violations, deep module narrowing, documentation gaps, and test coverage.

## Prompt Index

| # | Name | Tier | Dependencies | Files Created | Files Modified |
|---|------|------|-------------|---------------|----------------|
| 01 | gofmt-fix | T1 | — | — | `internal/supervisor/types.go` |
| 02 | shrink-task-processing | T1 | — | `internal/orchestrator/merge_execution.go` | `internal/orchestrator/task_processing.go` |
| 03 | shrink-prompt | T1 | — | `internal/prompt/prompt_review_fixer.go` | `internal/prompt/prompt.go` |
| 04 | reduce-orchestrator-imports | T1 | — | — | `internal/orchestrator/*.go` (targeted) |
| 05 | narrow-supervisor-surface | T1 | — | — | `internal/supervisor/*.go` |
| 06 | narrow-constraints-surface | T1 | — | — | `internal/constraints/*.go` |
| 07 | narrow-clarification-surface | T1 | — | — | `internal/clarification/*.go` |
| 08 | update-architecture-md | T2 | 01-07 | — | `ARCHITECTURE.md` |
| 09 | readme-clarification | T2 | 08 | — | `README.md` |
| 10 | readme-constraints-guide | T2 | 08 | — | `README.md` |
| 11 | readme-scoring-guide | T2 | 08 | — | `README.md` |
| 12 | readme-lifecycle-and-config | T2 | 08 | — | `README.md` |
| 13 | tui-test-coverage | T1 | — | `internal/tui/*_test.go` | — |
| 14 | agent-test-coverage | T1 | — | `internal/agent/*_test.go` | — |
| 15 | orchestrator-test-coverage | T1 | — | `internal/orchestrator/*_test.go` | — |

## Execution Order

### Tier 1 — All independent, run in parallel

```bash
# Constitution fixes (4 agents)
/swarm docs/quality-remediation/prompts/01-gofmt-fix.md
/swarm docs/quality-remediation/prompts/02-shrink-task-processing.md
/swarm docs/quality-remediation/prompts/03-shrink-prompt.md
/swarm docs/quality-remediation/prompts/04-reduce-orchestrator-imports.md

# Deep module narrowing (3 agents)
/swarm docs/quality-remediation/prompts/05-narrow-supervisor-surface.md
/swarm docs/quality-remediation/prompts/06-narrow-constraints-surface.md
/swarm docs/quality-remediation/prompts/07-narrow-clarification-surface.md

# Test coverage (3 agents)
/swarm docs/quality-remediation/prompts/13-tui-test-coverage.md
/swarm docs/quality-remediation/prompts/14-agent-test-coverage.md
/swarm docs/quality-remediation/prompts/15-orchestrator-test-coverage.md
```

### Tier 2 — After Tier 1 merges

```bash
# Architecture doc (1 agent — must see final state of code changes)
/swarm docs/quality-remediation/prompts/08-update-architecture-md.md

# README updates (4 agents — can run in parallel after 08 merges)
/swarm docs/quality-remediation/prompts/09-readme-clarification.md
/swarm docs/quality-remediation/prompts/10-readme-constraints-guide.md
/swarm docs/quality-remediation/prompts/11-readme-scoring-guide.md
/swarm docs/quality-remediation/prompts/12-readme-lifecycle-and-config.md
```

## Dependency Graph

```
Tier 1 (parallel):
  01-gofmt-fix ──────────────────┐
  02-shrink-task-processing ─────┤
  03-shrink-prompt ──────────────┤
  04-reduce-orchestrator-imports ┼──► 08-update-architecture-md ──┬──► 09-readme-clarification
  05-narrow-supervisor-surface ──┤                                ├──► 10-readme-constraints-guide
  06-narrow-constraints-surface ─┤                                ├──► 11-readme-scoring-guide
  07-narrow-clarification-surface┘                                └──► 12-readme-lifecycle-and-config

  13-tui-test-coverage ─────────── (independent)
  14-agent-test-coverage ────────── (independent)
  15-orchestrator-test-coverage ─── (independent)
```

## Conflict Risk

- **Low risk**: 01, 05-07, 13-14 (each touches different packages)
- **Medium risk**: 02 + 04 (both touch `internal/orchestrator/` — 02 extracts a function, 04 changes imports; the extraction in 02 may shift import locations that 04 is counting)
- **Medium risk**: 09-12 (all modify `README.md` — run in parallel but expect merge conflicts in adjacent sections)

**Recommendation**: Run 02 before 04. Run 09-12 sequentially or accept manual conflict resolution.
