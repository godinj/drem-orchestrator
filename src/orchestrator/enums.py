"""String enums for task status, agent type, and agent status."""

import enum


class TaskStatus(str, enum.Enum):
    BACKLOG = "backlog"
    PLANNING = "planning"
    PLAN_REVIEW = "plan_review"  # human gate: approve decomposition plan
    IN_PROGRESS = "in_progress"
    TESTING_READY = "testing_ready"
    MANUAL_TESTING = "manual_testing"  # human gate: approve feature
    MERGING = "merging"
    PAUSED = "paused"
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
