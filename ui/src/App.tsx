import { useState, useEffect } from "react";
import { QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
import { Board } from "./components/Board";
import { TaskCreateDialog } from "./components/TaskCreateDialog";
import { TaskDetail } from "./components/TaskDetail";
import { AgentSidebar } from "./components/AgentSidebar";
import { useBoard } from "./hooks/useBoard";
import { getProjects } from "./api";
import type { Task, Project } from "./types";

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      retry: 2,
      staleTime: 5000,
    },
  },
});

function AppContent() {
  const [selectedProjectId, setSelectedProjectId] = useState<string | null>(null);
  const [showCreateDialog, setShowCreateDialog] = useState(false);
  const [selectedTask, setSelectedTask] = useState<Task | null>(null);
  const [showAgentSidebar, setShowAgentSidebar] = useState(true);

  // Fetch projects
  const projectsQuery = useQuery({
    queryKey: ["projects"],
    queryFn: getProjects,
  });

  const projects: Project[] = projectsQuery.data ?? [];

  // Auto-select first project
  useEffect(() => {
    if (!selectedProjectId && projects.length > 0) {
      setSelectedProjectId(projects[0].id);
    }
  }, [projects, selectedProjectId]);

  const selectedProject = projects.find((p) => p.id === selectedProjectId);

  // Board hook (only active when we have a project)
  const board = useBoard(selectedProjectId);

  if (projectsQuery.isLoading) {
    return (
      <div className="h-screen flex items-center justify-center bg-gray-900">
        <div className="text-center">
          <svg
            className="animate-spin h-8 w-8 text-blue-400 mx-auto mb-3"
            fill="none"
            viewBox="0 0 24 24"
          >
            <circle
              className="opacity-25"
              cx="12"
              cy="12"
              r="10"
              stroke="currentColor"
              strokeWidth="4"
            />
            <path
              className="opacity-75"
              fill="currentColor"
              d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"
            />
          </svg>
          <p className="text-gray-400 text-sm">Loading projects...</p>
        </div>
      </div>
    );
  }

  if (projectsQuery.error) {
    return (
      <div className="h-screen flex items-center justify-center bg-gray-900">
        <div className="text-center max-w-md">
          <div className="text-red-400 text-lg font-semibold mb-2">
            Connection Error
          </div>
          <p className="text-gray-400 text-sm mb-4">
            Could not connect to the Drem Orchestrator API. Make sure the server
            is running on port 8000.
          </p>
          <pre className="text-xs text-gray-500 bg-gray-800 rounded-lg p-3 text-left">
            {String(projectsQuery.error)}
          </pre>
          <button
            onClick={() => projectsQuery.refetch()}
            className="mt-4 px-4 py-2 rounded-lg bg-blue-600 hover:bg-blue-500 text-white text-sm font-medium transition-colors"
          >
            Retry
          </button>
        </div>
      </div>
    );
  }

  if (projects.length === 0) {
    return (
      <div className="h-screen flex items-center justify-center bg-gray-900">
        <div className="text-center max-w-md">
          <div className="text-gray-300 text-lg font-semibold mb-2">
            No Projects
          </div>
          <p className="text-gray-400 text-sm">
            No projects have been created yet. Use the API to create a project
            first.
          </p>
          <button
            onClick={() => projectsQuery.refetch()}
            className="mt-4 px-4 py-2 rounded-lg bg-gray-700 hover:bg-gray-600 text-white text-sm font-medium transition-colors"
          >
            Refresh
          </button>
        </div>
      </div>
    );
  }

  return (
    <div className="h-screen flex flex-col bg-gray-900">
      {/* Top bar with project selector */}
      {projects.length > 1 && (
        <div className="flex-shrink-0 px-6 py-2 border-b border-gray-800 flex items-center gap-3">
          <span className="text-xs text-gray-500 uppercase tracking-wider font-medium">
            Project
          </span>
          <select
            value={selectedProjectId || ""}
            onChange={(e) => setSelectedProjectId(e.target.value)}
            className="bg-gray-800 border border-gray-700 rounded-lg px-3 py-1 text-sm text-gray-200 focus:outline-none focus:ring-2 focus:ring-blue-500"
          >
            {projects.map((project) => (
              <option key={project.id} value={project.id}>
                {project.name}
              </option>
            ))}
          </select>
        </div>
      )}

      {/* Main content */}
      <div className="flex-1 overflow-hidden">
        {board.isLoading ? (
          <div className="h-full flex items-center justify-center">
            <div className="text-center">
              <svg
                className="animate-spin h-6 w-6 text-blue-400 mx-auto mb-2"
                fill="none"
                viewBox="0 0 24 24"
              >
                <circle
                  className="opacity-25"
                  cx="12"
                  cy="12"
                  r="10"
                  stroke="currentColor"
                  strokeWidth="4"
                />
                <path
                  className="opacity-75"
                  fill="currentColor"
                  d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"
                />
              </svg>
              <p className="text-gray-400 text-sm">Loading board...</p>
            </div>
          </div>
        ) : board.error ? (
          <div className="h-full flex items-center justify-center">
            <div className="text-center">
              <p className="text-red-400 text-sm">
                Failed to load board: {String(board.error)}
              </p>
            </div>
          </div>
        ) : (
          <Board
            columns={board.columns}
            agents={board.agents}
            projectName={selectedProject?.name ?? "Project"}
            connected={board.connected}
            reconnecting={board.reconnecting}
            onCreateTask={() => setShowCreateDialog(true)}
            onReviewPlan={board.reviewPlan}
            onSubmitTest={board.submitTest}
            onRetryTask={board.retryTask}
            onPauseTask={board.pauseTask}
            onResumeTask={board.resumeTask}
            onSelectTask={setSelectedTask}
            onToggleAgents={() => setShowAgentSidebar(!showAgentSidebar)}
            agentCount={board.agents.length}
          />
        )}
      </div>

      {/* Agent sidebar */}
      <AgentSidebar
        agents={board.agents}
        columns={board.columns}
        open={showAgentSidebar}
        onToggle={() => setShowAgentSidebar(!showAgentSidebar)}
      />

      {/* Create task dialog */}
      <TaskCreateDialog
        open={showCreateDialog}
        onClose={() => setShowCreateDialog(false)}
        onCreate={board.createTask}
      />

      {/* Task detail slide-over */}
      {selectedTask && (
        <TaskDetail
          task={selectedTask}
          agents={board.agents}
          onClose={() => setSelectedTask(null)}
          onReviewPlan={board.reviewPlan}
          onSubmitTest={board.submitTest}
          onRetryTask={board.retryTask}
          onPauseTask={board.pauseTask}
          onResumeTask={board.resumeTask}
        />
      )}
    </div>
  );
}

export default function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <AppContent />
    </QueryClientProvider>
  );
}
