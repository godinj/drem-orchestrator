"""Tests for ORM model creation and relationships."""

import uuid

import pytest
from sqlalchemy import select

from orchestrator.enums import AgentStatus, AgentType, TaskStatus
from orchestrator.models import Agent, Memory, Project, Task, TaskEvent
from orchestrator.state_machine import transition_task


@pytest.mark.asyncio
class TestProjectModel:
    async def test_create_project(self, db_session):
        project = Project(
            name="test-project",
            bare_repo_path="/home/user/git/test-project.git",
            default_branch="main",
            description="A test project",
        )
        db_session.add(project)
        await db_session.commit()

        result = await db_session.execute(select(Project).where(Project.name == "test-project"))
        fetched = result.scalar_one()
        assert fetched.name == "test-project"
        assert fetched.bare_repo_path == "/home/user/git/test-project.git"
        assert fetched.default_branch == "main"
        assert fetched.description == "A test project"
        assert fetched.id is not None
        assert fetched.created_at is not None
        assert fetched.updated_at is not None


@pytest.mark.asyncio
class TestTaskModel:
    async def test_create_task(self, db_session):
        project = Project(
            name="task-test-project",
            bare_repo_path="/tmp/repo.git",
        )
        db_session.add(project)
        await db_session.flush()

        task = Task(
            project_id=project.id,
            title="Implement feature X",
            description="Detailed description of feature X",
            status=TaskStatus.BACKLOG.value,
            priority=5,
            labels=["feature", "backend"],
        )
        db_session.add(task)
        await db_session.commit()

        result = await db_session.execute(select(Task).where(Task.title == "Implement feature X"))
        fetched = result.scalar_one()
        assert fetched.project_id == project.id
        assert fetched.status == TaskStatus.BACKLOG.value
        assert fetched.priority == 5
        assert fetched.labels == ["feature", "backend"]

    async def test_self_referential_parent_subtask(self, db_session):
        """Test Task self-referential parent/subtask relationship."""
        project = Project(
            name="subtask-test-project",
            bare_repo_path="/tmp/repo.git",
        )
        db_session.add(project)
        await db_session.flush()

        parent = Task(
            project_id=project.id,
            title="Parent task",
            description="This is the parent",
            status=TaskStatus.BACKLOG.value,
        )
        db_session.add(parent)
        await db_session.flush()

        child1 = Task(
            project_id=project.id,
            title="Subtask 1",
            description="First subtask",
            status=TaskStatus.BACKLOG.value,
            parent_task_id=parent.id,
        )
        child2 = Task(
            project_id=project.id,
            title="Subtask 2",
            description="Second subtask",
            status=TaskStatus.BACKLOG.value,
            parent_task_id=parent.id,
        )
        db_session.add_all([child1, child2])
        await db_session.commit()

        # Verify parent relationship
        result = await db_session.execute(
            select(Task).where(Task.parent_task_id == parent.id)
        )
        subtasks = result.scalars().all()
        assert len(subtasks) == 2
        titles = {t.title for t in subtasks}
        assert titles == {"Subtask 1", "Subtask 2"}

        # Verify children have correct parent
        for subtask in subtasks:
            assert subtask.parent_task_id == parent.id

    async def test_plan_submission_and_review_fields(self, db_session):
        """Test plan submission and review fields."""
        project = Project(
            name="plan-test-project",
            bare_repo_path="/tmp/repo.git",
        )
        db_session.add(project)
        await db_session.flush()

        plan_data = [
            {
                "title": "Implement API endpoint",
                "description": "Create the REST endpoint",
                "agent_type": "coder",
                "estimated_files": ["src/api.py", "src/routes.py"],
            },
            {
                "title": "Write tests",
                "description": "Unit and integration tests",
                "agent_type": "coder",
                "estimated_files": ["tests/test_api.py"],
            },
        ]

        task = Task(
            project_id=project.id,
            title="Feature with plan",
            description="A task with a plan",
            status=TaskStatus.PLAN_REVIEW.value,
            plan=plan_data,
            test_plan="1. Start server\n2. Call endpoint\n3. Check response",
        )
        db_session.add(task)
        await db_session.commit()

        result = await db_session.execute(
            select(Task).where(Task.title == "Feature with plan")
        )
        fetched = result.scalar_one()
        assert fetched.plan == plan_data
        assert len(fetched.plan) == 2
        assert fetched.plan[0]["title"] == "Implement API endpoint"
        assert fetched.test_plan.startswith("1. Start server")
        assert fetched.plan_feedback is None

        # Simulate plan rejection with feedback
        fetched.plan_feedback = "Need more detail on error handling"
        fetched.status = TaskStatus.PLANNING.value
        await db_session.commit()

        result = await db_session.execute(
            select(Task).where(Task.title == "Feature with plan")
        )
        updated = result.scalar_one()
        assert updated.plan_feedback == "Need more detail on error handling"
        assert updated.status == TaskStatus.PLANNING.value


