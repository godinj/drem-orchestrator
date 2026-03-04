"""Tests for agent_runner.py — agent process lifecycle management.

Uses mocks for subprocess and database to avoid spawning real Claude sessions.
"""

from __future__ import annotations

import asyncio
import signal
import uuid
from datetime import UTC, datetime, timedelta
from pathlib import Path
from unittest.mock import AsyncMock, MagicMock, patch

from sqlalchemy import Update

from orchestrator.agent_runner import AgentRunner
from orchestrator.models import Agent, Task
from orchestrator.worktree import AgentWorktreeInfo


def _utcnow() -> datetime:
    return datetime.now(UTC)


def _make_task(
    project_id: uuid.UUID | None = None,
    task_id: uuid.UUID | None = None,
) -> Task:
    """Create a mock Task object."""
    task = MagicMock(spec=Task)
    task.id = task_id or uuid.uuid4()
    task.project_id = project_id or uuid.uuid4()
    task.title = "Implement feature X"
    task.description = "Build feature X with full test coverage"
    task.status = "in_progress"
    task.context = {"key": "value"}
    task.plan = {"files": ["src/feature.py"]}
    task.test_plan = "Run pytest"
    return task


def _make_worktree_info(tmp_path: Path) -> AgentWorktreeInfo:
    """Create a mock AgentWorktreeInfo."""
    wt_path = tmp_path / ".claude" / "worktrees" / f"agent-{uuid.uuid4().hex[:8]}"
    wt_path.mkdir(parents=True, exist_ok=True)
    return AgentWorktreeInfo(
        path=wt_path,
        branch=f"worktree-agent-{uuid.uuid4().hex[:8]}",
        head="a" * 40,
        parent_feature="feature/test",
    )


class _MockSessionContext:
    """A synchronous callable that returns an async context manager.

    Mimics the behaviour of ``async_sessionmaker``: calling the factory
    returns an object that can be used as ``async with factory() as session``.
    """

    def __init__(self, session: AsyncMock) -> None:
        self.session = session
        self.executed_statements: list = []

    def __call__(self) -> _MockSessionContext:
        return self

    async def __aenter__(self) -> AsyncMock:
        return self.session

    async def __aexit__(self, *args: object) -> bool:
        return False


def _extract_update_status(calls: list) -> list[str]:
    """Extract status values from SQLAlchemy Update statements in execute calls.

    Inspects the positional args of each mock execute() call, looks for
    SQLAlchemy Update constructs, and pulls out the ``status`` value from
    the compiled parameters.
    """
    statuses: list[str] = []
    for call in calls:
        args = call[0] if call[0] else ()
        for arg in args:
            if isinstance(arg, Update):
                # Access the compile-state parameters dict
                params = arg.compile().params
                if "status" in params:
                    statuses.append(params["status"])
    return statuses


def _make_db_session_factory() -> _MockSessionContext:
    """Create a mock async session factory that supports context manager usage."""
    session = AsyncMock()
    session.add = MagicMock()
    session.commit = AsyncMock()
    session.refresh = AsyncMock()
    session.execute = AsyncMock()

    return _MockSessionContext(session)


def _make_worktree_manager(worktree_info: AgentWorktreeInfo) -> AsyncMock:
    """Create a mock WorktreeManager."""
    manager = AsyncMock()
    manager.create_agent_worktree = AsyncMock(return_value=worktree_info)
    return manager


def _make_blocking_process() -> AsyncMock:
    """Create a mock process whose wait() blocks until explicitly told to exit.

    Returns (process_mock, exit_event).  Set exit_event to release wait().
    ``process_mock.returncode`` starts as ``None`` and must be set before
    signalling the event if you want a specific exit code.
    """
    process = AsyncMock()
    process.returncode = None
    process.pid = 12345
    process.send_signal = MagicMock()
    process.kill = MagicMock()

    exit_event = asyncio.Event()

    async def _blocking_wait() -> int:
        await exit_event.wait()
        return process.returncode if process.returncode is not None else 0

    process.wait = _blocking_wait
    return process, exit_event


