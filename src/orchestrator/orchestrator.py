"""Main orchestrator loop — watches the task board and drives the full task lifecycle.

Runs as a long-lived asyncio task. Polls for tasks in actionable states,
decomposes them via planner agents, assigns workers, and manages human gates.
"""

from __future__ import annotations

import asyncio
import json
import logging
import uuid
from pathlib import Path
from typing import Any, Callable, Coroutine

from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession, async_sessionmaker

from orchestrator.agent_prompt import generate_agent_prompt
from orchestrator.agent_runner import AgentRunner
from orchestrator.enums import AgentStatus, AgentType, TaskStatus
from orchestrator.models import Agent, Memory, Project, Task, TaskEvent
from orchestrator.schemas import SubtaskPlan
from orchestrator.state_machine import transition_task
from orchestrator.worktree import WorktreeManager

logger = logging.getLogger(__name__)

# States the orchestrator acts on during each poll iteration
ACTIONABLE_STATES = [
    TaskStatus.BACKLOG,
    TaskStatus.PLANNING,
    TaskStatus.IN_PROGRESS,
    TaskStatus.MERGING,
]

MAX_PLANNER_RETRIES = 3

# States owned by humans — orchestrator skips these
HUMAN_OWNED_STATES = [
    TaskStatus.PLAN_REVIEW,
    TaskStatus.TESTING_READY,
    TaskStatus.MANUAL_TESTING,
]


