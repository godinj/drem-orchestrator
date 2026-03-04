"""Pydantic v2 schemas for API request/response."""

from __future__ import annotations

import uuid
from datetime import datetime

from pydantic import BaseModel, ConfigDict, Field

from orchestrator.enums import AgentStatus, AgentType, TaskStatus


# --- Task schemas ---


class TaskCreate(BaseModel):
    title: str
    description: str
    project_id: uuid.UUID
    priority: int = 0
    labels: list[str] = Field(default_factory=list)
    parent_task_id: uuid.UUID | None = None


class TaskUpdate(BaseModel):
    title: str | None = None
    description: str | None = None
    priority: int | None = None
    labels: list[str] | None = None


class TaskResponse(BaseModel):
    model_config = ConfigDict(from_attributes=True)

    id: uuid.UUID
    title: str
    description: str
    status: TaskStatus
    priority: int
    labels: list[str] | None
    dependency_ids: list[str] | None
    assigned_agent_id: uuid.UUID | None
    plan: dict | list | None
    test_plan: str | None
    worktree_branch: str | None
    pr_url: str | None
    context: dict | None
    parent_task_id: uuid.UUID | None
    subtask_count: int = 0
    created_at: datetime
    updated_at: datetime


class TaskTransition(BaseModel):
    """For status changes; feedback used for plan rejection or test failure."""

    target_status: TaskStatus
    feedback: str | None = None


class SubtaskPlan(BaseModel):
    title: str
    description: str
    agent_type: AgentType
    estimated_files: list[str]


class PlanSubmission(BaseModel):
    """Orchestrator submits a decomposition plan."""

    plan: list[SubtaskPlan]


class PlanReview(BaseModel):
    """Human approves or rejects plan."""

    approved: bool
    feedback: str | None = None


class TestResult(BaseModel):
    """Human pass/fail for manual testing."""

    passed: bool
    feedback: str | None = None


# --- Agent schemas ---


class AgentResponse(BaseModel):
    model_config = ConfigDict(from_attributes=True)

    id: uuid.UUID
    name: str
    agent_type: AgentType
    status: AgentStatus
    current_task_id: uuid.UUID | None
    worktree_path: str | None
    worktree_branch: str | None
    heartbeat_at: datetime | None
    created_at: datetime


# --- Project schemas ---


class ProjectCreate(BaseModel):
    name: str
    bare_repo_path: str
    default_branch: str = "master"
    description: str | None = None


class ProjectResponse(BaseModel):
    model_config = ConfigDict(from_attributes=True)

    id: uuid.UUID
    name: str
    bare_repo_path: str
    default_branch: str
    description: str | None
    task_counts: dict[str, int] = Field(default_factory=dict)
    agent_count: int = 0
    created_at: datetime


# --- Event schemas ---


class TaskEventResponse(BaseModel):
    model_config = ConfigDict(from_attributes=True)

    id: uuid.UUID
    task_id: uuid.UUID
    event_type: str
    old_value: str | None
    new_value: str | None
    details: dict | None
    actor: str
    created_at: datetime
