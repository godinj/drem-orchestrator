import { useState } from "react";
import type { Task, Agent, TaskStatus } from "../types";
import { COLUMN_ORDER, COLUMN_LABELS } from "../types";
import { TaskCard } from "./TaskCard";

interface BoardProps {
  columns: Record<TaskStatus, Task[]>;
  agents: Agent[];
  projectName: string;
  connected: boolean;
  reconnecting: boolean;
  onCreateTask: () => void;
  onReviewPlan: (
    taskId: string,
    approved: boolean,
    feedback?: string,
  ) => Promise<void>;
  onSubmitTest: (
    taskId: string,
    passed: boolean,
    feedback?: string,
  ) => Promise<void>;
  onRetryTask: (taskId: string) => Promise<void>;
  onSelectTask: (task: Task) => void;
  onToggleAgents: () => void;
  agentCount: number;
}

const COLUMN_BG: Partial<Record<TaskStatus, string>> = {
  plan_review: "bg-blue-900/20",
  testing_ready: "bg-green-900/20",
  manual_testing: "bg-purple-900/15",
  failed: "bg-red-900/15",
};

const COLUMN_HEADER_COLORS: Partial<Record<TaskStatus, string>> = {
  plan_review: "text-blue-300",
  testing_ready: "text-green-300",
  manual_testing: "text-purple-300",
  done: "text-green-400",
  failed: "text-red-400",
};

const COLUMN_COUNT_BG: Partial<Record<TaskStatus, string>> = {
  plan_review: "bg-blue-600",
  testing_ready: "bg-green-600",
  manual_testing: "bg-purple-600",
};

// Columns that are displayed but can be collapsed
const COLLAPSIBLE_COLUMNS: TaskStatus[] = ["done", "failed"];

