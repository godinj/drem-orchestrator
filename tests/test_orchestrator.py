"""Tests for orchestrator.py — main orchestrator loop and task lifecycle."""

from __future__ import annotations

import uuid
from typing import Any
from unittest.mock import AsyncMock, MagicMock, patch

import pytest
from sqlalchemy.ext.asyncio import (
    AsyncSession,
    async_sessionmaker,
    create_async_engine,
)

from orchestrator.agent_runner import AgentRunner
from orchestrator.enums import AgentStatus, AgentType, TaskStatus
from orchestrator.models import Agent, Base, Project, Task
from orchestrator.orchestrator import Orchestrator
from orchestrator.state_machine import transition_task
from orchestrator.worktree import MergeResult, WorktreeInfo, WorktreeManager

TEST_DATABASE_URL = "sqlite+aiosqlite://"


@pytest.fixture
async def db_engine():
    """Create an in-memory database engine."""
    engine = create_async_engine(TEST_DATABASE_URL, echo=False)
    async with engine.begin() as conn:
        await conn.run_sync(Base.metadata.create_all)
    yield engine
    await engine.dispose()


@pytest.fixture
async def session_factory(db_engine):
    """Create a session factory for the test database."""
    factory = async_sessionmaker(
        db_engine, class_=AsyncSession, expire_on_commit=False
    )
    return factory


@pytest.fixture
async def session(session_factory):
    """Yield a session for setup/assertions."""
    async with session_factory() as s:
        yield s


@pytest.fixture
def mock_agent_runner():
    """Create a mock AgentRunner."""
    runner = MagicMock(spec=AgentRunner)
    runner.can_spawn = True
    runner.active_count = 0
    runner.spawn = AsyncMock()
    runner.stop = AsyncMock()
    runner.get_status = AsyncMock(return_value=AgentStatus.WORKING)
    runner.get_output = AsyncMock(return_value="Agent output")
    runner.cleanup_stale = AsyncMock(return_value=[])
    runner.list_running = AsyncMock(return_value=[])
    runner._processes = {}
    return runner


@pytest.fixture
def mock_worktree_manager():
    """Create a mock WorktreeManager."""
    manager = MagicMock(spec=WorktreeManager)
    manager.bare_repo = MagicMock()
    manager.get_default_branch = AsyncMock(return_value="main")
    manager.create_feature = AsyncMock(
        return_value=WorktreeInfo(
            path=MagicMock(),
            branch="feature/test",
            head="abc123",
            is_bare=False,
            agent_count=0,
            session_active=False,
        )
    )
    manager.remove_feature = AsyncMock()
    manager.create_agent_worktree = AsyncMock()
    manager.remove_agent_worktree = AsyncMock()
    manager.merge_branch = AsyncMock()
    manager.sync_all = AsyncMock(return_value=[])
    return manager


@pytest.fixture
def broadcast_messages():
    """Capture broadcast messages."""
    messages: list[dict[str, Any]] = []

    async def capture(msg: dict[str, Any]) -> None:
        messages.append(msg)

    return messages, capture


@pytest.fixture
def orchestrator(
    mock_agent_runner, mock_worktree_manager, session_factory, broadcast_messages
):
    """Create an Orchestrator with mocked dependencies."""
    _, broadcast_fn = broadcast_messages
    return Orchestrator(
        agent_runner=mock_agent_runner,
        worktree_manager=mock_worktree_manager,
        db_session_factory=session_factory,
        broadcast_fn=broadcast_fn,
    )


async def _create_project(session: AsyncSession) -> Project:
    """Create and return a test project."""
    project = Project(
        name="test-project",
        bare_repo_path="/tmp/test.git",
        default_branch="main",
    )
    session.add(project)
    await session.commit()
    await session.refresh(project)
    return project


