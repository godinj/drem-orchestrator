# Drem Orchestrator — feature/planner-agent

## Mission

Implement automatic planner agent spawning so tasks entering PLANNING state are decomposed by a Claude Code CLI agent that produces a `plan.json`, which gets parsed and presented for human review.

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

## What to Implement

### 1. AgentRunner API Surface (`src/orchestrator/agent_runner.py`)
- Add `can_spawn` property (check `len(self._processes) < self._max_concurrent`)
- Add `get_status(agent_id)` method (check in-memory process, fall back to DB)
- Add `spawn(agent_id, task_id, worktree_path, branch, prompt)` low-level spawn method

### 2. Planner Integration (`src/orchestrator/orchestrator.py`)
- Replace `_process_planning()` stub with real planner agent spawning
- Handle planner results in `_on_agent_completed()` (read `plan.json`, set `task.plan`, transition to PLAN_REVIEW)
- Handle planner failure in `_on_agent_failed()` (clear assignment, retry next tick)
- Fix API mismatches: `get_output` → `get_agent_output`, `spawn()` signature, `agent_type` → `task_id`
- Update `_handle_plan_approved()` to skip worktree creation if already exists
- Update `_handle_plan_rejected()` to clear `assigned_agent_id`

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

## Architecture

- Python 3.12+, FastAPI, SQLAlchemy async, Redis pub/sub
- `src/orchestrator/` — main package
- `ui/` — React + Vite task board frontend

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
