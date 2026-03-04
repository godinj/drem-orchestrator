"""Tests for memory.py — agent memory persistence and compaction."""

from __future__ import annotations

from datetime import UTC, datetime

import pytest
from sqlalchemy import select
from sqlalchemy.ext.asyncio import (
    AsyncSession,
    async_sessionmaker,
    create_async_engine,
)

from orchestrator.memory import MemoryManager
from orchestrator.models import Agent, Base, Project, Task

# ---------------------------------------------------------------------------
# Fixtures
# ---------------------------------------------------------------------------


@pytest.fixture
async def engine():
    """Create an async in-memory SQLite engine."""
    eng = create_async_engine("sqlite+aiosqlite://", echo=False)
    async with eng.begin() as conn:
        await conn.run_sync(Base.metadata.create_all)
    yield eng
    await eng.dispose()


@pytest.fixture
def session_factory(engine):
    """Return an async_sessionmaker bound to the test engine."""
    return async_sessionmaker(engine, class_=AsyncSession, expire_on_commit=False)


@pytest.fixture
async def seed_data(session_factory):
    """Seed the database with a project, two agents, and a task.

    Returns a dict with keys: project, agent_a, agent_b, task.
    """
    async with session_factory() as session:
        project = Project(
            name="test-project",
            bare_repo_path="/tmp/test.git",
            default_branch="main",
        )
        session.add(project)
        await session.flush()

        agent_a = Agent(
            project_id=project.id,
            agent_type="coder",
            name="agent-a",
            status="working",
            heartbeat_at=datetime.now(UTC),
        )
        agent_b = Agent(
            project_id=project.id,
            agent_type="coder",
            name="agent-b",
            status="working",
            heartbeat_at=datetime.now(UTC),
        )
        session.add_all([agent_a, agent_b])
        await session.flush()

        task = Task(
            project_id=project.id,
            title="Implement feature X",
            description="Build the feature",
            status="in_progress",
            assigned_agent_id=agent_a.id,
        )
        session.add(task)
        await session.commit()

        return {
            "project": project,
            "agent_a": agent_a,
            "agent_b": agent_b,
            "task": task,
        }


@pytest.fixture
def manager(session_factory):
    """Return a MemoryManager using the test session factory."""
    return MemoryManager(db_session_factory=session_factory)


# ---------------------------------------------------------------------------
# Tests
# ---------------------------------------------------------------------------


class TestStoreAndRetrieve:
    async def test_store_and_retrieve(self, manager, seed_data):
        """Store a memory and retrieve it by agent_id."""
        agent = seed_data["agent_a"]
        task = seed_data["task"]

        mem = await manager.store_memory(
            agent_id=agent.id,
            content="Decided to use async SQLAlchemy",
            memory_type="decision",
            task_id=task.id,
            metadata={"source": "test"},
        )

        assert mem.id is not None
        assert mem.agent_id == agent.id
        assert mem.content == "Decided to use async SQLAlchemy"
        assert mem.memory_type == "decision"
        assert mem.task_id == task.id
        assert mem.metadata_ == {"source": "test"}

        # Retrieve
        memories = await manager.get_memories(agent_id=agent.id)
        assert len(memories) == 1
        assert memories[0].id == mem.id


class TestFilterByType:
    async def test_filter_by_type(self, manager, seed_data):
        """Store multiple types, filter correctly."""
        agent = seed_data["agent_a"]

        await manager.store_memory(
            agent_id=agent.id,
            content="Used pathlib for paths",
            memory_type="decision",
        )
        await manager.store_memory(
            agent_id=agent.id,
            content="Created models.py",
            memory_type="file_change",
        )
        await manager.store_memory(
            agent_id=agent.id,
            content="Always use async session",
            memory_type="lesson",
        )

        decisions = await manager.get_memories(
            agent_id=agent.id, memory_type="decision"
        )
        assert len(decisions) == 1
        assert decisions[0].memory_type == "decision"

        file_changes = await manager.get_memories(
            agent_id=agent.id, memory_type="file_change"
        )
        assert len(file_changes) == 1
        assert file_changes[0].memory_type == "file_change"

        all_memories = await manager.get_memories(agent_id=agent.id)
        assert len(all_memories) == 3


class TestProjectMemories:
    async def test_project_memories(self, manager, seed_data):
        """Memories from multiple agents in same project are returned."""
        agent_a = seed_data["agent_a"]
        agent_b = seed_data["agent_b"]
        project = seed_data["project"]

        await manager.store_memory(
            agent_id=agent_a.id,
            content="Agent A decision",
            memory_type="decision",
        )
        await manager.store_memory(
            agent_id=agent_b.id,
            content="Agent B lesson",
            memory_type="lesson",
        )

        memories = await manager.get_project_memories(
            project_id=project.id,
        )
        assert len(memories) == 2

        # Filter by type
        decisions = await manager.get_project_memories(
            project_id=project.id,
            memory_types=["decision"],
        )
        assert len(decisions) == 1
        assert decisions[0].content == "Agent A decision"