class Orchestrator:
    """Central scheduling loop that orchestrates the task lifecycle.

    Each iteration:
    1. Poll for tasks in actionable states
    2. BACKLOG (top-level, no subtasks) -> transition to PLANNING
    3. PLANNING -> (planner agent runs, stub: immediately produce plan) -> PLAN_REVIEW
    4. PLAN_REVIEW -> skip (waiting for human)
    5. IN_PROGRESS (parent) -> schedule subtasks; check completion
    6. TESTING_READY -> skip (waiting for human)
    7. MANUAL_TESTING -> skip (waiting for human)
    8. MERGING -> execute merge workflow
    9. Clean up stale agents
    10. Sleep poll_interval
    """

    def __init__(
        self,
        agent_runner: AgentRunner,
        worktree_manager: WorktreeManager,
        db_session_factory: async_sessionmaker[AsyncSession],
        broadcast_fn: Callable[[dict[str, Any]], Coroutine[Any, Any, None]] | None = None,
    ) -> None:
        self.agent_runner = agent_runner
        self.worktree_manager = worktree_manager
        self.db_session_factory = db_session_factory
        self.broadcast_fn = broadcast_fn or _noop_broadcast
        self._running = False
        self._poll_interval = 5  # seconds

    async def start(self) -> None:
        """Main loop. Runs until stop() is called."""
        self._running = True
        logger.info("Orchestrator started")
        while self._running:
            try:
                await self._tick()
            except Exception:
                logger.exception("Error in orchestrator tick")
            await asyncio.sleep(self._poll_interval)
        logger.info("Orchestrator stopped")

    async def stop(self) -> None:
        """Signal the main loop to exit gracefully."""
        self._running = False

    async def _tick(self) -> None:
        """Single iteration of the orchestrator loop."""
        async with self.db_session_factory() as session:
            # 1. Process top-level BACKLOG tasks (no parent, no subtasks yet)
            backlog_tasks = await self._query_top_level_tasks(session, TaskStatus.BACKLOG)
            for task in backlog_tasks:
                await self._process_backlog(session, task)

            # 2. Process PLANNING tasks (run planner agent)
            planning_tasks = await self._query_top_level_tasks(session, TaskStatus.PLANNING)
            for task in planning_tasks:
                await self._process_planning(session, task)

            # 3. PLAN_REVIEW, TESTING_READY, MANUAL_TESTING — skip (human-owned)

            # 4. Process IN_PROGRESS parent tasks — schedule subtasks & check completion
            in_progress_parents = await self._query_parent_tasks(
                session, TaskStatus.IN_PROGRESS
            )
            for task in in_progress_parents:
                await self._schedule_subtasks(session, task)
                await self._check_feature_completion(session, task)

            # 5. Process MERGING tasks
            merging_tasks = await self._query_top_level_tasks(session, TaskStatus.MERGING)
            for task in merging_tasks:
                await self._execute_merge(session, task)

            # 6. Handle completed/failed agent subtasks
            await self._process_agent_results(session)

            # 7. Clean up stale agents
            await self.agent_runner.cleanup_stale_agents()

            await session.commit()

    # ---- Task queries ----

    async def _query_top_level_tasks(
        self, session: AsyncSession, status: TaskStatus
    ) -> list[Task]:
        """Query top-level tasks (no parent) in a given status."""
        result = await session.execute(
            select(Task).where(
                Task.status == status.value,
                Task.parent_task_id.is_(None),
            )
        )
        return list(result.scalars().all())

    async def _query_parent_tasks(
        self, session: AsyncSession, status: TaskStatus
    ) -> list[Task]:
        """Query parent tasks (have subtasks) in a given status."""
        result = await session.execute(
            select(Task).where(
                Task.status == status.value,
                Task.parent_task_id.is_(None),
            )
        )
        tasks = list(result.scalars().all())
        # Filter to those that actually have subtasks
        parent_tasks = []
        for task in tasks:
            subtask_result = await session.execute(
                select(Task).where(Task.parent_task_id == task.id).limit(1)
            )
            if subtask_result.scalars().first() is not None:
                parent_tasks.append(task)
        return parent_tasks

    async def _query_subtasks(
        self, session: AsyncSession, parent_id: uuid.UUID
    ) -> list[Task]:
        """Query all subtasks of a given parent task."""
        result = await session.execute(
            select(Task).where(Task.parent_task_id == parent_id)
        )
        return list(result.scalars().all())

    # ---- State handlers ----

    async def _process_backlog(self, session: AsyncSession, task: Task) -> None:
        """Transition BACKLOG -> PLANNING.

        For top-level tasks that need decomposition.
        """
        event = transition_task(task, TaskStatus.PLANNING, actor="orchestrator")
        session.add(event)
        logger.info(f"Task {task.id} ({task.title}): BACKLOG -> PLANNING")

    async def _fail_planner(
        self, session: AsyncSession, task: Task, reason: str
    ) -> None:
        """Transition a PLANNING task to FAILED after exhausting retries."""
        task.assigned_agent_id = None
        event = transition_task(
            task,
            TaskStatus.FAILED,
            actor="orchestrator",
            details={"reason": reason},
        )
        session.add(event)
        logger.warning(f"Task {task.id}: PLANNING -> FAILED ({reason})")
        await self.broadcast_fn({
            "type": "planner_failed",
            "task_id": str(task.id),
            "title": task.title,
            "reason": reason,
        })

    def _bump_planner_retries(self, task: Task, error: str) -> bool:
        """Increment planner retry count. Return True if retries exhausted."""
        task.context = task.context or {}
        count = task.context.get("planner_retries", 0) + 1
        task.context["planner_retries"] = count
        task.context["planner_error"] = error
        return count >= MAX_PLANNER_RETRIES

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
            branch = self.worktree_manager._ensure_prefix(feature_name)
            worktree_dir = self.worktree_manager.bare_repo / branch
            if worktree_dir.exists():
                # Worktree exists from a previous attempt — reuse it
                task.worktree_branch = branch
                logger.info(f"Task {task.id}: Reusing existing worktree {branch}")
            else:
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

    async def _handle_plan_approved(self, session: AsyncSession, task: Task) -> None:
        """Called when human approves a plan (PLAN_REVIEW -> IN_PROGRESS).

        1. Read the approved plan from task.plan
        2. Create a feature worktree for the task
        3. For each subtask in the plan, create a Task record (BACKLOG)
        4. Transition parent to IN_PROGRESS
        """
        if task.plan is None:
            logger.error(f"Task {task.id}: Cannot approve plan — no plan found")
            return

        plan_items = task.plan
        if not isinstance(plan_items, list):
            logger.error(f"Task {task.id}: Plan is not a list")
            return

        # Create a feature worktree for this task (skip if planner already created it)
        if task.worktree_branch is None:
            feature_name = _task_feature_name(task)
            try:
                wt_info = await self.worktree_manager.create_feature(feature_name)
                task.worktree_branch = wt_info.branch
            except Exception:
                logger.exception(f"Task {task.id}: Failed to create feature worktree")
                # Continue without worktree — subtask creation still proceeds

        # Create subtasks from the plan
        for i, item in enumerate(plan_items):
            subtask_plan = SubtaskPlan(**item) if isinstance(item, dict) else item
            subtask = Task(
                project_id=task.project_id,
                parent_task_id=task.id,
                title=subtask_plan.title,
                description=subtask_plan.description,
                status=TaskStatus.BACKLOG.value,
                priority=task.priority,
                context={
                    "agent_type": subtask_plan.agent_type
                    if isinstance(subtask_plan.agent_type, str)
                    else subtask_plan.agent_type.value,
                    "estimated_files": subtask_plan.estimated_files,
                    "subtask_index": i,
                },
            )
            session.add(subtask)

        # Transition parent: PLAN_REVIEW -> IN_PROGRESS
        event = transition_task(
            task,
            TaskStatus.IN_PROGRESS,
            actor="orchestrator",
            details={"subtask_count": len(plan_items)},
        )
        session.add(event)
        logger.info(
            f"Task {task.id}: PLAN_REVIEW -> IN_PROGRESS "
            f"({len(plan_items)} subtasks created)"
        )

    async def _handle_plan_rejected(
        self, session: AsyncSession, task: Task, feedback: str | None = None
    ) -> None:
        """Called when human rejects a plan (PLAN_REVIEW -> PLANNING).

        Stores feedback and clears plan so planner can regenerate.
        """
        task.plan_feedback = feedback
        task.plan = None  # Clear plan so planner will regenerate
        task.assigned_agent_id = None  # Clear so a fresh planner spawns on retry

        event = transition_task(
            task,
            TaskStatus.PLANNING,
            actor="human",
            details={"feedback": feedback},
        )
        session.add(event)
        logger.info(f"Task {task.id}: PLAN_REVIEW -> PLANNING (rejected)")

    async def _schedule_subtasks(self, session: AsyncSession, parent_task: Task) -> None:
        """Schedule BACKLOG subtasks of an IN_PROGRESS parent.

        For each BACKLOG subtask with no unmet dependencies:
        1. Find or spawn an idle agent
        2. Create agent worktree inside the feature worktree
        3. Spawn agent via AgentRunner
        4. Transition subtask: BACKLOG -> IN_PROGRESS
        """
        subtasks = await self._query_subtasks(session, parent_task.id)
        backlog_subtasks = [
            st for st in subtasks if st.status == TaskStatus.BACKLOG.value
        ]

        for subtask in backlog_subtasks:
            # Check dependencies
            if not await self._dependencies_met(session, subtask):
                continue

            if not self.agent_runner.can_spawn:
                logger.debug("Max concurrent agents reached, deferring subtask scheduling")
                break

            # Determine agent type from subtask context
            agent_type_str = (subtask.context or {}).get("agent_type", "coder")
            try:
                agent_type = AgentType(agent_type_str)
            except ValueError:
                agent_type = AgentType.CODER

            # Try to find an idle agent
            agent = await self._find_idle_agent(session, parent_task.project_id, agent_type)

            if agent is None:
                # Create a new agent record
                agent = Agent(
                    project_id=parent_task.project_id,
                    agent_type=agent_type.value,
                    name=f"{agent_type.value}-{uuid.uuid4().hex[:8]}",
                    status=AgentStatus.IDLE.value,
                )
                session.add(agent)
                await session.flush()  # Get the agent ID

            # Create agent worktree
            feature_name = parent_task.worktree_branch or _task_feature_name(parent_task)
            try:
                agent_wt = await self.worktree_manager.create_agent_worktree(feature_name)
                agent.worktree_path = str(agent_wt.path)
                agent.worktree_branch = agent_wt.branch
            except Exception:
                logger.exception(
                    f"Task {subtask.id}: Failed to create agent worktree"
                )
                continue

            # Build prompt for the agent
            prompt = _build_agent_prompt(subtask, parent_task)

            # Spawn the agent process
            await self.agent_runner.spawn(
                agent_id=agent.id,
                task_id=subtask.id,
                worktree_path=agent_wt.path,
                branch=agent_wt.branch,
                prompt=prompt,
            )

            # Update agent state
            agent.status = AgentStatus.WORKING.value
            agent.current_task_id = subtask.id

            # Update subtask
            subtask.assigned_agent_id = agent.id

            # Transition subtask: BACKLOG -> PLANNING -> PLAN_REVIEW -> IN_PROGRESS
            # Subtasks skip the planning flow and go directly to IN_PROGRESS.
            # We do: BACKLOG -> PLANNING, then PLANNING -> PLAN_REVIEW,
            #        then PLAN_REVIEW -> IN_PROGRESS
            # However, the state machine only allows BACKLOG -> PLANNING.
            # For subtasks, let's follow the transitions step by step.
            event1 = transition_task(subtask, TaskStatus.PLANNING, actor="orchestrator")
            session.add(event1)

            # Set a trivial plan so it can proceed
            subtask.plan = [{"title": subtask.title, "description": subtask.description,
                            "agent_type": agent_type.value, "estimated_files": []}]
            event2 = transition_task(subtask, TaskStatus.PLAN_REVIEW, actor="orchestrator")
            session.add(event2)

            event3 = transition_task(subtask, TaskStatus.IN_PROGRESS, actor="orchestrator")
            session.add(event3)

            logger.info(
                f"Subtask {subtask.id} ({subtask.title}): assigned to agent "
                f"{agent.id} ({agent.name})"
            )

    async def _check_feature_completion(
        self, session: AsyncSession, parent_task: Task
    ) -> None:
        """Check if all subtasks of a parent task are DONE.

        If all done:
        1. Merge all agent branches into the feature branch
        2. Generate test_plan
        3. Transition parent: IN_PROGRESS -> TESTING_READY
        4. Broadcast notification
        """
        subtasks = await self._query_subtasks(session, parent_task.id)
        if not subtasks:
            return

        all_done = all(st.status == TaskStatus.DONE.value for st in subtasks)
        any_failed = any(st.status == TaskStatus.FAILED.value for st in subtasks)

        if any_failed:
            # If any subtask failed, mark parent as failed
            event = transition_task(
                parent_task,
                TaskStatus.FAILED,
                actor="orchestrator",
                details={"reason": "One or more subtasks failed"},
            )
            session.add(event)
            logger.warning(f"Task {parent_task.id}: IN_PROGRESS -> FAILED (subtask failure)")
            return

        if not all_done:
            return

        # All subtasks done — generate test plan
        subtask_summaries = [
            f"- {st.title}: {st.description}" for st in subtasks
        ]
        parent_task.test_plan = (
            f"Feature: {parent_task.title}\n\n"
            f"Completed subtasks:\n" + "\n".join(subtask_summaries) + "\n\n"
            f"Please verify the following:\n"
            f"1. All subtask changes are properly merged\n"
            f"2. The feature works end-to-end as described\n"
            f"3. No regressions in existing functionality\n"
        )

        # Transition: IN_PROGRESS -> TESTING_READY
        event = transition_task(
            parent_task,
            TaskStatus.TESTING_READY,
            actor="orchestrator",
            details={"subtasks_completed": len(subtasks)},
        )
        session.add(event)
        logger.info(
            f"Task {parent_task.id}: IN_PROGRESS -> TESTING_READY "
            f"(all {len(subtasks)} subtasks done)"
        )

        await self.broadcast_fn({
            "type": "testing_ready",
            "task_id": str(parent_task.id),
            "title": parent_task.title,
            "test_plan": parent_task.test_plan,
        })

    async def _execute_merge(self, session: AsyncSession, task: Task) -> None:
        """Execute merge workflow for tasks in MERGING state.

        1. Merge feature branch into main
        2. If success: sync other worktrees, clean up, transition to DONE
        3. If conflict: transition to FAILED, notify human
        """
        if not task.worktree_branch:
            logger.error(f"Task {task.id}: No worktree branch for merge")
            event = transition_task(
                task,
                TaskStatus.FAILED,
                actor="orchestrator",
                details={"reason": "No worktree branch configured"},
            )
            session.add(event)
            return

        # Get the main worktree path
        default_branch = await self.worktree_manager.get_default_branch()
        main_worktree = self.worktree_manager.bare_repo / default_branch

        try:
            merge_result = await self.worktree_manager.merge_branch(
                task.worktree_branch, main_worktree
            )
        except Exception:
            logger.exception(f"Task {task.id}: Merge failed with exception")
            event = transition_task(
                task,
                TaskStatus.FAILED,
                actor="orchestrator",
                details={"reason": "Merge operation raised an exception"},
            )
            session.add(event)
            await self.broadcast_fn({
                "type": "merge_failed",
                "task_id": str(task.id),
                "title": task.title,
                "reason": "Exception during merge",
            })
            return

        if merge_result.success:
            # Sync other feature worktrees
            try:
                await self.worktree_manager.sync_all()
            except Exception:
                logger.exception("Failed to sync worktrees after merge")

            # Clean up the feature worktree
            feature_name = task.worktree_branch
            try:
                await self.worktree_manager.remove_feature(feature_name)
            except Exception:
                logger.exception(
                    f"Task {task.id}: Failed to clean up feature worktree"
                )

            # Transition: MERGING -> DONE
            event = transition_task(
                task,
                TaskStatus.DONE,
                actor="orchestrator",
                details={"merge_commit": merge_result.merge_commit},
            )
            session.add(event)
            logger.info(f"Task {task.id}: MERGING -> DONE (merged to {default_branch})")

            await self.broadcast_fn({
                "type": "merge_complete",
                "task_id": str(task.id),
                "title": task.title,
                "merge_commit": merge_result.merge_commit,
            })
        else:
            # Merge conflict
            event = transition_task(
                task,
                TaskStatus.FAILED,
                actor="orchestrator",
                details={
                    "reason": "Merge conflict",
                    "conflicts": merge_result.conflicts,
                },
            )
            session.add(event)
            logger.warning(
                f"Task {task.id}: MERGING -> FAILED "
                f"(conflicts: {merge_result.conflicts})"
            )

            await self.broadcast_fn({
                "type": "merge_conflict",
                "task_id": str(task.id),
                "title": task.title,
                "conflicts": merge_result.conflicts,
            })

    async def _on_agent_completed(
        self, session: AsyncSession, agent: Agent, task: Task
    ) -> None:
        """Handle agent completion. Planner agents set task.plan; coder agents fast-track to DONE."""

        # --- Planner agent completion ---
        if agent.agent_type == AgentType.PLANNER.value:
            await self._on_planner_completed(session, agent, task)
            return

        # --- Coder/researcher agent completion (existing logic below) ---
        # Read agent output
        output = await self.agent_runner.get_agent_output(agent.id)

        # Store output in task context
        task.context = task.context or {}
        task.context["agent_output"] = output[:5000]  # Truncate if too long

        # Create a memory record for the agent's work
        memory = Memory(
            agent_id=agent.id,
            task_id=task.id,
            content=f"Completed task: {task.title}. Output summary: {output[:500]}",
            memory_type="task_completion",
        )
        session.add(memory)

        # Merge agent branch into feature branch
        if agent.worktree_branch and task.parent_task_id:
            parent = await session.get(Task, task.parent_task_id)
            if parent and parent.worktree_branch:
                feature_dir = self.worktree_manager.bare_repo / parent.worktree_branch
                try:
                    merge_result = await self.worktree_manager.merge_branch(
                        agent.worktree_branch, feature_dir
                    )
                    if not merge_result.success:
                        logger.warning(
                            f"Agent branch merge had conflicts: "
                            f"{merge_result.conflicts}"
                        )
                except Exception:
                    logger.exception("Failed to merge agent branch into feature")

        # Clean up agent worktree
        if agent.worktree_branch:
            try:
                await self.worktree_manager.remove_agent_worktree(agent.worktree_branch)
            except Exception:
                logger.exception("Failed to remove agent worktree")

        # Transition subtask to DONE.
        # The state machine requires: IN_PROGRESS -> TESTING_READY -> MANUAL_TESTING -> MERGING -> DONE
        # For subtasks, we fast-track through the intermediate states.
        task.test_plan = "Auto-verified by agent completion"
        event1 = transition_task(task, TaskStatus.TESTING_READY, actor="orchestrator")
        session.add(event1)

        event2 = transition_task(task, TaskStatus.MANUAL_TESTING, actor="orchestrator")
        session.add(event2)

        event3 = transition_task(task, TaskStatus.MERGING, actor="orchestrator")
        session.add(event3)

        event4 = transition_task(task, TaskStatus.DONE, actor="orchestrator")
        session.add(event4)

        # Update agent status
        agent.status = AgentStatus.IDLE.value
        agent.current_task_id = None

        logger.info(f"Agent {agent.id} completed task {task.id} ({task.title})")

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
            output = await self.agent_runner.get_agent_output(agent.id)
            logger.warning(
                f"Task {task.id}: No plan.json found, planner output: {output[:500]}"
            )
            agent.status = AgentStatus.IDLE.value
            agent.current_task_id = None
            exhausted = self._bump_planner_retries(task, "No plan.json produced")
            if exhausted:
                await self._fail_planner(
                    session, task, "Planner produced no plan after max retries"
                )
            else:
                task.assigned_agent_id = None
            return

        # Transform plan.json format to SubtaskPlan format
        subtask_plans: list[dict[str, Any]] = []
        for item in plan_data.get("subtasks", []):
            subtask_plans.append({
                "title": item.get("title", "Untitled"),
                "description": item.get("description", ""),
                "agent_type": item.get("agent_type", "coder"),
                "estimated_files": item.get("files", []),
            })

        if not subtask_plans:
            logger.warning(f"Task {task.id}: Planner produced empty plan")
            agent.status = AgentStatus.IDLE.value
            agent.current_task_id = None
            exhausted = self._bump_planner_retries(task, "Empty plan")
            if exhausted:
                await self._fail_planner(
                    session, task, "Planner produced empty plan after max retries"
                )
            else:
                task.assigned_agent_id = None
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

    async def _on_agent_failed(
        self, session: AsyncSession, agent: Agent, task: Task
    ) -> None:
        """Handle agent failure."""
        output = await self.agent_runner.get_agent_output(agent.id)

        task.context = task.context or {}
        task.context["error_log"] = output[:5000]

        # Planner failure: retry up to MAX_PLANNER_RETRIES, then fail
        if agent.agent_type == AgentType.PLANNER.value:
            agent.status = AgentStatus.IDLE.value
            agent.current_task_id = None

            # Clean up agent worktree
            if agent.worktree_branch:
                try:
                    await self.worktree_manager.remove_agent_worktree(agent.worktree_branch)
                except Exception:
                    logger.exception("Failed to remove failed planner agent worktree")

            exhausted = self._bump_planner_retries(task, "Agent process failed")
            if exhausted:
                await self._fail_planner(
                    session, task, "Planner agent failed after max retries"
                )
            else:
                task.assigned_agent_id = None
                retries = task.context["planner_retries"]
                logger.warning(
                    f"Task {task.id}: Planner agent {agent.id} failed "
                    f"(attempt {retries}/{MAX_PLANNER_RETRIES}), will retry"
                )
                await self.broadcast_fn({
                    "type": "planner_failed",
                    "task_id": str(task.id),
                    "title": task.title,
                })
            return

        # --- Existing coder/researcher failure handling ---
        event = transition_task(
            task,
            TaskStatus.FAILED,
            actor="orchestrator",
            details={"reason": "Agent process failed", "output": output[:1000]},
        )
        session.add(event)

        # Update agent status
        agent.status = AgentStatus.IDLE.value
        agent.current_task_id = None

        logger.warning(f"Agent {agent.id} failed on task {task.id} ({task.title})")

        await self.broadcast_fn({
            "type": "agent_failed",
            "task_id": str(task.id),
            "agent_id": str(agent.id),
            "title": task.title,
        })

    async def _process_agent_results(self, session: AsyncSession) -> None:
        """Check for agents that have completed or failed and process results."""
        # Query agents with WORKING status
        result = await session.execute(
            select(Agent).where(Agent.status == AgentStatus.WORKING.value)
        )
        working_agents = list(result.scalars().all())

        for agent in working_agents:
            status = await self.agent_runner.get_status(agent.id)
            if status == AgentStatus.WORKING.value:
                continue  # Still running

            if agent.current_task_id is None:
                continue

            task = await session.get(Task, agent.current_task_id)
            if task is None:
                continue

            process = self.agent_runner._processes.get(agent.id)
            if process and process.process.returncode is not None and process.process.returncode != 0:
                await self._on_agent_failed(session, agent, task)
            else:
                await self._on_agent_completed(session, agent, task)

    # ---- Helpers ----

    async def _dependencies_met(
        self, session: AsyncSession, task: Task
    ) -> bool:
        """Check if all dependencies of a task are DONE."""
        dep_ids = task.dependency_ids or []
        if not dep_ids:
            return True

        for dep_id in dep_ids:
            try:
                dep_uuid = uuid.UUID(str(dep_id))
            except (ValueError, TypeError):
                continue
            dep_task = await session.get(Task, dep_uuid)
            if dep_task is None or dep_task.status != TaskStatus.DONE.value:
                return False
        return True

    async def _find_idle_agent(
        self,
        session: AsyncSession,
        project_id: uuid.UUID,
        agent_type: AgentType,
    ) -> Agent | None:
        """Find an idle agent of a given type for a project."""
        result = await session.execute(
            select(Agent).where(
                Agent.project_id == project_id,
                Agent.agent_type == agent_type.value,
                Agent.status == AgentStatus.IDLE.value,
            ).limit(1)
        )
        return result.scalars().first()


