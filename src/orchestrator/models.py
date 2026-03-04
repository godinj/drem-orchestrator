"""SQLAlchemy async ORM models using DeclarativeBase."""

from __future__ import annotations

import uuid
from datetime import datetime, timezone
from typing import Optional

from sqlalchemy import JSON, DateTime, Enum, ForeignKey, Integer, String, Text, Uuid
from sqlalchemy.orm import DeclarativeBase, Mapped, mapped_column, relationship


def _utcnow() -> datetime:
    return datetime.now(timezone.utc)


def _new_uuid() -> uuid.UUID:
    return uuid.uuid4()


class Base(DeclarativeBase):
    pass


class Project(Base):
    __tablename__ = "projects"

    id: Mapped[uuid.UUID] = mapped_column(Uuid, primary_key=True, default=_new_uuid)
    name: Mapped[str] = mapped_column(String(255), unique=True, nullable=False)
    bare_repo_path: Mapped[str] = mapped_column(String(1024), nullable=False)
    default_branch: Mapped[str] = mapped_column(String(255), nullable=False, default="master")
    description: Mapped[Optional[str]] = mapped_column(Text, nullable=True)
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), default=_utcnow)
    updated_at: Mapped[datetime] = mapped_column(
        DateTime(timezone=True), default=_utcnow, onupdate=_utcnow
    )

    # Relationships
    tasks: Mapped[list[Task]] = relationship("Task", back_populates="project")
    agents: Mapped[list[Agent]] = relationship("Agent", back_populates="project")


class Task(Base):
    __tablename__ = "tasks"

    id: Mapped[uuid.UUID] = mapped_column(Uuid, primary_key=True, default=_new_uuid)
    project_id: Mapped[uuid.UUID] = mapped_column(
        Uuid, ForeignKey("projects.id"), nullable=False, index=True
    )
    parent_task_id: Mapped[Optional[uuid.UUID]] = mapped_column(
        Uuid, ForeignKey("tasks.id"), nullable=True, index=True
    )
    title: Mapped[str] = mapped_column(String(500), nullable=False)
    description: Mapped[str] = mapped_column(Text, nullable=False)
    status: Mapped[str] = mapped_column(
        Enum(
            "backlog",
            "planning",
            "plan_review",
            "in_progress",
            "paused",
            "testing_ready",
            "manual_testing",
            "merging",
            "done",
            "failed",
            name="taskstatus",
        ),
        nullable=False,
        default="backlog",
        index=True,
    )
    priority: Mapped[int] = mapped_column(Integer, nullable=False, default=0)
    labels: Mapped[Optional[list]] = mapped_column(JSON, nullable=True, default=list)
    dependency_ids: Mapped[Optional[list]] = mapped_column(JSON, nullable=True, default=list)
    assigned_agent_id: Mapped[Optional[uuid.UUID]] = mapped_column(
        Uuid, ForeignKey("agents.id"), nullable=True, index=True
    )
    plan: Mapped[Optional[dict]] = mapped_column(JSON, nullable=True)
    plan_feedback: Mapped[Optional[str]] = mapped_column(Text, nullable=True)
    test_plan: Mapped[Optional[str]] = mapped_column(Text, nullable=True)
    test_feedback: Mapped[Optional[str]] = mapped_column(Text, nullable=True)
    worktree_branch: Mapped[Optional[str]] = mapped_column(String(255), nullable=True)
    pr_url: Mapped[Optional[str]] = mapped_column(String(1024), nullable=True)
    context: Mapped[Optional[dict]] = mapped_column(JSON, nullable=True, default=dict)
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), default=_utcnow)
    updated_at: Mapped[datetime] = mapped_column(
        DateTime(timezone=True), default=_utcnow, onupdate=_utcnow
    )

    # Relationships
    project: Mapped[Project] = relationship("Project", back_populates="tasks")
    parent_task: Mapped[Optional[Task]] = relationship(
        "Task", back_populates="subtasks", remote_side="Task.id"
    )
    subtasks: Mapped[list[Task]] = relationship("Task", back_populates="parent_task")
    assigned_agent: Mapped[Optional[Agent]] = relationship(
        "Agent", back_populates="assigned_tasks", foreign_keys=[assigned_agent_id]
    )
    events: Mapped[list[TaskEvent]] = relationship("TaskEvent", back_populates="task")