class TestSpawnAgent:
    async def test_spawn_agent(self, tmp_path: Path) -> None:
        """Spawning an agent should create DB record with WORKING status."""
        worktree_info = _make_worktree_info(tmp_path)
        wt_manager = _make_worktree_manager(worktree_info)
        db_factory = _make_db_session_factory()
        mock_process, exit_event = _make_blocking_process()

        runner = AgentRunner(
            worktree_manager=wt_manager,
            db_session_factory=db_factory,
            claude_bin=Path("/usr/bin/claude"),
            max_concurrent=5,
        )

        task = _make_task()

        with patch(
            "orchestrator.agent_runner.asyncio.create_subprocess_exec",
            new_callable=AsyncMock,
        ) as mock_exec:
            mock_exec.return_value = mock_process

            agent = await runner.spawn_agent(
                task=task,
                feature_name="test-feature",
                agent_type="coder",
                prompt="Build feature X",
            )

        # Verify worktree was created
        wt_manager.create_agent_worktree.assert_called_once_with("test-feature")

        # Verify Agent record was created
        assert agent.status == "working"
        assert agent.agent_type == "coder"
        assert agent.current_task_id == task.id
        assert agent.project_id == task.project_id
        assert agent.worktree_path == str(worktree_info.path)
        assert agent.worktree_branch == worktree_info.branch

        # Verify the agent is tracked in running processes
        running = await runner.get_running_agents()
        assert len(running) == 1
        assert running[0].agent_id == agent.id

        # Clean up: let monitor finish
        mock_process.returncode = 0
        exit_event.set()
        await asyncio.sleep(0.05)

    async def test_spawn_agent_writes_prompt_file(self, tmp_path: Path) -> None:
        """Spawning an agent should write prompt to the worktree."""
        worktree_info = _make_worktree_info(tmp_path)
        wt_manager = _make_worktree_manager(worktree_info)
        db_factory = _make_db_session_factory()
        mock_process, exit_event = _make_blocking_process()

        runner = AgentRunner(
            worktree_manager=wt_manager,
            db_session_factory=db_factory,
            claude_bin=Path("/usr/bin/claude"),
            max_concurrent=5,
        )

        task = _make_task()
        prompt_text = "Build feature X"

        with patch(
            "orchestrator.agent_runner.asyncio.create_subprocess_exec",
            new_callable=AsyncMock,
        ) as mock_exec:
            mock_exec.return_value = mock_process

            await runner.spawn_agent(
                task=task,
                feature_name="test-feature",
                agent_type="coder",
                prompt=prompt_text,
            )

        # Verify prompt file was written
        prompt_path = worktree_info.path / ".claude" / "agent-prompt.md"
        assert prompt_path.exists()
        assert prompt_text in prompt_path.read_text()

        # Clean up
        mock_process.returncode = 0
        exit_event.set()
        await asyncio.sleep(0.05)


