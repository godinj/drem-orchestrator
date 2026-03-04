"""Data models for the orchestrator.

Stub module — will be fleshed out by the data-model agent.
Provides minimal Task and Agent classes needed by merge orchestration.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from datetime import datetime
from pathlib import Path
from uuid import UUID, uuid4

from orchestrator.enums import AgentStatus, TaskStatus


@dataclass
class Agent:
    """Represents a Claude Code agent working on a subtask."""

    id: UUID = field(default_factory=uuid4)
    worktree_branch: str = ""
    worktree_path: Path | None = None
    status: AgentStatus = AgentStatus.IDLE
    completed_at: datetime | None = None


@dataclass
class Task:
    """Represents a task (feature or subtask) in the orchestrator."""

    id: UUID = field(default_factory=uuid4)
    project_id: UUID = field(default_factory=uuid4)
    title: str = ""
    status: TaskStatus = TaskStatus.PENDING
    feature_branch: str = ""
    parent_id: UUID | None = None
    subtasks: list[Task] = field(default_factory=list)
    agent: Agent | None = None
    completed_at: datetime | None = None
