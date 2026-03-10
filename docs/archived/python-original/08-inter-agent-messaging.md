# Agent: Inter-Agent Messaging

You are working on **Drem Orchestrator**, a Python application that orchestrates Claude Code agents
working in parallel across git worktrees. Your task is to build the Redis pub/sub messaging system
that enables agents to communicate with each other and the orchestrator.

## Context

Read these before starting:
- `CLAUDE.md` (project conventions)
- `src/orchestrator/models.py` (Agent model — id, project_id, current_task_id)
- `src/orchestrator/agent_runner.py` (AgentRunner — needs to deliver messages to agents)
- `src/orchestrator/config.py` (REDIS_URL)

## Dependencies

This agent depends on Agent 02 (Data Model) and Agent 05 (Agent Runner).
If those files don't exist yet, create stubs with the interfaces and implement against them.

## Deliverables

### New files (`src/orchestrator/`)

#### 1. `messaging.py`

Redis pub/sub messaging system for inter-agent communication.

```python
import redis.asyncio as redis
from dataclasses import dataclass
from enum import Enum

class MessageType(str, Enum):
    FILE_CHANGED = "file_changed"       # agent modified a file
    BLOCKED = "blocked"                 # agent is blocked on something
    CONTEXT_SHARE = "context_share"     # sharing knowledge with team
    REVIEW_REQUEST = "review_request"   # asking for code review
    TASK_COMPLETED = "task_completed"   # agent finished its task
    TASK_FAILED = "task_failed"         # agent failed its task
    OVERLAP_WARNING = "overlap_warning" # two agents touching same files

@dataclass
class AgentMessage:
    type: MessageType
    sender_id: UUID
    sender_name: str
    project_id: UUID
    content: dict          # type-specific payload
    timestamp: datetime

class MessageBus:
    def __init__(self, redis_url: str):
        self._redis: redis.Redis | None = None
        self._subscriptions: dict[str, list[Callable]] = {}

    async def connect(self) -> None:
        """Initialize Redis connection and pubsub."""

    async def disconnect(self) -> None:
        """Close Redis connection."""

    async def publish(self, channel: str, message: AgentMessage) -> None:
        """
        Publish a message to a channel.
        Serializes AgentMessage to JSON.
        """

    async def subscribe(
        self, channel: str, callback: Callable[[AgentMessage], Awaitable[None]]
    ) -> None:
        """
        Subscribe to a channel with an async callback.
        The callback is invoked for each message received.
        """

    async def unsubscribe(self, channel: str) -> None:
        """Unsubscribe from a channel."""

    async def listen(self) -> None:
        """
        Main listen loop. Runs as a background task.
        Dispatches received messages to registered callbacks.
        """

    # --- Convenience methods for standard channels ---

    def project_channel(self, project_id: UUID) -> str:
        """Returns: project:{project_id}:broadcast"""

    def feature_channel(self, project_id: UUID, feature: str) -> str:
        """Returns: project:{project_id}:feature:{feature}"""

    def agent_channel(self, agent_id: UUID) -> str:
        """Returns: agent:{agent_id}:inbox"""

    async def broadcast_to_project(
        self, project_id: UUID, message: AgentMessage
    ) -> None:
        """Publish to the project broadcast channel."""

    async def send_to_feature(
        self, project_id: UUID, feature: str, message: AgentMessage
    ) -> None:
        """Publish to a feature-specific channel."""

    async def send_to_agent(
        self, agent_id: UUID, message: AgentMessage
    ) -> None:
        """Publish to an agent's direct inbox."""
```

#### 2. `overlap_detector.py`

Detects when multiple agents are working on overlapping files.

```python
class OverlapDetector:
    def __init__(self, message_bus: MessageBus, db_session_factory):
        # Track which agents are modifying which files
        self._file_owners: dict[str, set[UUID]] = {}  # file_path → set of agent_ids

    async def start(self, project_id: UUID) -> None:
        """
        Subscribe to the project broadcast channel.
        Listen for FILE_CHANGED messages.
        """

    async def on_file_changed(self, message: AgentMessage) -> None:
        """
        Handle a FILE_CHANGED message.

        1. Extract file path from message.content
        2. Add sender to _file_owners[path]
        3. If multiple agents own the same file:
           a. Create OVERLAP_WARNING message
           b. Send to all agents working on that file
           c. Log the overlap for the orchestrator
        """

    async def get_overlaps(self) -> list[FileOverlap]:
        """Return all current file overlaps."""

    async def clear_agent(self, agent_id: UUID) -> None:
        """Remove an agent from all file ownership (on task completion)."""

@dataclass
class FileOverlap:
    file_path: str
    agent_ids: list[UUID]
    agent_names: list[str]
    feature: str
```

#### 3. `heartbeat.py`

Redis-based agent heartbeat system (faster than DB polling).

```python
class HeartbeatMonitor:
    def __init__(self, redis_url: str, timeout_seconds: int = 120):
        self._redis: redis.Redis | None = None
        self._timeout = timeout_seconds

    async def connect(self) -> None:
        """Initialize Redis connection."""

    async def beat(self, agent_id: UUID) -> None:
        """
        Record a heartbeat for an agent.
        Uses Redis SET with TTL = timeout_seconds.
        Key: heartbeat:{agent_id}
        Value: ISO timestamp
        """

    async def is_alive(self, agent_id: UUID) -> bool:
        """Check if an agent's heartbeat key exists (not expired)."""

    async def get_stale_agents(self, agent_ids: list[UUID]) -> list[UUID]:
        """Return agent_ids whose heartbeat has expired."""

    async def get_last_heartbeat(self, agent_id: UUID) -> datetime | None:
        """Get the timestamp of the last heartbeat, or None if expired."""
```

### Tests

#### 4. `tests/test_messaging.py`

Tests using a real Redis instance (or fakeredis if available):

- `test_publish_subscribe` — publish message, verify callback invoked
- `test_project_broadcast` — multiple subscribers receive broadcast
- `test_agent_direct_message` — message to specific agent inbox
- `test_message_serialization` — AgentMessage round-trips through JSON

If Redis is not available in test environment, use `fakeredis[aioredis]` as a dependency.

#### 5. `tests/test_overlap_detector.py`

- `test_no_overlap` — two agents, different files, no warning
- `test_overlap_detected` — two agents modify same file, warning sent
- `test_clear_agent` — agent completes, removed from tracking
- `test_multiple_overlaps` — complex scenario with 3 agents

#### 6. `tests/test_heartbeat.py`

- `test_beat_and_alive` — beat, verify alive
- `test_expired_heartbeat` — don't beat, verify stale after timeout
- `test_get_stale_agents` — mix of alive and stale

## Build Verification

```bash
uv sync
uv run pytest tests/test_messaging.py tests/test_overlap_detector.py tests/test_heartbeat.py -v
```