class TestAgentCompletion:
    async def test_agent_completion_success(self, tmp_path: Path) -> None:
        """Agent exiting with code 0 should update status to idle."""
        worktree_info = _make_worktree_info(tmp_path)
        wt_manager = _make_worktree_manager(worktree_info)
        db_factory = _make_db_session_factory()
        mock_process, exit_event = _make_blocking_process()

        runner = AgentRunner(
            worktree_manager=wt_manager,
            db_session_factory=db_factory,
            claude_bin=Path("/usr/bin/claude"),
            max_concurrent=5,
        )

        task = _make_task()

        with patch(
            "orchestrator.agent_runner.asyncio.create_subprocess_exec",
            new_callable=AsyncMock,
        ) as mock_exec:
            mock_exec.return_value = mock_process

            await runner.spawn_agent(
                task=task,
                feature_name="test-feature",
                agent_type="coder",
                prompt="Build it",
            )

        # Trigger process exit with code 0
        mock_process.returncode = 0
        exit_event.set()
        # Give the monitor task time to process
        await asyncio.sleep(0.1)

        # Verify status was updated to idle via the Update statement params
        session = db_factory.session
        statuses = _extract_update_status(session.execute.call_args_list)
        assert "idle" in statuses, (
            f"Expected 'idle' in DB update statuses, got: {statuses}"
        )

        # Agent should be cleaned up from running processes
        running = await runner.get_running_agents()
        assert len(running) == 0

    async def test_agent_failure(self, tmp_path: Path) -> None:
        """Agent exiting with non-zero code should update status to dead."""
        worktree_info = _make_worktree_info(tmp_path)
        wt_manager = _make_worktree_manager(worktree_info)
        db_factory = _make_db_session_factory()
        mock_process, exit_event = _make_blocking_process()

        runner = AgentRunner(
            worktree_manager=wt_manager,
            db_session_factory=db_factory,
            claude_bin=Path("/usr/bin/claude"),
            max_concurrent=5,
        )

        task = _make_task()

        with patch(
            "orchestrator.agent_runner.asyncio.create_subprocess_exec",
            new_callable=AsyncMock,
        ) as mock_exec:
            mock_exec.return_value = mock_process

            await runner.spawn_agent(
                task=task,
                feature_name="test-feature",
                agent_type="coder",
                prompt="Build it",
            )

        # Trigger process failure
        mock_process.returncode = 1
        exit_event.set()
        await asyncio.sleep(0.1)

        # Verify status was updated to dead
        session = db_factory.session
        statuses = _extract_update_status(session.execute.call_args_list)
        assert "dead" in statuses, (
            f"Expected 'dead' in DB update statuses, got: {statuses}"
        )


class TestMaxConcurrency:
    async def test_max_concurrency(self, tmp_path: Path) -> None:
        """Spawning more than max_concurrent agents should block."""
        max_concurrent = 2

        worktree_infos = [_make_worktree_info(tmp_path) for _ in range(3)]
        call_count = 0

        async def _create_worktree(name: str) -> AgentWorktreeInfo:
            nonlocal call_count
            info = worktree_infos[call_count]
            call_count += 1
            return info

        wt_manager = AsyncMock()
        wt_manager.create_agent_worktree = AsyncMock(side_effect=_create_worktree)
        db_factory = _make_db_session_factory()

        runner = AgentRunner(
            worktree_manager=wt_manager,
            db_session_factory=db_factory,
            claude_bin=Path("/usr/bin/claude"),
            max_concurrent=max_concurrent,
        )

        # Create blocking processes for each agent — they won't exit until told to
        processes_and_events = [_make_blocking_process() for _ in range(3)]

        spawn_idx = 0

        with patch(
            "orchestrator.agent_runner.asyncio.create_subprocess_exec",
            new_callable=AsyncMock,
        ) as mock_exec:

            def _next_process(*args, **kwargs):
                nonlocal spawn_idx
                proc = processes_and_events[spawn_idx][0]
                spawn_idx += 1
                return proc

            mock_exec.side_effect = _next_process

            # Spawn max_concurrent agents (should not block)
            for i in range(max_concurrent):
                task = _make_task()
                await runner.spawn_agent(
                    task=task,
                    feature_name=f"feature-{i}",
                    agent_type="coder",
                    prompt=f"Build feature {i}",
                )

            # Verify we have max_concurrent running
            running = await runner.get_running_agents()
            assert len(running) == max_concurrent

            # Try to spawn one more — it should block on the semaphore
            task = _make_task()
            spawn_task = asyncio.create_task(
                runner.spawn_agent(
                    task=task,
                    feature_name="feature-blocked",
                    agent_type="coder",
                    prompt="Build blocked feature",
                )
            )

            # Give it a moment — it should still be waiting on the semaphore
            await asyncio.sleep(0.1)
            assert not spawn_task.done(), "Third spawn should be blocked by semaphore"

            # Release one slot by letting the first agent's process exit
            proc0, event0 = processes_and_events[0]
            proc0.returncode = 0
            event0.set()
            await asyncio.sleep(0.1)

            # Now the blocked spawn should have completed
            assert spawn_task.done(), (
                "Third spawn should have unblocked after first agent exited"
            )

        # Clean up remaining processes
        for proc, event in processes_and_events[1:]:
            proc.returncode = 0
            event.set()
        await asyncio.sleep(0.05)


