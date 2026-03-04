"""Tests for scheduler.py — task scheduling and agent assignment logic."""

from __future__ import annotations

import uuid
from unittest.mock import AsyncMock, MagicMock

import pytest
from sqlalchemy.ext.asyncio import (
    AsyncSession,
    async_sessionmaker,
    create_async_engine,
)

from orchestrator.agent_runner import AgentRunner
from orchestrator.enums import AgentStatus, AgentType, TaskStatus
from orchestrator.models import Agent, Base, Project, Task
from orchestrator.scheduler import Scheduler

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
    return async_sessionmaker(
        db_engine, class_=AsyncSession, expire_on_commit=False
    )


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
    runner.get_output = AsyncMock(return_value="")
    runner.cleanup_stale = AsyncMock(return_value=[])
    runner._processes = {}
    return runner


@pytest.fixture
def scheduler(mock_agent_runner, session_factory):
    """Create a Scheduler with mocked dependencies."""
    return Scheduler(
        agent_runner=mock_agent_runner,
        db_session_factory=session_factory,
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
    dependency_ids: list | None = None,
    assigned_agent_id: uuid.UUID | None = None,
    context: dict | None = None,
) -> Task:
    """Create and return a test task."""
    task = Task(
        project_id=project.id,
        title=title,
        description=description,
        status=status,
        parent_task_id=parent_task_id,
        dependency_ids=dependency_ids,
        assigned_agent_id=assigned_agent_id,
        context=context,
    )
    session.add(task)
    await session.commit()
    await session.refresh(task)
    return task


async def _create_agent(
    session: AsyncSession,
    project: Project,
    agent_type: str = AgentType.CODER.value,
    status: str = AgentStatus.IDLE.value,
    name: str | None = None,
) -> Agent:
    """Create and return a test agent."""
    agent = Agent(
        project_id=project.id,
        agent_type=agent_type,
        name=name or f"agent-{uuid.uuid4().hex[:8]}",
        status=status,
    )
    session.add(agent)
    await session.commit()
    await session.refresh(agent)
    return agent


class TestAssignableTasks:
    """Test get_assignable_tasks respects dependencies."""

    async def test_assignable_tasks_respects_dependencies(
        self, scheduler: Scheduler, session: AsyncSession
    ) -> None:
        """Tasks with unmet dependencies should not be returned as assignable."""
        project = await _create_project(session)

        # Create parent task (IN_PROGRESS)
        parent = await _create_task(
            session,
            project,
            title="Parent",
            status=TaskStatus.IN_PROGRESS.value,
        )

        # Create a dependency task (not done yet)
        dep_task = await _create_task(
            session,
            project,
            title="Dependency",
            status=TaskStatus.IN_PROGRESS.value,
            parent_task_id=parent.id,
        )

        # Create a task that depends on dep_task
        blocked_task = await _create_task(
            session,
            project,
            title="Blocked task",
            status=TaskStatus.BACKLOG.value,
            parent_task_id=parent.id,
            dependency_ids=[str(dep_task.id)],
        )

        assignable = await scheduler.get_assignable_tasks(session, project.id)
        task_ids = [t.id for t in assignable]
        assert blocked_task.id not in task_ids

    async def test_assignable_tasks_includes_ready_tasks(
        self, scheduler: Scheduler, session: AsyncSession
    ) -> None:
        """Tasks with no dependencies and BACKLOG status should be assignable."""
        project = await _create_project(session)

        parent = await _create_task(
            session,
            project,
            title="Parent",
            status=TaskStatus.IN_PROGRESS.value,
        )

        ready_task = await _create_task(
            session,
            project,
            title="Ready task",
            status=TaskStatus.BACKLOG.value,
            parent_task_id=parent.id,
        )

        assignable = await scheduler.get_assignable_tasks(session, project.id)
        task_ids = [t.id for t in assignable]
        assert ready_task.id in task_ids

    async def test_assignable_tasks_met_dependencies(
        self, scheduler: Scheduler, session: AsyncSession
    ) -> None:
        """Tasks whose dependencies are all DONE should be assignable."""
        project = await _create_project(session)

        parent = await _create_task(
            session,
            project,
            title="Parent",
            status=TaskStatus.IN_PROGRESS.value,
        )

        dep_task = await _create_task(
            session,
            project,
            title="Done dep",
            status=TaskStatus.DONE.value,
            parent_task_id=parent.id,
        )

        dependent_task = await _create_task(
            session,
            project,
            title="Unblocked",
            status=TaskStatus.BACKLOG.value,
            parent_task_id=parent.id,
            dependency_ids=[str(dep_task.id)],
        )

        assignable = await scheduler.get_assignable_tasks(session, project.id)
        task_ids = [t.id for t in assignable]
        assert dependent_task.id in task_ids

    async def test_assigned_tasks_excluded(
        self, scheduler: Scheduler, session: AsyncSession
    ) -> None:
        """Tasks already assigned to an agent should not be returned."""
        project = await _create_project(session)
        agent = await _create_agent(session, project)

        parent = await _create_task(
            session,
            project,
            title="Parent",
            status=TaskStatus.IN_PROGRESS.value,
        )

        assigned_task = await _create_task(
            session,
            project,
            title="Assigned",
            status=TaskStatus.BACKLOG.value,
            parent_task_id=parent.id,
            assigned_agent_id=agent.id,
        )

        assignable = await scheduler.get_assignable_tasks(session, project.id)
        task_ids = [t.id for t in assignable]
        assert assigned_task.id not in task_ids

    async def test_top_level_backlog_not_assignable(
        self, scheduler: Scheduler, session: AsyncSession
    ) -> None:
        """Top-level BACKLOG tasks (no parent) should not be assignable.

        They go through PLANNING first.
        """
        project = await _create_project(session)
        top_task = await _create_task(
            session, project, title="Top level", status=TaskStatus.BACKLOG.value
        )

        assignable = await scheduler.get_assignable_tasks(session, project.id)
        task_ids = [t.id for t in assignable]
        assert top_task.id not in task_ids


