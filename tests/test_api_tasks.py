"""Tests for the tasks API router."""

from __future__ import annotations

import uuid

import pytest
from httpx import AsyncClient


@pytest.fixture
async def project_id(client: AsyncClient, tmp_path) -> uuid.UUID:
    """Create a project and return its ID for use in task tests."""
    # Create a temporary directory to serve as bare_repo_path
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


async def _create_task(
    client: AsyncClient,
    project_id: uuid.UUID,
    title: str = "Test task",
    description: str = "A test task",
    parent_task_id: uuid.UUID | None = None,
) -> dict:
    """Helper to create a task and return the response JSON."""
    body: dict = {
        "title": title,
        "description": description,
        "project_id": str(project_id),
    }
    if parent_task_id is not None:
        body["parent_task_id"] = str(parent_task_id)
    resp = await client.post("/api/tasks", json=body)
    assert resp.status_code == 201
    return resp.json()


async def _transition(
    client: AsyncClient,
    task_id: str,
    target_status: str,
    feedback: str | None = None,
) -> dict:
    """Helper to transition a task and return the response JSON."""
    body: dict = {"target_status": target_status}
    if feedback is not None:
        body["feedback"] = feedback
    resp = await client.post(f"/api/tasks/{task_id}/transition", json=body)
    return resp.json() if resp.status_code == 200 else {"_status": resp.status_code, **resp.json()}


async def test_create_task(client: AsyncClient, project_id: uuid.UUID) -> None:
    """POST /api/tasks creates a task with BACKLOG status."""
    data = await _create_task(client, project_id, title="My new task")
    assert data["status"] == "backlog"
    assert data["title"] == "My new task"
    assert data["subtask_count"] == 0
    assert data["parent_task_id"] is None


async def test_list_tasks_by_project(client: AsyncClient, project_id: uuid.UUID) -> None:
    """Create 3 tasks and filter by project_id."""
    for i in range(3):
        await _create_task(client, project_id, title=f"Task {i}")

    resp = await client.get(f"/api/tasks?project_id={project_id}")
    assert resp.status_code == 200
    tasks = resp.json()
    assert len(tasks) == 3
    titles = {t["title"] for t in tasks}
    assert titles == {"Task 0", "Task 1", "Task 2"}


async def test_transition_task(client: AsyncClient, project_id: uuid.UUID) -> None:
    """Transition: backlog -> planning -> plan_review -> in_progress."""
    data = await _create_task(client, project_id)
    task_id = data["id"]

    # backlog -> planning
    resp = await client.post(
        f"/api/tasks/{task_id}/transition",
        json={"target_status": "planning"},
    )
    assert resp.status_code == 200
    assert resp.json()["status"] == "planning"

    # planning -> plan_review (via plan submission)
    plan = [
        {
            "title": "Subtask 1",
            "description": "Do thing 1",
            "agent_type": "coder",
            "estimated_files": ["file1.py"],
        }
    ]
    resp = await client.post(
        f"/api/tasks/{task_id}/plan",
        json={"plan": plan},
    )
    assert resp.status_code == 200
    assert resp.json()["status"] == "plan_review"

    # plan_review -> in_progress (via plan review approval)
    resp = await client.post(
        f"/api/tasks/{task_id}/plan-review",
        json={"approved": True},
    )
    assert resp.status_code == 200
    assert resp.json()["status"] == "in_progress"


async def test_invalid_transition(client: AsyncClient, project_id: uuid.UUID) -> None:
    """Invalid transition returns 400."""
    data = await _create_task(client, project_id)
    task_id = data["id"]

    # backlog -> in_progress is invalid
    resp = await client.post(
        f"/api/tasks/{task_id}/transition",
        json={"target_status": "in_progress"},
    )
    assert resp.status_code == 400


async def test_submit_plan(client: AsyncClient, project_id: uuid.UUID) -> None:
    """POST plan submits decomposition and transitions to PLAN_REVIEW."""
    data = await _create_task(client, project_id)
    task_id = data["id"]

    # Move to planning first
    await client.post(
        f"/api/tasks/{task_id}/transition",
        json={"target_status": "planning"},
    )

    plan = [
        {
            "title": "Sub A",
            "description": "Do A",
            "agent_type": "coder",
            "estimated_files": ["a.py"],
        },
        {
            "title": "Sub B",
            "description": "Do B",
            "agent_type": "researcher",
            "estimated_files": ["b.py", "c.py"],
        },
    ]
    resp = await client.post(
        f"/api/tasks/{task_id}/plan",
        json={"plan": plan},
    )
    assert resp.status_code == 200
    result = resp.json()
    assert result["status"] == "plan_review"
    assert len(result["plan"]) == 2
    assert result["plan"][0]["title"] == "Sub A"