@pytest.mark.asyncio
class TestAgentModel:
    async def test_create_agent(self, db_session):
        project = Project(
            name="agent-test-project",
            bare_repo_path="/tmp/repo.git",
        )
        db_session.add(project)
        await db_session.flush()

        agent = Agent(
            project_id=project.id,
            agent_type=AgentType.CODER.value,
            name="coder-agent-1",
            status=AgentStatus.IDLE.value,
            config={"max_retries": 3},
        )
        db_session.add(agent)
        await db_session.commit()

        result = await db_session.execute(
            select(Agent).where(Agent.name == "coder-agent-1")
        )
        fetched = result.scalar_one()
        assert fetched.agent_type == AgentType.CODER.value
        assert fetched.status == AgentStatus.IDLE.value
        assert fetched.config == {"max_retries": 3}
        assert fetched.worktree_path is None
        assert fetched.heartbeat_at is None


@pytest.mark.asyncio
class TestMemoryModel:
    async def test_create_memory(self, db_session):
        project = Project(
            name="memory-test-project",
            bare_repo_path="/tmp/repo.git",
        )
        db_session.add(project)
        await db_session.flush()

        agent = Agent(
            project_id=project.id,
            agent_type=AgentType.CODER.value,
            name="coder-memory-test",
            status=AgentStatus.IDLE.value,
        )
        db_session.add(agent)
        await db_session.flush()

        task = Task(
            project_id=project.id,
            title="Memory test task",
            description="Task for memory test",
            status=TaskStatus.IN_PROGRESS.value,
        )
        db_session.add(task)
        await db_session.flush()

        memory = Memory(
            agent_id=agent.id,
            task_id=task.id,
            content="Discovered that the API requires auth headers",
            memory_type="decision",
            metadata_={"related_files": ["src/auth.py"]},
        )
        db_session.add(memory)
        await db_session.commit()

        result = await db_session.execute(
            select(Memory).where(Memory.agent_id == agent.id)
        )
        fetched = result.scalar_one()
        assert fetched.content == "Discovered that the API requires auth headers"
        assert fetched.memory_type == "decision"
        assert fetched.metadata_ == {"related_files": ["src/auth.py"]}
        assert fetched.task_id == task.id


@pytest.mark.asyncio
class TestTaskEventViaTransition:
    async def test_transition_task_creates_event(self, db_session):
        """Test TaskEvent creation via transition_task()."""
        project = Project(
            name="event-test-project",
            bare_repo_path="/tmp/repo.git",
        )
        db_session.add(project)
        await db_session.flush()

        task = Task(
            project_id=project.id,
            title="Event test task",
            description="Task for event testing",
            status=TaskStatus.BACKLOG.value,
        )
        db_session.add(task)
        await db_session.flush()

        # Transition from BACKLOG -> PLANNING
        event = transition_task(task, TaskStatus.PLANNING, actor="orchestrator")
        db_session.add(event)
        await db_session.commit()

        # Verify the event was persisted
        result = await db_session.execute(
            select(TaskEvent).where(TaskEvent.task_id == task.id)
        )
        fetched = result.scalar_one()
        assert fetched.event_type == "status_change"
        assert fetched.old_value == TaskStatus.BACKLOG.value
        assert fetched.new_value == TaskStatus.PLANNING.value
        assert fetched.actor == "orchestrator"

        # Verify the task status was updated
        assert task.status == TaskStatus.PLANNING.value

    async def test_multiple_transitions_create_events(self, db_session):
        """Multiple transitions produce multiple events."""
        project = Project(
            name="multi-event-project",
            bare_repo_path="/tmp/repo.git",
        )
        db_session.add(project)
        await db_session.flush()

        task = Task(
            project_id=project.id,
            title="Multi event task",
            description="Task with many transitions",
            status=TaskStatus.BACKLOG.value,
        )
        db_session.add(task)
        await db_session.flush()

        # Walk through: BACKLOG -> PLANNING -> PLAN_REVIEW -> IN_PROGRESS
        transitions = [
            (TaskStatus.PLANNING, "orchestrator"),
            (TaskStatus.PLAN_REVIEW, "orchestrator"),
            (TaskStatus.IN_PROGRESS, "human"),
        ]
        for target, actor in transitions:
            event = transition_task(task, target, actor=actor)
            db_session.add(event)

        await db_session.commit()

        result = await db_session.execute(
            select(TaskEvent).where(TaskEvent.task_id == task.id)
        )
        events = result.scalars().all()
        assert len(events) == 3
        assert events[0].old_value == TaskStatus.BACKLOG.value
        assert events[0].new_value == TaskStatus.PLANNING.value
        assert events[1].old_value == TaskStatus.PLANNING.value
        assert events[1].new_value == TaskStatus.PLAN_REVIEW.value
        assert events[2].old_value == TaskStatus.PLAN_REVIEW.value
        assert events[2].new_value == TaskStatus.IN_PROGRESS.value
