"""FastAPI application entry point."""

from collections.abc import AsyncGenerator
from contextlib import asynccontextmanager

from fastapi import APIRouter, FastAPI
from fastapi.middleware.cors import CORSMiddleware

from orchestrator.db import engine
from orchestrator.models import Base


@asynccontextmanager
async def lifespan(app: FastAPI) -> AsyncGenerator[None, None]:
    """Initialize DB engine on startup, dispose on shutdown."""
    async with engine.begin() as conn:
        await conn.run_sync(Base.metadata.create_all)
    yield
    await engine.dispose()


app = FastAPI(title="Drem Orchestrator", lifespan=lifespan)

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


# --- Stub routers ---

tasks_router = APIRouter(prefix="/api/tasks", tags=["tasks"])
agents_router = APIRouter(prefix="/api/agents", tags=["agents"])
projects_router = APIRouter(prefix="/api/projects", tags=["projects"])

app.include_router(tasks_router)
app.include_router(agents_router)
app.include_router(projects_router)