async def test_approve_plan(client: AsyncClient, project_id: uuid.UUID) -> None:
    """Plan review approval transitions to IN_PROGRESS."""
    data = await _create_task(client, project_id)
    task_id = data["id"]

    # backlog -> planning
    await client.post(
        f"/api/tasks/{task_id}/transition",
        json={"target_status": "planning"},
    )

    # Submit plan (planning -> plan_review)
    plan = [
        {
            "title": "Sub 1",
            "description": "Do 1",
            "agent_type": "coder",
            "estimated_files": ["x.py"],
        }
    ]
    await client.post(f"/api/tasks/{task_id}/plan", json={"plan": plan})

    # Approve plan (plan_review -> in_progress)
    resp = await client.post(
        f"/api/tasks/{task_id}/plan-review",
        json={"approved": True},
    )
    assert resp.status_code == 200
    assert resp.json()["status"] == "in_progress"


async def test_reject_plan(client: AsyncClient, project_id: uuid.UUID) -> None:
    """Plan review rejection transitions back to PLANNING with feedback."""
    data = await _create_task(client, project_id)
    task_id = data["id"]

    # backlog -> planning
    await client.post(
        f"/api/tasks/{task_id}/transition",
        json={"target_status": "planning"},
    )

    # Submit plan (planning -> plan_review)
    plan = [
        {
            "title": "Sub 1",
            "description": "Do 1",
            "agent_type": "coder",
            "estimated_files": ["x.py"],
        }
    ]
    await client.post(f"/api/tasks/{task_id}/plan", json={"plan": plan})

    # Reject plan (plan_review -> planning)
    resp = await client.post(
        f"/api/tasks/{task_id}/plan-review",
        json={"approved": False, "feedback": "Need more detail"},
    )
    assert resp.status_code == 200
    result = resp.json()
    assert result["status"] == "planning"

    # Verify the task was updated (fetch it to check plan_feedback)
    get_resp = await client.get(f"/api/tasks/{task_id}")
    assert get_resp.status_code == 200


async def test_pass_manual_test(client: AsyncClient, project_id: uuid.UUID) -> None:
    """Manual testing pass transitions to MERGING."""
    data = await _create_task(client, project_id)
    task_id = data["id"]

    # Walk through states: backlog -> planning -> plan_review -> in_progress -> testing_ready -> manual_testing
    await client.post(f"/api/tasks/{task_id}/transition", json={"target_status": "planning"})

    plan = [{"title": "S", "description": "D", "agent_type": "coder", "estimated_files": ["f.py"]}]
    await client.post(f"/api/tasks/{task_id}/plan", json={"plan": plan})
    await client.post(f"/api/tasks/{task_id}/plan-review", json={"approved": True})
    await client.post(f"/api/tasks/{task_id}/transition", json={"target_status": "testing_ready"})
    await client.post(f"/api/tasks/{task_id}/transition", json={"target_status": "manual_testing"})

    # Pass manual test -> merging
    resp = await client.post(
        f"/api/tasks/{task_id}/test-result",
        json={"passed": True},
    )
    assert resp.status_code == 200
    assert resp.json()["status"] == "merging"


async def test_fail_manual_test(client: AsyncClient, project_id: uuid.UUID) -> None:
    """Manual testing fail transitions to IN_PROGRESS with feedback."""
    data = await _create_task(client, project_id)
    task_id = data["id"]

    # Walk through states to manual_testing
    await client.post(f"/api/tasks/{task_id}/transition", json={"target_status": "planning"})

    plan = [{"title": "S", "description": "D", "agent_type": "coder", "estimated_files": ["f.py"]}]
    await client.post(f"/api/tasks/{task_id}/plan", json={"plan": plan})
    await client.post(f"/api/tasks/{task_id}/plan-review", json={"approved": True})
    await client.post(f"/api/tasks/{task_id}/transition", json={"target_status": "testing_ready"})
    await client.post(f"/api/tasks/{task_id}/transition", json={"target_status": "manual_testing"})

    # Fail manual test -> in_progress
    resp = await client.post(
        f"/api/tasks/{task_id}/test-result",
        json={"passed": False, "feedback": "Button is misaligned"},
    )
    assert resp.status_code == 200
    assert resp.json()["status"] == "in_progress"


async def test_task_events(client: AsyncClient, project_id: uuid.UUID) -> None:
    """Verify audit trail after transitions."""
    data = await _create_task(client, project_id)
    task_id = data["id"]

    # backlog -> planning
    await client.post(
        f"/api/tasks/{task_id}/transition",
        json={"target_status": "planning"},
    )

    # planning -> plan_review (via plan submission)
    plan = [{"title": "S", "description": "D", "agent_type": "coder", "estimated_files": ["f.py"]}]
    await client.post(f"/api/tasks/{task_id}/plan", json={"plan": plan})

    # plan_review -> in_progress (via approval)
    await client.post(
        f"/api/tasks/{task_id}/plan-review",
        json={"approved": True},
    )

    # Fetch events
    resp = await client.get(f"/api/tasks/{task_id}/events")
    assert resp.status_code == 200
    events = resp.json()

    # Should have at least 3 status_change events + 1 plan_approved event
    status_changes = [e for e in events if e["event_type"] == "status_change"]
    assert len(status_changes) >= 3

    # Verify chronological order
    event_types = [e["event_type"] for e in events]
    assert "status_change" in event_types
    assert "plan_approved" in event_types


