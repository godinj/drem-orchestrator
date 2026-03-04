"""Project management API endpoints."""

from __future__ import annotations

import uuid
from pathlib import Path

from fastapi import APIRouter, Depends, HTTPException, Request
from sqlalchemy import func, select
from sqlalchemy.ext.asyncio import AsyncSession
from sqlalchemy.orm import selectinload

from orchestrator.db import get_db
from orchestrator.enums import TaskStatus
from orchestrator.models import Agent, Project, Task
from orchestrator.schemas import AgentResponse, ProjectCreate, ProjectResponse, TaskResponse

router = APIRouter(prefix="/api/projects", tags=["projects"])


async def _build_project_response(
    project: Project, db: AsyncSession
) -> ProjectResponse:
    """Build a ProjectResponse with task_counts and agent_count."""
    # Count tasks by status
    task_count_stmt = (
        select(Task.status, func.count(Task.id))
        .where(Task.project_id == project.id)
        .group_by(Task.status)
    )
    task_count_result = await db.execute(task_count_stmt)
    task_counts: dict[str, int] = {}
    for status_val, count in task_count_result:
        task_counts[status_val] = count

    # Count agents
    agent_count_stmt = (
        select(func.count(Agent.id)).where(Agent.project_id == project.id)
    )
    agent_count_result = await db.execute(agent_count_stmt)
    agent_count = agent_count_result.scalar() or 0

    return ProjectResponse(
        id=project.id,
        name=project.name,
        bare_repo_path=project.bare_repo_path,
        default_branch=project.default_branch,
        description=project.description,
        task_counts=task_counts,
        agent_count=agent_count,
        created_at=project.created_at,
    )


@router.get("")
async def list_projects(
    db: AsyncSession = Depends(get_db),
) -> list[ProjectResponse]:
    """List all projects with task_counts and agent_count."""
    stmt = select(Project)
    result = await db.execute(stmt)
    projects = result.scalars().all()
    return [await _build_project_response(p, db) for p in projects]


@router.post("", status_code=201)
async def create_project(
    body: ProjectCreate,
    request: Request,
    db: AsyncSession = Depends(get_db),
) -> ProjectResponse:
    """Create a new project. Validates that bare_repo_path exists on disk."""
    repo_path = Path(body.bare_repo_path)
    if not repo_path.exists():
        raise HTTPException(
            status_code=400,
            detail=f"bare_repo_path does not exist: {body.bare_repo_path}",
        )

    project = Project(
        name=body.name,
        bare_repo_path=body.bare_repo_path,
        default_branch=body.default_branch,
        description=body.description,
    )
    db.add(project)
    await db.commit()
    await db.refresh(project)

    # Start orchestrator loop for the new project
    manager = getattr(request.app.state, "orchestrator_manager", None)
    if manager is not None:
        await manager.start_for_project(project.id, project.bare_repo_path)

    return await _build_project_response(project, db)


@router.get("/{project_id}")
async def get_project(
    project_id: uuid.UUID,
    db: AsyncSession = Depends(get_db),
) -> ProjectResponse:
    """Get a single project with detailed task_counts."""
    stmt = select(Project).where(Project.id == project_id)
    result = await db.execute(stmt)
    project = result.scalar_one_or_none()
    if project is None:
        raise HTTPException(status_code=404, detail="Project not found")

    return await _build_project_response(project, db)


@router.get("/{project_id}/board")
async def get_board(
    project_id: uuid.UUID,
    db: AsyncSession = Depends(get_db),
) -> dict[str, list[TaskResponse]]:
    """Return tasks grouped by status for kanban rendering.

    Only returns top-level tasks (parent_task_id is null).
    """
    # Verify project exists
    project_stmt = select(Project).where(Project.id == project_id)
    project_result = await db.execute(project_stmt)
    project = project_result.scalar_one_or_none()
    if project is None:
        raise HTTPException(status_code=404, detail="Project not found")

    stmt = (
        select(Task)
        .where(Task.project_id == project_id, Task.parent_task_id.is_(None))
        .options(selectinload(Task.subtasks))
    )
    result = await db.execute(stmt)
    tasks = result.scalars().all()

    # Group by status
    board: dict[str, list[TaskResponse]] = {}
    for status in TaskStatus:
        board[status.value] = []

    for task in tasks:
        subtask_count = len(task.subtasks) if task.subtasks else 0
        resp = TaskResponse(
            id=task.id,
            title=task.title,
            description=task.description,
            status=TaskStatus(task.status),
            priority=task.priority,
            labels=task.labels,
            dependency_ids=task.dependency_ids,
            assigned_agent_id=task.assigned_agent_id,
            plan=task.plan,
            test_plan=task.test_plan,
            worktree_branch=task.worktree_branch,
            pr_url=task.pr_url,
            context=task.context,
            parent_task_id=task.parent_task_id,
            subtask_count=subtask_count,
            created_at=task.created_at,
            updated_at=task.updated_at,
        )
        board[task.status].append(resp)

    return board


@router.get("/{project_id}/agents")
async def list_project_agents(
    project_id: uuid.UUID,
    db: AsyncSession = Depends(get_db),
) -> list[AgentResponse]:
    """List agents for a specific project."""
    stmt = select(Agent).where(Agent.project_id == project_id)
    result = await db.execute(stmt)
    agents = result.scalars().all()
    return [AgentResponse.model_validate(a) for a in agents]