class TestStopAgent:
    async def test_stop_agent(self, tmp_path: Path) -> None:
        """Stopping an agent should send SIGTERM and update DB."""
        worktree_info = _make_worktree_info(tmp_path)
        wt_manager = _make_worktree_manager(worktree_info)
        db_factory = _make_db_session_factory()
        mock_process, exit_event = _make_blocking_process()

        runner = AgentRunner(
            worktree_manager=wt_manager,
            db_session_factory=db_factory,
            claude_bin=Path("/usr/bin/claude"),
            max_concurrent=5,
        )

        task = _make_task()

        with patch(
            "orchestrator.agent_runner.asyncio.create_subprocess_exec",
            new_callable=AsyncMock,
        ) as mock_exec:
            mock_exec.return_value = mock_process

            agent = await runner.spawn_agent(
                task=task,
                feature_name="test-feature",
                agent_type="coder",
                prompt="Build it",
            )

        # stop_agent needs process.wait() to return quickly when SIGTERM is sent.
        # Replace the blocking wait with an immediate one.
        mock_process.wait = AsyncMock(return_value=0)

        await runner.stop_agent(agent.id)

        # Verify SIGTERM was sent
        mock_process.send_signal.assert_called_with(signal.SIGTERM)

        # Agent should be removed from running processes
        running = await runner.get_running_agents()
        assert len(running) == 0

        # Verify DB was updated to dead
        session = db_factory.session
        statuses = _extract_update_status(session.execute.call_args_list)
        assert "dead" in statuses, (
            f"Expected 'dead' in DB update statuses, got: {statuses}"
        )

    async def test_stop_agent_force(self, tmp_path: Path) -> None:
        """Force stopping should SIGKILL immediately."""
        worktree_info = _make_worktree_info(tmp_path)
        wt_manager = _make_worktree_manager(worktree_info)
        db_factory = _make_db_session_factory()
        mock_process, exit_event = _make_blocking_process()

        runner = AgentRunner(
            worktree_manager=wt_manager,
            db_session_factory=db_factory,
            claude_bin=Path("/usr/bin/claude"),
            max_concurrent=5,
        )

        task = _make_task()

        with patch(
            "orchestrator.agent_runner.asyncio.create_subprocess_exec",
            new_callable=AsyncMock,
        ) as mock_exec:
            mock_exec.return_value = mock_process

            agent = await runner.spawn_agent(
                task=task,
                feature_name="test-feature",
                agent_type="coder",
                prompt="Build it",
            )

        await runner.stop_agent(agent.id, force=True)

        # Verify SIGTERM was sent
        mock_process.send_signal.assert_called_with(signal.SIGTERM)
        # Verify SIGKILL was sent (force=True)
        mock_process.kill.assert_called_once()

    async def test_stop_unknown_agent(self, tmp_path: Path) -> None:
        """Stopping an unknown agent should be a no-op."""
        wt_manager = AsyncMock()
        db_factory = _make_db_session_factory()

        runner = AgentRunner(
            worktree_manager=wt_manager,
            db_session_factory=db_factory,
            claude_bin=Path("/usr/bin/claude"),
            max_concurrent=5,
        )

        # Should not raise
        await runner.stop_agent(uuid.uuid4())


