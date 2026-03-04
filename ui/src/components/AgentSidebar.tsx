import { useState } from "react";
import type { Agent, Task, TaskStatus } from "../types";

interface AgentSidebarProps {
  agents: Agent[];
  columns: Record<TaskStatus, Task[]>;
  open: boolean;
  onToggle: () => void;
}

const STATUS_DOT_COLORS: Record<string, string> = {
  idle: "bg-gray-400",
  working: "bg-green-400",
  blocked: "bg-yellow-400",
  dead: "bg-red-400",
};

const AGENT_TYPE_COLORS: Record<string, string> = {
  orchestrator: "bg-purple-600",
  planner: "bg-blue-600",
  coder: "bg-green-600",
  researcher: "bg-orange-600",
};

function relativeTime(dateStr: string | null): string {
  if (!dateStr) return "never";
  const date = new Date(dateStr);
  const now = new Date();
  const diffMs = now.getTime() - date.getTime();
  const diffSec = Math.floor(diffMs / 1000);

  if (diffSec < 5) return "just now";
  if (diffSec < 60) return `${diffSec}s ago`;
  const diffMin = Math.floor(diffSec / 60);
  if (diffMin < 60) return `${diffMin}m ago`;
  const diffHr = Math.floor(diffMin / 60);
  if (diffHr < 24) return `${diffHr}h ago`;
  return `${Math.floor(diffHr / 24)}d ago`;
}

function findTaskById(
  columns: Record<TaskStatus, Task[]>,
  taskId: string | null,
): Task | null {
  if (!taskId) return null;
  for (const tasks of Object.values(columns)) {
    const found = tasks.find((t) => t.id === taskId);
    if (found) return found;
  }
  return null;
}

export function AgentSidebar({
  agents,
  columns,
  open,
  onToggle,
}: AgentSidebarProps) {
  const [expandedAgent, setExpandedAgent] = useState<string | null>(null);

  return (
    <>
      {/* Toggle button when closed */}
      {!open && (
        <button
          onClick={onToggle}
          className="fixed right-0 top-1/2 -translate-y-1/2 z-30 bg-gray-800 border border-gray-700 border-r-0 rounded-l-lg px-2 py-4 text-gray-400 hover:text-gray-200 hover:bg-gray-700 transition-colors"
          title="Show agents"
        >
          <svg
            className="w-4 h-4"
            fill="none"
            stroke="currentColor"
            viewBox="0 0 24 24"
          >
            <path
              strokeLinecap="round"
              strokeLinejoin="round"
              strokeWidth={2}
              d="M15 19l-7-7 7-7"
            />
          </svg>
        </button>
      )}

      {/* Sidebar panel */}
      <div
        className={`fixed right-0 top-0 h-full w-72 bg-gray-850 border-l border-gray-700 z-20 transform transition-transform duration-300 ${
          open ? "translate-x-0" : "translate-x-full"
        }`}
        style={{ backgroundColor: "rgb(30, 33, 40)" }}
      >
        <div className="flex items-center justify-between p-4 border-b border-gray-700">
          <h2 className="text-sm font-semibold text-gray-200 uppercase tracking-wider">
            Agents ({agents.length})
          </h2>
          <button
            onClick={onToggle}
            className="text-gray-400 hover:text-gray-200 transition-colors"
          >
            <svg
              className="w-4 h-4"
              fill="none"
              stroke="currentColor"
              viewBox="0 0 24 24"
            >
              <path
                strokeLinecap="round"
                strokeLinejoin="round"
                strokeWidth={2}
                d="M9 5l7 7-7 7"
              />
            </svg>
          </button>
        </div>

        <div className="overflow-y-auto h-[calc(100%-57px)]">
          {agents.length === 0 ? (
            <div className="p-4 text-center text-gray-500 text-sm">
              No agents connected
            </div>
          ) : (
            <div className="divide-y divide-gray-700/50">
              {agents.map((agent) => {
                const currentTask = findTaskById(
                  columns,
                  agent.current_task_id,
                );
                const isExpanded = expandedAgent === agent.id;

                return (
                  <div
                    key={agent.id}
                    className="p-3 hover:bg-gray-800/50 transition-colors"
                  >
                    <div className="flex items-center gap-2 mb-1">
                      {/* Status dot */}
                      <span
                        className={`w-2 h-2 rounded-full flex-shrink-0 ${
                          STATUS_DOT_COLORS[agent.status] || "bg-gray-400"
                        }`}
                      />
                      {/* Name */}
                      <span className="text-sm font-medium text-gray-200 truncate">
                        {agent.name}
                      </span>
                      {/* Type badge */}
                      <span
                        className={`text-[10px] px-1.5 py-0.5 rounded-full text-white font-medium flex-shrink-0 ${
                          AGENT_TYPE_COLORS[agent.agent_type] || "bg-gray-600"
                        }`}
                      >
                        {agent.agent_type}
                      </span>
                    </div>

                    {/* Current task */}
                    {currentTask && (
                      <div className="ml-4 text-xs text-gray-400 truncate">
                        Working on:{" "}
                        <span className="text-gray-300">
                          {currentTask.title}
                        </span>
                      </div>
                    )}

                    {/* Worktree branch */}
                    {agent.worktree_branch && (
                      <div className="ml-4 mt-0.5">
                        <code className="text-[10px] text-gray-500 font-mono">
                          {agent.worktree_branch}
                        </code>
                      </div>
                    )}

                    {/* Heartbeat */}
                    <div className="ml-4 mt-0.5 text-[10px] text-gray-500">
                      Heartbeat: {relativeTime(agent.heartbeat_at)}
                    </div>

                    {/* Expand toggle */}
                    <button
                      onClick={() =>
                        setExpandedAgent(isExpanded ? null : agent.id)
                      }
                      className="ml-4 mt-1 text-[10px] text-blue-400 hover:text-blue-300"
                    >
                      {isExpanded ? "Hide details" : "Show details"}
                    </button>

                    {isExpanded && (
                      <div className="ml-4 mt-2 p-2 bg-gray-900 rounded text-[10px] font-mono text-gray-400">
                        <div>ID: {agent.id}</div>
                        <div>Status: {agent.status}</div>
                        <div>Type: {agent.agent_type}</div>
                        {agent.worktree_path && (
                          <div className="truncate">
                            Path: {agent.worktree_path}
                          </div>
                        )}
                        <div>
                          Created:{" "}
                          {new Date(agent.created_at).toLocaleString()}
                        </div>
                      </div>
                    )}
                  </div>
                );
              })}
            </div>
          )}
        </div>
      </div>
    </>
  );
}
