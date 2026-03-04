"""FastAPI application entry point."""

from collections.abc import AsyncGenerator
from contextlib import asynccontextmanager

from fastapi import FastAPI
from fastapi.middleware.cors import CORSMiddleware

from orchestrator.db import engine
from orchestrator.models import Base
from orchestrator.routers import agents, projects, tasks, ws
from orchestrator.routers.ws import broadcast


@asynccontextmanager
async def lifespan(app: FastAPI) -> AsyncGenerator[None, None]:
    """Initialize DB engine on startup, dispose on shutdown."""
    async with engine.begin() as conn:
        await conn.run_sync(Base.metadata.create_all)
    yield
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
