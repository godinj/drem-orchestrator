"""Task state machine with transition validation."""

from __future__ import annotations

from typing import TYPE_CHECKING

from orchestrator.enums import TaskStatus
from orchestrator.models import TaskEvent

if TYPE_CHECKING:
    from orchestrator.models import Task

VALID_TRANSITIONS: dict[TaskStatus, list[TaskStatus]] = {
    TaskStatus.BACKLOG: [TaskStatus.PLANNING, TaskStatus.PAUSED],
    TaskStatus.PLANNING: [TaskStatus.PLAN_REVIEW, TaskStatus.FAILED, TaskStatus.PAUSED],
    TaskStatus.PLAN_REVIEW: [TaskStatus.IN_PROGRESS, TaskStatus.PLANNING],  # approve or reject
    TaskStatus.IN_PROGRESS: [TaskStatus.TESTING_READY, TaskStatus.FAILED, TaskStatus.PAUSED],
    TaskStatus.TESTING_READY: [TaskStatus.MANUAL_TESTING],
    TaskStatus.MANUAL_TESTING: [TaskStatus.MERGING, TaskStatus.IN_PROGRESS],  # pass or fail
    TaskStatus.MERGING: [TaskStatus.DONE, TaskStatus.FAILED],
    TaskStatus.PAUSED: [TaskStatus.BACKLOG, TaskStatus.PLANNING, TaskStatus.IN_PROGRESS],  # resume
    TaskStatus.DONE: [],
    TaskStatus.FAILED: [TaskStatus.BACKLOG],  # retry
}

HUMAN_GATES: set[TaskStatus] = {TaskStatus.PLAN_REVIEW, TaskStatus.MANUAL_TESTING}


def validate_transition(current: TaskStatus, target: TaskStatus) -> bool:
    """Check whether transitioning from current to target status is valid."""
    return target in VALID_TRANSITIONS.get(current, [])


def get_available_transitions(current: TaskStatus) -> list[TaskStatus]:
    """Return the list of valid target statuses from the current status."""
    return list(VALID_TRANSITIONS.get(current, []))


def is_human_gate(status: TaskStatus) -> bool:
    """Return True if the given status is a human gate requiring manual approval."""
    return status in HUMAN_GATES


def transition_task(
    task: Task,
    target: TaskStatus,
    actor: str,
    details: dict | None = None,
) -> TaskEvent:
    """Validate transition, update task status, create and return a TaskEvent.

    Raises ValueError if the transition is not valid.
    """
    current = TaskStatus(task.status)
    if not validate_transition(current, target):
        raise ValueError(
            f"Invalid transition from {current.value!r} to {target.value!r}. "
            f"Valid targets: {[t.value for t in get_available_transitions(current)]}"
        )

    old_status = task.status
    task.status = target.value

    event = TaskEvent(
        task_id=task.id,
        event_type="status_change",
        old_value=old_status,
        new_value=target.value,
        details=details,
        actor=actor,
    )
    return event
