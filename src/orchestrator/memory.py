"""Agent memory persistence and compaction.

Provides MemoryManager for storing, retrieving, and compacting agent
memories.  Memories are persisted in the database via SQLAlchemy and can
be queried per-agent, per-task, or project-wide.
"""

from __future__ import annotations

import re
import uuid
from pathlib import Path
from typing import TYPE_CHECKING

from sqlalchemy import select, update

from orchestrator.models import Agent, Memory

if TYPE_CHECKING:
    from collections.abc import Callable

    from sqlalchemy.ext.asyncio import AsyncSession


class MemoryManager:
    """Manages storage, retrieval, and compaction of agent memories."""

    def __init__(
        self,
        db_session_factory: Callable[[], AsyncSession],
        claude_bin: Path | None = None,
    ) -> None:
        self._session_factory = db_session_factory
        self._claude_bin = claude_bin

    # ------------------------------------------------------------------
    # Store
    # ------------------------------------------------------------------

    async def store_memory(
        self,
        agent_id: uuid.UUID,
        content: str,
        memory_type: str,
        task_id: uuid.UUID | None = None,
        metadata: dict | None = None,
    ) -> Memory:
        """Create a Memory record in the database."""
        async with self._session_factory() as session:
            memory = Memory(
                agent_id=agent_id,
                content=content,
                memory_type=memory_type,
                task_id=task_id,
                metadata_=metadata,
            )
            session.add(memory)
            await session.commit()
            await session.refresh(memory)
            return memory

    # ------------------------------------------------------------------
    # Retrieve
    # ------------------------------------------------------------------

    async def get_memories(
        self,
        agent_id: uuid.UUID | None = None,
        task_id: uuid.UUID | None = None,
        memory_type: str | None = None,
        limit: int = 50,
    ) -> list[Memory]:
        """Retrieve memories ordered by created_at desc.

        Filter by agent, task, and/or type.
        """
        async with self._session_factory() as session:
            stmt = select(Memory).order_by(Memory.created_at.desc())
            if agent_id is not None:
                stmt = stmt.where(Memory.agent_id == agent_id)
            if task_id is not None:
                stmt = stmt.where(Memory.task_id == task_id)
            if memory_type is not None:
                stmt = stmt.where(Memory.memory_type == memory_type)
            stmt = stmt.limit(limit)
            result = await session.execute(stmt)
            return list(result.scalars().all())

    async def get_project_memories(
        self,
        project_id: uuid.UUID,
        memory_types: list[str] | None = None,
        limit: int = 100,
    ) -> list[Memory]:
        """Get memories across all agents in a project.

        Useful for building shared context.
        """
        async with self._session_factory() as session:
            stmt = (
                select(Memory)
                .join(Agent, Memory.agent_id == Agent.id)
                .where(Agent.project_id == project_id)
                .order_by(Memory.created_at.desc())
            )
            if memory_types is not None:
                stmt = stmt.where(Memory.memory_type.in_(memory_types))
            stmt = stmt.limit(limit)
            result = await session.execute(stmt)
            return list(result.scalars().all())

    # ------------------------------------------------------------------
    # Compact
    # ------------------------------------------------------------------

    async def compact_agent_memory(self, agent_id: uuid.UUID) -> str:
        """Summarize an agent's memories into a compact summary.

        1. Fetch all memories for this agent, ordered chronologically.
        2. Group by memory_type.
        3. Build a structured summary:
           - Key decisions made
           - Files created/modified
           - Lessons learned
           - Current task state
        4. Store as a new Memory with type ``conversation_summary``.
        5. Update ``Agent.memory_summary`` with the compact text.
        6. Archive (don't delete) older individual memories by setting
           their memory_type to ``archived_<original_type>``.
        7. Return the summary text.
        """
        async with self._session_factory() as session:
            # 1. Fetch all non-archived, non-summary memories chronologically
            stmt = (
                select(Memory)
                .where(Memory.agent_id == agent_id)
                .where(~Memory.memory_type.startswith("archived_"))
                .where(Memory.memory_type != "conversation_summary")
                .order_by(Memory.created_at.asc())
            )
            result = await session.execute(stmt)
            memories = list(result.scalars().all())

            if not memories:
                return ""

            # 2. Group by memory_type
            grouped: dict[str, list[Memory]] = {}
            for mem in memories:
                grouped.setdefault(mem.memory_type, []).append(mem)

            # 3. Build structured summary
            sections: list[str] = []

            if "decision" in grouped:
                items = [f"- {m.content}" for m in grouped["decision"]]
                sections.append("## Key Decisions\n" + "\n".join(items))

            if "file_change" in grouped:
                items = [f"- {m.content}" for m in grouped["file_change"]]
                sections.append("## Files Created/Modified\n" + "\n".join(items))

            if "lesson" in grouped:
                items = [f"- {m.content}" for m in grouped["lesson"]]
                sections.append("## Lessons Learned\n" + "\n".join(items))

            if "blocker" in grouped:
                items = [f"- {m.content}" for m in grouped["blocker"]]
                sections.append("## Blockers\n" + "\n".join(items))

            if "completion" in grouped:
                items = [f"- {m.content}" for m in grouped["completion"]]
                sections.append("## Completed\n" + "\n".join(items))

            # Include any other types not covered above
            covered = {"decision", "file_change", "lesson", "blocker", "completion"}
            for mtype, mems in grouped.items():
                if mtype not in covered:
                    items = [f"- {m.content}" for m in mems]
                    title = mtype.replace("_", " ").title()
                    sections.append(f"## {title}\n" + "\n".join(items))

            summary_text = "\n\n".join(sections)

            # 4. Store as conversation_summary
            summary_memory = Memory(
                agent_id=agent_id,
                content=summary_text,
                memory_type="conversation_summary",
            )
            session.add(summary_memory)

            # 5. Update Agent.memory_summary
            await session.execute(
                update(Agent)
                .where(Agent.id == agent_id)
                .values(memory_summary=summary_text)
            )

            # 6. Archive older individual memories
            for mem in memories:
                await session.execute(
                    update(Memory)
                    .where(Memory.id == mem.id)
                    .values(memory_type=f"archived_{mem.memory_type}")
                )

            await session.commit()
            return summary_text

    # ------------------------------------------------------------------
    # Context building
    # ------------------------------------------------------------------

    async def build_agent_context(
        self,
        agent_id: uuid.UUID,
        task_id: uuid.UUID,
        max_tokens: int = 8000,
    ) -> str:
        """Build context string for an agent session.

        Combines:
        1. Agent's memory_summary (if exists)
        2. Recent memories for this task
        3. Project-wide decisions and lessons (cross-agent)
        4. Truncate to max_tokens (rough estimate: 4 chars per token)

        Returns formatted context string for inclusion in agent prompt.
        """
        max_chars = max_tokens * 4
        parts: list[str] = []

        async with self._session_factory() as session:
            # 1. Agent's memory_summary
            agent_result = await session.execute(
                select(Agent).where(Agent.id == agent_id)
            )
            agent = agent_result.scalar_one_or_none()
            if agent and agent.memory_summary:
                parts.append(
                    "# Agent Memory Summary\n\n" + agent.memory_summary
                )

            # 2. Recent memories for this task
            task_stmt = (
                select(Memory)
                .where(Memory.agent_id == agent_id)
                .where(Memory.task_id == task_id)
                .where(~Memory.memory_type.startswith("archived_"))
                .where(Memory.memory_type != "conversation_summary")
                .order_by(Memory.created_at.desc())
                .limit(20)
            )
            task_result = await session.execute(task_stmt)
            task_memories = list(task_result.scalars().all())
            if task_memories:
                items = [
                    f"- [{m.memory_type}] {m.content}"
                    for m in reversed(task_memories)
                ]
                parts.append(
                    "# Recent Task Memories\n\n" + "\n".join(items)
                )

            # 3. Project-wide decisions and lessons
            if agent and agent.project_id:
                project_stmt = (
                    select(Memory)
                    .join(Agent, Memory.agent_id == Agent.id)
                    .where(Agent.project_id == agent.project_id)
                    .where(Agent.id != agent_id)
                    .where(
                        Memory.memory_type.in_(["decision", "lesson"])
                    )
                    .order_by(Memory.created_at.desc())
                    .limit(10)
                )
                proj_result = await session.execute(project_stmt)
                proj_memories = list(proj_result.scalars().all())
                if proj_memories:
                    items = [
                        f"- [{m.memory_type}] {m.content}"
                        for m in reversed(proj_memories)
                    ]
                    parts.append(
                        "# Project-Wide Context\n\n" + "\n".join(items)
                    )

        context = "\n\n---\n\n".join(parts)

        # 4. Truncate to max_tokens
        if len(context) > max_chars:
            context = context[:max_chars]

        return context

    # ------------------------------------------------------------------
    # Extract from output
    # ------------------------------------------------------------------

    # Regex patterns for extracting structured memories from agent output
    _DECISION_PATTERNS = [
        re.compile(r"(?:decided to|chose|approach:)\s*(.+)", re.IGNORECASE),
    ]
    _BLOCKER_PATTERNS = [
        re.compile(r"(?:blocked by|need|waiting for)\s*(.+)", re.IGNORECASE),
    ]
    _COMPLETION_PATTERNS = [
        re.compile(r"(?:completed|finished|done:)\s*(.+)", re.IGNORECASE),
    ]
    _FILE_CHANGE_PATTERNS = [
        re.compile(
            r"(?:created|modified|updated|added|deleted|removed)\s+"
            r"(?:file\s+)?([^\s,]+\.\w+)",
            re.IGNORECASE,
        ),
    ]

    async def extract_memories_from_output(
        self,
        agent_id: uuid.UUID,
        task_id: uuid.UUID,
        output: str,
    ) -> list[Memory]:
        """Parse agent output to extract structured memories.

        Looks for patterns:
        - File changes (created/modified/deleted <filename>)
        - Decisions ("decided to...", "chose...", "approach:")
        - Blockers ("blocked by...", "need...", "waiting for...")
        - Completions ("completed", "finished", "done:")

        Creates Memory records for each extracted item.
        Returns the created memories.
        """
        extracted: list[tuple[str, str]] = []  # (memory_type, content)

        for line in output.splitlines():
            line = line.strip()
            if not line:
                continue

            for pattern in self._DECISION_PATTERNS:
                match = pattern.search(line)
                if match:
                    extracted.append(("decision", match.group(0).strip()))
                    break

            for pattern in self._BLOCKER_PATTERNS:
                match = pattern.search(line)
                if match:
                    extracted.append(("blocker", match.group(0).strip()))
                    break

            for pattern in self._FILE_CHANGE_PATTERNS:
                match = pattern.search(line)
                if match:
                    extracted.append(("file_change", match.group(0).strip()))
                    break

            for pattern in self._COMPLETION_PATTERNS:
                match = pattern.search(line)
                if match:
                    extracted.append(("completion", match.group(0).strip()))
                    break

        # Deduplicate
        seen: set[tuple[str, str]] = set()
        unique: list[tuple[str, str]] = []
        for item in extracted:
            if item not in seen:
                seen.add(item)
                unique.append(item)

        # Store each extracted memory
        memories: list[Memory] = []
        for memory_type, content in unique:
            mem = await self.store_memory(
                agent_id=agent_id,
                content=content,
                memory_type=memory_type,
                task_id=task_id,
            )
            memories.append(mem)

        return memories
