# Agent: AgentRunner API Surface

You are working on the `feature/planner-agent` branch of Drem Orchestrator, a multi-agent task orchestration system that spawns Claude Code agents in parallel git worktrees.
Your task is to add missing methods to `AgentRunner` that the orchestrator expects but don't exist yet.

## Context

Read these before starting:
- `CLAUDE.md` (project conventions, build commands)
- `src/orchestrator/agent_runner.py` (full file — the class you're modifying)
- `src/orchestrator/orchestrator.py` (lines 295-392: `_schedule_subtasks()` which calls `spawn()`, line 314: `can_spawn`, line 677: `get_status()`)
- `src/orchestrator/enums.py` (`AgentStatus` enum values)
- `src/orchestrator/models.py` (Agent model — `id`, `status`, `worktree_path` fields)

## Deliverables

### Modified file: `src/orchestrator/agent_runner.py`

#### 1. `can_spawn` property

Add a read-only property that returns whether the runner can accept another agent.

```python
@property
def can_spawn(self) -> bool:
    """Whether we can spawn another agent (haven't hit max concurrency)."""
    return len(self._processes) < self._max_concurrent
```

#### 2. `get_status(agent_id)` method

Check the in-memory process state first, then fall back to the DB. Return a string matching `AgentStatus` values.

```python
async def get_status(self, agent_id: uuid.UUID) -> str:
    """Get agent status — check in-memory process first, fall back to DB."""
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
```

Import `Agent` from `orchestrator.models` (it's already imported).

#### 3. `spawn(agent_id, task_id, worktree_path, branch, prompt)` method

A lower-level spawn that starts a subprocess for a pre-existing Agent record. Unlike `spawn_agent()` which creates the Agent record and worktree itself, this method is for cases where the orchestrator has already created the Agent and worktree (used by `_schedule_subtasks()`).

```python
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
        process = await asyncio.create_subprocess_exec(
            str(self._claude_bin),
            "--agent",
            str(prompt_path),
            cwd=worktree_path,
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
```

Note the signature difference from `_schedule_subtasks()` which calls:
```python
await self.agent_runner.spawn(
    agent_id=agent.id,
    agent_type=agent_type,
    worktree_path=agent_wt.path,
    branch=agent_wt.branch,
    prompt=prompt,
)
```

The orchestrator passes `agent_type` but we don't need it (the type is already stored on the Agent record). Map the call: replace `agent_type` parameter with `task_id` in the orchestrator call site. The method needs `task_id` for the `AgentProcess` dataclass. Update the call in `_schedule_subtasks()` accordingly:

```python
await self.agent_runner.spawn(
    agent_id=agent.id,
    task_id=subtask.id,
    worktree_path=agent_wt.path,
    branch=agent_wt.branch,
    prompt=prompt,
)
```

## Conventions

- Async everywhere
- Type hints on all public functions
- snake_case for functions/variables
- `pathlib.Path` for file paths
- Build verification: `uv run pytest`
