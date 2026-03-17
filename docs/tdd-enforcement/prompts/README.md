# TDD Enforcement — Agent Prompts

## Overview

10 agent prompts implementing TDD enforcement across the orchestrator. Organized into 3 tiers by dependency. All prompts within a tier can run in parallel. Prompt 01 (context window monitoring) is already complete.

## Prompt Index

| # | Name | Tier | Dependencies | Files Created/Modified |
|---|------|------|-------------|----------------------|
| 01 | context-window-monitoring | — | — | `internal/ctxmon/` (3 files) | **Done** (fb390a9) |
| 02 | state-machine-and-enums | 1 | — | `internal/model/enums.go`, `internal/state/machine.go` |
| 03 | plan-schema-and-model | 1 | — | `internal/model/models.go`, `internal/orchestrator/orchestrator.go` (plan parsing) |
| 04 | plan-validation | 1 | — | `internal/orchestrator/plan_validation.go` |
| 05 | planner-prompt | 1 | — | `internal/prompt/prompt.go` |
| 06 | test-writing-flow | 2 | 02, 03, 04 | `internal/orchestrator/orchestrator.go`, `cmd/drem/config.go` |
| 07 | coder-prompts | 2 | 03, 05 | `internal/prompt/prompt.go` |
| 08 | test-review-gate | 3 | 06 | `internal/orchestrator/orchestrator.go`, `internal/tui/app.go` |
| 09 | per-agent-test-gate | 3 | 06 | `internal/orchestrator/orchestrator.go` |
| 10 | failure-recovery | 3 | 06, 09 | `internal/orchestrator/orchestrator.go`, `cmd/drem/config.go`, `cmd/drem/main.go` |

## Dependency Graph

```
Tier 1 (parallel)         Tier 2 (parallel)        Tier 3 (parallel)
┌──────┐
│  02  │──────────┐
└──────┘          │
┌──────┐          ├──────→ 06 ──────┬──→ 08
│  03  │──────────┤    ↗            │
└──────┘          │   /             ├──→ 09 ──→ 10
┌──────┐          ├──┘              │
│  04  │──────────┘                 │
└──────┘                            │
┌──────┐                            │
│  05  │──────────────────→ 07      │
└──────┘                            │
```

Note: 10 depends on both 06 and 09, so it runs after 09 completes.

## Execution

```bash
# Tier 1 (parallel — no dependencies between these)
/swarm docs/tdd-enforcement/prompts/02-state-machine-and-enums.md
/swarm docs/tdd-enforcement/prompts/03-plan-schema-and-model.md
/swarm docs/tdd-enforcement/prompts/04-plan-validation.md
/swarm docs/tdd-enforcement/prompts/05-planner-prompt.md

# Merge Tier 1 branches, then:

# Tier 2 (parallel — depends on Tier 1 being merged)
/swarm docs/tdd-enforcement/prompts/06-test-writing-flow.md
/swarm docs/tdd-enforcement/prompts/07-coder-prompts.md

# Merge Tier 2 branches, then:

# Tier 3 (08 and 09 can run in parallel; 10 after 09)
/swarm docs/tdd-enforcement/prompts/08-test-review-gate.md
/swarm docs/tdd-enforcement/prompts/09-per-agent-test-gate.md

# Merge 08 and 09, then:
/swarm docs/tdd-enforcement/prompts/10-failure-recovery.md
```

## File Ownership

Files modified by multiple agents across tiers (not within a tier):

| File | Tier 1 | Tier 2 | Tier 3 |
|------|--------|--------|--------|
| `internal/orchestrator/orchestrator.go` | 03 (plan parsing) | 06 (test-writing flow) | 08, 09, 10 |
| `internal/prompt/prompt.go` | 05 (planner/reviewer) | 07 (coder) | — |
| `cmd/drem/config.go` | — | 06 (test config) | 10 (fixer threshold) |
| `internal/model/enums.go` | 02 | — | — |
| `internal/model/models.go` | 03 | — | — |

Within each tier, no two prompts modify the same file.
