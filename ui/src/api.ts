import type { Task, Project, TaskEvent, Agent } from "./types";
import type { TaskStatus } from "./types";

const API = "/api";

class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message);
    this.name = "ApiError";
  }
}

async function request<T>(path: string, options?: RequestInit): Promise<T> {
  const res = await fetch(`${API}${path}`, {
    headers: {
      "Content-Type": "application/json",
      ...options?.headers,
    },
    ...options,
  });
  if (!res.ok) {
    const text = await res.text();
    throw new ApiError(res.status, `${res.status}: ${text}`);
  }
  return res.json();
}

// Projects
export async function getProjects(): Promise<Project[]> {
  return request<Project[]>("/projects");
}

export async function getProject(id: string): Promise<Project> {
  return request<Project>(`/projects/${id}`);
}

export async function getBoard(
  projectId: string,
): Promise<Record<TaskStatus, Task[]>> {
  return request<Record<TaskStatus, Task[]>>(`/projects/${projectId}/board`);
}

// Tasks
export async function createTask(data: {
  title: string;
  description: string;
  project_id: string;
  priority?: number;
}): Promise<Task> {
  return request<Task>("/tasks", {
    method: "POST",
    body: JSON.stringify(data),
  });
}

export async function getTask(id: string): Promise<Task> {
  return request<Task>(`/tasks/${id}`);
}

export async function updateTask(
  id: string,
  data: Partial<Task>,
): Promise<Task> {
  return request<Task>(`/tasks/${id}`, {
    method: "PATCH",
    body: JSON.stringify(data),
  });
}

export async function getTaskEvents(id: string): Promise<TaskEvent[]> {
  return request<TaskEvent[]>(`/tasks/${id}/events`);
}

// Human gate actions
export async function submitPlanReview(
  taskId: string,
  approved: boolean,
  feedback?: string,
): Promise<Task> {
  return request<Task>(`/tasks/${taskId}/plan-review`, {
    method: "POST",
    body: JSON.stringify({ approved, feedback: feedback || null }),
  });
}

export async function submitTestResult(
  taskId: string,
  passed: boolean,
  feedback?: string,
): Promise<Task> {
  return request<Task>(`/tasks/${taskId}/test-result`, {
    method: "POST",
    body: JSON.stringify({ passed, feedback: feedback || null }),
  });
}

// Task transitions
export async function transitionTask(
  taskId: string,
  targetStatus: TaskStatus,
): Promise<Task> {
  return request<Task>(`/tasks/${taskId}/transition`, {
    method: "POST",
    body: JSON.stringify({ target_status: targetStatus }),
  });
}

// Pause / Resume
export async function pauseTask(taskId: string): Promise<Task> {
  return request<Task>(`/tasks/${taskId}/pause`, { method: "POST" });
}

export async function resumeTask(taskId: string): Promise<Task> {
  return request<Task>(`/tasks/${taskId}/resume`, { method: "POST" });
}

// Agents
export async function getAgents(projectId: string): Promise<Agent[]> {
  return request<Agent[]>(`/projects/${projectId}/agents`);
}
