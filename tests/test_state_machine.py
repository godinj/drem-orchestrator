"""Tests for the task state machine."""

import uuid

import pytest

from orchestrator.enums import TaskStatus
from orchestrator.models import Task
from orchestrator.state_machine import (
    HUMAN_GATES,
    VALID_TRANSITIONS,
    get_available_transitions,
    is_human_gate,
    transition_task,
    validate_transition,
)


class TestValidateTransition:
    """Test all valid transitions succeed."""

    @pytest.mark.parametrize(
        "current,target",
        [
            (current, target)
            for current, targets in VALID_TRANSITIONS.items()
            for target in targets
        ],
    )
    def test_valid_transitions_succeed(self, current: TaskStatus, target: TaskStatus):
        assert validate_transition(current, target) is True

    @pytest.mark.parametrize(
        "current,target",
        [
            (TaskStatus.BACKLOG, TaskStatus.IN_PROGRESS),
            (TaskStatus.BACKLOG, TaskStatus.DONE),
            (TaskStatus.PLANNING, TaskStatus.IN_PROGRESS),
            (TaskStatus.IN_PROGRESS, TaskStatus.BACKLOG),
            (TaskStatus.DONE, TaskStatus.BACKLOG),
            (TaskStatus.DONE, TaskStatus.IN_PROGRESS),
            (TaskStatus.MERGING, TaskStatus.BACKLOG),
            (TaskStatus.DONE, TaskStatus.PAUSED),
            (TaskStatus.MERGING, TaskStatus.PAUSED),
            (TaskStatus.PLAN_REVIEW, TaskStatus.PAUSED),
        ],
    )
    def test_invalid_transitions_return_false(self, current: TaskStatus, target: TaskStatus):
        assert validate_transition(current, target) is False


class TestGetAvailableTransitions:
    def test_backlog_transitions(self):
        transitions = get_available_transitions(TaskStatus.BACKLOG)
        assert TaskStatus.PLANNING in transitions
        assert TaskStatus.PAUSED in transitions

    def test_plan_review_transitions(self):
        transitions = get_available_transitions(TaskStatus.PLAN_REVIEW)
        assert TaskStatus.IN_PROGRESS in transitions
        assert TaskStatus.PLANNING in transitions

    def test_manual_testing_transitions(self):
        transitions = get_available_transitions(TaskStatus.MANUAL_TESTING)
        assert TaskStatus.MERGING in transitions
        assert TaskStatus.IN_PROGRESS in transitions

    def test_done_has_no_transitions(self):
        assert get_available_transitions(TaskStatus.DONE) == []

    def test_failed_can_retry(self):
        assert get_available_transitions(TaskStatus.FAILED) == [TaskStatus.BACKLOG]


class TestHumanGates:
    def test_plan_review_is_human_gate(self):
        assert is_human_gate(TaskStatus.PLAN_REVIEW) is True

    def test_manual_testing_is_human_gate(self):
        assert is_human_gate(TaskStatus.MANUAL_TESTING) is True

    def test_backlog_is_not_human_gate(self):
        assert is_human_gate(TaskStatus.BACKLOG) is False

    def test_in_progress_is_not_human_gate(self):
        assert is_human_gate(TaskStatus.IN_PROGRESS) is False

    def test_done_is_not_human_gate(self):
        assert is_human_gate(TaskStatus.DONE) is False

    def test_paused_is_not_human_gate(self):
        assert is_human_gate(TaskStatus.PAUSED) is False

    def test_human_gates_set_contents(self):
        assert HUMAN_GATES == {TaskStatus.PLAN_REVIEW, TaskStatus.MANUAL_TESTING}