async def _create_task(
    session: AsyncSession,
    project: Project,
    title: str = "Test Task",
    description: str = "A test task",
    status: str = TaskStatus.BACKLOG.value,
    parent_task_id: uuid.UUID | None = None,
    plan: dict | list | None = None,
    worktree_branch: str | None = None,
) -> Task:
    """Create and return a test task."""
    task = Task(
        project_id=project.id,
        title=title,
        description=description,
        status=status,
        parent_task_id=parent_task_id,
        plan=plan,
        worktree_branch=worktree_branch,
    )
    session.add(task)
    await session.commit()
    await session.refresh(task)
    return task


class TestBacklogToPlanning:
    """Test BACKLOG -> PLANNING transition."""

    async def test_backlog_to_planning(
        self, orchestrator: Orchestrator, session: AsyncSession
    ) -> None:
        """A top-level BACKLOG task should transition to PLANNING."""
        project = await _create_project(session)
        task = await _create_task(session, project, title="Build auth system")

        # Run one tick
        await orchestrator._tick()

        # Reload the task
        await session.refresh(task)
        assert task.status == TaskStatus.PLANNING.value


class TestPlanningToPlanReview:
    """Test PLANNING -> PLAN_REVIEW transition."""

    async def test_planning_to_plan_review(
        self,
        orchestrator: Orchestrator,
        session: AsyncSession,
        broadcast_messages: tuple,
    ) -> None:
        """A PLANNING task with a plan should transition to PLAN_REVIEW."""
        messages, _ = broadcast_messages
        project = await _create_project(session)

        plan = [
            {
                "title": "Create user model",
                "description": "Define User SQLAlchemy model",
                "agent_type": "coder",
                "estimated_files": ["src/models/user.py"],
            },
            {
                "title": "Add auth routes",
                "description": "Implement login/register endpoints",
                "agent_type": "coder",
                "estimated_files": ["src/routes/auth.py"],
            },
        ]
        task = await _create_task(
            session,
            project,
            title="Build auth",
            status=TaskStatus.PLANNING.value,
            plan=plan,
        )

        await orchestrator._tick()

        await session.refresh(task)
        assert task.status == TaskStatus.PLAN_REVIEW.value

        # Verify broadcast was sent
        assert len(messages) > 0
        assert messages[-1]["type"] == "plan_ready"
        assert messages[-1]["task_id"] == str(task.id)

    async def test_planning_without_plan_stays_in_planning(
        self, orchestrator: Orchestrator, session: AsyncSession
    ) -> None:
        """A PLANNING task without a plan should remain in PLANNING."""
        project = await _create_project(session)
        task = await _create_task(
            session,
            project,
            title="Waiting for planner",
            status=TaskStatus.PLANNING.value,
        )

        await orchestrator._tick()

        await session.refresh(task)
        assert task.status == TaskStatus.PLANNING.value


class TestPlanApproval:
    """Test plan approval creating subtasks."""

    async def test_plan_approval_creates_subtasks(
        self, orchestrator: Orchestrator, session: AsyncSession
    ) -> None:
        """Approving a plan should create subtasks and transition to IN_PROGRESS."""
        project = await _create_project(session)
        plan = [
            {
                "title": "Subtask 1",
                "description": "First subtask",
                "agent_type": "coder",
                "estimated_files": ["src/a.py"],
            },
            {
                "title": "Subtask 2",
                "description": "Second subtask",
                "agent_type": "researcher",
                "estimated_files": ["docs/b.md"],
            },
        ]
        task = await _create_task(
            session,
            project,
            title="Parent task",
            status=TaskStatus.PLAN_REVIEW.value,
            plan=plan,
        )

        await orchestrator._handle_plan_approved(session, task)
        await session.commit()

        # Task should be IN_PROGRESS
        await session.refresh(task)
        assert task.status == TaskStatus.IN_PROGRESS.value

        # Should have subtasks
        from sqlalchemy import select

        result = await session.execute(
            select(Task).where(Task.parent_task_id == task.id)
        )
        subtasks = list(result.scalars().all())
        assert len(subtasks) == 2
        assert all(st.status == TaskStatus.BACKLOG.value for st in subtasks)
        assert subtasks[0].title == "Subtask 1"
        assert subtasks[1].title == "Subtask 2"

    async def test_plan_approval_sets_worktree_branch(
        self,
        orchestrator: Orchestrator,
        session: AsyncSession,
        mock_worktree_manager: MagicMock,
    ) -> None:
        """Plan approval should create a feature worktree."""
        project = await _create_project(session)
        plan = [
            {
                "title": "Task A",
                "description": "Do A",
                "agent_type": "coder",
                "estimated_files": [],
            },
        ]
        task = await _create_task(
            session,
            project,
            title="Feature with worktree",
            status=TaskStatus.PLAN_REVIEW.value,
            plan=plan,
        )

        await orchestrator._handle_plan_approved(session, task)
        await session.commit()

        mock_worktree_manager.create_feature.assert_called_once()
        await session.refresh(task)
        assert task.worktree_branch == "feature/test"


