# Merge Overhaul — Agent Prompts

## Overview

6 agent prompts implementing the merge pipeline reliability overhaul. Organized into 2 tiers by dependency. All prompts within a tier can run in parallel.

## Prompt Index

| # | Name | Tier | Dependencies | Files Created/Modified |
|---|------|------|-------------|----------------------|
| 01 | worktree-merge-improvements | 1 | — | `internal/worktree/manager.go`, `internal/worktree/git.go` |
| 02 | orchestrator-reliability | 1 | — | `internal/orchestrator/orchestrator.go`, `internal/state/machine.go` |
| 03 | agent-lifecycle | 1 | — | `internal/agent/runner.go` |
| 04 | planner-and-validation | 1 | — | `internal/prompt/prompt.go`, `internal/orchestrator/plan_validation.go` (new) |
| 05 | merge-pipeline | 2 | 01 | `internal/merge/merge.go` |
| 06 | wave-scheduling | 2 | 02, 04 | `internal/orchestrator/scheduling.go`, `internal/orchestrator/orchestrator.go` |

## Dependency Graph

```
Tier 1 (parallel)          Tier 2 (parallel)
┌──────┐
│  01  │──────────────────→ 05
└──────┘
┌──────┐
│  02  │──────────────────→ 06
└──────┘                    ↑
┌──────┐                    │
│  03  │                    │
└──────┘                    │
┌──────┐                    │
│  04  │────────────────────┘
└──────┘
```

## Execution

```bash
# Tier 1 (parallel — no dependencies between these)
/swarm docs/merge-overhaul/prompts/01-worktree-merge-improvements.md
/swarm docs/merge-overhaul/prompts/02-orchestrator-reliability.md
/swarm docs/merge-overhaul/prompts/03-agent-lifecycle.md
/swarm docs/merge-overhaul/prompts/04-planner-and-validation.md

# Merge Tier 1 branches, then:

# Tier 2 (parallel — depends on Tier 1 being merged)
/swarm docs/merge-overhaul/prompts/05-merge-pipeline.md
/swarm docs/merge-overhaul/prompts/06-wave-scheduling.md
```

## What's Excluded

Per the prioritization discussion, these items are deferred:

- **Merge resolver agent** — rebase-before-merge + wave scheduling should eliminate most conflicts
- **Fixer agent** — re-evaluate after Tiers 1-2 are live
- **Integrator agent** — replaced by planner prompt P-2 (mandatory integration subtask)
- **Reviewer agent** — human gate is working; this is QoL
- **Journal improvements (§4.10)** — low priority, quick follow-up
