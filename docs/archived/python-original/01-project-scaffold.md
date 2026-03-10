# Agent: Project Scaffold

You are building **Drem Orchestrator**, a Python application that orchestrates Claude Code agents
working in parallel across git worktrees. Your task is to scaffold the project structure, dependency
management, and foundational server skeleton.

## Context

This is a greenfield project. The bare repo has been initialized at `~/git/drem-orchestrator.git/`.
You are creating the `main/` worktree contents.

The project integrates with an existing worktree workflow (`wt` scripts in `~/git/godinj-dotfiles.git/wt/`)
that manages bare repos with nested feature and agent worktrees.

## Deliverables

### Project root files

#### 1. `pyproject.toml`

Python 3.12+ project using `uv` for dependency management.

Dependencies:
- `fastapi` — API server
- `uvicorn[standard]` — ASGI server
- `sqlalchemy[asyncio]` — ORM
- `alembic` — migrations
- `aiosqlite` — async SQLite driver (dev/local; PostgreSQL for production)
- `asyncpg` — async PostgreSQL driver
- `redis[hiredis]` — pub/sub messaging and heartbeats
- `pydantic>=2.0` — request/response schemas
- `websockets` — real-time UI updates
- `httpx` — HTTP client for Claude API

Dev dependencies:
- `pytest`, `pytest-asyncio` — testing
- `ruff` — linting/formatting

```toml
[project]
name = "drem-orchestrator"
version = "0.1.0"
requires-python = ">=3.12"

[build-system]
requires = ["hatchling"]
build-backend = "hatchling.build"
```

#### 2. `CLAUDE.md`

Project documentation following drem-canvas conventions:

```markdown
# Drem Orchestrator

## Build & Run

\```bash
uv sync
uv run alembic upgrade head
uv run uvicorn orchestrator.server:app --reload
\```

## Test

\```bash
uv run pytest
\```

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
```

#### 3. `src/orchestrator/__init__.py`

Empty init file.

#### 4. `src/orchestrator/server.py`

Minimal FastAPI application skeleton:

- `app = FastAPI(title="Drem Orchestrator")`
- Health check endpoint: `GET /health` → `{"status": "ok"}`
- CORS middleware allowing `http://localhost:5173` (Vite dev server)
- Lifespan handler that initializes DB engine on startup
- Include routers (empty stubs) for: `tasks`, `agents`, `projects`

#### 5. `src/orchestrator/config.py`

Configuration via environment variables with Pydantic `BaseSettings`:

- `DATABASE_URL` — default `sqlite+aiosqlite:///./orchestrator.db`
- `REDIS_URL` — default `redis://localhost:6379`
- `WT_BIN` — path to wt script, default `~/git/godinj-dotfiles.git/wt/wt.sh`
- `CLAUDE_BIN` — path to claude CLI, default `claude`
- `MAX_CONCURRENT_AGENTS` — default `5`
- `CONTEXT_COMPACTION_THRESHOLD` — default `0.7` (compact at 70% context usage)

#### 6. `src/orchestrator/db.py`

Async SQLAlchemy engine and session factory:

- `create_engine(settings.DATABASE_URL)`
- `async_sessionmaker` for request-scoped sessions
- `get_db()` async generator for FastAPI dependency injection

### Alembic setup

#### 7. `alembic.ini`

Standard alembic config pointing to `src/orchestrator/db.py` for the engine.

#### 8. `alembic/env.py`

Async alembic env that imports `Base.metadata` from `models.py`.

#### 9. `alembic/versions/` (empty directory)

Placeholder for migrations.

### Test skeleton

#### 10. `tests/__init__.py`

Empty.

#### 11. `tests/conftest.py`

Pytest fixtures:
- `db_session` — in-memory SQLite async session for tests
- `client` — `httpx.AsyncClient` with FastAPI test app

#### 12. `tests/test_health.py`

Single test: `GET /health` returns 200 with `{"status": "ok"}`.

### UI skeleton

#### 13. `ui/package.json`

Vite + React + TypeScript project. Dependencies:
- `react`, `react-dom`
- `@tanstack/react-query` — server state
- `tailwindcss`, `@tailwindcss/vite` — styling

#### 14. `ui/vite.config.ts`

Vite config with React plugin, proxy `/api` to `http://localhost:8000`.

#### 15. `ui/src/App.tsx`

Minimal app shell with "Drem Orchestrator" heading and placeholder board component.

### Scripts

#### 16. `scripts/bootstrap.sh`

```bash
#!/usr/bin/env bash
set -euo pipefail
echo "Installing Python dependencies..."
uv sync
echo "Installing UI dependencies..."
cd ui && npm install && cd ..
echo "Running migrations..."
uv run alembic upgrade head
echo "Done. Run: uv run uvicorn orchestrator.server:app --reload"
```

## Build Verification

```bash
uv sync
uv run pytest
cd ui && npm install && npx tsc --noEmit
```
