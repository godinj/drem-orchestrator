# Agent: Data Model

You are working on **Drem Orchestrator**, a Python application that orchestrates Claude Code agents
working in parallel across git worktrees. Your task is to define the SQLAlchemy ORM models,
Pydantic schemas, task state machine, and initial Alembic migration.

## Context

Read these before starting:
- `CLAUDE.md` (project conventions and architecture)
- `src/orchestrator/server.py` (existing FastAPI skeleton)
- `src/orchestrator/db.py` (async engine setup)
- `src/orchestrator/config.py` (settings)

## Dependencies

This agent depends on Agent 01 (Project Scaffold). If those files don't exist yet,
create stub files with the interfaces described above and implement against them.

## Deliverables

### New files (`src/orchestrator/`)

#### 1. `models.py`

SQLAlchemy async ORM models using `DeclarativeBase`. All models use UUID primary keys
(`uuid.uuid4`), `created_at`/`updated_at` timestamps.

**`Project`**
- `id: UUID` (PK)
- `name: str` (unique)
- `bare_repo_path: str` — absolute path to the bare git repo
- `default_branch: str` — default `"master"`
- `description: str | None`
- `created_at: datetime`, `updated_at: datetime`

**`Task`**
- `id: UUID` (PK)
- `project_id: UUID` (FK → Project)
- `parent_task_id: UUID | None` (FK → Task, self-referential for subtasks)
- `title: str`
- `description: str` — detailed task description
- `status: TaskStatus` — enum column
- `priority: int` — default 0, higher = more important
- `labels: JSON` — list of string tags
- `dependency_ids: JSON` — list of task UUIDs this task depends on
- `assigned_agent_id: UUID | None` (FK → Agent)
- `plan: JSON | None` — orchestrator's decomposition plan (subtask list, rationale)
- `plan_feedback: str | None` — human feedback on rejected plan
- `test_plan: str | None` — how to manually test this feature
- `test_feedback: str | None` — human feedback on failed test
- `worktree_branch: str | None` — feature branch name
- `pr_url: str | None`
- `context: JSON` — accumulated knowledge, key decisions
- `created_at: datetime`, `updated_at: datetime`

Relationships:
- `project` → Project
- `parent_task` → Task (self)
- `subtasks` → list[Task] (self, back_populates parent_task)
- `assigned_agent` → Agent
- `events` → list[TaskEvent]

**`Agent`**
- `id: UUID` (PK)
- `project_id: UUID` (FK → Project)
- `agent_type: AgentType` — enum: `orchestrator`, `planner`, `coder`, `researcher`
- `name: str` — human-readable name
- `status: AgentStatus` — enum: `idle`, `working`, `blocked`, `dead`
- `current_task_id: UUID | None` (FK → Task)
- `worktree_path: str | None` — absolute path to agent's worktree
- `worktree_branch: str | None`
- `memory_summary: str | None` — compacted context from prior work
- `heartbeat_at: datetime | None`
- `config: JSON` — agent-specific configuration
- `created_at: datetime`, `updated_at: datetime`

**`TaskEvent`**
- `id: UUID` (PK)
- `task_id: UUID` (FK → Task)
- `event_type: str` — e.g. `"status_change"`, `"plan_submitted"`, `"plan_approved"`, `"plan_rejected"`, `"test_passed"`, `"test_failed"`, `"agent_assigned"`, `"merge_started"`, `"merge_completed"`
- `old_value: str | None`
- `new_value: str | None`
- `details: JSON | None` — extra context
- `actor: str` — `"human"`, `"orchestrator"`, or agent name
- `created_at: datetime`

**`Memory`**
- `id: UUID` (PK)
- `agent_id: UUID` (FK → Agent)
- `task_id: UUID | None` (FK → Task)
- `content: str` — the memory content (summary, decision, insight)
- `memory_type: str` — `"conversation_summary"`, `"decision"`, `"file_change"`, `"lesson_learned"`
- `metadata: JSON | None`
- `created_at: datetime`

#### 2. `enums.py`

Python string enums:

```python
import enum

class TaskStatus(str, enum.Enum):
    BACKLOG = "backlog"
    PLANNING = "planning"
    PLAN_REVIEW = "plan_review"         # human gate: approve decomposition plan
    IN_PROGRESS = "in_progress"
    TESTING_READY = "testing_ready"
    MANUAL_TESTING = "manual_testing"   # human gate: approve feature
    MERGING = "merging"
    DONE = "done"
    FAILED = "failed"

class AgentType(str, enum.Enum):
    ORCHESTRATOR = "orchestrator"
    PLANNER = "planner"
    CODER = "coder"
    RESEARCHER = "researcher"

class AgentStatus(str, enum.Enum):
    IDLE = "idle"
    WORKING = "working"
    BLOCKED = "blocked"
    DEAD = "dead"
```

