"""Detects when multiple agents are working on overlapping files.

Subscribes to FILE_CHANGED messages on the project broadcast channel and
tracks which agents have modified which files. When an overlap is detected
(two or more agents touching the same file), it sends an OVERLAP_WARNING to
all affected agents.
"""

from __future__ import annotations

import logging
from dataclasses import dataclass, field
from datetime import datetime, timezone
from uuid import UUID

from orchestrator.messaging import AgentMessage, MessageBus, MessageType

logger = logging.getLogger(__name__)


@dataclass
class FileOverlap:
    """Represents a file being modified by multiple agents."""

    file_path: str
    agent_ids: list[UUID]
    agent_names: list[str]
    feature: str


class OverlapDetector:
    """Monitors FILE_CHANGED messages and warns when agents touch the same files.

    Usage::

        detector = OverlapDetector(message_bus)
        await detector.start(project_id)
        # ... detector runs as part of the message bus listen loop
        overlaps = await detector.get_overlaps()
    """

    def __init__(self, message_bus: MessageBus) -> None:
        self._bus = message_bus
        # file_path -> set of agent_ids
        self._file_owners: dict[str, set[UUID]] = {}
        # agent_id -> agent_name (for building overlap reports)
        self._agent_names: dict[UUID, str] = {}
        # file_path -> feature name (from the first FILE_CHANGED message)
        self._file_features: dict[str, str] = {}
        self._project_id: UUID | None = None

    async def start(self, project_id: UUID) -> None:
        """Subscribe to the project broadcast channel.

        Listens for FILE_CHANGED messages and routes them to the handler.
        """
        self._project_id = project_id
        channel = self._bus.project_channel(project_id)
        await self._bus.subscribe(channel, self._on_message)
        logger.info("OverlapDetector started for project %s", project_id)

    async def _on_message(self, message: AgentMessage) -> None:
        """Route FILE_CHANGED messages to the handler."""
        if message.type == MessageType.FILE_CHANGED:
            await self.on_file_changed(message)

    async def on_file_changed(self, message: AgentMessage) -> None:
        """Handle a FILE_CHANGED message.

        1. Extract file path from message.content
        2. Add sender to _file_owners[path]
        3. If multiple agents own the same file:
           a. Create OVERLAP_WARNING message
           b. Send to all agents working on that file
           c. Log the overlap for the orchestrator
        """
        file_path = message.content.get("file_path", "")
        if not file_path:
            logger.warning("FILE_CHANGED message missing file_path in content")
            return

        feature = message.content.get("feature", "unknown")

        # Track the agent name for overlap reports
        self._agent_names[message.sender_id] = message.sender_name

        # Add sender to file owners
        if file_path not in self._file_owners:
            self._file_owners[file_path] = set()
            self._file_features[file_path] = feature

        self._file_owners[file_path].add(message.sender_id)

        # Check for overlap
        owners = self._file_owners[file_path]
        if len(owners) > 1:
            overlap = FileOverlap(
                file_path=file_path,
                agent_ids=list(owners),
                agent_names=[
                    self._agent_names.get(aid, str(aid)) for aid in owners
                ],
                feature=self._file_features.get(file_path, feature),
            )

            logger.warning(
                "File overlap detected: %s modified by agents %s",
                file_path,
                ", ".join(overlap.agent_names),
            )

            # Send OVERLAP_WARNING to each affected agent
            warning_message = AgentMessage(
                type=MessageType.OVERLAP_WARNING,
                sender_id=message.sender_id,
                sender_name="overlap_detector",
                project_id=message.project_id,
                content={
                    "file_path": file_path,
                    "agent_ids": [str(aid) for aid in overlap.agent_ids],
                    "agent_names": overlap.agent_names,
                    "feature": overlap.feature,
                },
                timestamp=datetime.now(timezone.utc),
            )

            for agent_id in owners:
                await self._bus.send_to_agent(agent_id, warning_message)

    async def get_overlaps(self) -> list[FileOverlap]:
        """Return all current file overlaps (files touched by 2+ agents)."""
        overlaps: list[FileOverlap] = []
        for file_path, agent_ids in self._file_owners.items():
            if len(agent_ids) > 1:
                overlaps.append(
                    FileOverlap(
                        file_path=file_path,
                        agent_ids=list(agent_ids),
                        agent_names=[
                            self._agent_names.get(aid, str(aid)) for aid in agent_ids
                        ],
                        feature=self._file_features.get(file_path, "unknown"),
                    )
                )
        return overlaps

    async def clear_agent(self, agent_id: UUID) -> None:
        """Remove an agent from all file ownership.

        Called when an agent completes its task or is terminated.
        """
        empty_paths: list[str] = []
        for file_path, agent_ids in self._file_owners.items():
            agent_ids.discard(agent_id)
            if not agent_ids:
                empty_paths.append(file_path)

        # Clean up paths with no remaining owners
        for file_path in empty_paths:
            del self._file_owners[file_path]
            self._file_features.pop(file_path, None)

        self._agent_names.pop(agent_id, None)
        logger.info("Cleared agent %s from overlap tracking", agent_id)