class TestAssignTask:
    """Test assign_task reuses idle agents or spawns new ones."""

    async def test_assign_reuses_idle_agent(
        self,
        scheduler: Scheduler,
        session: AsyncSession,
        mock_agent_runner: MagicMock,
    ) -> None:
        """An idle agent of matching type should be reused."""
        project = await _create_project(session)
        idle_agent = await _create_agent(
            session, project, agent_type=AgentType.CODER.value, status=AgentStatus.IDLE.value
        )

        parent = await _create_task(
            session, project, title="Parent", status=TaskStatus.IN_PROGRESS.value
        )
        task = await _create_task(
            session,
            project,
            title="Code task",
            status=TaskStatus.BACKLOG.value,
            parent_task_id=parent.id,
            context={"agent_type": "coder"},
        )

        agent = await scheduler.assign_task(session, task, "feature/test")
        await session.commit()

        assert agent.id == idle_agent.id
        assert agent.status == AgentStatus.WORKING.value
        assert agent.current_task_id == task.id

        await session.refresh(task)
        assert task.assigned_agent_id == idle_agent.id

        mock_agent_runner.spawn.assert_called_once()

    async def test_assign_spawns_new_agent(
        self,
        scheduler: Scheduler,
        session: AsyncSession,
        mock_agent_runner: MagicMock,
    ) -> None:
        """When no idle agents exist, a new agent should be spawned."""
        project = await _create_project(session)

        parent = await _create_task(
            session, project, title="Parent", status=TaskStatus.IN_PROGRESS.value
        )
        task = await _create_task(
            session,
            project,
            title="Code task",
            status=TaskStatus.BACKLOG.value,
            parent_task_id=parent.id,
            context={"agent_type": "coder"},
        )

        agent = await scheduler.assign_task(session, task, "feature/test")
        await session.commit()

        # A new agent should have been created
        assert agent.agent_type == AgentType.CODER.value
        assert agent.status == AgentStatus.WORKING.value
        assert agent.current_task_id == task.id

        mock_agent_runner.spawn.assert_called_once()

    async def test_assign_respects_max_concurrent(
        self,
        scheduler: Scheduler,
        session: AsyncSession,
        mock_agent_runner: MagicMock,
    ) -> None:
        """Should raise RuntimeError when max concurrent agents reached."""
        mock_agent_runner.can_spawn = False

        project = await _create_project(session)
        parent = await _create_task(
            session, project, title="Parent", status=TaskStatus.IN_PROGRESS.value
        )
        task = await _create_task(
            session,
            project,
            title="Task",
            status=TaskStatus.BACKLOG.value,
            parent_task_id=parent.id,
        )

        with pytest.raises(RuntimeError, match="max concurrent"):
            await scheduler.assign_task(session, task, "feature/test")

    async def test_assign_matches_agent_type(
        self,
        scheduler: Scheduler,
        session: AsyncSession,
    ) -> None:
        """Should pick an idle agent matching the requested type."""
        project = await _create_project(session)

        # Create agents of different types
        coder = await _create_agent(
            session, project, agent_type=AgentType.CODER.value, name="coder-1"
        )
        researcher = await _create_agent(
            session, project, agent_type=AgentType.RESEARCHER.value, name="researcher-1"
        )

        parent = await _create_task(
            session, project, title="Parent", status=TaskStatus.IN_PROGRESS.value
        )
        task = await _create_task(
            session,
            project,
            title="Research task",
            status=TaskStatus.BACKLOG.value,
            parent_task_id=parent.id,
            context={"agent_type": "researcher"},
        )

        agent = await scheduler.assign_task(session, task, "feature/test")
        assert agent.id == researcher.id


