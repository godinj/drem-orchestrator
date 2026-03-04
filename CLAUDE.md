# Drem Orchestrator

## Build & Run

```bash
uv sync
uv run alembic upgrade head
uv run uvicorn orchestrator.server:app --reload
```

## Test

```bash
uv run pytest
```

## Architecture

- Python 3.12+, FastAPI, SQLAlchemy async, Redis pub/sub
- `src/orchestrator/` — main package
- `src/orchestrator/server.py` — FastAPI application entry point
- `src/orchestrator/models.py` — SQLAlchemy ORM models
- `src/orchestrator/schemas.py` — Pydantic request/response schemas
- `src/orchestrator/worktree.py` — Wrapper around wt shell scripts
- `src/orchestrator/agent_runner.py` — Spawn/monitor Claude Code sessions
- `src/orchestrator/orchestrator.py` — Main scheduling loop
- `src/orchestrator/scheduler.py` — Task assignment and concurrency
- `src/orchestrator/memory.py` — Agent memory persistence and compaction
- `src/orchestrator/messaging.py` — Redis pub/sub inter-agent messaging
- `src/orchestrator/merge.py` — Merge orchestration and conflict handling
- `ui/` — React + Vite task board frontend

## Key Files

| File | Role |
|------|------|
| `src/orchestrator/agent_runner.py` | Agent process lifecycle |
| `src/orchestrator/orchestrator.py` | Main scheduling loop |
| `src/orchestrator/agent_prompt.py` | Prompt generation (`generate_agent_prompt`, `_planner_instructions`) |
| `src/orchestrator/worktree.py` | Git worktree management |
| `src/orchestrator/schemas.py` | `SubtaskPlan` model |
| `src/orchestrator/models.py` | Task/Agent ORM models |
| `src/orchestrator/state_machine.py` | `transition_task()` |
| `src/orchestrator/enums.py` | `AgentType`, `AgentStatus`, `TaskStatus` |

## Prompts

See `docs/planner-agent/prompts/README.md` for the agent execution plan.

## Conventions

- Async everywhere (asyncio, async SQLAlchemy, async subprocess)
- Type hints on all public functions
- snake_case for functions/variables, PascalCase for classes
- Use `pathlib.Path` for file paths, not strings
- f-strings for formatting
- `ruff` for linting and formatting

## Worktree Integration

This project drives the `wt` workflow from godinj-dotfiles:
- Bare repos with `main/` + `feature/X/` worktrees
- Agent worktrees nested in `feature/X/.claude/worktrees/agent-<uuid>/`
- `wt new`, `wt rm`, `wt list`, `wt agent spawn` for lifecycle