class TestCleanupStale:
    async def test_cleanup_stale_agents(self, tmp_path: Path) -> None:
        """Agents with old heartbeats should be cleaned up."""
        worktree_info = _make_worktree_info(tmp_path)
        wt_manager = _make_worktree_manager(worktree_info)
        db_factory = _make_db_session_factory()
        mock_process, exit_event = _make_blocking_process()

        runner = AgentRunner(
            worktree_manager=wt_manager,
            db_session_factory=db_factory,
            claude_bin=Path("/usr/bin/claude"),
            max_concurrent=5,
        )

        task = _make_task()

        with patch(
            "orchestrator.agent_runner.asyncio.create_subprocess_exec",
            new_callable=AsyncMock,
        ) as mock_exec:
            mock_exec.return_value = mock_process

            agent = await runner.spawn_agent(
                task=task,
                feature_name="test-feature",
                agent_type="coder",
                prompt="Build it",
            )

        # Simulate a stale agent by creating a mock DB query result
        stale_agent = MagicMock(spec=Agent)
        stale_agent.id = agent.id
        stale_agent.status = "working"
        stale_agent.heartbeat_at = _utcnow() - timedelta(seconds=600)

        # Mock the DB query to return the stale agent
        mock_result = MagicMock()
        mock_scalars = MagicMock()
        mock_scalars.all = MagicMock(return_value=[stale_agent])
        mock_result.scalars = MagicMock(return_value=mock_scalars)

        session = db_factory.session
        session.execute = AsyncMock(return_value=mock_result)

        cleaned = await runner.cleanup_stale_agents(timeout_seconds=300)

        assert len(cleaned) == 1
        assert cleaned[0] == agent.id

        # Agent should be removed from running processes
        running = await runner.get_running_agents()
        assert len(running) == 0

        # Process should have been killed
        mock_process.kill.assert_called()


class TestGetAgentOutput:
    async def test_get_agent_output(self, tmp_path: Path) -> None:
        """Reading agent output should return log file contents."""
        worktree_info = _make_worktree_info(tmp_path)
        wt_manager = _make_worktree_manager(worktree_info)
        db_factory = _make_db_session_factory()
        mock_process, exit_event = _make_blocking_process()

        runner = AgentRunner(
            worktree_manager=wt_manager,
            db_session_factory=db_factory,
            claude_bin=Path("/usr/bin/claude"),
            max_concurrent=5,
        )

        task = _make_task()

        with patch(
            "orchestrator.agent_runner.asyncio.create_subprocess_exec",
            new_callable=AsyncMock,
        ) as mock_exec:
            mock_exec.return_value = mock_process

            agent = await runner.spawn_agent(
                task=task,
                feature_name="test-feature",
                agent_type="coder",
                prompt="Build it",
            )

        # Write some content to the log file
        agent_process = runner._processes[agent.id]
        agent_process.log_path.write_text(
            "Agent output line 1\nAgent output line 2\n"
        )

        output = await runner.get_agent_output(agent.id)
        assert "Agent output line 1" in output
        assert "Agent output line 2" in output

        # Clean up
        mock_process.returncode = 0
        exit_event.set()
        await asyncio.sleep(0.05)

    async def test_get_output_unknown_agent(self, tmp_path: Path) -> None:
        """Getting output for unknown agent returns empty string."""
        db_factory = _make_db_session_factory()
        session = db_factory.session

        # Mock the query to return None (agent not found)
        mock_result = MagicMock()
        mock_result.scalar_one_or_none = MagicMock(return_value=None)
        session.execute = AsyncMock(return_value=mock_result)

        runner = AgentRunner(
            worktree_manager=AsyncMock(),
            db_session_factory=db_factory,
            claude_bin=Path("/usr/bin/claude"),
            max_concurrent=5,
        )

        output = await runner.get_agent_output(uuid.uuid4())
        assert output == ""