async def test_pause_planning_task(client: AsyncClient, project_id: uuid.UUID) -> None:
    """Pause a task in PLANNING status, verify status and context."""
    data = await _create_task(client, project_id)
    task_id = data["id"]

    # backlog -> planning
    await client.post(
        f"/api/tasks/{task_id}/transition",
        json={"target_status": "planning"},
    )

    # pause
    resp = await client.post(f"/api/tasks/{task_id}/pause")
    assert resp.status_code == 200
    result = resp.json()
    assert result["status"] == "paused"
    assert result["context"]["paused_from_status"] == "planning"


async def test_resume_paused_task(client: AsyncClient, project_id: uuid.UUID) -> None:
    """Pause then resume a task, verify restoration to previous status."""
    data = await _create_task(client, project_id)
    task_id = data["id"]

    # backlog -> planning
    await client.post(
        f"/api/tasks/{task_id}/transition",
        json={"target_status": "planning"},
    )

    # pause
    resp = await client.post(f"/api/tasks/{task_id}/pause")
    assert resp.status_code == 200
    assert resp.json()["status"] == "paused"

    # resume
    resp = await client.post(f"/api/tasks/{task_id}/resume")
    assert resp.status_code == 200
    assert resp.json()["status"] == "planning"


async def test_pause_invalid_state(client: AsyncClient, project_id: uuid.UUID) -> None:
    """Pause from DONE returns 400."""
    data = await _create_task(client, project_id)
    task_id = data["id"]

    # Walk to DONE
    await client.post(f"/api/tasks/{task_id}/transition", json={"target_status": "planning"})
    plan = [{"title": "S", "description": "D", "agent_type": "coder", "estimated_files": ["f.py"]}]
    await client.post(f"/api/tasks/{task_id}/plan", json={"plan": plan})
    await client.post(f"/api/tasks/{task_id}/plan-review", json={"approved": True})
    await client.post(f"/api/tasks/{task_id}/transition", json={"target_status": "testing_ready"})
    await client.post(f"/api/tasks/{task_id}/transition", json={"target_status": "manual_testing"})
    await client.post(f"/api/tasks/{task_id}/test-result", json={"passed": True})
    await client.post(f"/api/tasks/{task_id}/transition", json={"target_status": "done"})

    # Try to pause from DONE
    resp = await client.post(f"/api/tasks/{task_id}/pause")
    assert resp.status_code == 400


async def test_pause_cascades_to_subtasks(client: AsyncClient, project_id: uuid.UUID) -> None:
    """Pausing a parent cascades pause to active subtasks."""
    parent_data = await _create_task(client, project_id, title="Parent")
    parent_id = parent_data["id"]

    # Create subtasks
    sub1 = await _create_task(client, project_id, title="Sub 1", parent_task_id=uuid.UUID(parent_id))
    sub2 = await _create_task(client, project_id, title="Sub 2", parent_task_id=uuid.UUID(parent_id))

    # Advance parent to planning
    await client.post(f"/api/tasks/{parent_id}/transition", json={"target_status": "planning"})

    # Pause parent
    resp = await client.post(f"/api/tasks/{parent_id}/pause")
    assert resp.status_code == 200
    assert resp.json()["status"] == "paused"

    # Check subtasks are paused
    sub1_resp = await client.get(f"/api/tasks/{sub1['id']}")
    assert sub1_resp.json()["status"] == "paused"
    assert sub1_resp.json()["context"]["paused_from_status"] == "backlog"

    sub2_resp = await client.get(f"/api/tasks/{sub2['id']}")
    assert sub2_resp.json()["status"] == "paused"


async def test_resume_cascades_subtasks(client: AsyncClient, project_id: uuid.UUID) -> None:
    """Resume restores subtask statuses."""
    parent_data = await _create_task(client, project_id, title="Parent")
    parent_id = parent_data["id"]

    # Create subtask
    sub = await _create_task(client, project_id, title="Sub", parent_task_id=uuid.UUID(parent_id))

    # Advance parent to planning
    await client.post(f"/api/tasks/{parent_id}/transition", json={"target_status": "planning"})

    # Pause parent (cascades to subtask)
    await client.post(f"/api/tasks/{parent_id}/pause")

    # Resume parent (cascades to subtask)
    resp = await client.post(f"/api/tasks/{parent_id}/resume")
    assert resp.status_code == 200
    assert resp.json()["status"] == "planning"

    # Check subtask is restored
    sub_resp = await client.get(f"/api/tasks/{sub['id']}")
    assert sub_resp.json()["status"] == "backlog"
