# Agent: Orchestrator Core

You are working on **Drem Orchestrator**, a Python application that orchestrates Claude Code agents
working in parallel across git worktrees. Your task is to build the orchestrator — the central
scheduling loop that watches the task board, decomposes tasks, assigns workers, and manages the
full task lifecycle including human gates.

## Context

Read these before starting:
- `CLAUDE.md` (project conventions)
- `src/orchestrator/models.py` (Task, Agent, Project models)
- `src/orchestrator/enums.py` (TaskStatus — especially PLAN_REVIEW and MANUAL_TESTING gates)
- `src/orchestrator/state_machine.py` (transition validation, HUMAN_GATES)
- `src/orchestrator/agent_runner.py` (AgentRunner — spawn/stop/monitor agents)
- `src/orchestrator/worktree.py` (WorktreeManager — create/remove worktrees)
- `src/orchestrator/schemas.py` (PlanSubmission, SubtaskPlan)
- `src/orchestrator/config.py` (settings)

## Dependencies

This agent depends on Agent 02 (Data Model) and Agent 03 (Worktree Integration).
If those files don't exist yet, create stubs with the interfaces and implement against them.

## Deliverables

### New files (`src/orchestrator/`)

#### 1. `orchestrator.py`

The main orchestrator loop. Runs as a long-lived asyncio task.

```python
class Orchestrator:
    def __init__(
        self,
        agent_runner: AgentRunner,
        worktree_manager: WorktreeManager,
        db_session_factory,
        broadcast_fn,          # WebSocket broadcast function
    ):
        self._running = False
        self._poll_interval = 5  # seconds

    async def start(self) -> None:
        """
        Main loop. Runs until stop() is called.

        Each iteration:
        1. Poll for tasks in actionable states
        2. For each BACKLOG task with no subtasks: transition to PLANNING
        3. For each PLANNING task: call planner agent to decompose
        4. PLAN_REVIEW tasks: skip (waiting for human — UI handles this)
        5. For each IN_PROGRESS task: check if all subtasks are done
           → if yes, mark parent as TESTING_READY with test_plan
        6. TESTING_READY tasks: skip (waiting for human to start testing)
        7. MANUAL_TESTING tasks: skip (waiting for human pass/fail)
        8. For each MERGING task: execute merge workflow
        9. Clean up stale agents
        10. Sleep poll_interval
        """

    async def stop(self) -> None:
        """Signal the main loop to exit gracefully."""

    async def _process_backlog(self, task: Task) -> None:
        """
        Transition BACKLOG → PLANNING.
        Create a planner agent to decompose the task.

        The planner agent receives the high-level task description and returns
        a PlanSubmission with subtasks. The orchestrator then:
        1. Stores the plan on the task record
        2. Transitions to PLAN_REVIEW
        3. Broadcasts WebSocket event to notify human

        The human sees the plan in the UI and can approve or reject with feedback.
        """

    async def _handle_plan_approved(self, task: Task) -> None:
        """
        Called when human approves a plan (PLAN_REVIEW → IN_PROGRESS).

        1. Read the approved plan from task.plan
        2. Create a feature worktree for the task
        3. For each subtask in the plan:
           a. Create Task record (parent_task_id = this task)
           b. Status: BACKLOG (simple subtasks skip planning, go straight to work)
        4. Start assigning subtasks to agents via scheduler
        """

    async def _schedule_subtasks(self, parent_task: Task) -> None:
        """
        Look at IN_PROGRESS parent tasks.
        For each BACKLOG subtask with no unmet dependencies:
        1. Find or spawn an idle agent
        2. Create agent worktree inside the feature worktree
        3. Generate prompt from subtask description + parent context
        4. Spawn agent via AgentRunner
        5. Transition subtask: BACKLOG → IN_PROGRESS
        """

    async def _check_feature_completion(self, parent_task: Task) -> None:
        """
        Check if all subtasks of a parent task are DONE.
        If yes:
        1. Merge all agent branches into the feature branch
        2. Run build verification in the feature worktree
        3. Generate test_plan summarizing what was built and how to test it
        4. Transition parent: IN_PROGRESS → TESTING_READY
        5. Broadcast WebSocket notification to human
        """

    async def _execute_merge(self, task: Task) -> None:
        """
        Called for tasks in MERGING state (human passed manual testing).

        1. Merge feature branch into main (via WorktreeManager.merge_branch)
        2. If merge succeeds:
           a. Sync all other feature worktrees (rebase onto new main)
           b. Clean up the feature worktree
           c. Transition: MERGING → DONE
        3. If merge conflicts:
           a. Log conflict details
           b. Transition: MERGING → FAILED with conflict info
           c. Notify human via WebSocket
        """

    async def _on_agent_completed(self, agent: Agent, task: Task) -> None:
        """
        Callback when AgentRunner reports an agent finished.

        1. Read agent output/log
        2. Extract key decisions and file changes → store in task.context
        3. Create Memory records for significant findings
        4. Merge agent branch into feature branch
        5. Clean up agent worktree
        6. Transition subtask: IN_PROGRESS → DONE
        7. Check if parent task is now complete
        """

    async def _on_agent_failed(self, agent: Agent, task: Task) -> None:
        """
        Callback when an agent fails (non-zero exit).

        1. Read agent error log
        2. Transition subtask: IN_PROGRESS → FAILED
        3. Decide: retry (create new agent) or escalate
        4. If retries exhausted: notify human
        """
```