class TestPlanRejection:
    """Test plan rejection returning to PLANNING."""

    async def test_plan_rejection_returns_to_planning(
        self, orchestrator: Orchestrator, session: AsyncSession
    ) -> None:
        """Rejecting a plan should transition back to PLANNING with feedback."""
        project = await _create_project(session)
        plan = [
            {
                "title": "Bad subtask",
                "description": "This is wrong",
                "agent_type": "coder",
                "estimated_files": [],
            },
        ]
        task = await _create_task(
            session,
            project,
            title="Rejected plan",
            status=TaskStatus.PLAN_REVIEW.value,
            plan=plan,
        )

        feedback = "The plan needs more granular subtasks"
        await orchestrator._handle_plan_rejected(session, task, feedback=feedback)
        await session.commit()

        await session.refresh(task)
        assert task.status == TaskStatus.PLANNING.value
        assert task.plan_feedback == feedback
        assert task.plan is None  # Plan cleared for replanning


class TestSubtaskCompletion:
    """Test subtask completion triggering TESTING_READY."""

    async def test_subtask_completion_triggers_testing_ready(
        self,
        orchestrator: Orchestrator,
        session: AsyncSession,
        broadcast_messages: tuple,
    ) -> None:
        """When all subtasks are DONE, parent should transition to TESTING_READY."""
        messages, _ = broadcast_messages
        project = await _create_project(session)
        parent = await _create_task(
            session,
            project,
            title="Parent feature",
            status=TaskStatus.IN_PROGRESS.value,
        )

        # Create subtasks that are all DONE
        for i in range(3):
            await _create_task(
                session,
                project,
                title=f"Subtask {i}",
                status=TaskStatus.DONE.value,
                parent_task_id=parent.id,
            )

        await orchestrator._check_feature_completion(session, parent)
        await session.commit()

        await session.refresh(parent)
        assert parent.status == TaskStatus.TESTING_READY.value
        assert parent.test_plan is not None
        assert "Parent feature" in parent.test_plan

        # Verify broadcast
        assert any(m["type"] == "testing_ready" for m in messages)

    async def test_incomplete_subtasks_keep_in_progress(
        self, orchestrator: Orchestrator, session: AsyncSession
    ) -> None:
        """If some subtasks are not DONE, parent stays IN_PROGRESS."""
        project = await _create_project(session)
        parent = await _create_task(
            session,
            project,
            title="Incomplete feature",
            status=TaskStatus.IN_PROGRESS.value,
        )

        await _create_task(
            session,
            project,
            title="Done subtask",
            status=TaskStatus.DONE.value,
            parent_task_id=parent.id,
        )
        await _create_task(
            session,
            project,
            title="Still working",
            status=TaskStatus.IN_PROGRESS.value,
            parent_task_id=parent.id,
        )

        await orchestrator._check_feature_completion(session, parent)

        await session.refresh(parent)
        assert parent.status == TaskStatus.IN_PROGRESS.value

    async def test_failed_subtask_fails_parent(
        self, orchestrator: Orchestrator, session: AsyncSession
    ) -> None:
        """If any subtask is FAILED, parent should transition to FAILED."""
        project = await _create_project(session)
        parent = await _create_task(
            session,
            project,
            title="Feature with failure",
            status=TaskStatus.IN_PROGRESS.value,
        )

        await _create_task(
            session,
            project,
            title="Done subtask",
            status=TaskStatus.DONE.value,
            parent_task_id=parent.id,
        )
        await _create_task(
            session,
            project,
            title="Failed subtask",
            status=TaskStatus.FAILED.value,
            parent_task_id=parent.id,
        )

        await orchestrator._check_feature_completion(session, parent)
        await session.commit()

        await session.refresh(parent)
        assert parent.status == TaskStatus.FAILED.value