class TestTransitionTask:
    def _make_task(self, status: str) -> Task:
        """Create a Task instance with the given status for testing."""
        task = Task(
            id=uuid.uuid4(),
            project_id=uuid.uuid4(),
            title="Test task",
            description="Test description",
            status=status,
        )
        return task

    def test_plan_review_to_planning_rejected(self):
        """Plan rejected: PLAN_REVIEW -> PLANNING."""
        task = self._make_task(TaskStatus.PLAN_REVIEW.value)
        event = transition_task(task, TaskStatus.PLANNING, actor="human", details={"reason": "needs more detail"})
        assert task.status == TaskStatus.PLANNING.value
        assert event.event_type == "status_change"
        assert event.old_value == TaskStatus.PLAN_REVIEW.value
        assert event.new_value == TaskStatus.PLANNING.value
        assert event.actor == "human"

    def test_plan_review_to_in_progress_approved(self):
        """Plan approved: PLAN_REVIEW -> IN_PROGRESS."""
        task = self._make_task(TaskStatus.PLAN_REVIEW.value)
        event = transition_task(task, TaskStatus.IN_PROGRESS, actor="human")
        assert task.status == TaskStatus.IN_PROGRESS.value
        assert event.old_value == TaskStatus.PLAN_REVIEW.value
        assert event.new_value == TaskStatus.IN_PROGRESS.value

    def test_manual_testing_to_in_progress_failed(self):
        """Test failed: MANUAL_TESTING -> IN_PROGRESS."""
        task = self._make_task(TaskStatus.MANUAL_TESTING.value)
        event = transition_task(
            task, TaskStatus.IN_PROGRESS, actor="human", details={"feedback": "button broken"}
        )
        assert task.status == TaskStatus.IN_PROGRESS.value
        assert event.new_value == TaskStatus.IN_PROGRESS.value

    def test_manual_testing_to_merging_passed(self):
        """Test passed: MANUAL_TESTING -> MERGING."""
        task = self._make_task(TaskStatus.MANUAL_TESTING.value)
        event = transition_task(task, TaskStatus.MERGING, actor="human")
        assert task.status == TaskStatus.MERGING.value
        assert event.new_value == TaskStatus.MERGING.value

    def test_failed_to_backlog_retry(self):
        """Retry: FAILED -> BACKLOG."""
        task = self._make_task(TaskStatus.FAILED.value)
        event = transition_task(task, TaskStatus.BACKLOG, actor="orchestrator")
        assert task.status == TaskStatus.BACKLOG.value
        assert event.new_value == TaskStatus.BACKLOG.value

    def test_invalid_transition_raises_value_error(self):
        """Invalid transition raises ValueError."""
        task = self._make_task(TaskStatus.BACKLOG.value)
        with pytest.raises(ValueError, match="Invalid transition"):
            transition_task(task, TaskStatus.DONE, actor="orchestrator")

    def test_invalid_transition_does_not_change_status(self):
        """Task status should not change on invalid transition."""
        task = self._make_task(TaskStatus.BACKLOG.value)
        try:
            transition_task(task, TaskStatus.DONE, actor="orchestrator")
        except ValueError:
            pass
        assert task.status == TaskStatus.BACKLOG.value

    def test_transition_event_has_details(self):
        """Event should carry details dict."""
        task = self._make_task(TaskStatus.BACKLOG.value)
        details = {"triggered_by": "scheduler"}
        event = transition_task(task, TaskStatus.PLANNING, actor="orchestrator", details=details)
        assert event.details == details

    def test_full_happy_path(self):
        """Walk through the entire happy-path lifecycle."""
        task = self._make_task(TaskStatus.BACKLOG.value)
        happy_path = [
            (TaskStatus.PLANNING, "orchestrator"),
            (TaskStatus.PLAN_REVIEW, "orchestrator"),
            (TaskStatus.IN_PROGRESS, "human"),
            (TaskStatus.TESTING_READY, "coder-agent"),
            (TaskStatus.MANUAL_TESTING, "orchestrator"),
            (TaskStatus.MERGING, "human"),
            (TaskStatus.DONE, "orchestrator"),
        ]
        for target, actor in happy_path:
            event = transition_task(task, target, actor=actor)
            assert task.status == target.value
            assert event.new_value == target.value
