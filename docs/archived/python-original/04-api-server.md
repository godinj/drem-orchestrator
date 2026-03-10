# Agent: API Server

You are working on **Drem Orchestrator**, a Python application that orchestrates Claude Code agents
working in parallel across git worktrees. Your task is to build the FastAPI REST and WebSocket API
that the task board UI consumes.

## Context

Read these before starting:
- `CLAUDE.md` (project conventions)
- `src/orchestrator/models.py` (SQLAlchemy models — Task, Agent, Project, Memory, TaskEvent)
- `src/orchestrator/schemas.py` (Pydantic schemas — TaskCreate, TaskResponse, PlanReview, TestResult, etc.)
- `src/orchestrator/enums.py` (TaskStatus, AgentType, AgentStatus)
- `src/orchestrator/state_machine.py` (transition validation, human gates)
- `src/orchestrator/db.py` (async session factory)
- `src/orchestrator/server.py` (existing FastAPI skeleton)

## Dependencies

This agent depends on Agent 02 (Data Model). If those files don't exist yet,
create stub files with the interfaces from the model prompt and implement against them.

## Deliverables

### New files (`src/orchestrator/`)

#### 1. `routers/tasks.py`

FastAPI APIRouter mounted at `/api/tasks`.

**Endpoints:**

```
GET    /api/tasks?project_id=&status=&parent_task_id=
       → list[TaskResponse]
       Filters: project_id (required), status (optional), parent_task_id (optional, null=top-level)
       Includes subtask_count as computed field.

POST   /api/tasks
       Body: TaskCreate
       → TaskResponse (status: BACKLOG)
       Creates task, emits WebSocket event.

GET    /api/tasks/{task_id}
       → TaskResponse
       Includes full subtask list.

PATCH  /api/tasks/{task_id}
       Body: TaskUpdate
       → TaskResponse
       Updates title, description, priority, labels.

POST   /api/tasks/{task_id}/transition
       Body: TaskTransition(target_status, feedback?)
       → TaskResponse
       Validates via state_machine.validate_transition().
       If target is PLAN_REVIEW → requires plan field to be set.
       If target is IN_PROGRESS from PLAN_REVIEW → plan was approved.
       If target is PLANNING from PLAN_REVIEW → plan was rejected, stores feedback.
       If target is MERGING from MANUAL_TESTING → test passed.
       If target is IN_PROGRESS from MANUAL_TESTING → test failed, stores feedback.
       Creates TaskEvent. Emits WebSocket event.

POST   /api/tasks/{task_id}/plan
       Body: PlanSubmission(plan: list[SubtaskPlan])
       → TaskResponse
       Orchestrator submits its decomposition plan.
       Stores plan as JSON on the task.
       Transitions task to PLAN_REVIEW.
       Emits WebSocket event (notification to human).

POST   /api/tasks/{task_id}/plan-review
       Body: PlanReview(approved: bool, feedback?: str)
       → TaskResponse
       Human approves or rejects the plan.
       If approved: transitions to IN_PROGRESS, orchestrator creates subtasks from plan.
       If rejected: transitions back to PLANNING, stores feedback in plan_feedback.
       Creates TaskEvent. Emits WebSocket event.

POST   /api/tasks/{task_id}/test-result
       Body: TestResult(passed: bool, feedback?: str)
       → TaskResponse
       Human reports manual testing result.
       If passed: transitions to MERGING.
       If failed: transitions to IN_PROGRESS, stores feedback in test_feedback.
       Creates TaskEvent. Emits WebSocket event.

GET    /api/tasks/{task_id}/events
       → list[TaskEventResponse]
       Full audit trail for a task.
```

#### 2. `routers/agents.py`

FastAPI APIRouter mounted at `/api/agents`.

```
GET    /api/agents?project_id=&status=
       → list[AgentResponse]

GET    /api/agents/{agent_id}
       → AgentResponse (includes current task info)

POST   /api/agents/{agent_id}/heartbeat
       → {"ok": true}
       Updates heartbeat_at. Used by agent_runner.
```

#### 3. `routers/projects.py`

FastAPI APIRouter mounted at `/api/projects`.

```
GET    /api/projects
       → list[ProjectResponse]
       Each includes task_counts (dict of status → count) and agent_count.

POST   /api/projects
       Body: ProjectCreate
       → ProjectResponse
       Validates bare_repo_path exists on disk.

GET    /api/projects/{project_id}
       → ProjectResponse (detailed, with task_counts)

GET    /api/projects/{project_id}/board
       → dict[TaskStatus, list[TaskResponse]]
       Returns tasks grouped by status for kanban rendering.
       Only returns top-level tasks (parent_task_id is null).
```

#### 4. `routers/ws.py`

WebSocket endpoint for real-time UI updates.

```
WS     /api/ws/{project_id}
```

On connect: sends current board state.
On task/agent changes: broadcasts JSON events:

```json
{"type": "task_updated", "task": TaskResponse}
{"type": "task_created", "task": TaskResponse}
{"type": "agent_updated", "agent": AgentResponse}
{"type": "plan_submitted", "task_id": "...", "plan": [...]}
{"type": "testing_ready", "task_id": "...", "test_plan": "..."}
```

Implementation:
- Maintain a `dict[UUID, set[WebSocket]]` of connections per project
- On disconnect, remove from set
- `broadcast(project_id, event)` sends to all connected clients
- Export `broadcast` function for use by other modules (routers, orchestrator)

#### 5. `routers/__init__.py`

Empty init.

### Migration

#### 6. `src/orchestrator/server.py`

Update the existing skeleton:
- Import and include all routers with `/api` prefix
- Wire up WebSocket endpoint
- Add `broadcast` to app state for dependency injection

### Tests

#### 7. `tests/test_api_tasks.py`

- `test_create_task` — POST, verify BACKLOG status
- `test_list_tasks_by_project` — create 3, filter by project
- `test_transition_task` — backlog → planning → plan_review → in_progress
- `test_invalid_transition` — returns 400
- `test_submit_plan` — POST plan, verify PLAN_REVIEW status
- `test_approve_plan` — plan review → in_progress
- `test_reject_plan` — plan review → planning with feedback
- `test_pass_manual_test` — manual_testing → merging
- `test_fail_manual_test` — manual_testing → in_progress with feedback
- `test_task_events` — verify audit trail after transitions

#### 8. `tests/test_api_projects.py`

- `test_create_project` — POST, verify response
- `test_get_board` — create tasks in different statuses, verify grouping

## Build Verification

```bash
uv sync
uv run pytest tests/test_api_tasks.py tests/test_api_projects.py -v
```