class TestCompactAgentMemory:
    async def test_compact_agent_memory(self, manager, seed_data, session_factory):
        """Verify summary created and agent.memory_summary updated."""
        agent = seed_data["agent_a"]

        # Store some memories of various types
        await manager.store_memory(
            agent_id=agent.id,
            content="Use async everywhere",
            memory_type="decision",
        )
        await manager.store_memory(
            agent_id=agent.id,
            content="Created memory.py",
            memory_type="file_change",
        )
        await manager.store_memory(
            agent_id=agent.id,
            content="Always flush before reading IDs",
            memory_type="lesson",
        )

        summary = await manager.compact_agent_memory(agent.id)

        # Summary should contain the content
        assert "Use async everywhere" in summary
        assert "Created memory.py" in summary
        assert "Always flush before reading IDs" in summary
        assert "Key Decisions" in summary
        assert "Files Created/Modified" in summary
        assert "Lessons Learned" in summary

        # Agent.memory_summary should be updated
        async with session_factory() as session:
            result = await session.execute(
                select(Agent).where(Agent.id == agent.id)
            )
            refreshed_agent = result.scalar_one()
            assert refreshed_agent.memory_summary == summary

        # A conversation_summary memory should exist
        summaries = await manager.get_memories(
            agent_id=agent.id, memory_type="conversation_summary"
        )
        assert len(summaries) == 1
        assert summaries[0].content == summary

        # Original memories should be archived (not deleted)
        archived = await manager.get_memories(
            agent_id=agent.id, memory_type="archived_decision"
        )
        assert len(archived) == 1


class TestBuildAgentContext:
    async def test_build_agent_context(self, manager, seed_data, session_factory):
        """Verify context includes summary + recent memories."""
        agent_a = seed_data["agent_a"]
        agent_b = seed_data["agent_b"]
        task = seed_data["task"]

        # Set agent's memory summary
        async with session_factory() as session:
            await session.execute(
                select(Agent).where(Agent.id == agent_a.id)
            )
            from sqlalchemy import update

            await session.execute(
                update(Agent)
                .where(Agent.id == agent_a.id)
                .values(memory_summary="Previous session summary here.")
            )
            await session.commit()

        # Add task-specific memory
        await manager.store_memory(
            agent_id=agent_a.id,
            content="Working on auth module",
            memory_type="decision",
            task_id=task.id,
        )

        # Add project-wide memory from another agent
        await manager.store_memory(
            agent_id=agent_b.id,
            content="API uses REST not GraphQL",
            memory_type="decision",
        )

        context = await manager.build_agent_context(
            agent_id=agent_a.id,
            task_id=task.id,
        )

        assert "Previous session summary here." in context
        assert "Working on auth module" in context
        assert "API uses REST not GraphQL" in context

    async def test_context_truncation(self, manager, seed_data):
        """Verify max_tokens is respected."""
        agent = seed_data["agent_a"]
        task = seed_data["task"]

        # Store a large memory
        large_content = "x" * 10000
        await manager.store_memory(
            agent_id=agent.id,
            content=large_content,
            memory_type="decision",
            task_id=task.id,
        )

        # Build context with a small max_tokens
        context = await manager.build_agent_context(
            agent_id=agent.id,
            task_id=task.id,
            max_tokens=100,
        )

        # 100 tokens * 4 chars = 400 chars max
        assert len(context) <= 400


class TestExtractMemoriesFromOutput:
    async def test_extract_decisions(self, manager, seed_data):
        """Extract decision patterns from output."""
        agent = seed_data["agent_a"]
        task = seed_data["task"]

        output = """Working on the feature.
I decided to use async SQLAlchemy for the database layer.
Also chose Redis for the pub/sub system.
"""
        memories = await manager.extract_memories_from_output(
            agent_id=agent.id,
            task_id=task.id,
            output=output,
        )

        decision_memories = [m for m in memories if m.memory_type == "decision"]
        assert len(decision_memories) == 2

    async def test_extract_file_changes(self, manager, seed_data):
        """Extract file change patterns from output."""
        agent = seed_data["agent_a"]
        task = seed_data["task"]

        output = "Created file memory.py and modified models.py\n"
        memories = await manager.extract_memories_from_output(
            agent_id=agent.id,
            task_id=task.id,
            output=output,
        )

        file_memories = [m for m in memories if m.memory_type == "file_change"]
        assert len(file_memories) >= 1

    async def test_extract_blockers(self, manager, seed_data):
        """Extract blocker patterns from output."""
        agent = seed_data["agent_a"]
        task = seed_data["task"]

        output = "I'm blocked by the missing API schema.\n"
        memories = await manager.extract_memories_from_output(
            agent_id=agent.id,
            task_id=task.id,
            output=output,
        )

        blocker_memories = [m for m in memories if m.memory_type == "blocker"]
        assert len(blocker_memories) == 1

    async def test_extract_completions(self, manager, seed_data):
        """Extract completion patterns from output."""
        agent = seed_data["agent_a"]
        task = seed_data["task"]

        output = "Finished implementing the auth module.\n"
        memories = await manager.extract_memories_from_output(
            agent_id=agent.id,
            task_id=task.id,
            output=output,
        )

        completion_memories = [m for m in memories if m.memory_type == "completion"]
        assert len(completion_memories) == 1

    async def test_deduplication(self, manager, seed_data):
        """Duplicate patterns in output should not create duplicates."""
        agent = seed_data["agent_a"]
        task = seed_data["task"]

        output = """decided to use X
decided to use X
"""
        memories = await manager.extract_memories_from_output(
            agent_id=agent.id,
            task_id=task.id,
            output=output,
        )

        assert len(memories) == 1