class Agent(Base):
    __tablename__ = "agents"

    id: Mapped[uuid.UUID] = mapped_column(Uuid, primary_key=True, default=_new_uuid)
    project_id: Mapped[uuid.UUID] = mapped_column(
        Uuid, ForeignKey("projects.id"), nullable=False, index=True
    )
    agent_type: Mapped[str] = mapped_column(
        Enum("orchestrator", "planner", "coder", "researcher", name="agenttype"),
        nullable=False,
    )
    name: Mapped[str] = mapped_column(String(255), nullable=False)
    status: Mapped[str] = mapped_column(
        Enum("idle", "working", "blocked", "dead", name="agentstatus"),
        nullable=False,
        default="idle",
        index=True,
    )
    current_task_id: Mapped[Optional[uuid.UUID]] = mapped_column(
        Uuid, ForeignKey("tasks.id"), nullable=True
    )
    worktree_path: Mapped[Optional[str]] = mapped_column(String(1024), nullable=True)
    worktree_branch: Mapped[Optional[str]] = mapped_column(String(255), nullable=True)
    memory_summary: Mapped[Optional[str]] = mapped_column(Text, nullable=True)
    heartbeat_at: Mapped[Optional[datetime]] = mapped_column(
        DateTime(timezone=True), nullable=True
    )
    config: Mapped[Optional[dict]] = mapped_column(JSON, nullable=True, default=dict)
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), default=_utcnow)
    updated_at: Mapped[datetime] = mapped_column(
        DateTime(timezone=True), default=_utcnow, onupdate=_utcnow
    )

    # Relationships
    project: Mapped[Project] = relationship("Project", back_populates="agents")
    current_task: Mapped[Optional[Task]] = relationship(
        "Task", foreign_keys=[current_task_id]
    )
    assigned_tasks: Mapped[list[Task]] = relationship(
        "Task", back_populates="assigned_agent", foreign_keys=[Task.assigned_agent_id]
    )
    memories: Mapped[list[Memory]] = relationship("Memory", back_populates="agent")


class TaskEvent(Base):
    __tablename__ = "task_events"

    id: Mapped[uuid.UUID] = mapped_column(Uuid, primary_key=True, default=_new_uuid)
    task_id: Mapped[uuid.UUID] = mapped_column(
        Uuid, ForeignKey("tasks.id"), nullable=False, index=True
    )
    event_type: Mapped[str] = mapped_column(String(100), nullable=False)
    old_value: Mapped[Optional[str]] = mapped_column(String(255), nullable=True)
    new_value: Mapped[Optional[str]] = mapped_column(String(255), nullable=True)
    details: Mapped[Optional[dict]] = mapped_column(JSON, nullable=True)
    actor: Mapped[str] = mapped_column(String(255), nullable=False)
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), default=_utcnow)

    # Relationships
    task: Mapped[Task] = relationship("Task", back_populates="events")


class Memory(Base):
    __tablename__ = "memories"

    id: Mapped[uuid.UUID] = mapped_column(Uuid, primary_key=True, default=_new_uuid)
    agent_id: Mapped[uuid.UUID] = mapped_column(
        Uuid, ForeignKey("agents.id"), nullable=False, index=True
    )
    task_id: Mapped[Optional[uuid.UUID]] = mapped_column(
        Uuid, ForeignKey("tasks.id"), nullable=True
    )
    content: Mapped[str] = mapped_column(Text, nullable=False)
    memory_type: Mapped[str] = mapped_column(String(100), nullable=False)
    metadata_: Mapped[Optional[dict]] = mapped_column("metadata", JSON, nullable=True)
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), default=_utcnow)

    # Relationships
    agent: Mapped[Agent] = relationship("Agent", back_populates="memories")
    task: Mapped[Optional[Task]] = relationship("Task")
