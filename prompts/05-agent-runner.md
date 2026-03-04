# Agent: Agent Runner

You are working on **Drem Orchestrator**, a Python application that orchestrates Claude Code agents
working in parallel across git worktrees. Your task is to build the agent runner — the module
that spawns, monitors, and manages Claude Code CLI sessions running in worktrees.

## Context

Read these before starting:
- `CLAUDE.md` (project conventions)
- `src/orchestrator/models.py` (Agent model — status, worktree_path, heartbeat_at)
- `src/orchestrator/worktree.py` (WorktreeManager — create/remove agent worktrees)
- `src/orchestrator/config.py` (CLAUDE_BIN, MAX_CONCURRENT_AGENTS)
- `src/orchestrator/db.py` (async session)

## Dependencies

This agent depends on Agent 02 (Data Model) and Agent 03 (Worktree Integration).
If those files don't exist yet, create stubs with the interfaces described and implement against them.

## Deliverables

### New files (`src/orchestrator/`)

#### 1. `agent_runner.py`

Manages the lifecycle of Claude Code agent processes.

```python
from dataclasses import dataclass, field
from pathlib import Path
import asyncio

@dataclass
class AgentProcess:
    agent_id: UUID
    process: asyncio.subprocess.Process
    worktree_path: Path
    branch: str
    task_id: UUID
    started_at: datetime
    log_path: Path          # stdout/stderr captured to file

class AgentRunner:
    def __init__(
        self,
        worktree_manager: WorktreeManager,
        db_session_factory,
        claude_bin: Path,
        max_concurrent: int = 5,
    ):
        self._processes: dict[UUID, AgentProcess] = {}
        self._semaphore = asyncio.Semaphore(max_concurrent)

    async def spawn_agent(
        self,
        task: Task,
        feature_name: str,
        agent_type: AgentType,
        prompt: str,
    ) -> Agent:
        """
        Spawn a Claude Code agent in a new agent worktree.

        1. Acquire semaphore slot (blocks if at max concurrency)
        2. Create agent worktree via WorktreeManager.create_agent_worktree()
        3. Create Agent record in DB (status: WORKING)
        4. Write prompt to a temp file in the worktree
        5. Launch: claude --agent <prompt_file> in the worktree directory
        6. Capture stdout/stderr to log file at <worktree>/.claude/agent.log
        7. Start monitoring task (heartbeat + completion detection)
        8. Return Agent record
        """

    async def stop_agent(self, agent_id: UUID, force: bool = False) -> None:
        """
        Stop a running agent.
        1. Send SIGTERM to the process
        2. Wait up to 10s for graceful exit
        3. If force or timeout: SIGKILL
        4. Update Agent status to DEAD in DB
        5. Release semaphore slot
        """

    async def get_agent_output(self, agent_id: UUID) -> str:
        """Read the agent's log file and return contents."""

    async def get_running_agents(self) -> list[AgentProcess]:
        """Return list of currently running agent processes."""

    async def _monitor_agent(self, agent_id: UUID) -> None:
        """
        Background task per agent:
        1. Periodically update heartbeat_at in DB (every 30s)
        2. Wait for process to exit
        3. On exit:
           a. Read exit code
           b. If success (0): update Agent status to IDLE, update Task
           c. If failure (non-0): update Agent status to DEAD, log error
           d. Release semaphore slot
           e. Clean up from _processes dict
           f. Emit completion event
        """

    async def _heartbeat_loop(self, agent_id: UUID) -> None:
        """Update heartbeat_at every 30s while agent is alive."""

    async def cleanup_stale_agents(self, timeout_seconds: int = 300) -> list[UUID]:
        """
        Find agents with heartbeat older than timeout.
        Mark them DEAD, kill processes if still running, release resources.
        Returns list of cleaned up agent IDs.
        """
```

#### 2. `agent_prompt.py`

Generates the prompt file that gets passed to `claude --agent`.

```python
def generate_agent_prompt(
    task: Task,
    project: Project,
    agent_type: AgentType,
    worktree_path: Path,
    memories: list[Memory] | None = None,
    parent_context: dict | None = None,
) -> str:
    """
    Build the full prompt for a Claude Code agent session.

    Includes:
    - Task description and acceptance criteria
    - Project context (from project.description and task.context)
    - Relevant memories from prior work
    - Worktree info (branch, path)
    - Build/test commands from CLAUDE.md
    - Scope limitation: only modify files relevant to this task
    - Instruction to commit work and report completion

    For coder agents:
    - Include file list to modify
    - Include test expectations
    - Instruct to run build verification

    For researcher agents:
    - Include research questions
    - Instruct to write findings to a report file

    For planner agents:
    - Include high-level task description
    - Instruct to decompose into subtasks with file lists
    """

def write_prompt_file(worktree_path: Path, prompt: str) -> Path:
    """Write prompt to <worktree>/.claude/agent-prompt.md, return path."""
```

### Tests

#### 3. `tests/test_agent_runner.py`

- `test_spawn_agent` — mock subprocess, verify Agent record created with WORKING status
- `test_agent_completion` — mock process exit(0), verify status → IDLE
- `test_agent_failure` — mock process exit(1), verify status → DEAD
- `test_max_concurrency` — spawn MAX+1 agents, verify last one waits
- `test_stop_agent` — verify SIGTERM sent, cleanup occurs
- `test_cleanup_stale` — set old heartbeat, verify cleanup

Use `unittest.mock.AsyncMock` for subprocess mocking. Do not spawn real Claude sessions in tests.

#### 4. `tests/test_agent_prompt.py`

- `test_coder_prompt_includes_task` — verify task description in output
- `test_researcher_prompt_format` — verify research-specific instructions
- `test_memories_included` — verify prior memories appear in prompt
- `test_prompt_file_written` — verify file created at expected path

## Build Verification

```bash
uv sync
uv run pytest tests/test_agent_runner.py tests/test_agent_prompt.py -v
```
