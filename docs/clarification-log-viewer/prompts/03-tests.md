# Agent: Tests — Clarification Flow & Log Viewer

You are working on the `master` branch of Drem Orchestrator, a Python FastAPI application that orchestrates Claude Code agents.
Your task is writing tests for two new features: (1) a clarification question flow for planner agents, and (2) an agent log viewer endpoint.

## Context

Read these files before starting:
- `CLAUDE.md` (project conventions, test commands)
- `tests/conftest.py` (test fixtures: `db_session`, `client` with in-memory SQLite)
- `tests/test_state_machine.py` (state machine test patterns — `TestValidateTransition`, `TestHumanGates`, `TestTransitionTask`)
- `tests/test_api_tasks.py` (API test patterns — `_create_task()`, `_transition()` helpers, `project_id` fixture)
- `tests/test_agent_prompt.py` (prompt test patterns — `_make_task()`, `_make_project()` mock helpers)
- `src/orchestrator/enums.py` (will have new `NEEDS_CLARIFICATION` status)
- `src/orchestrator/state_machine.py` (will have new transitions and human gate)
- `src/orchestrator/schemas.py` (will have `ClarificationSubmission`, `ClarificationAnswer`)
- `src/orchestrator/orchestrator.py` (will have `_on_planner_asked_questions()`)
- `src/orchestrator/agent_prompt.py` (will have updated `_planner_instructions()` with clarification option and prior Q&A rendering)
- `src/orchestrator/routers/tasks.py` (will have `POST /{task_id}/clarifications`)
- `src/orchestrator/routers/agents.py` (will have `GET /{agent_id}/log`)

## Dependencies

This agent depends on Agent 01 (Backend). If the backend changes don't exist yet, create stubs matching the spec below and test against them.

The new `TaskStatus.NEEDS_CLARIFICATION` enum value will be available. The state machine will have:
- `PLANNING -> NEEDS_CLARIFICATION` (valid)
- `NEEDS_CLARIFICATION -> PLANNING` (valid)
- `NEEDS_CLARIFICATION` in `HUMAN_GATES`

The API will have:
- `POST /api/tasks/{task_id}/clarifications` with body `{"answers": [{"question_id": "q1", "answer": "..."}]}`
- `GET /api/agents/{agent_id}/log` returning `{"log": "..."}`

## Deliverables

### 1. `tests/test_state_machine.py` (update existing)

**a)** Update `TestHumanGates`:

Add test:
```python
def test_needs_clarification_is_human_gate(self):
    assert is_human_gate(TaskStatus.NEEDS_CLARIFICATION) is True
```

Update `test_human_gates_set_contents`:
```python
def test_human_gates_set_contents(self):
    assert HUMAN_GATES == {
        TaskStatus.PLAN_REVIEW,
        TaskStatus.NEEDS_CLARIFICATION,
        TaskStatus.MANUAL_TESTING,
    }
```

**b)** The parametrized `test_valid_transitions_succeed` already auto-discovers all valid transitions from `VALID_TRANSITIONS`, so the new `PLANNING -> NEEDS_CLARIFICATION` and `NEEDS_CLARIFICATION -> PLANNING` will be tested automatically.

**c)** Add explicit invalid transition tests in `TestValidateTransition`:

```python
(TaskStatus.NEEDS_CLARIFICATION, TaskStatus.IN_PROGRESS),
(TaskStatus.NEEDS_CLARIFICATION, TaskStatus.DONE),
(TaskStatus.BACKLOG, TaskStatus.NEEDS_CLARIFICATION),
```

**d)** Add to `TestTransitionTask`:

```python
def test_planning_to_needs_clarification(self):
    """PLANNING -> NEEDS_CLARIFICATION (agent asked questions)."""
    task = self._make_task(TaskStatus.PLANNING.value)
    event = transition_task(
        task, TaskStatus.NEEDS_CLARIFICATION, actor="orchestrator",
        details={"questions": [{"id": "q1", "question": "Which auth?"}]}
    )
    assert task.status == TaskStatus.NEEDS_CLARIFICATION.value
    assert event.old_value == TaskStatus.PLANNING.value
    assert event.new_value == TaskStatus.NEEDS_CLARIFICATION.value

def test_needs_clarification_to_planning(self):
    """NEEDS_CLARIFICATION -> PLANNING (human answered)."""
    task = self._make_task(TaskStatus.NEEDS_CLARIFICATION.value)
    event = transition_task(
        task, TaskStatus.PLANNING, actor="human",
        details={"action": "clarification_answered"}
    )
    assert task.status == TaskStatus.PLANNING.value
    assert event.old_value == TaskStatus.NEEDS_CLARIFICATION.value
```

### 2. `tests/test_clarifications.py` (new file)

Create a comprehensive test file for the clarification API endpoint. Follow the patterns from `test_api_tasks.py`:

```python
"""Tests for the clarification question/answer flow."""

from __future__ import annotations

import uuid

import pytest
from httpx import AsyncClient


@pytest.fixture
async def project_id(client: AsyncClient, tmp_path) -> uuid.UUID:
    """Create a project and return its ID."""
    repo_dir = tmp_path / "test-repo.git"
    repo_dir.mkdir()
    resp = await client.post(
        "/api/projects",
        json={
            "name": f"test-project-{uuid.uuid4().hex[:8]}",
            "bare_repo_path": str(repo_dir),
        },
    )
    assert resp.status_code == 201
    return uuid.UUID(resp.json()["id"])


async def _create_task(client, project_id, **kwargs):
    """Helper to create a task."""
    body = {
        "title": kwargs.get("title", "Test task"),
        "description": kwargs.get("description", "A test task"),
        "project_id": str(project_id),
    }
    resp = await client.post("/api/tasks", json=body)
    assert resp.status_code == 201
    return resp.json()


async def _transition_to_needs_clarification(client, task_id):
    """Walk task to NEEDS_CLARIFICATION via transition endpoints.

    backlog -> planning -> needs_clarification
    """
    # backlog -> planning
    resp = await client.post(
        f"/api/tasks/{task_id}/transition",
        json={"target_status": "planning"},
    )
    assert resp.status_code == 200

    # planning -> needs_clarification
    resp = await client.post(
        f"/api/tasks/{task_id}/transition",
        json={"target_status": "needs_clarification"},
    )
    assert resp.status_code == 200
    return resp.json()


async def test_submit_clarification_answers(
    client: AsyncClient, project_id: uuid.UUID
) -> None:
    """POST clarification answers transitions NEEDS_CLARIFICATION -> PLANNING."""
    data = await _create_task(client, project_id)
    task_id = data["id"]

    await _transition_to_needs_clarification(client, task_id)

    # Submit answers
    resp = await client.post(
        f"/api/tasks/{task_id}/clarifications",
        json={
            "answers": [
                {"question_id": "q1", "answer": "Use JWT with refresh tokens"},
            ]
        },
    )
    assert resp.status_code == 200
    result = resp.json()
    assert result["status"] == "planning"


async def test_clarification_wrong_status(
    client: AsyncClient, project_id: uuid.UUID
) -> None:
    """POST clarifications on a non-NEEDS_CLARIFICATION task returns 400."""
    data = await _create_task(client, project_id)
    task_id = data["id"]

    # Task is in BACKLOG
    resp = await client.post(
        f"/api/tasks/{task_id}/clarifications",
        json={"answers": [{"question_id": "q1", "answer": "anything"}]},
    )
    assert resp.status_code == 400


async def test_clarification_not_found(client: AsyncClient) -> None:
    """POST clarifications on non-existent task returns 404."""
    fake_id = str(uuid.uuid4())
    resp = await client.post(
        f"/api/tasks/{fake_id}/clarifications",
        json={"answers": [{"question_id": "q1", "answer": "anything"}]},
    )
    assert resp.status_code == 404


async def test_clarification_events(
    client: AsyncClient, project_id: uuid.UUID
) -> None:
    """Verify audit trail includes clarification events."""
    data = await _create_task(client, project_id)
    task_id = data["id"]

    await _transition_to_needs_clarification(client, task_id)

    await client.post(
        f"/api/tasks/{task_id}/clarifications",
        json={"answers": [{"question_id": "q1", "answer": "Use JWT"}]},
    )

    # Fetch events
    resp = await client.get(f"/api/tasks/{task_id}/events")
    assert resp.status_code == 200
    events = resp.json()
    event_types = [e["event_type"] for e in events]
    assert "clarification_answered" in event_types
```

