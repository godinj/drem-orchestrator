"""Tests for overlap_detector.py — file overlap detection between agents.

Uses fakeredis to avoid requiring a real Redis instance.
"""

from __future__ import annotations

import asyncio
from datetime import datetime, timezone
from uuid import UUID, uuid4

import pytest

from orchestrator.messaging import AgentMessage, MessageBus, MessageType
from orchestrator.overlap_detector import OverlapDetector


@pytest.fixture
def project_id() -> UUID:
    return uuid4()


def _file_changed_message(
    *,
    sender_id: UUID,
    sender_name: str,
    project_id: UUID,
    file_path: str,
    feature: str = "auth",
) -> AgentMessage:
    return AgentMessage(
        type=MessageType.FILE_CHANGED,
        sender_id=sender_id,
        sender_name=sender_name,
        project_id=project_id,
        content={"file_path": file_path, "feature": feature},
        timestamp=datetime.now(timezone.utc),
    )


@pytest.fixture
async def bus() -> MessageBus:
    """Create a MessageBus backed by fakeredis."""
    import fakeredis.aioredis

    mb = MessageBus("redis://fake")
    mb._redis = fakeredis.aioredis.FakeRedis(decode_responses=True)
    mb._pubsub = mb._redis.pubsub()
    yield mb
    await mb.disconnect()


@pytest.fixture
def detector(bus: MessageBus) -> OverlapDetector:
    return OverlapDetector(bus)


class TestNoOverlap:
    async def test_no_overlap(
        self, detector: OverlapDetector, project_id: UUID
    ) -> None:
        """Two agents modifying different files should produce no overlaps."""
        agent_a = uuid4()
        agent_b = uuid4()

        msg_a = _file_changed_message(
            sender_id=agent_a,
            sender_name="agent-a",
            project_id=project_id,
            file_path="src/auth.py",
        )
        msg_b = _file_changed_message(
            sender_id=agent_b,
            sender_name="agent-b",
            project_id=project_id,
            file_path="src/models.py",
        )

        await detector.on_file_changed(msg_a)
        await detector.on_file_changed(msg_b)

        overlaps = await detector.get_overlaps()
        assert len(overlaps) == 0


class TestOverlapDetected:
    async def test_overlap_detected(
        self, detector: OverlapDetector, bus: MessageBus, project_id: UUID
    ) -> None:
        """Two agents modifying the same file should trigger a warning."""
        agent_a = uuid4()
        agent_b = uuid4()

        # Track warnings sent to agents
        warnings_a: list[AgentMessage] = []
        warnings_b: list[AgentMessage] = []

        async def handler_a(msg: AgentMessage) -> None:
            warnings_a.append(msg)

        async def handler_b(msg: AgentMessage) -> None:
            warnings_b.append(msg)

        await bus.subscribe(bus.agent_channel(agent_a), handler_a)
        await bus.subscribe(bus.agent_channel(agent_b), handler_b)

        # Start listening in the background
        listen_task = asyncio.create_task(bus.listen())

        # Agent A modifies file
        msg_a = _file_changed_message(
            sender_id=agent_a,
            sender_name="agent-a",
            project_id=project_id,
            file_path="src/shared.py",
        )
        await detector.on_file_changed(msg_a)

        # No overlap yet
        overlaps = await detector.get_overlaps()
        assert len(overlaps) == 0

        # Agent B modifies the same file
        msg_b = _file_changed_message(
            sender_id=agent_b,
            sender_name="agent-b",
            project_id=project_id,
            file_path="src/shared.py",
        )
        await detector.on_file_changed(msg_b)

        # Wait for messages to be delivered
        await asyncio.sleep(0.1)
        bus._listening = False
        await listen_task

        # Should have overlap
        overlaps = await detector.get_overlaps()
        assert len(overlaps) == 1
        assert overlaps[0].file_path == "src/shared.py"
        assert set(overlaps[0].agent_ids) == {agent_a, agent_b}
        assert "agent-a" in overlaps[0].agent_names
        assert "agent-b" in overlaps[0].agent_names

        # Both agents should have received warnings
        assert len(warnings_a) == 1
        assert warnings_a[0].type == MessageType.OVERLAP_WARNING
        assert len(warnings_b) == 1
        assert warnings_b[0].type == MessageType.OVERLAP_WARNING


