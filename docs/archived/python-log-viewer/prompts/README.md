# Agent Clarification Questions + Log Viewer — Prompts

## Prompt Summary

| # | Name | Tier | Dependencies | Files Created | Files Modified |
|---|------|------|-------------|---------------|----------------|
| 01 | Backend | 1 | None | `alembic/versions/002_add_needs_clarification.py` | `enums.py`, `state_machine.py`, `models.py`, `schemas.py`, `orchestrator.py`, `agent_prompt.py`, `routers/tasks.py`, `routers/agents.py` |
| 02 | Frontend | 1 | None | `ui/src/components/LogModal.tsx` | `types.ts`, `api.ts`, `useBoard.ts`, `Board.tsx`, `TaskCard.tsx`, `AgentSidebar.tsx`, `App.tsx` |
| 03 | Tests | 1 | None* | `tests/test_clarifications.py` | `tests/test_state_machine.py`, `tests/test_agent_prompt.py`, `tests/test_api_tasks.py` |

*Tests use in-memory SQLite (no migration needed) but logically depend on the backend enum/schema changes being present. If running before backend merges, tests for the API endpoint will fail until backend changes land.

## Execution Order

All prompts are Tier 1 and can run in parallel:

```
┌─────────────┐
│ 01-backend  │──┐
├─────────────┤  │
│ 02-frontend │──┼── All Tier 1 (parallel)
├─────────────┤  │
│ 03-tests    │──┘
└─────────────┘
```

## Launch Commands

```bash
# Tier 1 (all parallel)
claude -p docs/clarification-log-viewer/prompts/01-backend.md
claude -p docs/clarification-log-viewer/prompts/02-frontend.md
claude -p docs/clarification-log-viewer/prompts/03-tests.md
```

## Verification

After all agents complete and changes are merged:

```bash
uv run ruff check src/
uv run pytest -v
cd ui && npm run build
```
