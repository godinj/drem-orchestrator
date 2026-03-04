"""Tests for messaging.py — Redis pub/sub message bus.

Uses fakeredis to avoid requiring a real Redis instance.
"""

from __future__ import annotations

import asyncio
from datetime import datetime, timezone
from uuid import UUID, uuid4

import pytest

from orchestrator.messaging import AgentMessage, MessageBus, MessageType


@pytest.fixture
def project_id() -> UUID:
    return uuid4()


@pytest.fixture
def agent_id() -> UUID:
    return uuid4()


def _make_message(
    *,
    msg_type: MessageType = MessageType.FILE_CHANGED,
    sender_id: UUID | None = None,
    project_id: UUID | None = None,
) -> AgentMessage:
    return AgentMessage(
        type=msg_type,
        sender_id=sender_id or uuid4(),
        sender_name="test-agent",
        project_id=project_id or uuid4(),
        content={"file_path": "src/main.py"},
        timestamp=datetime.now(timezone.utc),
    )


@pytest.fixture
async def bus() -> MessageBus:
    """Create a MessageBus backed by fakeredis."""
    import fakeredis.aioredis

    mb = MessageBus("redis://fake")
    # Override the connection with a fakeredis instance
    mb._redis = fakeredis.aioredis.FakeRedis(decode_responses=True)
    mb._pubsub = mb._redis.pubsub()
    yield mb
    await mb.disconnect()


class TestMessageSerialization:
    def test_round_trip(self) -> None:
        """AgentMessage should survive a JSON round-trip."""
        original = _make_message()
        json_str = original.to_json()
        restored = AgentMessage.from_json(json_str)

        assert restored.type == original.type
        assert restored.sender_id == original.sender_id
        assert restored.sender_name == original.sender_name
        assert restored.project_id == original.project_id
        assert restored.content == original.content
        assert restored.timestamp == original.timestamp

    def test_all_message_types(self) -> None:
        """All MessageType values should serialize and deserialize."""
        for msg_type in MessageType:
            msg = _make_message(msg_type=msg_type)
            restored = AgentMessage.from_json(msg.to_json())
            assert restored.type == msg_type

    def test_complex_content(self) -> None:
        """Content dict with nested structures should round-trip."""
        msg = _make_message()
        msg.content = {
            "files": ["a.py", "b.py"],
            "metadata": {"key": "value"},
            "count": 42,
        }
        restored = AgentMessage.from_json(msg.to_json())
        assert restored.content == msg.content


class TestPublishSubscribe:
    async def test_publish_subscribe(self, bus: MessageBus) -> None:
        """Publishing a message should invoke the subscriber callback."""
        received: list[AgentMessage] = []

        async def handler(msg: AgentMessage) -> None:
            received.append(msg)

        channel = "test:channel"
        await bus.subscribe(channel, handler)

        message = _make_message()
        await bus.publish(channel, message)

        # Run the listen loop briefly to process the message
        listen_task = asyncio.create_task(bus.listen())
        await asyncio.sleep(0.1)
        bus._listening = False
        await listen_task

        assert len(received) == 1
        assert received[0].type == message.type
        assert received[0].sender_id == message.sender_id

    async def test_multiple_callbacks(self, bus: MessageBus) -> None:
        """Multiple callbacks on the same channel should all fire."""
        received_a: list[AgentMessage] = []
        received_b: list[AgentMessage] = []

        async def handler_a(msg: AgentMessage) -> None:
            received_a.append(msg)

        async def handler_b(msg: AgentMessage) -> None:
            received_b.append(msg)

        channel = "test:multi"
        await bus.subscribe(channel, handler_a)
        await bus.subscribe(channel, handler_b)

        await bus.publish(channel, _make_message())

        listen_task = asyncio.create_task(bus.listen())
        await asyncio.sleep(0.1)
        bus._listening = False
        await listen_task

        assert len(received_a) == 1
        assert len(received_b) == 1

    async def test_unsubscribe(self, bus: MessageBus) -> None:
        """After unsubscribe, callback should not be invoked."""
        received: list[AgentMessage] = []

        async def handler(msg: AgentMessage) -> None:
            received.append(msg)

        channel = "test:unsub"
        await bus.subscribe(channel, handler)
        await bus.unsubscribe(channel)

        await bus.publish(channel, _make_message())

        listen_task = asyncio.create_task(bus.listen())
        await asyncio.sleep(0.1)
        bus._listening = False
        await listen_task

        assert len(received) == 0


class TestConvenienceChannels:
    def test_project_channel(self, bus: MessageBus, project_id: UUID) -> None:
        """project_channel should return the expected format."""
        channel = bus.project_channel(project_id)
        assert channel == f"project:{project_id}:broadcast"

    def test_feature_channel(self, bus: MessageBus, project_id: UUID) -> None:
        """feature_channel should return the expected format."""
        channel = bus.feature_channel(project_id, "auth")
        assert channel == f"project:{project_id}:feature:auth"

    def test_agent_channel(self, bus: MessageBus, agent_id: UUID) -> None:
        """agent_channel should return the expected format."""
        channel = bus.agent_channel(agent_id)
        assert channel == f"agent:{agent_id}:inbox"


class TestProjectBroadcast:
    async def test_project_broadcast(self, bus: MessageBus, project_id: UUID) -> None:
        """Multiple subscribers should receive a project broadcast."""
        received_1: list[AgentMessage] = []
        received_2: list[AgentMessage] = []

        async def handler_1(msg: AgentMessage) -> None:
            received_1.append(msg)

        async def handler_2(msg: AgentMessage) -> None:
            received_2.append(msg)

        channel = bus.project_channel(project_id)
        await bus.subscribe(channel, handler_1)
        await bus.subscribe(channel, handler_2)

        message = _make_message(project_id=project_id)
        await bus.broadcast_to_project(project_id, message)

        listen_task = asyncio.create_task(bus.listen())
        await asyncio.sleep(0.1)
        bus._listening = False
        await listen_task

        assert len(received_1) == 1
        assert len(received_2) == 1
        assert received_1[0].project_id == project_id


class TestAgentDirectMessage:
    async def test_agent_direct_message(
        self, bus: MessageBus, agent_id: UUID
    ) -> None:
        """A message sent to an agent inbox should reach the subscriber."""
        received: list[AgentMessage] = []

        async def handler(msg: AgentMessage) -> None:
            received.append(msg)

        channel = bus.agent_channel(agent_id)
        await bus.subscribe(channel, handler)

        message = _make_message()
        await bus.send_to_agent(agent_id, message)

        listen_task = asyncio.create_task(bus.listen())
        await asyncio.sleep(0.1)
        bus._listening = False
        await listen_task

        assert len(received) == 1
        assert received[0].type == message.type


class TestErrorHandling:
    async def test_publish_without_connect_raises(self) -> None:
        """Publishing without connecting should raise RuntimeError."""
        bus = MessageBus("redis://fake")
        with pytest.raises(RuntimeError, match="not connected"):
            await bus.publish("channel", _make_message())

    async def test_subscribe_without_connect_raises(self) -> None:
        """Subscribing without connecting should raise RuntimeError."""
        bus = MessageBus("redis://fake")

        async def handler(msg: AgentMessage) -> None:
            pass

        with pytest.raises(RuntimeError, match="not connected"):
            await bus.subscribe("channel", handler)