### 3. `tests/test_agent_prompt.py` (update existing)

Add tests to `TestPlannerPrompt`:

```python
def test_planner_prompt_includes_clarification_option(self) -> None:
    """Planner prompt should mention clarifications.json as an option."""
    task = _make_task()
    project = _make_project()
    worktree_path = Path("/tmp/worktree")

    prompt = generate_agent_prompt(
        task=task, project=project, agent_type="planner", worktree_path=worktree_path,
    )

    assert "clarifications.json" in prompt
    assert "clarification" in prompt.lower()

def test_planner_prompt_includes_prior_qa(self) -> None:
    """Planner prompt should render prior Q&A when clarifications exist in context."""
    task = _make_task(
        context={
            "clarifications": [
                {
                    "round": 1,
                    "questions": [
                        {"id": "q1", "question": "JWT or sessions?", "context": "Auth approach unclear"}
                    ],
                    "answers": [
                        {"question_id": "q1", "answer": "Use JWT with refresh tokens"}
                    ],
                }
            ]
        },
    )
    project = _make_project()
    worktree_path = Path("/tmp/worktree")

    prompt = generate_agent_prompt(
        task=task, project=project, agent_type="planner", worktree_path=worktree_path,
    )

    assert "JWT or sessions?" in prompt
    assert "Use JWT with refresh tokens" in prompt
    assert "Round 1" in prompt
    assert "Do NOT re-ask" in prompt or "Do not re-ask" in prompt.lower()

def test_planner_prompt_no_qa_without_clarifications(self) -> None:
    """Planner prompt should not have Q&A section when no clarifications."""
    task = _make_task(context={})
    project = _make_project()
    worktree_path = Path("/tmp/worktree")

    prompt = generate_agent_prompt(
        task=task, project=project, agent_type="planner", worktree_path=worktree_path,
    )

    assert "Prior Clarification" not in prompt
```

### 4. `tests/test_api_tasks.py` (update existing)

Add a test for the full clarification round-trip:

```python
async def test_clarification_round_trip(
    client: AsyncClient, project_id: uuid.UUID
) -> None:
    """Full flow: backlog -> planning -> needs_clarification -> planning -> plan_review."""
    data = await _create_task(client, project_id)
    task_id = data["id"]

    # backlog -> planning
    await client.post(
        f"/api/tasks/{task_id}/transition",
        json={"target_status": "planning"},
    )

    # planning -> needs_clarification
    resp = await client.post(
        f"/api/tasks/{task_id}/transition",
        json={"target_status": "needs_clarification"},
    )
    assert resp.status_code == 200
    assert resp.json()["status"] == "needs_clarification"

    # Submit answers -> planning
    resp = await client.post(
        f"/api/tasks/{task_id}/clarifications",
        json={"answers": [{"question_id": "q1", "answer": "Use JWT"}]},
    )
    assert resp.status_code == 200
    assert resp.json()["status"] == "planning"

    # Submit plan -> plan_review
    plan = [{"title": "S", "description": "D", "agent_type": "coder", "estimated_files": ["f.py"]}]
    resp = await client.post(f"/api/tasks/{task_id}/plan", json={"plan": plan})
    assert resp.status_code == 200
    assert resp.json()["status"] == "plan_review"
```

## Conventions

- `pytest` with `pytest-asyncio` (async tests)
- In-memory SQLite via `conftest.py` fixtures
- Follow existing test class/function naming patterns
- Use `_create_task()` and `_transition()` helpers
- Type hints on test fixtures

## Build Verification

```bash
uv run pytest -v
```
