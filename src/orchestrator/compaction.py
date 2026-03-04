"""Orchestrator-level compaction and state checkpoint/restore.

Manages the orchestrator's own context window by periodically saving
and restoring state snapshots.  Also provides threshold-based compaction
decisions for individual agents.
"""

from __future__ import annotations

import json
import uuid
from dataclasses import dataclass, field
from datetime import UTC, datetime, timedelta

from sqlalchemy import func, select, update

from orchestrator.memory import MemoryManager
from orchestrator.models import Agent, Memory, Task


@dataclass
class OrchestratorSnapshot:
    """State snapshot used to resume the orchestrator after a restart."""

    active_tasks: list[Task] = field(default_factory=list)
    pending_reviews: list[Task] = field(default_factory=list)
    active_agents: list[Agent] = field(default_factory=list)
    stale_agents: list[Agent] = field(default_factory=list)
    last_checkpoint: datetime | None = None


class OrchestratorCompaction:
    """Manages orchestrator state checkpoints and compaction decisions."""

    # Heartbeats older than this are considered stale.
    STALE_HEARTBEAT_SECONDS = 120

    def __init__(
        self,
        memory_manager: MemoryManager,
        compaction_threshold: float = 0.7,
    ) -> None:
        self._memory = memory_manager
        self._compaction_threshold = compaction_threshold

    # ------------------------------------------------------------------
    # Save / Restore
    # ------------------------------------------------------------------

    async def save_orchestrator_state(
        self, orchestrator_agent_id: uuid.UUID
    ) -> None:
        """Periodic checkpoint of orchestrator state.

        Saves:
        - Current task assignments (which agent has which task)
        - Pending decisions and their context
        - Recent events summary
        - Known blockers and their status

        Stored as Memory records with type ``orchestrator_state``.
        """
        async with self._memory._session_factory() as session:
            # Gather task assignments
            stmt = (
                select(Task)
                .where(Task.status == "in_progress")
                .where(Task.assigned_agent_id.isnot(None))
            )
            result = await session.execute(stmt)
            in_progress = list(result.scalars().all())

            assignments = [
                {
                    "task_id": str(t.id),
                    "title": t.title,
                    "agent_id": str(t.assigned_agent_id),
                    "status": t.status,
                }
                for t in in_progress
            ]

            # Pending reviews
            stmt_review = select(Task).where(
                Task.status.in_(["plan_review", "testing_ready"])
            )
            result_review = await session.execute(stmt_review)
            reviews = list(result_review.scalars().all())

            pending = [
                {
                    "task_id": str(t.id),
                    "title": t.title,
                    "status": t.status,
                }
                for t in reviews
            ]

            # Active agents
            stmt_agents = select(Agent).where(Agent.status == "working")
            result_agents = await session.execute(stmt_agents)
            agents = list(result_agents.scalars().all())

            active_agents = [
                {
                    "agent_id": str(a.id),
                    "name": a.name,
                    "current_task_id": (
                        str(a.current_task_id)
                        if a.current_task_id
                        else None
                    ),
                    "heartbeat_at": (
                        a.heartbeat_at.isoformat()
                        if a.heartbeat_at
                        else None
                    ),
                }
                for a in agents
            ]

            # Known blockers
            blocker_stmt = (
                select(Memory)
                .where(Memory.agent_id == orchestrator_agent_id)
                .where(Memory.memory_type == "blocker")
                .order_by(Memory.created_at.desc())
                .limit(20)
            )
            blocker_result = await session.execute(blocker_stmt)
            blockers = [m.content for m in blocker_result.scalars().all()]

            # Build state content
            state = {
                "assignments": assignments,
                "pending_reviews": pending,
                "active_agents": active_agents,
                "blockers": blockers,
                "checkpoint_at": datetime.now(UTC).isoformat(),
            }

        # Store as a memory record
        await self._memory.store_memory(
            agent_id=orchestrator_agent_id,
            content=json.dumps(state),
            memory_type="orchestrator_state",
            metadata={"version": 1},
        )

    async def restore_orchestrator_state(
        self, orchestrator_agent_id: uuid.UUID
    ) -> OrchestratorSnapshot:
        """Restore orchestrator state after restart.

        1. Load latest ``orchestrator_state`` memory.
        2. Load all in-progress tasks.
        3. Load all active agents and their heartbeats.
        4. Reconcile: mark agents with stale heartbeats as DEAD.
        5. Return snapshot for the orchestrator to resume from.
        """
        snapshot = OrchestratorSnapshot()

        async with self._memory._session_factory() as session:
            # 1. Latest orchestrator_state checkpoint
            state_stmt = (
                select(Memory)
                .where(Memory.agent_id == orchestrator_agent_id)
                .where(Memory.memory_type == "orchestrator_state")
                .order_by(Memory.created_at.desc())
                .limit(1)
            )
            state_result = await session.execute(state_stmt)
            state_mem = state_result.scalar_one_or_none()
            if state_mem:
                snapshot.last_checkpoint = state_mem.created_at

            # 2. In-progress tasks
            task_stmt = select(Task).where(Task.status == "in_progress")
            task_result = await session.execute(task_stmt)
            snapshot.active_tasks = list(task_result.scalars().all())

            # Pending reviews
            review_stmt = select(Task).where(
                Task.status.in_(["plan_review", "testing_ready"])
            )
            review_result = await session.execute(review_stmt)
            snapshot.pending_reviews = list(review_result.scalars().all())

            # 3. Working agents
            agent_stmt = select(Agent).where(Agent.status == "working")
            agent_result = await session.execute(agent_stmt)
            working_agents = list(agent_result.scalars().all())

            # 4. Reconcile stale heartbeats
            now = datetime.now(UTC)
            cutoff = now - timedelta(seconds=self.STALE_HEARTBEAT_SECONDS)

            for agent in working_agents:
                heartbeat = agent.heartbeat_at
                # Normalise naive datetimes (e.g. from SQLite) to UTC
                if heartbeat is not None and heartbeat.tzinfo is None:
                    heartbeat = heartbeat.replace(tzinfo=UTC)
                if heartbeat is None or heartbeat < cutoff:
                    # Mark as dead
                    await session.execute(
                        update(Agent)
                        .where(Agent.id == agent.id)
                        .values(status="dead")
                    )
                    agent.status = "dead"
                    snapshot.stale_agents.append(agent)
                else:
                    snapshot.active_agents.append(agent)

            await session.commit()

        return snapshot

    # ------------------------------------------------------------------
    # Compaction decision
    # ------------------------------------------------------------------

    async def should_compact(self, agent_id: uuid.UUID) -> bool:
        """Determine if an agent's memory needs compaction.

        Based on total memory count and estimated token size.
        Returns True if over threshold.

        The threshold is expressed as a fraction (0..1).  We consider
        compaction necessary when:
        - memory count > 50 * threshold  (i.e. ~35 at 0.7), OR
        - estimated tokens > 8000 * threshold  (i.e. ~5600 at 0.7)
        """
        async with self._memory._session_factory() as session:
            # Count non-archived memories
            count_stmt = (
                select(func.count(Memory.id))
                .where(Memory.agent_id == agent_id)
                .where(~Memory.memory_type.startswith("archived_"))
                .where(Memory.memory_type != "conversation_summary")
            )
            count_result = await session.execute(count_stmt)
            count = count_result.scalar() or 0

            # Estimate total tokens (sum content lengths / 4)
            size_stmt = (
                select(func.sum(func.length(Memory.content)))
                .where(Memory.agent_id == agent_id)
                .where(~Memory.memory_type.startswith("archived_"))
                .where(Memory.memory_type != "conversation_summary")
            )
            size_result = await session.execute(size_stmt)
            total_chars = size_result.scalar() or 0
            estimated_tokens = total_chars / 4

        count_threshold = int(50 * self._compaction_threshold)
        token_threshold = int(8000 * self._compaction_threshold)

        return count > count_threshold or estimated_tokens > token_threshold
