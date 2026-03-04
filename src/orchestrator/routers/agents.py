"""Agent management API endpoints."""

from __future__ import annotations

import uuid
from datetime import datetime, timezone

from fastapi import APIRouter, Depends, HTTPException
from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession

from orchestrator.db import get_db
from orchestrator.enums import AgentStatus
from orchestrator.models import Agent
from orchestrator.routers.ws import broadcast
from orchestrator.schemas import AgentResponse

router = APIRouter(prefix="/api/agents", tags=["agents"])


@router.get("")
async def list_agents(
    project_id: uuid.UUID | None = None,
    status: AgentStatus | None = None,
    db: AsyncSession = Depends(get_db),
) -> list[AgentResponse]:
    """List agents with optional filters."""
    stmt = select(Agent)
    if project_id is not None:
        stmt = stmt.where(Agent.project_id == project_id)
    if status is not None:
        stmt = stmt.where(Agent.status == status.value)

    result = await db.execute(stmt)
    agents = result.scalars().all()
    return [AgentResponse.model_validate(a) for a in agents]


@router.get("/{agent_id}")
async def get_agent(
    agent_id: uuid.UUID,
    db: AsyncSession = Depends(get_db),
) -> AgentResponse:
    """Get a single agent including current task info."""
    stmt = select(Agent).where(Agent.id == agent_id)
    result = await db.execute(stmt)
    agent = result.scalar_one_or_none()
    if agent is None:
        raise HTTPException(status_code=404, detail="Agent not found")
    return AgentResponse.model_validate(agent)


@router.post("/{agent_id}/heartbeat")
async def heartbeat(
    agent_id: uuid.UUID,
    db: AsyncSession = Depends(get_db),
) -> dict[str, bool]:
    """Update agent heartbeat timestamp. Used by agent_runner."""
    stmt = select(Agent).where(Agent.id == agent_id)
    result = await db.execute(stmt)
    agent = result.scalar_one_or_none()
    if agent is None:
        raise HTTPException(status_code=404, detail="Agent not found")

    agent.heartbeat_at = datetime.now(timezone.utc)
    await db.commit()

    await broadcast(
        agent.project_id,
        {"type": "agent_updated", "agent": AgentResponse.model_validate(agent).model_dump(mode="json")},
    )
    return {"ok": True}
