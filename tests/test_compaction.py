"""Tests for compaction.py — orchestrator state checkpoint/restore."""

from __future__ import annotations

import json
from datetime import UTC, datetime, timedelta

import pytest
from sqlalchemy import select, update
from sqlalchemy.ext.asyncio import (
    AsyncSession,
    async_sessionmaker,
    create_async_engine,
)

from orchestrator.compaction import OrchestratorCompaction, OrchestratorSnapshot
from orchestrator.memory import MemoryManager
from orchestrator.models import Agent, Base, Memory, Project, Task

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
def manager(session_factory):
    """Return a MemoryManager using the test session factory."""
    return MemoryManager(db_session_factory=session_factory)


@pytest.fixture
def compaction(manager):
    """Return an OrchestratorCompaction with default threshold."""
    return OrchestratorCompaction(memory_manager=manager, compaction_threshold=0.7)


@pytest.fixture
async def seed_data(session_factory):
    """Seed the database with a project, orchestrator agent, worker agent, and task.

    Returns a dict with keys: project, orchestrator, worker, task.
    """
    async with session_factory() as session:
        project = Project(
            name="test-project",
            bare_repo_path="/tmp/test.git",
            default_branch="main",
        )
        session.add(project)
        await session.flush()

        orchestrator = Agent(
            project_id=project.id,
            agent_type="orchestrator",
            name="orchestrator-main",
            status="working",
            heartbeat_at=datetime.now(UTC),
        )
        worker = Agent(
            project_id=project.id,
            agent_type="coder",
            name="worker-1",
            status="working",
            heartbeat_at=datetime.now(UTC),
        )
        session.add_all([orchestrator, worker])
        await session.flush()

        task = Task(
            project_id=project.id,
            title="Build auth module",
            description="Implement authentication",
            status="in_progress",
            assigned_agent_id=worker.id,
        )
        session.add(task)
        await session.commit()

        return {
            "project": project,
            "orchestrator": orchestrator,
            "worker": worker,
            "task": task,
        }


# ---------------------------------------------------------------------------
# Tests
# ---------------------------------------------------------------------------


class TestSaveAndRestoreState:
    async def test_save_and_restore_state(
        self, compaction, seed_data, session_factory
    ):
        """Save checkpoint, restore, verify snapshot."""
        orch = seed_data["orchestrator"]
        worker = seed_data["worker"]
        task = seed_data["task"]

        # Save state
        await compaction.save_orchestrator_state(orch.id)

        # Verify a memory was stored
        async with session_factory() as session:
            stmt = (
                select(Memory)
                .where(Memory.agent_id == orch.id)
                .where(Memory.memory_type == "orchestrator_state")
            )
            result = await session.execute(stmt)
            state_mem = result.scalar_one()
            state = json.loads(state_mem.content)

            assert len(state["assignments"]) == 1
            assert state["assignments"][0]["task_id"] == str(task.id)
            assert state["assignments"][0]["agent_id"] == str(worker.id)
            assert "checkpoint_at" in state

        # Restore state
        snapshot = await compaction.restore_orchestrator_state(orch.id)

        assert isinstance(snapshot, OrchestratorSnapshot)
        assert snapshot.last_checkpoint is not None
        # The worker agent should still be active (recent heartbeat)
        assert len(snapshot.active_agents) >= 1
        assert len(snapshot.active_tasks) == 1
        assert snapshot.active_tasks[0].id == task.id

    async def test_restore_with_pending_reviews(
        self, compaction, seed_data, session_factory
    ):
        """Restore should include pending review tasks."""
        orch = seed_data["orchestrator"]

        # Add a plan_review task
        async with session_factory() as session:
            review_task = Task(
                project_id=seed_data["project"].id,
                title="Review auth plan",
                description="Review the plan",
                status="plan_review",
            )
            session.add(review_task)
            await session.commit()
            review_id = review_task.id

        await compaction.save_orchestrator_state(orch.id)
        snapshot = await compaction.restore_orchestrator_state(orch.id)

        assert len(snapshot.pending_reviews) == 1
        assert snapshot.pending_reviews[0].id == review_id