#### 3. `state_machine.py`

Task state machine with transition validation:

```python
VALID_TRANSITIONS: dict[TaskStatus, list[TaskStatus]] = {
    TaskStatus.BACKLOG:         [TaskStatus.PLANNING],
    TaskStatus.PLANNING:        [TaskStatus.PLAN_REVIEW],
    TaskStatus.PLAN_REVIEW:     [TaskStatus.IN_PROGRESS, TaskStatus.PLANNING],  # approve or reject
    TaskStatus.IN_PROGRESS:     [TaskStatus.TESTING_READY, TaskStatus.FAILED],
    TaskStatus.TESTING_READY:   [TaskStatus.MANUAL_TESTING],
    TaskStatus.MANUAL_TESTING:  [TaskStatus.MERGING, TaskStatus.IN_PROGRESS],   # pass or fail
    TaskStatus.MERGING:         [TaskStatus.DONE, TaskStatus.FAILED],
    TaskStatus.DONE:            [],
    TaskStatus.FAILED:          [TaskStatus.BACKLOG],  # retry
}

HUMAN_GATES = {TaskStatus.PLAN_REVIEW, TaskStatus.MANUAL_TESTING}
```

Functions:
- `validate_transition(current: TaskStatus, target: TaskStatus) -> bool`
- `get_available_transitions(current: TaskStatus) -> list[TaskStatus]`
- `is_human_gate(status: TaskStatus) -> bool`
- `transition_task(task: Task, target: TaskStatus, actor: str, details: dict | None = None) -> TaskEvent` — validates transition, updates task status, creates TaskEvent, returns event

#### 4. `schemas.py`

Pydantic v2 schemas for API request/response:

**Task schemas:**
- `TaskCreate(title, description, project_id, priority?, labels?, parent_task_id?)`
- `TaskUpdate(title?, description?, priority?, labels?)`
- `TaskResponse(id, title, description, status, priority, labels, dependency_ids, assigned_agent_id, plan, test_plan, worktree_branch, pr_url, context, parent_task_id, subtask_count, created_at, updated_at)` — includes computed `subtask_count`
- `TaskTransition(target_status, feedback?)` — for status changes; `feedback` used for plan rejection or test failure
- `PlanSubmission(plan: list[SubtaskPlan])` — orchestrator submits decomposition
- `SubtaskPlan(title, description, agent_type, estimated_files: list[str])`
- `PlanReview(approved: bool, feedback?: str)` — human approves/rejects plan
- `TestResult(passed: bool, feedback?: str)` — human pass/fail

**Agent schemas:**
- `AgentResponse(id, name, agent_type, status, current_task_id, worktree_path, worktree_branch, heartbeat_at, created_at)`

**Project schemas:**
- `ProjectCreate(name, bare_repo_path, default_branch?, description?)`
- `ProjectResponse(id, name, bare_repo_path, default_branch, description, task_counts: dict[TaskStatus, int], agent_count, created_at)`

**Event schemas:**
- `TaskEventResponse(id, task_id, event_type, old_value, new_value, details, actor, created_at)`

### Migration

#### 5. `alembic/versions/001_initial_schema.py`

Alembic migration that creates all tables: `projects`, `tasks`, `agents`, `task_events`, `memories`.

Include indexes on:
- `tasks.project_id`
- `tasks.status`
- `tasks.parent_task_id`
- `tasks.assigned_agent_id`
- `agents.project_id`
- `agents.status`
- `task_events.task_id`
- `memories.agent_id`

### Tests

#### 6. `tests/test_state_machine.py`

Tests for the state machine:
- Test all valid transitions succeed
- Test invalid transitions raise `ValueError`
- Test human gates are correctly identified
- Test `PLAN_REVIEW → PLANNING` (plan rejected) and `PLAN_REVIEW → IN_PROGRESS` (plan approved)
- Test `MANUAL_TESTING → IN_PROGRESS` (test failed) and `MANUAL_TESTING → MERGING` (test passed)
- Test `FAILED → BACKLOG` (retry)

#### 7. `tests/test_models.py`

Tests for model creation:
- Create a project, task, agent, and memory
- Test Task self-referential parent/subtask relationship
- Test TaskEvent creation via `transition_task()`
- Test plan submission and review fields

## Build Verification

```bash
uv sync
uv run alembic upgrade head
uv run pytest tests/test_state_machine.py tests/test_models.py -v
```
