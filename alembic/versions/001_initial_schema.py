"""Initial schema: projects, tasks, agents, task_events, memories.

Revision ID: 001
Revises:
Create Date: 2026-03-03
"""

from typing import Sequence, Union

import sqlalchemy as sa
from alembic import op

revision: str = "001"
down_revision: Union[str, None] = None
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None


def upgrade() -> None:
    # --- projects ---
    op.create_table(
        "projects",
        sa.Column("id", sa.Uuid(), nullable=False),
        sa.Column("name", sa.String(255), nullable=False),
        sa.Column("bare_repo_path", sa.String(1024), nullable=False),
        sa.Column("default_branch", sa.String(255), nullable=False, server_default="master"),
        sa.Column("description", sa.Text(), nullable=True),
        sa.Column("created_at", sa.DateTime(timezone=True), nullable=False),
        sa.Column("updated_at", sa.DateTime(timezone=True), nullable=False),
        sa.PrimaryKeyConstraint("id"),
        sa.UniqueConstraint("name"),
    )

    # --- agents (created before tasks to allow tasks.assigned_agent_id FK) ---
    op.create_table(
        "agents",
        sa.Column("id", sa.Uuid(), nullable=False),
        sa.Column("project_id", sa.Uuid(), nullable=False),
        sa.Column(
            "agent_type",
            sa.Enum("orchestrator", "planner", "coder", "researcher", name="agenttype"),
            nullable=False,
        ),
        sa.Column("name", sa.String(255), nullable=False),
        sa.Column(
            "status",
            sa.Enum("idle", "working", "blocked", "dead", name="agentstatus"),
            nullable=False,
            server_default="idle",
        ),
        sa.Column("current_task_id", sa.Uuid(), nullable=True),
        sa.Column("worktree_path", sa.String(1024), nullable=True),
        sa.Column("worktree_branch", sa.String(255), nullable=True),
        sa.Column("memory_summary", sa.Text(), nullable=True),
        sa.Column("heartbeat_at", sa.DateTime(timezone=True), nullable=True),
        sa.Column("config", sa.JSON(), nullable=True),
        sa.Column("created_at", sa.DateTime(timezone=True), nullable=False),
        sa.Column("updated_at", sa.DateTime(timezone=True), nullable=False),
        sa.PrimaryKeyConstraint("id"),
        sa.ForeignKeyConstraint(["project_id"], ["projects.id"]),
        # NOTE: current_task_id FK to tasks deferred — circular dependency.
        # Enforced at application level.
    )
    op.create_index("ix_agents_project_id", "agents", ["project_id"])
    op.create_index("ix_agents_status", "agents", ["status"])

    # --- tasks ---
    op.create_table(
        "tasks",
        sa.Column("id", sa.Uuid(), nullable=False),
        sa.Column("project_id", sa.Uuid(), nullable=False),
        sa.Column("parent_task_id", sa.Uuid(), nullable=True),
        sa.Column("title", sa.String(500), nullable=False),
        sa.Column("description", sa.Text(), nullable=False),
        sa.Column(
            "status",
            sa.Enum(
                "backlog",
                "planning",
                "plan_review",
                "in_progress",
                "testing_ready",
                "manual_testing",
                "merging",
                "done",
                "failed",
                name="taskstatus",
            ),
            nullable=False,
            server_default="backlog",
        ),
        sa.Column("priority", sa.Integer(), nullable=False, server_default="0"),
        sa.Column("labels", sa.JSON(), nullable=True),
        sa.Column("dependency_ids", sa.JSON(), nullable=True),
        sa.Column("assigned_agent_id", sa.Uuid(), nullable=True),
        sa.Column("plan", sa.JSON(), nullable=True),
        sa.Column("plan_feedback", sa.Text(), nullable=True),
        sa.Column("test_plan", sa.Text(), nullable=True),
        sa.Column("test_feedback", sa.Text(), nullable=True),
        sa.Column("worktree_branch", sa.String(255), nullable=True),
        sa.Column("pr_url", sa.String(1024), nullable=True),
        sa.Column("context", sa.JSON(), nullable=True),
        sa.Column("created_at", sa.DateTime(timezone=True), nullable=False),
        sa.Column("updated_at", sa.DateTime(timezone=True), nullable=False),
        sa.PrimaryKeyConstraint("id"),
        sa.ForeignKeyConstraint(["project_id"], ["projects.id"]),
        sa.ForeignKeyConstraint(["parent_task_id"], ["tasks.id"]),
        sa.ForeignKeyConstraint(["assigned_agent_id"], ["agents.id"]),
    )
    op.create_index("ix_tasks_project_id", "tasks", ["project_id"])
    op.create_index("ix_tasks_status", "tasks", ["status"])
    op.create_index("ix_tasks_parent_task_id", "tasks", ["parent_task_id"])
    op.create_index("ix_tasks_assigned_agent_id", "tasks", ["assigned_agent_id"])

    # --- task_events ---
    op.create_table(
        "task_events",
        sa.Column("id", sa.Uuid(), nullable=False),
        sa.Column("task_id", sa.Uuid(), nullable=False),
        sa.Column("event_type", sa.String(100), nullable=False),
        sa.Column("old_value", sa.String(255), nullable=True),
        sa.Column("new_value", sa.String(255), nullable=True),
        sa.Column("details", sa.JSON(), nullable=True),
        sa.Column("actor", sa.String(255), nullable=False),
        sa.Column("created_at", sa.DateTime(timezone=True), nullable=False),
        sa.PrimaryKeyConstraint("id"),
        sa.ForeignKeyConstraint(["task_id"], ["tasks.id"]),
    )
    op.create_index("ix_task_events_task_id", "task_events", ["task_id"])

    # --- memories ---
    op.create_table(
        "memories",
        sa.Column("id", sa.Uuid(), nullable=False),
        sa.Column("agent_id", sa.Uuid(), nullable=False),
        sa.Column("task_id", sa.Uuid(), nullable=True),
        sa.Column("content", sa.Text(), nullable=False),
        sa.Column("memory_type", sa.String(100), nullable=False),
        sa.Column("metadata", sa.JSON(), nullable=True),
        sa.Column("created_at", sa.DateTime(timezone=True), nullable=False),
        sa.PrimaryKeyConstraint("id"),
        sa.ForeignKeyConstraint(["agent_id"], ["agents.id"]),
        sa.ForeignKeyConstraint(["task_id"], ["tasks.id"]),
    )
    op.create_index("ix_memories_agent_id", "memories", ["agent_id"])


def downgrade() -> None:
    op.drop_table("memories")
    op.drop_table("task_events")
    op.drop_table("tasks")
    op.drop_table("agents")
    op.drop_table("projects")
