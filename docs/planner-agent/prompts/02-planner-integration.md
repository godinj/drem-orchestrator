# Agent: Planner Integration

You are working on the `feature/planner-agent` branch of Drem Orchestrator, a multi-agent task orchestration system that spawns Claude Code agents in parallel git worktrees.
Your task is to implement the planner agent lifecycle: spawning a planner when a task enters PLANNING, handling its results, and fixing API mismatches throughout the orchestrator.

## Context

Read these before starting:
- `CLAUDE.md` (project conventions, build commands)
- `src/orchestrator/orchestrator.py` (full file — the main file you're modifying)
- `src/orchestrator/agent_runner.py` (API surface: `spawn_agent()`, `get_agent_output()`, `can_spawn`, `get_status()`, `spawn()`)
- `src/orchestrator/agent_prompt.py` (`generate_agent_prompt()` function and `_planner_instructions()`)
- `src/orchestrator/worktree.py` (`WorktreeManager.create_feature()`, `create_agent_worktree()`, `remove_agent_worktree()`)
- `src/orchestrator/schemas.py` (`SubtaskPlan` model: `title`, `description`, `agent_type: AgentType`, `estimated_files: list[str]`)
- `src/orchestrator/models.py` (`Task.plan` is JSON, `Task.assigned_agent_id`, `Task.worktree_branch`, `Task.context`, `Task.plan_feedback`)
- `src/orchestrator/enums.py` (`AgentType.PLANNER`, `AgentStatus`, `TaskStatus`)
- `src/orchestrator/state_machine.py` (`transition_task()` and `VALID_TRANSITIONS`)

## Dependencies

This agent depends on Agent 01 (AgentRunner API Surface). If `can_spawn`, `get_status()`, or `spawn()` don't exist yet on AgentRunner, implement against the signatures described in the Agent 01 prompt.

## Deliverables

### Modified file: `src/orchestrator/orchestrator.py`

#### 1. Add imports

Add at the top of the file:

```python
import json
from pathlib import Path

from orchestrator.agent_prompt import generate_agent_prompt
```

#### 2. Implement `_process_planning()` (replace lines 179-214)

Replace the stub with real planner agent spawning:

```python
async def _process_planning(self, session: AsyncSession, task: Task) -> None:
    """Spawn a planner agent to decompose the task, or transition if plan ready."""
    # Plan already exists — move to review
    if task.plan is not None:
        event = transition_task(
            task,
            TaskStatus.PLAN_REVIEW,
            actor="orchestrator",
            details={"plan": task.plan},
        )
        session.add(event)
        logger.info(f"Task {task.id}: PLANNING -> PLAN_REVIEW (plan ready)")
        await self.broadcast_fn({
            "type": "plan_ready",
            "task_id": str(task.id),
            "title": task.title,
        })
        return

    # Planner already assigned and running — wait for it
    if task.assigned_agent_id is not None:
        return

    # Check capacity
    if not self.agent_runner.can_spawn:
        logger.debug("Max concurrent agents reached, deferring planner spawn")
        return

    # Look up the project for prompt generation
    project = await session.get(Project, task.project_id)
    if project is None:
        logger.error(f"Task {task.id}: Project {task.project_id} not found")
        return

    # Create feature worktree if not already created
    feature_name = _task_feature_name(task)
    if task.worktree_branch is None:
        try:
            wt_info = await self.worktree_manager.create_feature(feature_name)
            task.worktree_branch = wt_info.branch
        except Exception:
            logger.exception(f"Task {task.id}: Failed to create feature worktree")
            return

    # Build planner prompt
    prompt = generate_agent_prompt(
        task=task,
        project=project,
        agent_type="planner",
        worktree_path=self.worktree_manager.bare_repo / task.worktree_branch,
    )

    # Spawn planner agent
    try:
        agent = await self.agent_runner.spawn_agent(
            task=task,
            feature_name=feature_name,
            agent_type="planner",
            prompt=prompt,
        )
    except Exception:
        logger.exception(f"Task {task.id}: Failed to spawn planner agent")
        return

    task.assigned_agent_id = agent.id
    logger.info(
        f"Task {task.id} ({task.title}): Spawned planner agent {agent.id}"
    )
    await self.broadcast_fn({
        "type": "planner_spawned",
        "task_id": str(task.id),
        "agent_id": str(agent.id),
        "title": task.title,
    })
```

**Important:** Import `Project` from `orchestrator.models` (add to existing import line).

#### 3. Modify `_on_agent_completed()` for planner agents

At the **top** of `_on_agent_completed()` (before the existing coder logic), add a branch for planner agents:

```python
async def _on_agent_completed(
    self, session: AsyncSession, agent: Agent, task: Task
) -> None:
    """Handle agent completion. Planner agents set task.plan; coder agents fast-track to DONE."""

    # --- Planner agent completion ---
    if agent.agent_type == AgentType.PLANNER.value:
        await self._on_planner_completed(session, agent, task)
        return

    # --- Coder/researcher agent completion (existing logic below) ---
    output = await self.agent_runner.get_agent_output(agent.id)
    # ... rest of existing code ...
```

Add a new private method:

```python
async def _on_planner_completed(
    self, session: AsyncSession, agent: Agent, task: Task
) -> None:
    """Handle planner agent completion: read plan.json and set task.plan."""
    # Read plan.json from agent worktree
    plan_path = Path(agent.worktree_path) / "plan.json" if agent.worktree_path else None
    plan_data = None

    if plan_path and plan_path.exists():
        try:
            raw = plan_path.read_text()
            plan_data = json.loads(raw)
        except (json.JSONDecodeError, OSError):
            logger.exception(f"Task {task.id}: Failed to parse plan.json")

    if plan_data is None:
        # Fallback: try to extract plan from agent log output
        output = await self.agent_runner.get_agent_output(agent.id)
        logger.warning(
            f"Task {task.id}: No plan.json found, planner output: {output[:500]}"
        )
        task.context = task.context or {}
        task.context["planner_error"] = "No plan.json produced"
        task.assigned_agent_id = None
        agent.status = AgentStatus.IDLE.value
        agent.current_task_id = None
        # Stay in PLANNING — next tick will retry
        return

    # Transform plan.json format to SubtaskPlan format
    subtask_plans = []
    for item in plan_data.get("subtasks", []):
        subtask_plans.append({
            "title": item.get("title", "Untitled"),
            "description": item.get("description", ""),
            "agent_type": item.get("agent_type", "coder"),
            "estimated_files": item.get("files", []),
        })

    if not subtask_plans:
        logger.warning(f"Task {task.id}: Planner produced empty plan")
        task.context = task.context or {}
        task.context["planner_error"] = "Empty plan"
        task.assigned_agent_id = None
        agent.status = AgentStatus.IDLE.value
        agent.current_task_id = None
        return

    # Set the plan on the task
    task.plan = subtask_plans

    # Clean up planner agent worktree (not the feature worktree)
    if agent.worktree_branch:
        try:
            await self.worktree_manager.remove_agent_worktree(agent.worktree_branch)
        except Exception:
            logger.exception("Failed to remove planner agent worktree")

    # Update agent state
    agent.status = AgentStatus.IDLE.value
    agent.current_task_id = None
    task.assigned_agent_id = None

    # Transition PLANNING -> PLAN_REVIEW
    event = transition_task(
        task,
        TaskStatus.PLAN_REVIEW,
        actor="orchestrator",
        details={"plan": subtask_plans, "subtask_count": len(subtask_plans)},
    )
    session.add(event)

    logger.info(
        f"Task {task.id}: PLANNING -> PLAN_REVIEW "
        f"({len(subtask_plans)} subtasks proposed)"
    )
    await self.broadcast_fn({
        "type": "plan_ready",
        "task_id": str(task.id),
        "title": task.title,
        "subtask_count": len(subtask_plans),
    })
```

#### 4. Modify `_on_agent_failed()` for planner agents

At the top, add planner-specific handling that clears `assigned_agent_id` so the next tick retries:

```python
async def _on_agent_failed(
    self, session: AsyncSession, agent: Agent, task: Task
) -> None:
    output = await self.agent_runner.get_agent_output(agent.id)

    task.context = task.context or {}
    task.context["error_log"] = output[:5000]

    # Planner failure: clear assignment so next tick retries
    if agent.agent_type == AgentType.PLANNER.value:
        task.assigned_agent_id = None
        agent.status = AgentStatus.IDLE.value
        agent.current_task_id = None

        # Clean up agent worktree
        if agent.worktree_branch:
            try:
                await self.worktree_manager.remove_agent_worktree(agent.worktree_branch)
            except Exception:
                logger.exception("Failed to remove failed planner agent worktree")

        logger.warning(
            f"Task {task.id}: Planner agent {agent.id} failed, will retry next tick"
        )
        await self.broadcast_fn({
            "type": "planner_failed",
            "task_id": str(task.id),
            "title": task.title,
        })
        return

    # --- Existing coder/researcher failure handling ---
    event = transition_task(
        task, TaskStatus.FAILED,
        actor="orchestrator",
        details={"reason": "Agent process failed", "output": output[:1000]},
    )
    session.add(event)
    # ... rest of existing code
```

#### 5. Fix API call mismatches

These are scattered line-level fixes:

**Line ~355** in `_schedule_subtasks()`: Fix `spawn()` call signature:
```python
# OLD:
await self.agent_runner.spawn(
    agent_id=agent.id,
    agent_type=agent_type,
    worktree_path=agent_wt.path,
    branch=agent_wt.branch,
    prompt=prompt,
)

# NEW:
await self.agent_runner.spawn(
    agent_id=agent.id,
    task_id=subtask.id,
    worktree_path=agent_wt.path,
    branch=agent_wt.branch,
    prompt=prompt,
)
```

**Line ~571** in `_on_agent_completed()` (coder path): Fix method name:
```python
# OLD:
output = await self.agent_runner.get_output(agent.id)
# NEW:
output = await self.agent_runner.get_agent_output(agent.id)
```

**Line ~642** in `_on_agent_failed()` (coder path): Fix method name:
```python
# OLD:
output = await self.agent_runner.get_output(agent.id)
# NEW:
output = await self.agent_runner.get_agent_output(agent.id)
```

**Line ~677** in `_process_agent_results()`: `get_status()` now exists on AgentRunner (from Agent 01), no change needed.

#### 6. Update `_handle_plan_approved()` (~line 217)

Skip feature worktree creation if already set (planner created it):

```python
# In _handle_plan_approved(), replace the worktree creation block:
# OLD:
feature_name = _task_feature_name(task)
try:
    wt_info = await self.worktree_manager.create_feature(feature_name)
    task.worktree_branch = wt_info.branch
except Exception:
    logger.exception(f"Task {task.id}: Failed to create feature worktree")

# NEW:
if task.worktree_branch is None:
    feature_name = _task_feature_name(task)
    try:
        wt_info = await self.worktree_manager.create_feature(feature_name)
        task.worktree_branch = wt_info.branch
    except Exception:
        logger.exception(f"Task {task.id}: Failed to create feature worktree")
```

#### 7. Update `_handle_plan_rejected()` (~line 276)

Clear `assigned_agent_id` so a fresh planner spawns on retry:

```python
# Add after the existing task.plan = None line:
task.assigned_agent_id = None
```

## Conventions

- Async everywhere (asyncio, async SQLAlchemy, async subprocess)
- Type hints on all public functions
- snake_case for functions/variables, PascalCase for classes
- `pathlib.Path` for file paths, not strings
- f-strings for formatting
- Build verification: `uv run pytest`
