# Planner Agent Integration — Prompts

## Overview

Implement automatic planner agent spawning so tasks in PLANNING state get decomposed by a Claude Code agent instead of sitting idle forever.

## Prompt Summary

| # | Name | Tier | Dependencies | Files Modified |
|---|------|------|-------------|----------------|
| 01 | AgentRunner API Surface | 1 | None | `src/orchestrator/agent_runner.py` |
| 02 | Planner Integration | 2 | Agent 01 | `src/orchestrator/orchestrator.py` |

## Execution Order

```bash
# Tier 1 (no dependencies)
claude --agent docs/planner-agent/prompts/01-agent-runner-api.md

# Tier 2 (after Tier 1 merges)
claude --agent docs/planner-agent/prompts/02-planner-integration.md
```

## Verification

After both agents complete:
```bash
uv run pytest
uv run uvicorn orchestrator.server:app --reload
# Create a task — it should auto-transition BACKLOG → PLANNING → (planner runs) → PLAN_REVIEW
```