class TestTestPassTriggersMerge:
    """Test that passing manual testing triggers merge."""

    async def test_test_pass_triggers_merge(
        self,
        orchestrator: Orchestrator,
        session: AsyncSession,
        mock_worktree_manager: MagicMock,
        broadcast_messages: tuple,
    ) -> None:
        """After test pass (MERGING state), successful merge transitions to DONE."""
        messages, _ = broadcast_messages
        project = await _create_project(session)
        task = await _create_task(
            session,
            project,
            title="Ready to merge",
            status=TaskStatus.MERGING.value,
            worktree_branch="feature/ready-to-merge",
        )

        # Mock successful merge
        mock_worktree_manager.merge_branch.return_value = MergeResult(
            success=True,
            source_branch="feature/ready-to-merge",
            target_branch="main",
            merge_commit="abc123def456",
        )

        await orchestrator._execute_merge(session, task)
        await session.commit()

        await session.refresh(task)
        assert task.status == TaskStatus.DONE.value

        mock_worktree_manager.merge_branch.assert_called_once()
        mock_worktree_manager.sync_all.assert_called_once()
        mock_worktree_manager.remove_feature.assert_called_once()

        assert any(m["type"] == "merge_complete" for m in messages)


class TestTestFailReturnsToInProgress:
    """Test that failing manual testing returns task to IN_PROGRESS."""

    async def test_test_fail_returns_to_in_progress(
        self, session: AsyncSession
    ) -> None:
        """A test failure should return the task to IN_PROGRESS with feedback.

        This is done via state machine transitions, not the orchestrator directly.
        The API endpoint handles this by calling transition_task.
        """
        project = await _create_project(session)
        task = await _create_task(
            session,
            project,
            title="Failed test",
            status=TaskStatus.MANUAL_TESTING.value,
        )

        feedback = "Login button doesn't work"
        task.test_feedback = feedback
        event = transition_task(task, TaskStatus.IN_PROGRESS, actor="human")
        session.add(event)
        await session.commit()

        await session.refresh(task)
        assert task.status == TaskStatus.IN_PROGRESS.value
        assert task.test_feedback == feedback


class TestMergeSuccess:
    """Test successful merge workflow."""

    async def test_merge_success(
        self,
        orchestrator: Orchestrator,
        session: AsyncSession,
        mock_worktree_manager: MagicMock,
    ) -> None:
        """Successful merge should clean up worktree and sync all."""
        project = await _create_project(session)
        task = await _create_task(
            session,
            project,
            title="Merge me",
            status=TaskStatus.MERGING.value,
            worktree_branch="feature/merge-me",
        )

        mock_worktree_manager.merge_branch.return_value = MergeResult(
            success=True,
            source_branch="feature/merge-me",
            target_branch="main",
            merge_commit="deadbeef",
        )

        await orchestrator._execute_merge(session, task)
        await session.commit()

        await session.refresh(task)
        assert task.status == TaskStatus.DONE.value

        # Verify cleanup
        mock_worktree_manager.remove_feature.assert_called_once_with("feature/merge-me")
        mock_worktree_manager.sync_all.assert_called_once()


