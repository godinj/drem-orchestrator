"""FastAPI application entry point."""

from __future__ import annotations

import asyncio
import logging
import uuid
from collections.abc import AsyncGenerator
from contextlib import asynccontextmanager
from pathlib import Path

from fastapi import FastAPI
from fastapi.middleware.cors import CORSMiddleware
from sqlalchemy import select

from orchestrator.agent_runner import AgentRunner
from orchestrator.config import settings
from orchestrator.db import async_session, engine
from orchestrator.models import Base, Project
from orchestrator.orchestrator import Orchestrator
from orchestrator.routers import agents, projects, tasks, ws
from orchestrator.routers.ws import broadcast
from orchestrator.worktree import WorktreeManager

logger = logging.getLogger(__name__)


class OrchestratorManager:
    """Manages per-project orchestrator loops."""

    def __init__(self) -> None:
        self._orchestrators: dict[uuid.UUID, Orchestrator] = {}
        self._tasks: dict[uuid.UUID, asyncio.Task[None]] = {}

    async def start_for_project(self, project_id: uuid.UUID, bare_repo_path: str) -> None:
        """Start an orchestrator loop for a project (if not already running)."""
        if project_id in self._tasks:
            return

        wt_manager = WorktreeManager(
            bare_repo=Path(bare_repo_path),
            wt_bin=settings.WT_BIN,
        )
        agent_runner = AgentRunner(
            worktree_manager=wt_manager,
            db_session_factory=async_session,
            claude_bin=Path(settings.CLAUDE_BIN),
            max_concurrent=settings.MAX_CONCURRENT_AGENTS,
        )
        async def _project_broadcast(event: dict) -> None:
            await broadcast(project_id, event)

        orchestrator = Orchestrator(
            agent_runner=agent_runner,
            worktree_manager=wt_manager,
            db_session_factory=async_session,
            broadcast_fn=_project_broadcast,
        )
        self._orchestrators[project_id] = orchestrator
        self._tasks[project_id] = asyncio.create_task(
            orchestrator.start(), name=f"orchestrator-{project_id}"
        )
        logger.info(f"Started orchestrator for project {project_id}")

    async def stop_all(self) -> None:
        """Stop all running orchestrator loops."""
        for project_id, orchestrator in self._orchestrators.items():
            await orchestrator.stop()
            logger.info(f"Stopped orchestrator for project {project_id}")
        for task in self._tasks.values():
            task.cancel()
        self._orchestrators.clear()
        self._tasks.clear()


orchestrator_manager = OrchestratorManager()


@asynccontextmanager
async def lifespan(app: FastAPI) -> AsyncGenerator[None, None]:
    """Initialize DB, start orchestrator loops on startup, clean up on shutdown."""
    async with engine.begin() as conn:
        await conn.run_sync(Base.metadata.create_all)

    # Start orchestrator for each existing project
    async with async_session() as session:
        result = await session.execute(select(Project))
        for project in result.scalars().all():
            await orchestrator_manager.start_for_project(
                project.id, project.bare_repo_path
            )

    app.state.orchestrator_manager = orchestrator_manager

    yield

    await orchestrator_manager.stop_all()
    await engine.dispose()


app = FastAPI(title="Drem Orchestrator", lifespan=lifespan)

# Store broadcast in app state for dependency injection by other modules
app.state.broadcast = broadcast

app.add_middleware(
    CORSMiddleware,
    allow_origins=["http://localhost:5173"],
    allow_credentials=True,
    allow_methods=["*"],
    allow_headers=["*"],
)


# --- Health check ---


@app.get("/health")
async def health() -> dict[str, str]:
    """Health check endpoint."""
    return {"status": "ok"}


# --- Include routers ---

app.include_router(tasks.router)
app.include_router(agents.router)
app.include_router(projects.router)
app.include_router(ws.router)
