"""Redis-based agent heartbeat system.

Uses Redis key expiration (TTL) to track agent liveness. Each agent
periodically calls ``beat()`` which sets a key with a TTL. If the key
expires, the agent is considered stale/dead.

This is faster than polling the database for heartbeat timestamps.
"""

from __future__ import annotations

import logging
from datetime import datetime, timezone
from uuid import UUID

import redis.asyncio as redis

logger = logging.getLogger(__name__)


class HeartbeatMonitor:
    """Monitors agent liveness via Redis key TTLs.

    Usage::

        monitor = HeartbeatMonitor("redis://localhost:6379", timeout_seconds=120)
        await monitor.connect()

        # Agent side: call periodically
        await monitor.beat(agent_id)

        # Orchestrator side: check liveness
        alive = await monitor.is_alive(agent_id)
        stale = await monitor.get_stale_agents(all_agent_ids)
    """

    def __init__(self, redis_url: str, timeout_seconds: int = 120) -> None:
        self._redis_url = redis_url
        self._redis: redis.Redis | None = None
        self._timeout = timeout_seconds

    async def connect(self) -> None:
        """Initialize Redis connection."""
        self._redis = redis.from_url(self._redis_url, decode_responses=True)
        logger.info("HeartbeatMonitor connected to %s", self._redis_url)

    async def disconnect(self) -> None:
        """Close Redis connection."""
        if self._redis is not None:
            await self._redis.aclose()
            self._redis = None
        logger.info("HeartbeatMonitor disconnected")

    def _key(self, agent_id: UUID) -> str:
        """Return the Redis key for an agent's heartbeat."""
        return f"heartbeat:{agent_id}"

    async def beat(self, agent_id: UUID) -> None:
        """Record a heartbeat for an agent.

        Uses Redis SET with EX (TTL) = timeout_seconds.
        Key: ``heartbeat:{agent_id}``
        Value: ISO timestamp of the heartbeat.
        """
        if self._redis is None:
            raise RuntimeError("HeartbeatMonitor is not connected. Call connect() first.")

        now = datetime.now(timezone.utc).isoformat()
        await self._redis.set(self._key(agent_id), now, ex=self._timeout)
        logger.debug("Heartbeat recorded for agent %s", agent_id)

    async def is_alive(self, agent_id: UUID) -> bool:
        """Check if an agent's heartbeat key exists (not expired)."""
        if self._redis is None:
            raise RuntimeError("HeartbeatMonitor is not connected. Call connect() first.")

        return await self._redis.exists(self._key(agent_id)) > 0

    async def get_stale_agents(self, agent_ids: list[UUID]) -> list[UUID]:
        """Return agent_ids whose heartbeat has expired.

        Args:
            agent_ids: List of agent IDs to check.

        Returns:
            List of agent IDs that are stale (no heartbeat key in Redis).
        """
        if self._redis is None:
            raise RuntimeError("HeartbeatMonitor is not connected. Call connect() first.")

        stale: list[UUID] = []
        for agent_id in agent_ids:
            if not await self.is_alive(agent_id):
                stale.append(agent_id)
        return stale

    async def get_last_heartbeat(self, agent_id: UUID) -> datetime | None:
        """Get the timestamp of the last heartbeat, or None if expired.

        Returns:
            The datetime of the last heartbeat if the key still exists,
            or None if the heartbeat has expired.
        """
        if self._redis is None:
            raise RuntimeError("HeartbeatMonitor is not connected. Call connect() first.")

        value = await self._redis.get(self._key(agent_id))
        if value is None:
            return None
        return datetime.fromisoformat(value)
