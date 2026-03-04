import { useCallback } from "react";
import { useQuery, useQueryClient, useMutation } from "@tanstack/react-query";
import type { Task, Agent, TaskStatus, WSEvent } from "../types";
import * as api from "../api";
import { useWebSocket } from "./useWebSocket";
import { COLUMN_ORDER } from "../types";

function emptyColumns(): Record<TaskStatus, Task[]> {
  const cols = {} as Record<TaskStatus, Task[]>;
  for (const status of COLUMN_ORDER) {
    cols[status] = [];
  }
  return cols;
}

export function useBoard(projectId: string | null) {
  const queryClient = useQueryClient();

  // Fetch initial board data
  const boardQuery = useQuery({
    queryKey: ["board", projectId],
    queryFn: () => api.getBoard(projectId!),
    enabled: !!projectId,
    refetchInterval: 30000, // Refetch every 30s as a fallback
  });

  // Fetch agents
  const agentsQuery = useQuery({
    queryKey: ["agents", projectId],
    queryFn: () => api.getAgents(projectId!),
    enabled: !!projectId,
    refetchInterval: 10000, // Agents heartbeat frequently
  });

  // Handle WebSocket events by updating the cache
  const handleWSEvent = useCallback(
    (event: WSEvent) => {
      switch (event.type) {
        case "task_updated": {
          queryClient.setQueryData<Record<TaskStatus, Task[]>>(
            ["board", projectId],
            (old) => {
              if (!old) return old;
              const updated = { ...old };
              // Remove task from all columns
              for (const status of COLUMN_ORDER) {
                updated[status] = (updated[status] || []).filter(
                  (t) => t.id !== event.task.id,
                );
              }
              // Add to the correct column
              const targetStatus = event.task.status;
              updated[targetStatus] = [
                ...(updated[targetStatus] || []),
                event.task,
              ];
              return updated;
            },
          );
          break;
        }
        case "task_created": {
          queryClient.setQueryData<Record<TaskStatus, Task[]>>(
            ["board", projectId],
            (old) => {
              if (!old) return old;
              const updated = { ...old };
              const targetStatus = event.task.status;
              updated[targetStatus] = [
                ...(updated[targetStatus] || []),
                event.task,
              ];
              return updated;
            },
          );
          break;
        }
        case "agent_updated": {
          queryClient.setQueryData<Agent[]>(
            ["agents", projectId],
            (old) => {
              if (!old) return [event.agent];
              const idx = old.findIndex((a) => a.id === event.agent.id);
              if (idx >= 0) {
                const updated = [...old];
                updated[idx] = event.agent;
                return updated;
              }
              return [...old, event.agent];
            },
          );
          break;
        }
        case "plan_submitted":
        case "testing_ready": {
          // These events require a refetch to get the full task data
          queryClient.invalidateQueries({ queryKey: ["board", projectId] });
          break;
        }
      }
    },
    [projectId, queryClient],
  );

  // WebSocket connection
  const { connected, reconnecting } = useWebSocket(projectId, handleWSEvent);

  // Mutations
  const createTaskMutation = useMutation({
    mutationFn: (data: { title: string; description: string }) =>
      api.createTask({
        title: data.title,
        description: data.description,
        project_id: projectId!,
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["board", projectId] });
    },
  });

  const reviewPlanMutation = useMutation({
    mutationFn: (data: {
      taskId: string;
      approved: boolean;
      feedback?: string;
    }) => api.submitPlanReview(data.taskId, data.approved, data.feedback),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["board", projectId] });
    },
  });

  const submitTestMutation = useMutation({
    mutationFn: (data: {
      taskId: string;
      passed: boolean;
      feedback?: string;
    }) => api.submitTestResult(data.taskId, data.passed, data.feedback),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["board", projectId] });
    },
  });

  const columns = boardQuery.data ?? emptyColumns();

  return {
    columns,
    agents: agentsQuery.data ?? [],
    isLoading: boardQuery.isLoading,
    error: boardQuery.error,
    connected,
    reconnecting,
    createTask: async (title: string, description: string) => {
      await createTaskMutation.mutateAsync({ title, description });
    },
    reviewPlan: async (
      taskId: string,
      approved: boolean,
      feedback?: string,
    ) => {
      await reviewPlanMutation.mutateAsync({ taskId, approved, feedback });
    },
    submitTest: async (
      taskId: string,
      passed: boolean,
      feedback?: string,
    ) => {
      await submitTestMutation.mutateAsync({ taskId, passed, feedback });
    },
  };
}