class TestClearAgent:
    async def test_clear_agent(
        self, detector: OverlapDetector, project_id: UUID
    ) -> None:
        """Clearing an agent should remove it from file ownership."""
        agent_a = uuid4()
        agent_b = uuid4()

        # Both modify the same file
        msg_a = _file_changed_message(
            sender_id=agent_a,
            sender_name="agent-a",
            project_id=project_id,
            file_path="src/overlap.py",
        )
        msg_b = _file_changed_message(
            sender_id=agent_b,
            sender_name="agent-b",
            project_id=project_id,
            file_path="src/overlap.py",
        )
        await detector.on_file_changed(msg_a)
        await detector.on_file_changed(msg_b)

        # Overlap exists
        overlaps = await detector.get_overlaps()
        assert len(overlaps) == 1

        # Clear agent A (task completed)
        await detector.clear_agent(agent_a)

        # No more overlap — only agent B remains
        overlaps = await detector.get_overlaps()
        assert len(overlaps) == 0

    async def test_clear_agent_with_sole_ownership(
        self, detector: OverlapDetector, project_id: UUID
    ) -> None:
        """Clearing the only agent on a file should remove the file entry."""
        agent_a = uuid4()

        msg = _file_changed_message(
            sender_id=agent_a,
            sender_name="agent-a",
            project_id=project_id,
            file_path="src/solo.py",
        )
        await detector.on_file_changed(msg)

        await detector.clear_agent(agent_a)

        # File should be completely removed from tracking
        assert "src/solo.py" not in detector._file_owners


class TestMultipleOverlaps:
    async def test_multiple_overlaps(
        self, detector: OverlapDetector, bus: MessageBus, project_id: UUID
    ) -> None:
        """Three agents with complex overlaps across multiple files."""
        agent_a = uuid4()
        agent_b = uuid4()
        agent_c = uuid4()

        # Track warnings
        warnings: dict[UUID, list[AgentMessage]] = {
            agent_a: [],
            agent_b: [],
            agent_c: [],
        }

        for aid in [agent_a, agent_b, agent_c]:
            async def make_handler(target_list: list[AgentMessage]):
                async def handler(msg: AgentMessage) -> None:
                    target_list.append(msg)
                return handler

            handler = await make_handler(warnings[aid])
            await bus.subscribe(bus.agent_channel(aid), handler)

        listen_task = asyncio.create_task(bus.listen())

        # Agent A: modifies file1, file2
        for fp in ["src/file1.py", "src/file2.py"]:
            msg = _file_changed_message(
                sender_id=agent_a,
                sender_name="agent-a",
                project_id=project_id,
                file_path=fp,
            )
            await detector.on_file_changed(msg)

        # Agent B: modifies file2, file3
        for fp in ["src/file2.py", "src/file3.py"]:
            msg = _file_changed_message(
                sender_id=agent_b,
                sender_name="agent-b",
                project_id=project_id,
                file_path=fp,
            )
            await detector.on_file_changed(msg)

        # Agent C: modifies file1, file3
        for fp in ["src/file1.py", "src/file3.py"]:
            msg = _file_changed_message(
                sender_id=agent_c,
                sender_name="agent-c",
                project_id=project_id,
                file_path=fp,
            )
            await detector.on_file_changed(msg)

        await asyncio.sleep(0.1)
        bus._listening = False
        await listen_task

        overlaps = await detector.get_overlaps()

        # Three files have overlaps
        assert len(overlaps) == 3

        overlap_map = {o.file_path: set(o.agent_ids) for o in overlaps}
        assert overlap_map["src/file1.py"] == {agent_a, agent_c}
        assert overlap_map["src/file2.py"] == {agent_a, agent_b}
        assert overlap_map["src/file3.py"] == {agent_b, agent_c}


class TestStartSubscription:
    async def test_start_subscribes_to_project(
        self, detector: OverlapDetector, bus: MessageBus, project_id: UUID
    ) -> None:
        """start() should subscribe to the project broadcast channel."""
        await detector.start(project_id)

        channel = bus.project_channel(project_id)
        assert channel in bus._subscriptions
        assert len(bus._subscriptions[channel]) == 1

    async def test_start_filters_non_file_changed(
        self, detector: OverlapDetector, bus: MessageBus, project_id: UUID
    ) -> None:
        """start() handler should ignore non-FILE_CHANGED messages."""
        await detector.start(project_id)

        # Send a TASK_COMPLETED message directly through the handler
        msg = AgentMessage(
            type=MessageType.TASK_COMPLETED,
            sender_id=uuid4(),
            sender_name="agent-a",
            project_id=project_id,
            content={"task_id": str(uuid4())},
            timestamp=datetime.now(timezone.utc),
        )

        # Call the internal handler directly
        await detector._on_message(msg)

        # No file tracking should have occurred
        assert len(detector._file_owners) == 0
