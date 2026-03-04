"""Task state machine.

Stub module — will be fleshed out by the orchestrator-core agent.
Provides the transition_task function needed by merge orchestration.
"""

from __future__ import annotations

from orchestrator.enums import TaskStatus
from orchestrator.models import Task

# Valid transitions: (from_status, to_status)
_VALID_TRANSITIONS: set[tuple[TaskStatus, TaskStatus]] = {
    (TaskStatus.PENDING, TaskStatus.ASSIGNED),
    (TaskStatus.ASSIGNED, TaskStatus.RUNNING),
    (TaskStatus.RUNNING, TaskStatus.MERGING),
    (TaskStatus.RUNNING, TaskStatus.FAILED),
    (TaskStatus.MERGING, TaskStatus.DONE),
    (TaskStatus.MERGING, TaskStatus.FAILED),
    (TaskStatus.FAILED, TaskStatus.PENDING),
    (TaskStatus.BLOCKED, TaskStatus.PENDING),
}


class InvalidTransitionError(Exception):
    """Raised when a task status transition is not allowed."""


def transition_task(task: Task, new_status: TaskStatus) -> None:
    """Transition a task to a new status.

    Raises InvalidTransition if the transition is not allowed.
    Mutates the task in place.
    """
    if (task.status, new_status) not in _VALID_TRANSITIONS:
        raise InvalidTransitionError(
            f"Cannot transition from {task.status} to {new_status}"
        )
    task.status = new_status
