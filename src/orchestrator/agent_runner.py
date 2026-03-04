"""Manages the lifecycle of Claude Code agent processes.

Spawns Claude Code CLI sessions in git worktrees, monitors them via
heartbeats, and handles graceful shutdown and stale-agent cleanup.
"""

from __future__ import annotations

import asyncio
import logging
import signal
import uuid
from dataclasses import dataclass, field
from datetime import UTC, datetime, timedelta
from pathlib import Path

from sqlalchemy import select, update
from sqlalchemy.ext.asyncio import AsyncSession, async_sessionmaker

from orchestrator.agent_prompt import write_prompt_file
from orchestrator.models import Agent, Task
from orchestrator.worktree import WorktreeManager

logger = logging.getLogger(__name__)


def _utcnow() -> datetime:
    return datetime.now(UTC)


@dataclass
class AgentProcess:
    """In-memory record of a running Claude Code agent process."""

    agent_id: uuid.UUID
    process: asyncio.subprocess.Process
    worktree_path: Path
    branch: str
    task_id: uuid.UUID
    started_at: datetime
    log_path: Path
    _monitor_task: asyncio.Task[None] | None = field(default=None, repr=False)
    _heartbeat_task: asyncio.Task[None] | None = field(default=None, repr=False)


class AgentRunner:
    """Spawn, monitor, and manage Claude Code CLI sessions in worktrees."""

    def __init__(
        self,
        worktree_manager: WorktreeManager,
        db_session_factory: async_sessionmaker[AsyncSession],
        claude_bin: Path,
        max_concurrent: int = 5,
    ) -> None:
        self._worktree_manager = worktree_manager
        self._db_session_factory = db_session_factory
        self._claude_bin = claude_bin
        self._max_concurrent = max_concurrent
        self._processes: dict[uuid.UUID, AgentProcess] = {}
        self._semaphore = asyncio.Semaphore(max_concurrent)
        self._completion_events: dict[uuid.UUID, asyncio.Event] = {}

    @property
    def can_spawn(self) -> bool:
        """Whether we can spawn another agent (haven't hit max concurrency)."""
        return len(self._processes) < self._max_concurrent

    async def get_status(self, agent_id: uuid.UUID) -> str:
        """Get agent status -- check in-memory process first, fall back to DB."""
        proc = self._processes.get(agent_id)
        if proc is not None:
            if proc.process.returncode is None:
                return "working"
            return "idle" if proc.process.returncode == 0 else "dead"
        # Not tracked in memory — check DB
        async with self._db_session_factory() as session:
            agent = await session.get(Agent, agent_id)
            if agent is not None:
                return agent.status
        return "dead"

    async def spawn(
        self,
        agent_id: uuid.UUID,
        task_id: uuid.UUID,
        worktree_path: Path,
        branch: str,
        prompt: str,
    ) -> None:
        """Start a subprocess for an already-created Agent record.

        Unlike spawn_agent() which creates everything, this just launches
        the CLI process and starts monitoring.
        """
        await self._semaphore.acquire()

        try:
            # Write prompt file
            prompt_path = write_prompt_file(worktree_path, prompt)

            # Prepare log file
            log_dir = worktree_path / ".claude"
            log_dir.mkdir(parents=True, exist_ok=True)
            log_path = log_dir / "agent.log"

            # Launch Claude Code process
            log_file = open(log_path, "w")  # noqa: SIM115
            prompt_file = open(prompt_path)  # noqa: SIM115
            process = await asyncio.create_subprocess_exec(
                str(self._claude_bin),
                "-p",
                "--dangerously-skip-permissions",
                cwd=worktree_path,
                stdin=prompt_file,
                stdout=log_file,
                stderr=log_file,
            )

            # Track the process
            agent_process = AgentProcess(
                agent_id=agent_id,
                process=process,
                worktree_path=worktree_path,
                branch=branch,
                task_id=task_id,
                started_at=_utcnow(),
                log_path=log_path,
            )
            self._processes[agent_id] = agent_process

            # Create a completion event
            self._completion_events[agent_id] = asyncio.Event()

            # Start background monitoring and heartbeat
            agent_process._monitor_task = asyncio.create_task(
                self._monitor_agent(agent_id)
            )
            agent_process._heartbeat_task = asyncio.create_task(
                self._heartbeat_loop(agent_id)
            )

            logger.info("Spawned agent %s (low-level) in %s", agent_id, worktree_path)

        except Exception:
            self._semaphore.release()
            raise

    async def spawn_agent(
        self,
        task: Task,
        feature_name: str,
        agent_type: str,
        prompt: str,
    ) -> Agent:
        """Spawn a Claude Code agent in a new agent worktree.

        1. Acquire semaphore slot (blocks if at max concurrency)
        2. Create agent worktree via WorktreeManager.create_agent_worktree()
        3. Create Agent record in DB (status: working)
        4. Write prompt to a temp file in the worktree
        5. Launch: claude --agent <prompt_file> in the worktree directory
        6. Capture stdout/stderr to log file at <worktree>/.claude/agent.log
        7. Start monitoring task (heartbeat + completion detection)
        8. Return Agent record
        """
        await self._semaphore.acquire()

        try:
            # Create worktree
            worktree_info = await self._worktree_manager.create_agent_worktree(
                feature_name
            )

            # Create Agent record in DB
            agent = Agent(
                id=uuid.uuid4(),
                project_id=task.project_id,
                agent_type=agent_type,
                name=f"{agent_type}-{worktree_info.branch}",
                status="working",
                current_task_id=task.id,
                worktree_path=str(worktree_info.path),
                worktree_branch=worktree_info.branch,
                heartbeat_at=_utcnow(),
            )

            async with self._db_session_factory() as session:
                session.add(agent)
                await session.commit()
                await session.refresh(agent)

            # Write prompt file
            prompt_path = write_prompt_file(worktree_info.path, prompt)

            # Prepare log file
            log_dir = worktree_info.path / ".claude"
            log_dir.mkdir(parents=True, exist_ok=True)
            log_path = log_dir / "agent.log"

            # Launch Claude Code process
            log_file = open(log_path, "w")  # noqa: SIM115
            prompt_file = open(prompt_path)  # noqa: SIM115
            process = await asyncio.create_subprocess_exec(
                str(self._claude_bin),
                "-p",
                "--dangerously-skip-permissions",
                cwd=worktree_info.path,
                stdin=prompt_file,
                stdout=log_file,
                stderr=log_file,
            )

            # Track the process
            agent_process = AgentProcess(
                agent_id=agent.id,
                process=process,
                worktree_path=worktree_info.path,
                branch=worktree_info.branch,
                task_id=task.id,
                started_at=_utcnow(),
                log_path=log_path,
            )
            self._processes[agent.id] = agent_process

            # Create a completion event for this agent
            self._completion_events[agent.id] = asyncio.Event()

            # Start background monitoring and heartbeat tasks
            agent_process._monitor_task = asyncio.create_task(
                self._monitor_agent(agent.id)
            )
            agent_process._heartbeat_task = asyncio.create_task(
                self._heartbeat_loop(agent.id)
            )

            logger.info(
                "Spawned agent %s (type=%s) in %s",
                agent.id,
                agent_type,
                worktree_info.path,
            )

            return agent

        except Exception:
            # Release semaphore if spawn failed
            self._semaphore.release()
            raise

    async def stop_agent(self, agent_id: uuid.UUID, force: bool = False) -> None:
        """Stop a running agent.

        1. Send SIGTERM to the process
        2. Wait up to 10s for graceful exit
        3. If force or timeout: SIGKILL
        4. Update Agent status to DEAD in DB
        5. Release semaphore slot
        """
        agent_process = self._processes.get(agent_id)
        if agent_process is None:
            logger.warning("stop_agent called for unknown agent %s", agent_id)
            return

        process = agent_process.process

        # Cancel the monitor and heartbeat tasks
        if agent_process._heartbeat_task is not None:
            agent_process._heartbeat_task.cancel()
        if agent_process._monitor_task is not None:
            agent_process._monitor_task.cancel()

        # Send SIGTERM
        if process.returncode is None:
            try:
                process.send_signal(signal.SIGTERM)
            except ProcessLookupError:
                pass

            if force:
                # Immediately kill
                try:
                    process.kill()
                except ProcessLookupError:
                    pass
            else:
                # Wait up to 10 seconds for graceful exit
                try:
                    await asyncio.wait_for(process.wait(), timeout=10.0)
                except TimeoutError:
                    try:
                        process.kill()
                    except ProcessLookupError:
                        pass

        # Update DB status to dead
        async with self._db_session_factory() as session:
            await session.execute(
                update(Agent)
                .where(Agent.id == agent_id)
                .values(status="dead", heartbeat_at=_utcnow())
            )
            await session.commit()

        # Clean up
        self._processes.pop(agent_id, None)
        self._semaphore.release()

        # Signal completion
        event = self._completion_events.pop(agent_id, None)
        if event is not None:
            event.set()

        logger.info("Stopped agent %s", agent_id)

    async def get_agent_output(self, agent_id: uuid.UUID) -> str:
        """Read the agent's log file and return contents."""
        agent_process = self._processes.get(agent_id)
        if agent_process is None:
            # Try to find log from DB record
            async with self._db_session_factory() as session:
                result = await session.execute(
                    select(Agent).where(Agent.id == agent_id)
                )
                agent = result.scalar_one_or_none()
                if agent is not None and agent.worktree_path is not None:
                    log_path = Path(agent.worktree_path) / ".claude" / "agent.log"
                    if log_path.exists():
                        return log_path.read_text()
            return ""

        if agent_process.log_path.exists():
            return agent_process.log_path.read_text()
        return ""

    async def get_running_agents(self) -> list[AgentProcess]:
        """Return list of currently running agent processes."""
        return list(self._processes.values())

    async def _monitor_agent(self, agent_id: uuid.UUID) -> None:
        """Background task per agent.

        1. Wait for process to exit
        2. On exit:
           a. Read exit code
           b. If success (0): update Agent status to idle
           c. If failure (non-0): update Agent status to dead, log error
           d. Release semaphore slot
           e. Clean up from _processes dict
           f. Signal completion event
        """
        agent_process = self._processes.get(agent_id)
        if agent_process is None:
            return

        try:
            # Wait for the process to finish
            return_code = await agent_process.process.wait()

            # Cancel heartbeat loop since process is done
            if agent_process._heartbeat_task is not None:
                agent_process._heartbeat_task.cancel()

            # Update DB based on exit code
            if return_code == 0:
                new_status = "idle"
                logger.info(
                    "Agent %s completed successfully (exit code 0)", agent_id
                )
            else:
                new_status = "dead"
                logger.error(
                    "Agent %s failed with exit code %d", agent_id, return_code
                )

            async with self._db_session_factory() as session:
                await session.execute(
                    update(Agent)
                    .where(Agent.id == agent_id)
                    .values(status=new_status, heartbeat_at=_utcnow())
                )
                await session.commit()

        except asyncio.CancelledError:
            # Monitor was cancelled (e.g., by stop_agent), just exit
            return
        finally:
            # Release semaphore and clean up
            self._processes.pop(agent_id, None)
            self._semaphore.release()

            # Signal completion
            event = self._completion_events.pop(agent_id, None)
            if event is not None:
                event.set()

    async def _heartbeat_loop(self, agent_id: uuid.UUID) -> None:
        """Update heartbeat_at every 30s while agent is alive."""
        try:
            while True:
                await asyncio.sleep(30)

                agent_process = self._processes.get(agent_id)
                if agent_process is None:
                    break

                # Check if process is still running
                if agent_process.process.returncode is not None:
                    break

                async with self._db_session_factory() as session:
                    await session.execute(
                        update(Agent)
                        .where(Agent.id == agent_id)
                        .values(heartbeat_at=_utcnow())
                    )
                    await session.commit()

        except asyncio.CancelledError:
            return

    async def cleanup_stale_agents(self, timeout_seconds: int = 300) -> list[uuid.UUID]:
        """Find agents with heartbeat older than timeout.

        Mark them DEAD, kill processes if still running, release resources.
        Returns list of cleaned up agent IDs.
        """
        cutoff = _utcnow() - timedelta(seconds=timeout_seconds)
        cleaned: list[uuid.UUID] = []

        async with self._db_session_factory() as session:
            result = await session.execute(
                select(Agent).where(
                    Agent.status == "working",
                    Agent.heartbeat_at < cutoff,
                )
            )
            stale_agents = result.scalars().all()

        for agent in stale_agents:
            agent_process = self._processes.get(agent.id)
            if agent_process is not None:
                # Kill the process if still running
                if agent_process.process.returncode is None:
                    try:
                        agent_process.process.kill()
                    except ProcessLookupError:
                        pass

                # Cancel background tasks
                if agent_process._heartbeat_task is not None:
                    agent_process._heartbeat_task.cancel()
                if agent_process._monitor_task is not None:
                    agent_process._monitor_task.cancel()

                self._processes.pop(agent.id, None)
                self._semaphore.release()

            # Update DB
            async with self._db_session_factory() as session:
                await session.execute(
                    update(Agent)
                    .where(Agent.id == agent.id)
                    .values(status="dead", heartbeat_at=_utcnow())
                )
                await session.commit()

            cleaned.append(agent.id)
            logger.info("Cleaned up stale agent %s", agent.id)

        return cleaned