class TestMergeConflict:
    """Test merge conflict handling."""

    async def test_merge_conflict(
        self,
        orchestrator: Orchestrator,
        session: AsyncSession,
        mock_worktree_manager: MagicMock,
        broadcast_messages: tuple,
    ) -> None:
        """Merge conflict should transition to FAILED and notify human."""
        messages, _ = broadcast_messages
        project = await _create_project(session)
        task = await _create_task(
            session,
            project,
            title="Conflicting merge",
            status=TaskStatus.MERGING.value,
            worktree_branch="feature/conflict",
        )

        mock_worktree_manager.merge_branch.return_value = MergeResult(
            success=False,
            source_branch="feature/conflict",
            target_branch="main",
            merge_commit=None,
            conflicts=["src/main.py", "README.md"],
        )

        await orchestrator._execute_merge(session, task)
        await session.commit()

        await session.refresh(task)
        assert task.status == TaskStatus.FAILED.value

        # Should not clean up or sync on failure
        mock_worktree_manager.remove_feature.assert_not_called()
        mock_worktree_manager.sync_all.assert_not_called()

        # Should notify human
        assert any(m["type"] == "merge_conflict" for m in messages)
        conflict_msg = next(m for m in messages if m["type"] == "merge_conflict")
        assert "src/main.py" in conflict_msg["conflicts"]


class TestHumanGatesRespected:
    """Verify orchestrator does not bypass human gates."""

    async def test_plan_review_not_auto_progressed(
        self, orchestrator: Orchestrator, session: AsyncSession
    ) -> None:
        """Tasks in PLAN_REVIEW should not be auto-progressed by the orchestrator."""
        project = await _create_project(session)
        task = await _create_task(
            session,
            project,
            title="Awaiting plan review",
            status=TaskStatus.PLAN_REVIEW.value,
            plan=[{"title": "ST", "description": "D", "agent_type": "coder",
                   "estimated_files": []}],
        )

        # Run several ticks
        for _ in range(3):
            await orchestrator._tick()

        await session.refresh(task)
        assert task.status == TaskStatus.PLAN_REVIEW.value

    async def test_testing_ready_not_auto_progressed(
        self, orchestrator: Orchestrator, session: AsyncSession
    ) -> None:
        """Tasks in TESTING_READY should not be auto-progressed."""
        project = await _create_project(session)
        task = await _create_task(
            session,
            project,
            title="Awaiting testing",
            status=TaskStatus.TESTING_READY.value,
        )

        for _ in range(3):
            await orchestrator._tick()

        await session.refresh(task)
        assert task.status == TaskStatus.TESTING_READY.value

    async def test_manual_testing_not_auto_progressed(
        self, orchestrator: Orchestrator, session: AsyncSession
    ) -> None:
        """Tasks in MANUAL_TESTING should not be auto-progressed."""
        project = await _create_project(session)
        task = await _create_task(
            session,
            project,
            title="Being tested",
            status=TaskStatus.MANUAL_TESTING.value,
        )

        for _ in range(3):
            await orchestrator._tick()

        await session.refresh(task)
        assert task.status == TaskStatus.MANUAL_TESTING.value


class TestStartStop:
    """Test orchestrator start/stop lifecycle."""

    async def test_stop_exits_loop(self, orchestrator: Orchestrator) -> None:
        """Calling stop() should make start() exit."""
        import asyncio

        # Set a very short poll interval
        orchestrator._poll_interval = 0.01

        async def stop_after_short_delay():
            await asyncio.sleep(0.05)
            await orchestrator.stop()

        # Run start and stop concurrently
        await asyncio.gather(orchestrator.start(), stop_after_short_delay())

        assert orchestrator._running is False
