"""Tests for heartbeat.py — Redis-based agent heartbeat system.

Uses fakeredis to avoid requiring a real Redis instance.
"""

from __future__ import annotations

from datetime import datetime, timezone
from uuid import uuid4

import pytest

from orchestrator.heartbeat import HeartbeatMonitor


@pytest.fixture
async def monitor() -> HeartbeatMonitor:
    """Create a HeartbeatMonitor backed by fakeredis."""
    import fakeredis.aioredis

    hm = HeartbeatMonitor("redis://fake", timeout_seconds=2)
    hm._redis = fakeredis.aioredis.FakeRedis(decode_responses=True)
    yield hm
    await hm.disconnect()


class TestBeatAndAlive:
    async def test_beat_and_alive(self, monitor: HeartbeatMonitor) -> None:
        """An agent that has sent a heartbeat should be alive."""
        agent_id = uuid4()

        # Before beating, agent is not alive
        assert await monitor.is_alive(agent_id) is False

        # Beat
        await monitor.beat(agent_id)

        # Now alive
        assert await monitor.is_alive(agent_id) is True

    async def test_beat_updates_timestamp(self, monitor: HeartbeatMonitor) -> None:
        """Subsequent beats should update the stored timestamp."""
        agent_id = uuid4()

        await monitor.beat(agent_id)
        ts1 = await monitor.get_last_heartbeat(agent_id)
        assert ts1 is not None

        await monitor.beat(agent_id)
        ts2 = await monitor.get_last_heartbeat(agent_id)
        assert ts2 is not None
        assert ts2 >= ts1

    async def test_get_last_heartbeat(self, monitor: HeartbeatMonitor) -> None:
        """get_last_heartbeat should return a valid datetime."""
        agent_id = uuid4()

        # No heartbeat yet
        assert await monitor.get_last_heartbeat(agent_id) is None

        await monitor.beat(agent_id)
        ts = await monitor.get_last_heartbeat(agent_id)
        assert ts is not None
        assert isinstance(ts, datetime)

        # Timestamp should be recent (within last 5 seconds)
        now = datetime.now(timezone.utc)
        delta = (now - ts).total_seconds()
        assert delta < 5.0


class TestExpiredHeartbeat:
    async def test_expired_heartbeat(self) -> None:
        """An agent whose heartbeat TTL has expired should not be alive.

        Uses a very short timeout and manually expires the key.
        """
        import fakeredis.aioredis

        # Create a monitor with 1-second timeout
        hm = HeartbeatMonitor("redis://fake", timeout_seconds=1)
        hm._redis = fakeredis.aioredis.FakeRedis(decode_responses=True)

        agent_id = uuid4()
        await hm.beat(agent_id)
        assert await hm.is_alive(agent_id) is True

        # Manually delete the key to simulate expiration
        # (fakeredis doesn't always support real-time TTL expiration)
        await hm._redis.delete(hm._key(agent_id))

        assert await hm.is_alive(agent_id) is False
        assert await hm.get_last_heartbeat(agent_id) is None

        await hm.disconnect()


class TestGetStaleAgents:
    async def test_get_stale_agents(self, monitor: HeartbeatMonitor) -> None:
        """Should correctly identify stale vs alive agents."""
        alive_1 = uuid4()
        alive_2 = uuid4()
        stale_1 = uuid4()
        stale_2 = uuid4()

        # Beat for alive agents only
        await monitor.beat(alive_1)
        await monitor.beat(alive_2)

        all_agents = [alive_1, alive_2, stale_1, stale_2]
        stale = await monitor.get_stale_agents(all_agents)

        assert set(stale) == {stale_1, stale_2}

    async def test_get_stale_agents_empty_list(
        self, monitor: HeartbeatMonitor
    ) -> None:
        """Empty input should return empty stale list."""
        stale = await monitor.get_stale_agents([])
        assert stale == []

    async def test_get_stale_agents_all_alive(
        self, monitor: HeartbeatMonitor
    ) -> None:
        """All alive agents should return empty stale list."""
        agents = [uuid4(), uuid4(), uuid4()]
        for agent_id in agents:
            await monitor.beat(agent_id)

        stale = await monitor.get_stale_agents(agents)
        assert stale == []

    async def test_get_stale_agents_all_stale(
        self, monitor: HeartbeatMonitor
    ) -> None:
        """All stale agents should all be returned."""
        agents = [uuid4(), uuid4(), uuid4()]
        stale = await monitor.get_stale_agents(agents)
        assert set(stale) == set(agents)


class TestErrorHandling:
    async def test_beat_without_connect_raises(self) -> None:
        """Beating without connecting should raise RuntimeError."""
        hm = HeartbeatMonitor("redis://fake")
        with pytest.raises(RuntimeError, match="not connected"):
            await hm.beat(uuid4())

    async def test_is_alive_without_connect_raises(self) -> None:
        """Checking liveness without connecting should raise RuntimeError."""
        hm = HeartbeatMonitor("redis://fake")
        with pytest.raises(RuntimeError, match="not connected"):
            await hm.is_alive(uuid4())