#### 2. `scheduler.py`

Task scheduling and agent assignment logic.

```python
class Scheduler:
    def __init__(self, agent_runner: AgentRunner, db_session_factory):
        pass

    async def get_assignable_tasks(self, project_id: UUID) -> list[Task]:
        """
        Find tasks that are ready to be assigned:
        - Status: BACKLOG (for subtasks that skip planning)
        - No unmet dependencies (all dependency_ids tasks are DONE)
        - Not already assigned to an agent
        - Parent task is IN_PROGRESS
        """

    async def get_idle_agents(self, project_id: UUID) -> list[Agent]:
        """Find agents with status IDLE for this project."""

    async def assign_task(self, task: Task, feature_name: str) -> Agent:
        """
        Assign a task to an agent:
        1. Determine agent_type from task (code tasks → coder, research → researcher)
        2. Check for idle agent of matching type → reuse
        3. If none idle → spawn new agent via AgentRunner
        4. Update task.assigned_agent_id
        5. Transition task to IN_PROGRESS
        6. Return the agent
        """

    async def get_schedule_summary(self, project_id: UUID) -> ScheduleSummary:
        """
        Return overview: tasks per status, agents per status,
        blocked tasks and what blocks them, estimated queue depth.
        """

@dataclass
class ScheduleSummary:
    tasks_by_status: dict[TaskStatus, int]
    agents_by_status: dict[AgentStatus, int]
    blocked_tasks: list[tuple[UUID, list[UUID]]]  # (task_id, blocking_task_ids)
    queue_depth: int  # assignable tasks waiting for agents
```

### Tests

#### 3. `tests/test_orchestrator.py`

- `test_backlog_to_planning` — create backlog task, run one iteration, verify PLANNING
- `test_planning_to_plan_review` — mock planner agent completion, verify plan stored and PLAN_REVIEW
- `test_plan_approval_creates_subtasks` — approve plan, verify subtasks created as BACKLOG
- `test_plan_rejection_returns_to_planning` — reject with feedback, verify PLANNING + feedback stored
- `test_subtask_completion_triggers_testing_ready` — all subtasks DONE → parent TESTING_READY
- `test_test_pass_triggers_merge` — pass test → MERGING → DONE
- `test_test_fail_returns_to_in_progress` — fail test → IN_PROGRESS with feedback
- `test_merge_success` — verify feature merged to main, worktree cleaned
- `test_merge_conflict` — verify FAILED status on conflict

#### 4. `tests/test_scheduler.py`

- `test_assignable_tasks_respects_dependencies` — task with unmet deps not returned
- `test_assign_reuses_idle_agent` — idle agent picked over spawning new
- `test_assign_spawns_new_agent` — no idle agents → new agent spawned
- `test_schedule_summary` — verify counts

## Critical: Human Gate Flow

The orchestrator MUST NOT bypass human gates. Specifically:

1. **Plan Review Gate**: After the planner agent decomposes a task, the orchestrator
   transitions to `PLAN_REVIEW` and **stops**. It does NOT create subtasks or assign
   agents until the human approves via the API (`POST /api/tasks/{id}/plan-review`).

2. **Manual Testing Gate**: After all subtasks complete and agent branches are merged,
   the orchestrator transitions to `TESTING_READY` and **stops**. It does NOT merge
   to main until the human passes the test via the API (`POST /api/tasks/{id}/test-result`).

The orchestrator loop simply skips tasks in `PLAN_REVIEW`, `TESTING_READY`, and
`MANUAL_TESTING` states — those are owned by the human.

## Build Verification

```bash
uv sync
uv run pytest tests/test_orchestrator.py tests/test_scheduler.py -v
```