class TestRestoreMarksStaleAgents:
    async def test_restore_marks_stale_agents(
        self, compaction, seed_data, session_factory
    ):
        """Old heartbeat should move agent to stale_agents list."""
        orch = seed_data["orchestrator"]
        worker = seed_data["worker"]

        # Make worker's heartbeat stale
        stale_time = datetime.now(UTC) - timedelta(seconds=300)
        async with session_factory() as session:
            await session.execute(
                update(Agent)
                .where(Agent.id == worker.id)
                .values(heartbeat_at=stale_time)
            )
            await session.commit()

        # Ensure orchestrator has fresh heartbeat so it isn't marked stale
        async with session_factory() as session:
            await session.execute(
                update(Agent)
                .where(Agent.id == orch.id)
                .values(heartbeat_at=datetime.now(UTC))
            )
            await session.commit()

        snapshot = await compaction.restore_orchestrator_state(orch.id)

        # Worker should be in stale list
        stale_ids = [a.id for a in snapshot.stale_agents]
        assert worker.id in stale_ids

        # Worker status should be updated to dead in the database
        async with session_factory() as session:
            result = await session.execute(
                select(Agent).where(Agent.id == worker.id)
            )
            refreshed = result.scalar_one()
            assert refreshed.status == "dead"

    async def test_fresh_agents_not_stale(
        self, compaction, seed_data, session_factory
    ):
        """Agents with fresh heartbeats should not be stale."""
        orch = seed_data["orchestrator"]
        worker = seed_data["worker"]

        # Ensure both have fresh heartbeats
        now = datetime.now(UTC)
        async with session_factory() as session:
            await session.execute(
                update(Agent)
                .where(Agent.id.in_([orch.id, worker.id]))
                .values(heartbeat_at=now)
            )
            await session.commit()

        snapshot = await compaction.restore_orchestrator_state(orch.id)

        assert len(snapshot.stale_agents) == 0
        active_ids = [a.id for a in snapshot.active_agents]
        assert worker.id in active_ids


class TestShouldCompact:
    async def test_should_compact_under_threshold(
        self, compaction, seed_data, manager
    ):
        """Few memories should not trigger compaction."""
        agent = seed_data["worker"]

        # Store just a few small memories
        for i in range(5):
            await manager.store_memory(
                agent_id=agent.id,
                content=f"Memory {i}",
                memory_type="decision",
            )

        result = await compaction.should_compact(agent.id)
        assert result is False

    async def test_should_compact_over_count_threshold(
        self, compaction, seed_data, manager
    ):
        """Many memories should trigger compaction."""
        agent = seed_data["worker"]

        # Store more than 50 * 0.7 = 35 memories
        for i in range(40):
            await manager.store_memory(
                agent_id=agent.id,
                content=f"Memory {i}",
                memory_type="decision",
            )

        result = await compaction.should_compact(agent.id)
        assert result is True

    async def test_should_compact_over_token_threshold(
        self, compaction, seed_data, manager
    ):
        """Large total content should trigger compaction (token-based)."""
        agent = seed_data["worker"]

        # Store a few very large memories
        # Threshold is 8000 * 0.7 = 5600 tokens = 22400 chars
        for i in range(5):
            await manager.store_memory(
                agent_id=agent.id,
                content="x" * 5000,
                memory_type="decision",
            )

        # 5 * 5000 = 25000 chars / 4 = 6250 tokens > 5600
        result = await compaction.should_compact(agent.id)
        assert result is True

    async def test_archived_memories_not_counted(
        self, compaction, seed_data, manager
    ):
        """Archived memories should not count toward compaction threshold."""
        agent = seed_data["worker"]

        # Store many memories then compact them
        for i in range(40):
            await manager.store_memory(
                agent_id=agent.id,
                content=f"Memory {i}",
                memory_type="decision",
            )

        await manager.compact_agent_memory(agent.id)

        # After compaction, only the summary should remain as non-archived
        result = await compaction.should_compact(agent.id)
        assert result is False
