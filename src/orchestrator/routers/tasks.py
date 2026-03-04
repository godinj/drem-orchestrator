"""Task management API endpoints."""

from __future__ import annotations

import uuid

from fastapi import APIRouter, Depends, HTTPException
from sqlalchemy import func, select
from sqlalchemy.ext.asyncio import AsyncSession
from sqlalchemy.orm import selectinload

from orchestrator.db import get_db
from orchestrator.enums import TaskStatus
from orchestrator.models import Task, TaskEvent
from orchestrator.routers.ws import broadcast
from orchestrator.schemas import (
    PlanReview,
    PlanSubmission,
    TaskCreate,
    TaskEventResponse,
    TaskResponse,
    TaskTransition,
    TaskUpdate,
    TestResult,
)
from orchestrator.state_machine import transition_task, validate_transition

router = APIRouter(prefix="/api/tasks", tags=["tasks"])


def _task_response(task: Task) -> TaskResponse:
    """Build a TaskResponse from a Task ORM model."""
    subtask_count = len(task.subtasks) if task.subtasks else 0
    return TaskResponse(
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


@router.get("")
async def list_tasks(
    project_id: uuid.UUID,
    status: TaskStatus | None = None,
    parent_task_id: uuid.UUID | None = None,
    db: AsyncSession = Depends(get_db),
) -> list[TaskResponse]:
    """List tasks filtered by project, optional status, and optional parent.

    If parent_task_id is not provided, returns all tasks for the project.
    Pass parent_task_id explicitly if you want to filter by parent (use the
    query parameter with a UUID, or omit it for all tasks).
    """
    stmt = (
        select(Task)
        .where(Task.project_id == project_id)
        .options(selectinload(Task.subtasks))
    )
    if status is not None:
        stmt = stmt.where(Task.status == status.value)
    if parent_task_id is not None:
        stmt = stmt.where(Task.parent_task_id == parent_task_id)

    result = await db.execute(stmt)
    tasks = result.scalars().all()
    return [_task_response(t) for t in tasks]


@router.post("", status_code=201)
async def create_task(
    body: TaskCreate,
    db: AsyncSession = Depends(get_db),
) -> TaskResponse:
    """Create a new task in BACKLOG status."""
    task = Task(
        project_id=body.project_id,
        parent_task_id=body.parent_task_id,
        title=body.title,
        description=body.description,
        status=TaskStatus.BACKLOG.value,
        priority=body.priority,
        labels=body.labels,
    )
    db.add(task)
    await db.commit()
    await db.refresh(task)

    # Eagerly load subtasks for the response
    stmt = (
        select(Task)
        .where(Task.id == task.id)
        .options(selectinload(Task.subtasks))
    )
    result = await db.execute(stmt)
    task = result.scalar_one()

    resp = _task_response(task)
    await broadcast(task.project_id, {"type": "task_created", "task": resp.model_dump(mode="json")})
    return resp


@router.get("/{task_id}")
async def get_task(
    task_id: uuid.UUID,
    db: AsyncSession = Depends(get_db),
) -> TaskResponse:
    """Get a single task including its full subtask list."""
    stmt = (
        select(Task)
        .where(Task.id == task_id)
        .options(selectinload(Task.subtasks))
    )
    result = await db.execute(stmt)
    task = result.scalar_one_or_none()
    if task is None:
        raise HTTPException(status_code=404, detail="Task not found")
    return _task_response(task)


@router.patch("/{task_id}")
async def update_task(
    task_id: uuid.UUID,
    body: TaskUpdate,
    db: AsyncSession = Depends(get_db),
) -> TaskResponse:
    """Update task title, description, priority, or labels."""
    stmt = (
        select(Task)
        .where(Task.id == task_id)
        .options(selectinload(Task.subtasks))
    )
    result = await db.execute(stmt)
    task = result.scalar_one_or_none()
    if task is None:
        raise HTTPException(status_code=404, detail="Task not found")

    if body.title is not None:
        task.title = body.title
    if body.description is not None:
        task.description = body.description
    if body.priority is not None:
        task.priority = body.priority
    if body.labels is not None:
        task.labels = body.labels

    await db.commit()
    await db.refresh(task)

    # Reload with subtasks
    stmt = (
        select(Task)
        .where(Task.id == task.id)
        .options(selectinload(Task.subtasks))
    )
    result = await db.execute(stmt)
    task = result.scalar_one()

    resp = _task_response(task)
    await broadcast(task.project_id, {"type": "task_updated", "task": resp.model_dump(mode="json")})
    return resp


@router.post("/{task_id}/transition")
async def transition_task_endpoint(
    task_id: uuid.UUID,
    body: TaskTransition,
    db: AsyncSession = Depends(get_db),
) -> TaskResponse:
    """Transition a task to a new status.

    Validates via state_machine.validate_transition().
    Creates a TaskEvent and emits a WebSocket event.
    """
    stmt = (
        select(Task)
        .where(Task.id == task_id)
        .options(selectinload(Task.subtasks))
    )
    result = await db.execute(stmt)
    task = result.scalar_one_or_none()
    if task is None:
        raise HTTPException(status_code=404, detail="Task not found")

    current = TaskStatus(task.status)
    target = body.target_status

    if not validate_transition(current, target):
        raise HTTPException(
            status_code=400,
            detail=f"Invalid transition from {current.value!r} to {target.value!r}",
        )

    # Handle specific transition side effects
    details: dict | None = None
    if body.feedback:
        details = {"feedback": body.feedback}

    # Plan review transitions
    if current == TaskStatus.PLAN_REVIEW and target == TaskStatus.PLANNING:
        # Plan was rejected, store feedback
        if body.feedback:
            task.plan_feedback = body.feedback

    # Manual testing transitions
    if current == TaskStatus.MANUAL_TESTING and target == TaskStatus.IN_PROGRESS:
        # Test failed, store feedback
        if body.feedback:
            task.test_feedback = body.feedback

    event = transition_task(task, target, actor="human", details=details)
    db.add(event)
    await db.commit()
    await db.refresh(task)

    # Reload with subtasks
    stmt = (
        select(Task)
        .where(Task.id == task.id)
        .options(selectinload(Task.subtasks))
    )
    result = await db.execute(stmt)
    task = result.scalar_one()

    resp = _task_response(task)
    await broadcast(task.project_id, {"type": "task_updated", "task": resp.model_dump(mode="json")})
    return resp


@router.post("/{task_id}/plan")
async def submit_plan(
    task_id: uuid.UUID,
    body: PlanSubmission,
    db: AsyncSession = Depends(get_db),
) -> TaskResponse:
    """Orchestrator submits its decomposition plan.

    Stores plan as JSON on the task and transitions to PLAN_REVIEW.
    """
    stmt = (
        select(Task)
        .where(Task.id == task_id)
        .options(selectinload(Task.subtasks))
    )
    result = await db.execute(stmt)
    task = result.scalar_one_or_none()
    if task is None:
        raise HTTPException(status_code=404, detail="Task not found")

    # Store the plan as JSON
    task.plan = [sp.model_dump() for sp in body.plan]

    # Transition to PLAN_REVIEW
    current = TaskStatus(task.status)
    target = TaskStatus.PLAN_REVIEW
    if not validate_transition(current, target):
        raise HTTPException(
            status_code=400,
            detail=f"Cannot submit plan: invalid transition from {current.value!r} to {target.value!r}",
        )

    event = transition_task(task, target, actor="orchestrator", details={"plan": task.plan})
    db.add(event)
    await db.commit()
    await db.refresh(task)

    # Reload with subtasks
    stmt = (
        select(Task)
        .where(Task.id == task.id)
        .options(selectinload(Task.subtasks))
    )
    result = await db.execute(stmt)
    task = result.scalar_one()

    resp = _task_response(task)
    await broadcast(
        task.project_id,
        {
            "type": "plan_submitted",
            "task_id": str(task.id),
            "plan": task.plan,
        },
    )
    await broadcast(task.project_id, {"type": "task_updated", "task": resp.model_dump(mode="json")})
    return resp


@router.post("/{task_id}/plan-review")
async def review_plan(
    task_id: uuid.UUID,
    body: PlanReview,
    db: AsyncSession = Depends(get_db),
) -> TaskResponse:
    """Human approves or rejects the plan.

    If approved: transitions to IN_PROGRESS.
    If rejected: transitions back to PLANNING, stores feedback in plan_feedback.
    """
    stmt = (
        select(Task)
        .where(Task.id == task_id)
        .options(selectinload(Task.subtasks))
    )
    result = await db.execute(stmt)
    task = result.scalar_one_or_none()
    if task is None:
        raise HTTPException(status_code=404, detail="Task not found")

    current = TaskStatus(task.status)
    if current != TaskStatus.PLAN_REVIEW:
        raise HTTPException(
            status_code=400,
            detail=f"Task is not in PLAN_REVIEW status (current: {current.value!r})",
        )

    if body.approved:
        target = TaskStatus.IN_PROGRESS
        event = transition_task(
            task, target, actor="human", details={"action": "plan_approved"}
        )
        # Create a plan_approved event
        plan_event = TaskEvent(
            task_id=task.id,
            event_type="plan_approved",
            old_value=None,
            new_value=None,
            details={"plan": task.plan},
            actor="human",
        )
        db.add(plan_event)
    else:
        target = TaskStatus.PLANNING
        task.plan_feedback = body.feedback
        event = transition_task(
            task, target, actor="human",
            details={"action": "plan_rejected", "feedback": body.feedback},
        )
        # Create a plan_rejected event
        plan_event = TaskEvent(
            task_id=task.id,
            event_type="plan_rejected",
            old_value=None,
            new_value=None,
            details={"feedback": body.feedback},
            actor="human",
        )
        db.add(plan_event)

    db.add(event)
    await db.commit()
    await db.refresh(task)

    # Reload with subtasks
    stmt = (
        select(Task)
        .where(Task.id == task.id)
        .options(selectinload(Task.subtasks))
    )
    result = await db.execute(stmt)
    task = result.scalar_one()

    resp = _task_response(task)
    await broadcast(task.project_id, {"type": "task_updated", "task": resp.model_dump(mode="json")})
    return resp


@router.post("/{task_id}/test-result")
async def submit_test_result(
    task_id: uuid.UUID,
    body: TestResult,
    db: AsyncSession = Depends(get_db),
) -> TaskResponse:
    """Human reports manual testing result.

    If passed: transitions to MERGING.
    If failed: transitions to IN_PROGRESS, stores feedback in test_feedback.
    """
    stmt = (
        select(Task)
        .where(Task.id == task_id)
        .options(selectinload(Task.subtasks))
    )
    result = await db.execute(stmt)
    task = result.scalar_one_or_none()
    if task is None:
        raise HTTPException(status_code=404, detail="Task not found")

    current = TaskStatus(task.status)
    if current != TaskStatus.MANUAL_TESTING:
        raise HTTPException(
            status_code=400,
            detail=f"Task is not in MANUAL_TESTING status (current: {current.value!r})",
        )

    if body.passed:
        target = TaskStatus.MERGING
        event = transition_task(
            task, target, actor="human", details={"action": "test_passed"}
        )
        test_event = TaskEvent(
            task_id=task.id,
            event_type="test_passed",
            old_value=None,
            new_value=None,
            details=None,
            actor="human",
        )
        db.add(test_event)
    else:
        target = TaskStatus.IN_PROGRESS
        task.test_feedback = body.feedback
        event = transition_task(
            task, target, actor="human",
            details={"action": "test_failed", "feedback": body.feedback},
        )
        test_event = TaskEvent(
            task_id=task.id,
            event_type="test_failed",
            old_value=None,
            new_value=None,
            details={"feedback": body.feedback},
            actor="human",
        )
        db.add(test_event)

    db.add(event)
    await db.commit()
    await db.refresh(task)

    # Reload with subtasks
    stmt = (
        select(Task)
        .where(Task.id == task.id)
        .options(selectinload(Task.subtasks))
    )
    result = await db.execute(stmt)
    task = result.scalar_one()

    resp = _task_response(task)
    event_type = "task_updated"
    ws_payload: dict = {"type": event_type, "task": resp.model_dump(mode="json")}
    if body.passed:
        # Also send a testing_ready-style notification
        pass
    await broadcast(task.project_id, ws_payload)
    return resp


PAUSABLE_STATUSES = {TaskStatus.BACKLOG, TaskStatus.PLANNING, TaskStatus.IN_PROGRESS}


@router.post("/{task_id}/pause")
async def pause_task(
    task_id: uuid.UUID,
    db: AsyncSession = Depends(get_db),
) -> TaskResponse:
    """Pause a task, stopping agent work and preventing scheduling.

    Saves the current status in context["paused_from_status"] so resume
    can restore it. Cascades to active subtasks.
    """
    stmt = (
        select(Task)
        .where(Task.id == task_id)
        .options(selectinload(Task.subtasks))
    )
    result = await db.execute(stmt)
    task = result.scalar_one_or_none()
    if task is None:
        raise HTTPException(status_code=404, detail="Task not found")

    current = TaskStatus(task.status)
    if current not in PAUSABLE_STATUSES:
        raise HTTPException(
            status_code=400,
            detail=f"Cannot pause task in {current.value!r} status. "
            f"Pausable statuses: {[s.value for s in PAUSABLE_STATUSES]}",
        )

    # Save current status for resume
    task.context = task.context or {}
    task.context["paused_from_status"] = current.value

    event = transition_task(task, TaskStatus.PAUSED, actor="human", details={"paused_from": current.value})
    db.add(event)

    # Cascade: pause active subtasks
    subtask_stmt = select(Task).where(
        Task.parent_task_id == task_id,
        Task.status.in_([s.value for s in PAUSABLE_STATUSES]),
    )
    subtask_result = await db.execute(subtask_stmt)
    for subtask in subtask_result.scalars().all():
        subtask.context = subtask.context or {}
        subtask.context["paused_from_status"] = subtask.status
        sub_event = transition_task(subtask, TaskStatus.PAUSED, actor="human", details={"paused_from": subtask.status, "cascade": True})
        db.add(sub_event)

    await db.commit()
    await db.refresh(task)

    # Reload with subtasks
    stmt = (
        select(Task)
        .where(Task.id == task.id)
        .options(selectinload(Task.subtasks))
    )
    result = await db.execute(stmt)
    task = result.scalar_one()

    resp = _task_response(task)
    await broadcast(task.project_id, {"type": "task_updated", "task": resp.model_dump(mode="json")})
    return resp


@router.post("/{task_id}/resume")
async def resume_task(
    task_id: uuid.UUID,
    db: AsyncSession = Depends(get_db),
) -> TaskResponse:
    """Resume a paused task, restoring it to its previous status.

    Reads context["paused_from_status"] for the target state (fallback: BACKLOG).
    Cascades to paused subtasks.
    """
    stmt = (
        select(Task)
        .where(Task.id == task_id)
        .options(selectinload(Task.subtasks))
    )
    result = await db.execute(stmt)
    task = result.scalar_one_or_none()
    if task is None:
        raise HTTPException(status_code=404, detail="Task not found")

    current = TaskStatus(task.status)
    if current != TaskStatus.PAUSED:
        raise HTTPException(
            status_code=400,
            detail=f"Cannot resume task in {current.value!r} status. Task must be paused.",
        )

    # Determine resume target
    paused_from = (task.context or {}).get("paused_from_status", TaskStatus.BACKLOG.value)
    target = TaskStatus(paused_from)

    event = transition_task(task, target, actor="human", details={"resumed_to": target.value})
    db.add(event)

    # Cascade: resume paused subtasks
    subtask_stmt = select(Task).where(
        Task.parent_task_id == task_id,
        Task.status == TaskStatus.PAUSED.value,
    )
    subtask_result = await db.execute(subtask_stmt)
    for subtask in subtask_result.scalars().all():
        sub_target_value = (subtask.context or {}).get("paused_from_status", TaskStatus.BACKLOG.value)
        sub_target = TaskStatus(sub_target_value)
        sub_event = transition_task(subtask, sub_target, actor="human", details={"resumed_to": sub_target.value, "cascade": True})
        db.add(sub_event)

    await db.commit()
    await db.refresh(task)

    # Reload with subtasks
    stmt = (
        select(Task)
        .where(Task.id == task.id)
        .options(selectinload(Task.subtasks))
    )
    result = await db.execute(stmt)
    task = result.scalar_one()

    resp = _task_response(task)
    await broadcast(task.project_id, {"type": "task_updated", "task": resp.model_dump(mode="json")})
    return resp


@router.get("/{task_id}/events")
async def list_task_events(
    task_id: uuid.UUID,
    db: AsyncSession = Depends(get_db),
) -> list[TaskEventResponse]:
    """Full audit trail for a task."""
    # Verify task exists
    task_stmt = select(Task).where(Task.id == task_id)
    task_result = await db.execute(task_stmt)
    task = task_result.scalar_one_or_none()
    if task is None:
        raise HTTPException(status_code=404, detail="Task not found")

    stmt = (
        select(TaskEvent)
        .where(TaskEvent.task_id == task_id)
        .order_by(TaskEvent.created_at)
    )
    result = await db.execute(stmt)
    events = result.scalars().all()
    return [TaskEventResponse.model_validate(e) for e in events]
