"""Task scheduling and agent assignment logic.

The Scheduler determines which tasks are ready to be assigned,
finds or spawns agents, and tracks the overall schedule state.
"""

from __future__ import annotations

import logging
import uuid
from dataclasses import dataclass, field

from sqlalchemy import func, select
from sqlalchemy.ext.asyncio import AsyncSession, async_sessionmaker

from orchestrator.agent_runner import AgentRunner
from orchestrator.enums import AgentStatus, AgentType, TaskStatus
from orchestrator.models import Agent, Task
from orchestrator.worktree import WorktreeManager

logger = logging.getLogger(__name__)


@dataclass
class ScheduleSummary:
    """Overview of the current schedule state."""

    tasks_by_status: dict[TaskStatus, int] = field(default_factory=dict)
    agents_by_status: dict[AgentStatus, int] = field(default_factory=dict)
    blocked_tasks: list[tuple[uuid.UUID, list[uuid.UUID]]] = field(
        default_factory=list
    )  # (task_id, blocking_task_ids)
    queue_depth: int = 0  # assignable tasks waiting for agents


class Scheduler:
    """Task scheduling and agent assignment.

    Responsible for:
    - Finding tasks ready for assignment
    - Matching tasks to idle agents or spawning new ones
    - Providing schedule overviews
    """

    def __init__(
        self,
        agent_runner: AgentRunner,
        db_session_factory: async_sessionmaker[AsyncSession],
        worktree_manager: WorktreeManager | None = None,
    ) -> None:
        self.agent_runner = agent_runner
        self.db_session_factory = db_session_factory
        self.worktree_manager = worktree_manager

    async def get_assignable_tasks(
        self, session: AsyncSession, project_id: uuid.UUID
    ) -> list[Task]:
        """Find tasks that are ready to be assigned.

        Criteria:
        - Status: BACKLOG (subtasks that skip planning)
        - Has a parent_task_id (i.e., is a subtask)
        - Parent task is IN_PROGRESS
        - No unmet dependencies (all dependency_ids tasks are DONE)
        - Not already assigned to an agent
        """
        # Find BACKLOG subtasks with no assigned agent
        result = await session.execute(
            select(Task).where(
                Task.project_id == project_id,
                Task.status == TaskStatus.BACKLOG.value,
                Task.parent_task_id.isnot(None),
                Task.assigned_agent_id.is_(None),
            )
        )
        candidates = list(result.scalars().all())

        assignable = []
        for task in candidates:
            # Check parent is IN_PROGRESS
            parent = await session.get(Task, task.parent_task_id)
            if parent is None or parent.status != TaskStatus.IN_PROGRESS.value:
                continue

            # Check dependencies are met
            if not await self._dependencies_met(session, task):
                continue

            assignable.append(task)

        return assignable

    async def get_idle_agents(
        self, session: AsyncSession, project_id: uuid.UUID
    ) -> list[Agent]:
        """Find agents with status IDLE for this project."""
        result = await session.execute(
            select(Agent).where(
                Agent.project_id == project_id,
                Agent.status == AgentStatus.IDLE.value,
            )
        )
        return list(result.scalars().all())

    async def assign_task(
        self,
        session: AsyncSession,
        task: Task,
        feature_name: str,
    ) -> Agent:
        """Assign a task to an agent.

        1. Determine agent_type from task context
        2. Check for idle agent of matching type -> reuse
        3. If none idle -> spawn new agent via AgentRunner
        4. Update task.assigned_agent_id
        5. Return the agent

        Args:
            session: Database session.
            task: The task to assign.
            feature_name: Feature branch name for worktree creation.

        Returns:
            The Agent record assigned to the task.

        Raises:
            RuntimeError: If agent cannot be spawned (max concurrent reached).
        """
        # Determine agent type
        agent_type = self._determine_agent_type(task)

        # Look for an idle agent of matching type
        result = await session.execute(
            select(Agent).where(
                Agent.project_id == task.project_id,
                Agent.agent_type == agent_type.value,
                Agent.status == AgentStatus.IDLE.value,
            ).limit(1)
        )
        agent = result.scalars().first()

        if agent is None:
            # No idle agent — create a new one
            if not self.agent_runner.can_spawn:
                raise RuntimeError(
                    "Cannot spawn new agent: max concurrent agents reached"
                )

            agent = Agent(
                project_id=task.project_id,
                agent_type=agent_type.value,
                name=f"{agent_type.value}-{uuid.uuid4().hex[:8]}",
                status=AgentStatus.IDLE.value,
            )
            session.add(agent)
            await session.flush()

        # Create agent worktree if worktree manager available
        if self.worktree_manager:
            try:
                agent_wt = await self.worktree_manager.create_agent_worktree(
                    feature_name
                )
                agent.worktree_path = str(agent_wt.path)
                agent.worktree_branch = agent_wt.branch
            except Exception:
                logger.exception(
                    f"Failed to create agent worktree for task {task.id}"
                )

        # Spawn the agent process
        prompt = self._build_prompt(task)
        await self.agent_runner.spawn(
            agent_id=agent.id,
            agent_type=agent_type,
            worktree_path=agent.worktree_path or "",
            branch=agent.worktree_branch or "",
            prompt=prompt,
        )

        # Update states
        agent.status = AgentStatus.WORKING.value
        agent.current_task_id = task.id
        task.assigned_agent_id = agent.id

        logger.info(
            f"Assigned task {task.id} ({task.title}) to agent "
            f"{agent.id} ({agent.name})"
        )
        return agent

    async def get_schedule_summary(
        self, session: AsyncSession, project_id: uuid.UUID
    ) -> ScheduleSummary:
        """Return overview of the schedule state.

        Includes:
        - Tasks per status
        - Agents per status
        - Blocked tasks and what blocks them
        - Queue depth (assignable tasks waiting for agents)
        """
        # Tasks by status
        task_counts_result = await session.execute(
            select(Task.status, func.count(Task.id))
            .where(Task.project_id == project_id)
            .group_by(Task.status)
        )
        tasks_by_status: dict[TaskStatus, int] = {}
        for status_val, count in task_counts_result:
            try:
                tasks_by_status[TaskStatus(status_val)] = count
            except ValueError:
                pass

        # Agents by status
        agent_counts_result = await session.execute(
            select(Agent.status, func.count(Agent.id))
            .where(Agent.project_id == project_id)
            .group_by(Agent.status)
        )
        agents_by_status: dict[AgentStatus, int] = {}
        for status_val, count in agent_counts_result:
            try:
                agents_by_status[AgentStatus(status_val)] = count
            except ValueError:
                pass

        # Blocked tasks
        blocked_tasks: list[tuple[uuid.UUID, list[uuid.UUID]]] = []
        backlog_result = await session.execute(
            select(Task).where(
                Task.project_id == project_id,
                Task.status == TaskStatus.BACKLOG.value,
                Task.parent_task_id.isnot(None),
            )
        )
        for task in backlog_result.scalars().all():
            blocking = await self._get_blocking_tasks(session, task)
            if blocking:
                blocked_tasks.append((task.id, blocking))

        # Queue depth
        assignable = await self.get_assignable_tasks(session, project_id)
        queue_depth = len(assignable)

        return ScheduleSummary(
            tasks_by_status=tasks_by_status,
            agents_by_status=agents_by_status,
            blocked_tasks=blocked_tasks,
            queue_depth=queue_depth,
        )

    # ---- Helpers ----

    def _determine_agent_type(self, task: Task) -> AgentType:
        """Determine the agent type for a task based on its context."""
        agent_type_str = (task.context or {}).get("agent_type", "coder")
        try:
            return AgentType(agent_type_str)
        except ValueError:
            return AgentType.CODER

    def _build_prompt(self, task: Task) -> str:
        """Build a prompt for an agent working on a task."""
        return f"## Task: {task.title}\n\n{task.description}"

    async def _dependencies_met(
        self, session: AsyncSession, task: Task
    ) -> bool:
        """Check if all dependency tasks are DONE."""
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

    async def _get_blocking_tasks(
        self, session: AsyncSession, task: Task
    ) -> list[uuid.UUID]:
        """Return the list of dependency task IDs that are not yet DONE."""
        dep_ids = task.dependency_ids or []
        if not dep_ids:
            return []

        blocking: list[uuid.UUID] = []
        for dep_id in dep_ids:
            try:
                dep_uuid = uuid.UUID(str(dep_id))
            except (ValueError, TypeError):
                continue
            dep_task = await session.get(Task, dep_uuid)
            if dep_task is None or dep_task.status != TaskStatus.DONE.value:
                blocking.append(dep_uuid)
        return blocking