export function Board({
  columns,
  agents,
  projectName,
  connected,
  reconnecting,
  onCreateTask,
  onReviewPlan,
  onSubmitTest,
  onRetryTask,
  onSelectTask,
  onToggleAgents,
  agentCount,
}: BoardProps) {
  const [collapsedColumns, setCollapsedColumns] = useState<
    Set<TaskStatus>
  >(new Set(["done"]));

  const toggleColumn = (status: TaskStatus) => {
    setCollapsedColumns((prev) => {
      const next = new Set(prev);
      if (next.has(status)) {
        next.delete(status);
      } else {
        next.add(status);
      }
      return next;
    });
  };

  return (
    <div className="h-full flex flex-col">
      {/* Header */}
      <header className="flex-shrink-0 px-6 py-4 border-b border-gray-700 flex items-center gap-4">
        <h1 className="text-xl font-bold text-gray-100">{projectName}</h1>

        {/* Connection status */}
        <div className="flex items-center gap-1.5">
          <span
            className={`w-2 h-2 rounded-full ${
              connected
                ? "bg-green-400"
                : reconnecting
                  ? "bg-yellow-400 animate-pulse"
                  : "bg-red-400"
            }`}
          />
          <span className="text-xs text-gray-400">
            {connected ? "Live" : reconnecting ? "Reconnecting..." : "Disconnected"}
          </span>
        </div>

        <div className="flex-1" />

        {/* Agent count badge */}
        <button
          onClick={onToggleAgents}
          className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-gray-800 border border-gray-700 hover:bg-gray-700 text-sm text-gray-300 transition-colors"
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
              d="M17 20h5v-2a3 3 0 00-5.356-1.857M17 20H7m10 0v-2c0-.656-.126-1.283-.356-1.857M7 20H2v-2a3 3 0 015.356-1.857M7 20v-2c0-.656.126-1.283.356-1.857m0 0a5.002 5.002 0 019.288 0M15 7a3 3 0 11-6 0 3 3 0 016 0zm6 3a2 2 0 11-4 0 2 2 0 014 0zM7 10a2 2 0 11-4 0 2 2 0 014 0z"
            />
          </svg>
          <span>{agentCount} Agent{agentCount !== 1 ? "s" : ""}</span>
        </button>

        {/* New Task button */}
        <button
          onClick={onCreateTask}
          className="flex items-center gap-1.5 px-4 py-1.5 rounded-lg bg-blue-600 hover:bg-blue-500 text-sm text-white font-medium transition-colors"
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
              d="M12 4v16m8-8H4"
            />
          </svg>
          New Task
        </button>
      </header>

      {/* Board columns */}
      <div className="flex-1 overflow-x-auto overflow-y-hidden">
        <div className="flex h-full gap-3 p-4 min-w-max">
          {COLUMN_ORDER.map((status) => {
            const tasks = columns[status] || [];
            const isCollapsible = COLLAPSIBLE_COLUMNS.includes(status);
            const isCollapsed = collapsedColumns.has(status);
            const bg = COLUMN_BG[status] || "";
            const headerColor =
              COLUMN_HEADER_COLORS[status] || "text-gray-300";
            const countBg = COLUMN_COUNT_BG[status] || "bg-gray-600";
            const isHumanGate =
              status === "plan_review" ||
              status === "testing_ready" ||
              status === "manual_testing";

            return (
              <div
                key={status}
                className={`flex-shrink-0 flex flex-col rounded-xl border border-gray-700/50 ${bg} ${
                  isCollapsed ? "w-12" : "w-[280px]"
                } transition-all duration-300`}
              >
                {/* Column header */}
                <div
                  className={`flex items-center gap-2 px-3 py-2.5 border-b border-gray-700/50 flex-shrink-0 ${
                    isCollapsible ? "cursor-pointer" : ""
                  }`}
                  onClick={() => isCollapsible && toggleColumn(status)}
                >
                  {isCollapsed ? (
                    <div className="flex flex-col items-center gap-1 w-full">
                      <span
                        className={`text-[10px] font-semibold uppercase tracking-wider ${headerColor} writing-mode-vertical`}
                        style={{ writingMode: "vertical-rl" }}
                      >
                        {COLUMN_LABELS[status]}
                      </span>
                      {tasks.length > 0 && (
                        <span
                          className={`text-[10px] px-1.5 py-0.5 rounded-full text-white font-medium ${countBg}`}
                        >
                          {tasks.length}
                        </span>
                      )}
                    </div>
                  ) : (
                    <>
                      {isHumanGate && (
                        <span className="gate-badge text-xs">
                          {status === "plan_review"
                            ? "👁"
                            : status === "testing_ready"
                              ? "🔔"
                              : "🧪"}
                        </span>
                      )}
                      <span
                        className={`text-xs font-semibold uppercase tracking-wider flex-1 ${headerColor}`}
                      >
                        {COLUMN_LABELS[status]}
                      </span>
                      {tasks.length > 0 && (
                        <span
                          className={`text-[10px] px-1.5 py-0.5 rounded-full text-white font-medium ${countBg}`}
                        >
                          {tasks.length}
                        </span>
                      )}
                      {isCollapsible && (
                        <svg
                          className="w-3 h-3 text-gray-500"
                          fill="none"
                          stroke="currentColor"
                          viewBox="0 0 24 24"
                        >
                          <path
                            strokeLinecap="round"
                            strokeLinejoin="round"
                            strokeWidth={2}
                            d="M19 9l-7 7-7-7"
                          />
                        </svg>
                      )}
                    </>
                  )}
                </div>

                {/* Cards */}
                {!isCollapsed && (
                  <div className="flex-1 overflow-y-auto p-2 space-y-2">
                    {tasks.length === 0 ? (
                      <div className="text-center text-xs text-gray-600 py-8">
                        No tasks
                      </div>
                    ) : (
                      tasks.map((task) => (
                        <TaskCard
                          key={task.id}
                          task={task}
                          agents={agents}
                          onReviewPlan={onReviewPlan}
                          onSubmitTest={onSubmitTest}
                          onRetryTask={onRetryTask}
                          onSelect={onSelectTask}
                        />
                      ))
                    )}
                  </div>
                )}
              </div>
            );
          })}
        </div>
      </div>
    </div>
  );
}