class TestScheduleSummary:
    """Test get_schedule_summary."""

    async def test_schedule_summary(
        self, scheduler: Scheduler, session: AsyncSession
    ) -> None:
        """Schedule summary should report correct counts."""
        project = await _create_project(session)

        # Create some tasks in various statuses
        parent = await _create_task(
            session, project, title="Parent", status=TaskStatus.IN_PROGRESS.value
        )
        await _create_task(
            session, project, title="Backlog 1",
            status=TaskStatus.BACKLOG.value, parent_task_id=parent.id
        )
        await _create_task(
            session, project, title="Backlog 2",
            status=TaskStatus.BACKLOG.value, parent_task_id=parent.id
        )
        await _create_task(
            session, project, title="Done task",
            status=TaskStatus.DONE.value, parent_task_id=parent.id
        )

        # Create agents
        await _create_agent(
            session, project, status=AgentStatus.IDLE.value, name="idle-1"
        )
        await _create_agent(
            session, project, status=AgentStatus.WORKING.value, name="working-1"
        )

        summary = await scheduler.get_schedule_summary(session, project.id)

        assert summary.tasks_by_status[TaskStatus.IN_PROGRESS] == 1
        assert summary.tasks_by_status[TaskStatus.BACKLOG] == 2
        assert summary.tasks_by_status[TaskStatus.DONE] == 1
        assert summary.agents_by_status[AgentStatus.IDLE] == 1
        assert summary.agents_by_status[AgentStatus.WORKING] == 1
        assert summary.queue_depth == 2  # Two backlog subtasks with met deps

    async def test_schedule_summary_blocked_tasks(
        self, scheduler: Scheduler, session: AsyncSession
    ) -> None:
        """Schedule summary should report blocked tasks."""
        project = await _create_project(session)

        parent = await _create_task(
            session, project, title="Parent", status=TaskStatus.IN_PROGRESS.value
        )
        dep = await _create_task(
            session, project, title="Dep",
            status=TaskStatus.IN_PROGRESS.value, parent_task_id=parent.id
        )
        blocked = await _create_task(
            session, project, title="Blocked",
            status=TaskStatus.BACKLOG.value,
            parent_task_id=parent.id,
            dependency_ids=[str(dep.id)],
        )

        summary = await scheduler.get_schedule_summary(session, project.id)

        assert len(summary.blocked_tasks) == 1
        blocked_id, blocking_ids = summary.blocked_tasks[0]
        assert blocked_id == blocked.id
        assert dep.id in blocking_ids

    async def test_schedule_summary_empty_project(
        self, scheduler: Scheduler, session: AsyncSession
    ) -> None:
        """Schedule summary for empty project should have zero counts."""
        project = await _create_project(session)

        summary = await scheduler.get_schedule_summary(session, project.id)

        assert summary.tasks_by_status == {}
        assert summary.agents_by_status == {}
        assert summary.blocked_tasks == []
        assert summary.queue_depth == 0
