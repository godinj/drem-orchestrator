# Agent: Memory & Compaction

You are working on **Drem Orchestrator**, a Python application that orchestrates Claude Code agents
working in parallel across git worktrees. Your task is to build the memory persistence and context
compaction system that allows the orchestrator and agents to maintain durable state across restarts.

## Context

Read these before starting:
- `CLAUDE.md` (project conventions)
- `src/orchestrator/models.py` (Memory model — content, memory_type, agent_id, task_id)
- `src/orchestrator/models.py` (Agent model — memory_summary field)
- `src/orchestrator/config.py` (CONTEXT_COMPACTION_THRESHOLD)
- `src/orchestrator/db.py` (async session)

## Dependencies

This agent depends on Agent 02 (Data Model).
If those files don't exist yet, create stubs with the interfaces and implement against them.

## Deliverables

### New files (`src/orchestrator/`)

#### 1. `memory.py`

Memory management — storing, retrieving, and compacting agent memories.

```python
class MemoryManager:
    def __init__(self, db_session_factory, claude_bin: Path | None = None):
        pass

    async def store_memory(
        self,
        agent_id: UUID,
        content: str,
        memory_type: str,
        task_id: UUID | None = None,
        metadata: dict | None = None,
    ) -> Memory:
        """Create a Memory record in the database."""

    async def get_memories(
        self,
        agent_id: UUID | None = None,
        task_id: UUID | None = None,
        memory_type: str | None = None,
        limit: int = 50,
    ) -> list[Memory]:
        """
        Retrieve memories, ordered by created_at desc.
        Filter by agent, task, and/or type.
        """

    async def get_project_memories(
        self,
        project_id: UUID,
        memory_types: list[str] | None = None,
        limit: int = 100,
    ) -> list[Memory]:
        """
        Get memories across all agents in a project.
        Useful for building shared context.
        """

    async def compact_agent_memory(self, agent_id: UUID) -> str:
        """
        Summarize an agent's memories into a compact summary.

        1. Fetch all memories for this agent, ordered chronologically
        2. Group by memory_type
        3. Build a structured summary:
           - Key decisions made
           - Files created/modified
           - Lessons learned
           - Current task state
        4. Store as a new Memory with type "conversation_summary"
        5. Update Agent.memory_summary with the compact text
        6. Archive (don't delete) older individual memories
        7. Return the summary text
        """

    async def build_agent_context(
        self,
        agent_id: UUID,
        task_id: UUID,
        max_tokens: int = 8000,
    ) -> str:
        """
        Build context string for an agent session.

        Combines:
        1. Agent's memory_summary (if exists)
        2. Recent memories for this task
        3. Project-wide decisions and lessons (cross-agent)
        4. Truncate to max_tokens (rough estimate: 4 chars per token)

        Returns formatted context string for inclusion in agent prompt.
        """

    async def extract_memories_from_output(
        self,
        agent_id: UUID,
        task_id: UUID,
        output: str,
    ) -> list[Memory]:
        """
        Parse agent output to extract structured memories.

        Looks for patterns:
        - File changes (git diff summary)
        - Decisions ("decided to...", "chose...", "approach:")
        - Blockers ("blocked by...", "need...", "waiting for...")
        - Completions ("completed", "finished", "done:")

        Creates Memory records for each extracted item.
        Returns the created memories.
        """
```

#### 2. `compaction.py`

Orchestrator-level compaction — manages the orchestrator's own context window.

```python
class OrchestratorCompaction:
    def __init__(
        self,
        memory_manager: MemoryManager,
        compaction_threshold: float = 0.7,
    ):
        pass

    async def save_orchestrator_state(self, orchestrator_agent_id: UUID) -> None:
        """
        Periodic checkpoint of orchestrator state.

        Saves:
        - Current task assignments (which agent has which task)
        - Pending decisions and their context
        - Recent events summary
        - Known blockers and their status

        Stored as Memory records with type "orchestrator_state".
        """

    async def restore_orchestrator_state(
        self, orchestrator_agent_id: UUID
    ) -> OrchestratorSnapshot:
        """
        Restore orchestrator state after restart.

        1. Load latest "orchestrator_state" memory
        2. Load all in-progress tasks
        3. Load all active agents and their heartbeats
        4. Reconcile: mark agents with stale heartbeats as DEAD
        5. Return snapshot for orchestrator to resume from
        """

    async def should_compact(self, agent_id: UUID) -> bool:
        """
        Determine if an agent's memory needs compaction.
        Based on total memory count and estimated token size.
        Returns True if over threshold.
        """

@dataclass
class OrchestratorSnapshot:
    active_tasks: list[Task]          # IN_PROGRESS tasks
    pending_reviews: list[Task]       # PLAN_REVIEW and TESTING_READY tasks
    active_agents: list[Agent]        # WORKING agents
    stale_agents: list[Agent]         # agents marked DEAD during restore
    last_checkpoint: datetime | None
```

### Tests

#### 3. `tests/test_memory.py`

- `test_store_and_retrieve` — store memory, retrieve by agent_id
- `test_filter_by_type` — store multiple types, filter correctly
- `test_project_memories` — memories from multiple agents in same project
- `test_compact_agent_memory` — verify summary created, agent.memory_summary updated
- `test_build_agent_context` — verify context includes summary + recent memories
- `test_context_truncation` — verify max_tokens respected

#### 4. `tests/test_compaction.py`

- `test_save_and_restore_state` — save checkpoint, restore, verify snapshot
- `test_restore_marks_stale_agents` — old heartbeat → stale_agents list
- `test_should_compact` — verify threshold logic

## Build Verification

```bash
uv sync
uv run pytest tests/test_memory.py tests/test_compaction.py -v
```
