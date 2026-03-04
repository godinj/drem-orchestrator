export type TaskStatus =
  | "backlog"
  | "planning"
  | "plan_review"
  | "in_progress"
  | "paused"
  | "testing_ready"
  | "manual_testing"
  | "merging"
  | "done"
  | "failed";

export type AgentType = "orchestrator" | "planner" | "coder" | "researcher";
export type AgentStatus = "idle" | "working" | "blocked" | "dead";

export interface SubtaskPlan {
  title: string;
  description: string;
  agent_type: AgentType;
  estimated_files: string[];
}

export interface Task {
  id: string;
  title: string;
  description: string;
  status: TaskStatus;
  priority: number;
  labels: string[];
  dependency_ids: string[];
  assigned_agent_id: string | null;
  plan: SubtaskPlan[] | null;
  plan_feedback: string | null;
  test_plan: string | null;
  test_feedback: string | null;
  worktree_branch: string | null;
  pr_url: string | null;
  context: Record<string, unknown>;
  parent_task_id: string | null;
  subtask_count: number;
  created_at: string;
  updated_at: string;
}

export interface Agent {
  id: string;
  name: string;
  agent_type: AgentType;
  status: AgentStatus;
  current_task_id: string | null;
  worktree_path: string | null;
  worktree_branch: string | null;
  heartbeat_at: string | null;
  created_at: string;
}

export interface Project {
  id: string;
  name: string;
  bare_repo_path: string;
  default_branch: string;
  description: string | null;
  task_counts: Record<TaskStatus, number>;
  agent_count: number;
  created_at: string;
}

export interface TaskEvent {
  id: string;
  task_id: string;
  event_type: string;
  old_value: string | null;
  new_value: string | null;
  details: Record<string, unknown> | null;
  actor: string;
  created_at: string;
}

// WebSocket event types
export type WSEvent =
  | { type: "task_updated"; task: Task }
  | { type: "task_created"; task: Task }
  | { type: "agent_updated"; agent: Agent }
  | { type: "plan_submitted"; task_id: string; plan: SubtaskPlan[] }
  | { type: "testing_ready"; task_id: string; test_plan: string };

// Column definitions for the board
export const COLUMN_ORDER: TaskStatus[] = [
  "backlog",
  "planning",
  "plan_review",
  "in_progress",
  "paused",
  "testing_ready",
  "manual_testing",
  "merging",
  "done",
  "failed",
];

export const COLUMN_LABELS: Record<TaskStatus, string> = {
  backlog: "Backlog",
  planning: "Planning",
  plan_review: "Plan Review",
  in_progress: "In Progress",
  paused: "Paused",
  testing_ready: "Testing Ready",
  manual_testing: "Manual Testing",
  merging: "Merging",
  done: "Done",
  failed: "Failed",
};
