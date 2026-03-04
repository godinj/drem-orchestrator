"""Tests for the projects API router."""

from __future__ import annotations

import uuid

import pytest
from httpx import AsyncClient


async def test_create_project(client: AsyncClient, tmp_path) -> None:
    """POST /api/projects creates a project and returns it."""
    repo_dir = tmp_path / "test-repo.git"
    repo_dir.mkdir()

    resp = await client.post(
        "/api/projects",
        json={
            "name": f"proj-{uuid.uuid4().hex[:8]}",
            "bare_repo_path": str(repo_dir),
            "description": "A test project",
        },
    )
    assert resp.status_code == 201
    data = resp.json()
    assert data["bare_repo_path"] == str(repo_dir)
    assert data["description"] == "A test project"
    assert data["default_branch"] == "master"
    assert data["task_counts"] == {}
    assert data["agent_count"] == 0


async def test_create_project_invalid_path(client: AsyncClient) -> None:
    """POST /api/projects with nonexistent bare_repo_path returns 400."""
    resp = await client.post(
        "/api/projects",
        json={
            "name": "bad-project",
            "bare_repo_path": "/nonexistent/path/repo.git",
        },
    )
    assert resp.status_code == 400


async def test_get_board(client: AsyncClient, tmp_path) -> None:
    """GET /api/projects/{id}/board returns tasks grouped by status."""
    # Create project
    repo_dir = tmp_path / "board-repo.git"
    repo_dir.mkdir()

    proj_resp = await client.post(
        "/api/projects",
        json={
            "name": f"board-proj-{uuid.uuid4().hex[:8]}",
            "bare_repo_path": str(repo_dir),
        },
    )
    assert proj_resp.status_code == 201
    project_id = proj_resp.json()["id"]

    # Create task 1 (stays in backlog)
    await client.post(
        "/api/tasks",
        json={
            "title": "Backlog task",
            "description": "Stays in backlog",
            "project_id": project_id,
        },
    )

    # Create task 2 and move to planning
    resp2 = await client.post(
        "/api/tasks",
        json={
            "title": "Planning task",
            "description": "In planning",
            "project_id": project_id,
        },
    )
    task2_id = resp2.json()["id"]
    await client.post(
        f"/api/tasks/{task2_id}/transition",
        json={"target_status": "planning"},
    )

    # Create task 3 and move to planning, then submit plan (plan_review)
    resp3 = await client.post(
        "/api/tasks",
        json={
            "title": "Review task",
            "description": "In plan review",
            "project_id": project_id,
        },
    )
    task3_id = resp3.json()["id"]
    await client.post(
        f"/api/tasks/{task3_id}/transition",
        json={"target_status": "planning"},
    )
    plan = [
        {
            "title": "Sub",
            "description": "D",
            "agent_type": "coder",
            "estimated_files": ["f.py"],
        }
    ]
    await client.post(f"/api/tasks/{task3_id}/plan", json={"plan": plan})

    # Get board
    board_resp = await client.get(f"/api/projects/{project_id}/board")
    assert board_resp.status_code == 200
    board = board_resp.json()

    # Board should have all TaskStatus keys
    assert "backlog" in board
    assert "planning" in board
    assert "plan_review" in board
    assert "in_progress" in board
    assert "done" in board

    # Verify correct grouping
    assert len(board["backlog"]) == 1
    assert board["backlog"][0]["title"] == "Backlog task"

    assert len(board["planning"]) == 1
    assert board["planning"][0]["title"] == "Planning task"

    assert len(board["plan_review"]) == 1
    assert board["plan_review"][0]["title"] == "Review task"


async def test_list_projects(client: AsyncClient, tmp_path) -> None:
    """GET /api/projects returns all projects."""
    repo_dir = tmp_path / "list-repo.git"
    repo_dir.mkdir()

    await client.post(
        "/api/projects",
        json={
            "name": f"list-proj-{uuid.uuid4().hex[:8]}",
            "bare_repo_path": str(repo_dir),
        },
    )

    resp = await client.get("/api/projects")
    assert resp.status_code == 200
    projects = resp.json()
    assert len(projects) >= 1


async def test_get_project(client: AsyncClient, tmp_path) -> None:
    """GET /api/projects/{id} returns a single project with task_counts."""
    repo_dir = tmp_path / "get-repo.git"
    repo_dir.mkdir()

    create_resp = await client.post(
        "/api/projects",
        json={
            "name": f"get-proj-{uuid.uuid4().hex[:8]}",
            "bare_repo_path": str(repo_dir),
        },
    )
    project_id = create_resp.json()["id"]

    # Create a task
    await client.post(
        "/api/tasks",
        json={
            "title": "A task",
            "description": "Desc",
            "project_id": project_id,
        },
    )

    resp = await client.get(f"/api/projects/{project_id}")
    assert resp.status_code == 200
    data = resp.json()
    assert data["id"] == project_id
    assert data["task_counts"].get("backlog", 0) == 1