def _task_feature_name(task: Task) -> str:
    """Generate a feature branch name from a task."""
    # Sanitize the title for use as a branch name
    slug = task.title.lower().replace(" ", "-")
    # Keep only alphanumeric and hyphens, limit length
    slug = "".join(c for c in slug if c.isalnum() or c == "-")[:40]
    return f"{slug}-{str(task.id)[:8]}"


def _build_agent_prompt(subtask: Task, parent_task: Task) -> str:
    """Build a prompt for an agent working on a subtask."""
    estimated_files = (subtask.context or {}).get("estimated_files", [])
    files_section = ""
    if estimated_files:
        files_section = (
            "\n\nFiles likely to be involved:\n"
            + "\n".join(f"- {f}" for f in estimated_files)
        )

    return (
        f"## Task: {subtask.title}\n\n"
        f"{subtask.description}\n\n"
        f"### Parent Feature\n"
        f"{parent_task.title}: {parent_task.description}\n"
        f"{files_section}\n\n"
        f"### Instructions\n"
        f"- Work in the current worktree\n"
        f"- Commit your changes with descriptive messages\n"
        f"- Follow project conventions from CLAUDE.md\n"
    )


async def _noop_broadcast(message: dict[str, Any]) -> None:
    """No-op broadcast function used when none is provided."""
    pass
