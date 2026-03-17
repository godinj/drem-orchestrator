# Constraint System — Agent Prompts

## Overview

6 agent prompts implementing a project-agnostic constraint system. Constraints are defined in `.drem/constraints.toml` and enforced at plan validation, post-agent merge, and the integration gate. Organized into 3 tiers by dependency.

## Prompt Index

| # | Name | Tier | Dependencies | Creates | Modifies |
|---|------|------|-------------|---------|----------|
| 01 | constraints-package | 1 | — | `internal/constraints/` (5 files), `.drem/constraints.toml` | — |
| 02 | planner-prompt-context | 2 | 01 | `internal/prompt/prompt_context_test.go` | `internal/prompt/prompt.go` |
| 03 | plan-constraint-validation | 2 | 01 | `internal/orchestrator/plan_constraint_test.go` | `internal/orchestrator/plan_validation.go`, `internal/orchestrator/agent_results.go` |
| 04 | post-agent-constraint-gate | 2 | 01 | `internal/orchestrator/post_agent_constraint_test.go` | `internal/orchestrator/agent_results.go` |
| 05 | integration-gate | 2 | 01 | `internal/orchestrator/integration_gate_test.go` | `internal/orchestrator/task_processing.go` |
| 06 | migration-and-docs | 3 | 01-05 | `cmd/check-constraints/main.go` | `scripts/check_constitution.sh`, `ARCHITECTURE.md` |

## Dependency Graph

```
Tier 1               Tier 2 (parallel)         Tier 3
┌──────┐
│  01  │──────┬──→ 02 (prompt)
│      │      ├──→ 03 (plan validation)
│      │      ├──→ 04 (post-agent gate)  ──→ 06 (migration)
│      │      └──→ 05 (integration gate)
└──────┘
```

## Execution

```bash
# Tier 1 (single agent — foundation)
/swarm docs/constraints-system/prompts/01-constraints-package.md

# Merge Tier 1 branch, then:

# Tier 2 (parallel — 4 independent agents)
/swarm docs/constraints-system/prompts/02-planner-prompt-context.md
/swarm docs/constraints-system/prompts/03-plan-constraint-validation.md
/swarm docs/constraints-system/prompts/04-post-agent-constraint-gate.md
/swarm docs/constraints-system/prompts/05-integration-gate.md

# Merge Tier 2 branches, then:

# Tier 3 (single agent — cleanup)
/swarm docs/constraints-system/prompts/06-migration-and-docs.md
```

## File Ownership

Files modified by multiple agents across tiers (not within a tier):

| File | Tier 1 | Tier 2 | Tier 3 |
|------|--------|--------|--------|
| `internal/orchestrator/agent_results.go` | — | 03 (plan validation wiring), 04 (post-agent gate) | — |
| `internal/constraints/*` | 01 (creates) | — | — |

Within Tier 2, agents 03 and 04 both modify `agent_results.go` but at different code locations:
- Agent 03: adds a call in `onPlannerCompleted()` (~line 259)
- Agent 04: adds a call in `onAgentCompleted()` (~line 113)

These are in separate functions and should merge cleanly.

## Expected Outcome

| Metric | Before | After |
|--------|--------|-------|
| Constitution checks in orchestrator pipeline | 0 | 3 (plan, post-agent, integration gate) |
| Planner awareness of architecture constraints | none | full (via context_files) |
| check_constitution.sh maintenance | hand-maintained bash | thin wrapper over Go runner |
| Project-agnostic constraint config | none | `.drem/constraints.toml` |
| ARCHITECTURE.md rules marked [enforced] | 0 | 7 |
