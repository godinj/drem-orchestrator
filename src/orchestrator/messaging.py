"""Inter-agent messaging bus.

Stub module — will be fleshed out by the messaging agent.
Provides the MessageBus interface needed by merge orchestration.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from datetime import datetime
from uuid import UUID, uuid4


@dataclass
class Message:
    """A message sent through the bus."""

    id: UUID = field(default_factory=uuid4)
    topic: str = ""
    payload: dict = field(default_factory=dict)
    timestamp: datetime = field(default_factory=datetime.now)


class MessageBus:
    """Simple in-memory message bus for inter-agent communication.

    Stub implementation — stores published messages for later retrieval.
    Will be replaced with a proper pub/sub implementation.
    """

    def __init__(self) -> None:
        self._messages: list[Message] = []
        self._subscribers: dict[str, list] = {}

    async def publish(self, topic: str, payload: dict) -> None:
        """Publish a message to a topic."""
        msg = Message(topic=topic, payload=payload)
        self._messages.append(msg)

        # Notify subscribers
        for callback in self._subscribers.get(topic, []):
            await callback(msg)

    def subscribe(self, topic: str, callback) -> None:
        """Subscribe to a topic with a callback."""
        self._subscribers.setdefault(topic, []).append(callback)

    @property
    def messages(self) -> list[Message]:
        """Return all published messages (for testing)."""
        return list(self._messages)
