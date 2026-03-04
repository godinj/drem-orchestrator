"""Redis pub/sub messaging system for inter-agent communication.

Provides a MessageBus that allows agents and the orchestrator to exchange
typed messages over Redis channels. Supports project broadcasts, feature
channels, and direct agent-to-agent messaging.
"""

from __future__ import annotations

import asyncio
import json
import logging
from collections.abc import Awaitable, Callable
from dataclasses import asdict, dataclass
from datetime import datetime, timezone
from enum import Enum
from typing import Any
from uuid import UUID

import redis.asyncio as redis

logger = logging.getLogger(__name__)


class MessageType(str, Enum):
    """Types of messages exchanged between agents."""

    FILE_CHANGED = "file_changed"
    BLOCKED = "blocked"
    CONTEXT_SHARE = "context_share"
    REVIEW_REQUEST = "review_request"
    TASK_COMPLETED = "task_completed"
    TASK_FAILED = "task_failed"
    OVERLAP_WARNING = "overlap_warning"


@dataclass
class AgentMessage:
    """A message sent between agents or from the orchestrator."""

    type: MessageType
    sender_id: UUID
    sender_name: str
    project_id: UUID
    content: dict[str, Any]
    timestamp: datetime

    def to_json(self) -> str:
        """Serialize to a JSON string for Redis transport."""
        data = asdict(self)
        data["type"] = self.type.value
        data["sender_id"] = str(self.sender_id)
        data["project_id"] = str(self.project_id)
        data["timestamp"] = self.timestamp.isoformat()
        return json.dumps(data)

    @classmethod
    def from_json(cls, raw: str) -> AgentMessage:
        """Deserialize from a JSON string received from Redis."""
        data = json.loads(raw)
        return cls(
            type=MessageType(data["type"]),
            sender_id=UUID(data["sender_id"]),
            sender_name=data["sender_name"],
            project_id=UUID(data["project_id"]),
            content=data["content"],
            timestamp=datetime.fromisoformat(data["timestamp"]),
        )


class MessageBus:
    """Redis pub/sub message bus for inter-agent communication.

    Usage::

        bus = MessageBus("redis://localhost:6379")
        await bus.connect()

        async def handler(msg: AgentMessage) -> None:
            print(f"Got {msg.type} from {msg.sender_name}")

        await bus.subscribe("project:abc:broadcast", handler)
        asyncio.create_task(bus.listen())

        await bus.publish("project:abc:broadcast", message)
    """

    def __init__(self, redis_url: str) -> None:
        self._redis_url = redis_url
        self._redis: redis.Redis | None = None
        self._pubsub: redis.client.PubSub | None = None
        self._subscriptions: dict[str, list[Callable[[AgentMessage], Awaitable[None]]]] = {}
        self._listening = False

    async def connect(self) -> None:
        """Initialize Redis connection and pubsub."""
        self._redis = redis.from_url(self._redis_url, decode_responses=True)
        self._pubsub = self._redis.pubsub()
        logger.info("MessageBus connected to %s", self._redis_url)

    async def disconnect(self) -> None:
        """Close Redis connection."""
        self._listening = False
        if self._pubsub is not None:
            await self._pubsub.unsubscribe()
            await self._pubsub.aclose()
            self._pubsub = None
        if self._redis is not None:
            await self._redis.aclose()
            self._redis = None
        self._subscriptions.clear()
        logger.info("MessageBus disconnected")

    async def publish(self, channel: str, message: AgentMessage) -> None:
        """Publish a message to a channel.

        Serializes AgentMessage to JSON and publishes via Redis PUBLISH.
        """
        if self._redis is None:
            raise RuntimeError("MessageBus is not connected. Call connect() first.")
        payload = message.to_json()
        await self._redis.publish(channel, payload)
        logger.debug("Published %s to %s", message.type.value, channel)

    async def subscribe(
        self,
        channel: str,
        callback: Callable[[AgentMessage], Awaitable[None]],
    ) -> None:
        """Subscribe to a channel with an async callback.

        The callback is invoked for each message received on the channel.
        Multiple callbacks can be registered for the same channel.
        """
        if self._pubsub is None:
            raise RuntimeError("MessageBus is not connected. Call connect() first.")

        if channel not in self._subscriptions:
            self._subscriptions[channel] = []
            await self._pubsub.subscribe(channel)
            logger.info("Subscribed to channel %s", channel)

        self._subscriptions[channel].append(callback)

    async def unsubscribe(self, channel: str) -> None:
        """Unsubscribe from a channel and remove all callbacks."""
        if self._pubsub is None:
            return

        if channel in self._subscriptions:
            del self._subscriptions[channel]
            await self._pubsub.unsubscribe(channel)
            logger.info("Unsubscribed from channel %s", channel)

    async def listen(self) -> None:
        """Main listen loop. Runs as a background task.

        Dispatches received messages to registered callbacks.
        Should be run via ``asyncio.create_task(bus.listen())``.
        """
        if self._pubsub is None:
            raise RuntimeError("MessageBus is not connected. Call connect() first.")

        self._listening = True
        logger.info("MessageBus listen loop started")

        while self._listening:
            try:
                raw_message = await self._pubsub.get_message(
                    ignore_subscribe_messages=True,
                    timeout=1.0,
                )
                if raw_message is None:
                    await asyncio.sleep(0.01)
                    continue

                channel = raw_message["channel"]
                data = raw_message["data"]

                # Skip non-string data (e.g. subscription confirmations)
                if not isinstance(data, str):
                    continue

                try:
                    agent_message = AgentMessage.from_json(data)
                except (json.JSONDecodeError, KeyError, ValueError) as exc:
                    logger.warning("Failed to parse message on %s: %s", channel, exc)
                    continue

                callbacks = self._subscriptions.get(channel, [])
                for callback in callbacks:
                    try:
                        await callback(agent_message)
                    except Exception:
                        logger.exception(
                            "Error in callback for %s on %s",
                            agent_message.type.value,
                            channel,
                        )
            except redis.ConnectionError:
                logger.error("Redis connection lost, stopping listen loop")
                break
            except asyncio.CancelledError:
                logger.info("MessageBus listen loop cancelled")
                break

        self._listening = False
        logger.info("MessageBus listen loop stopped")

    # --- Convenience methods for standard channels ---

    def project_channel(self, project_id: UUID) -> str:
        """Return the broadcast channel for a project.

        Returns: ``project:{project_id}:broadcast``
        """
        return f"project:{project_id}:broadcast"

    def feature_channel(self, project_id: UUID, feature: str) -> str:
        """Return the channel for a specific feature within a project.

        Returns: ``project:{project_id}:feature:{feature}``
        """
        return f"project:{project_id}:feature:{feature}"

    def agent_channel(self, agent_id: UUID) -> str:
        """Return the direct inbox channel for an agent.

        Returns: ``agent:{agent_id}:inbox``
        """
        return f"agent:{agent_id}:inbox"

    async def broadcast_to_project(
        self, project_id: UUID, message: AgentMessage
    ) -> None:
        """Publish to the project broadcast channel."""
        channel = self.project_channel(project_id)
        await self.publish(channel, message)

    async def send_to_feature(
        self, project_id: UUID, feature: str, message: AgentMessage
    ) -> None:
        """Publish to a feature-specific channel."""
        channel = self.feature_channel(project_id, feature)
        await self.publish(channel, message)

    async def send_to_agent(
        self, agent_id: UUID, message: AgentMessage
    ) -> None:
        """Publish to an agent's direct inbox."""
        channel = self.agent_channel(agent_id)
        await self.publish(channel, message)
