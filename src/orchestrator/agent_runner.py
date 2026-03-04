"""Stub for AgentRunner — spawn/stop/monitor Claude Code agent processes.

This module will be fully implemented by Agent 05 (Agent Runner).
The Orchestrator and Scheduler depend on its interface.
"""

from __future__ import annotations

import logging
import uuid
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Callable, Coroutine

from orchestrator.enums import AgentStatus, AgentType

logger = logging.getLogger(__name__)


@dataclass
class AgentProcess:
    """Tracks a running agent subprocess."""

    agent_id: uuid.UUID
    agent_type: AgentType
    worktree_path: Path
    branch: str
    pid: int | None = None
    log_path: Path | None = None
    exit_code: int | None = None


class AgentRunner:
    """Manages Claude Code agent processes.

    Provides methods to spawn, stop, and monitor agent subprocesses.
    Each agent runs as a Claude Code CLI session in its own worktree.
    """

    def __init__(
        self,
        claude_bin: str = "claude",
        max_concurrent: int = 5,
        on_completed: Callable[
            [uuid.UUID], Coroutine[Any, Any, None]
        ]
        | None = None,
        on_failed: Callable[
            [uuid.UUID], Coroutine[Any, Any, None]
        ]
        | None = None,
    ) -> None:
        self.claude_bin = claude_bin
        self.max_concurrent = max_concurrent
        self.on_completed = on_completed
        self.on_failed = on_failed
        self._processes: dict[uuid.UUID, AgentProcess] = {}

    async def spawn(
        self,
        agent_id: uuid.UUID,
        agent_type: AgentType,
        worktree_path: Path,
        branch: str,
        prompt: str,
    ) -> AgentProcess:
        """Spawn a new Claude Code agent process.

        Args:
            agent_id: UUID of the Agent record.
            agent_type: Type of agent (planner, coder, researcher).
            worktree_path: Path to the worktree for the agent.
            branch: Git branch the agent operates on.
            prompt: Initial prompt / task description for the agent.

        Returns:
            AgentProcess tracking the spawned subprocess.
        """
        process = AgentProcess(
            agent_id=agent_id,
            agent_type=agent_type,
            worktree_path=worktree_path,
            branch=branch,
        )
        self._processes[agent_id] = process
        logger.info(f"Spawned agent {agent_id} ({agent_type.value}) in {worktree_path}")
        return process

    async def stop(self, agent_id: uuid.UUID) -> None:
        """Stop a running agent process.

        Args:
            agent_id: UUID of the agent to stop.
        """
        process = self._processes.pop(agent_id, None)
        if process:
            logger.info(f"Stopped agent {agent_id}")

    async def get_status(self, agent_id: uuid.UUID) -> AgentStatus:
        """Check the status of an agent process.

        Args:
            agent_id: UUID of the agent to check.

        Returns:
            AgentStatus reflecting the process state.
        """
        process = self._processes.get(agent_id)
        if process is None:
            return AgentStatus.DEAD
        if process.exit_code is not None:
            return AgentStatus.IDLE
        return AgentStatus.WORKING

    async def get_output(self, agent_id: uuid.UUID) -> str:
        """Read the output/log of an agent process.

        Args:
            agent_id: UUID of the agent.

        Returns:
            The agent's stdout/log content.
        """
        process = self._processes.get(agent_id)
        if process and process.log_path and process.log_path.exists():
            return process.log_path.read_text()
        return ""

    async def list_running(self) -> list[AgentProcess]:
        """List all currently tracked agent processes."""
        return list(self._processes.values())

    async def cleanup_stale(self, timeout_seconds: int = 300) -> list[uuid.UUID]:
        """Find and clean up agents that appear stale.

        Args:
            timeout_seconds: Seconds after which an unresponsive agent is stale.

        Returns:
            List of agent IDs that were cleaned up.
        """
        # Stub: real implementation will check heartbeats / process liveness
        return []

    @property
    def active_count(self) -> int:
        """Number of currently active agent processes."""
        return len(self._processes)

    @property
    def can_spawn(self) -> bool:
        """Whether we can spawn another agent without exceeding the limit."""
        return self.active_count < self.max_concurrent
