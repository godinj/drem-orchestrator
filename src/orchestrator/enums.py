"""Enumerations used across the orchestrator.

Stub module — will be fleshed out by the data-model agent.
"""

from __future__ import annotations

from enum import StrEnum


class TaskStatus(StrEnum):
    """Status of a task in the orchestrator."""

    PENDING = "pending"
    ASSIGNED = "assigned"
    RUNNING = "running"
    MERGING = "merging"
    DONE = "done"
    FAILED = "failed"
    BLOCKED = "blocked"


class AgentStatus(StrEnum):
    """Status of an agent."""

    IDLE = "idle"
    WORKING = "working"
    DONE = "done"
    FAILED = "failed"
