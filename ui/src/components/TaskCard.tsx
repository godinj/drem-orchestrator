import { useState } from "react";
import type { Task, Agent, SubtaskPlan, TaskStatus } from "../types";

interface TaskCardProps {
  task: Task;
  agents: Agent[];
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
  onSelect: (task: Task) => void;
}

const PRIORITY_BADGES: Record<number, { label: string; className: string }> = {
  0: { label: "P0", className: "bg-gray-600 text-gray-300" },
  1: { label: "P1", className: "bg-blue-600 text-blue-100" },
  2: { label: "P2", className: "bg-orange-600 text-orange-100" },
  3: { label: "P3", className: "bg-red-600 text-red-100" },
};

const AGENT_TYPE_BADGE_COLORS: Record<string, string> = {
  orchestrator: "bg-purple-900/50 text-purple-300",
  planner: "bg-blue-900/50 text-blue-300",
  coder: "bg-green-900/50 text-green-300",
  researcher: "bg-orange-900/50 text-orange-300",
};

function relativeTime(dateStr: string): string {
  const date = new Date(dateStr);
  const now = new Date();
  const diffMs = now.getTime() - date.getTime();
  const diffSec = Math.floor(diffMs / 1000);

  if (diffSec < 60) return "< 1m";
  const diffMin = Math.floor(diffSec / 60);
  if (diffMin < 60) return `${diffMin}m`;
  const diffHr = Math.floor(diffMin / 60);
  if (diffHr < 24) return `${diffHr}h`;
  return `${Math.floor(diffHr / 24)}d`;
}

function PlanSection({
  plan,
  planFeedback,
}: {
  plan: SubtaskPlan[];
  planFeedback: string | null;
}) {
  const [expanded, setExpanded] = useState(true);

  return (
    <div className="mt-2">
      <button
        onClick={(e) => {
          e.stopPropagation();
          setExpanded(!expanded);
        }}
        className="text-xs text-blue-400 hover:text-blue-300 flex items-center gap-1"
      >
        <svg
          className={`w-3 h-3 transition-transform ${expanded ? "rotate-90" : ""}`}
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
        Plan ({plan.length} subtasks)
      </button>

      {expanded && (
        <div className="mt-1 space-y-1.5">
          {planFeedback && (
            <div className="text-xs p-2 bg-yellow-900/30 border border-yellow-700/50 rounded text-yellow-200">
              Previous feedback: {planFeedback}
            </div>
          )}
          {plan.map((subtask, i) => (
            <div
              key={i}
              className="p-2 bg-gray-900/50 rounded border border-gray-700/50"
            >
              <div className="flex items-center gap-1.5">
                <span className="text-xs font-medium text-gray-200">
                  {subtask.title}
                </span>
                <span
                  className={`text-[9px] px-1 py-0.5 rounded font-medium ${
                    AGENT_TYPE_BADGE_COLORS[subtask.agent_type] ||
                    "bg-gray-700 text-gray-300"
                  }`}
                >
                  {subtask.agent_type}
                </span>
              </div>
              {subtask.description && (
                <p className="text-[10px] text-gray-400 mt-0.5 line-clamp-2">
                  {subtask.description}
                </p>
              )}
              {subtask.estimated_files.length > 0 && (
                <div className="mt-1 flex flex-wrap gap-1">
                  {subtask.estimated_files.map((file, j) => (
                    <code
                      key={j}
                      className="text-[9px] px-1 py-0.5 bg-gray-800 rounded text-gray-400 font-mono"
                    >
                      {file}
                    </code>
                  ))}
                </div>
              )}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

function TestPlanSection({
  testPlan,
  testFeedback,
  worktreeBranch,
}: {
  testPlan: string | null;
  testFeedback: string | null;
  worktreeBranch: string | null;
}) {
  const [copied, setCopied] = useState(false);

  const handleCopy = (text: string) => {
    navigator.clipboard.writeText(text).then(() => {
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    });
  };

  return (
    <div className="mt-2 space-y-2">
      {testFeedback && (
        <div className="text-xs p-2 bg-yellow-900/30 border border-yellow-700/50 rounded text-yellow-200">
          Previous feedback: {testFeedback}
        </div>
      )}
      {testPlan && (
        <div className="text-xs">
          <div className="text-gray-400 font-medium mb-1">Test Plan:</div>
          <pre className="p-2 bg-gray-900 rounded border border-gray-700/50 text-gray-300 whitespace-pre-wrap text-[11px] font-mono overflow-x-auto">
            {testPlan}
          </pre>
        </div>
      )}
      {worktreeBranch && (
        <div className="flex items-center gap-1.5">
          <code className="text-[10px] font-mono text-gray-400 bg-gray-900 px-1.5 py-0.5 rounded">
            {worktreeBranch}
          </code>
          <button
            onClick={(e) => {
              e.stopPropagation();
              handleCopy(worktreeBranch);
            }}
            className="text-[10px] text-blue-400 hover:text-blue-300"
          >
            {copied ? "Copied!" : "Copy"}
          </button>
        </div>
      )}
    </div>
  );
}

export function TaskCard({
  task,
  agents,
  onReviewPlan,
  onSubmitTest,
  onRetryTask,
  onSelect,
}: TaskCardProps) {
  const [rejectFeedback, setRejectFeedback] = useState("");
  const [failFeedback, setFailFeedback] = useState("");
  const [showRejectForm, setShowRejectForm] = useState(false);
  const [showFailForm, setShowFailForm] = useState(false);
  const [actionLoading, setActionLoading] = useState(false);

  const assignedAgent = task.assigned_agent_id
    ? agents.find((a) => a.id === task.assigned_agent_id)
    : null;

  const priorityBadge = PRIORITY_BADGES[task.priority] || PRIORITY_BADGES[0];

  const isHumanGate =
    task.status === "plan_review" ||
    task.status === "testing_ready" ||
    task.status === "manual_testing";

  const borderColor =
    task.status === "plan_review"
      ? "border-blue-500/50"
      : task.status === "testing_ready"
        ? "border-blue-400/60"
        : task.status === "manual_testing"
          ? "border-purple-500/50"
          : task.status === "failed"
            ? "border-red-500/50"
            : task.status === "done"
              ? "border-green-500/30"
              : "border-gray-700/50";

  const handleAction = async (fn: () => Promise<void>) => {
    setActionLoading(true);
    try {
      await fn();
    } catch (err) {
      console.error("Action failed:", err);
    } finally {
      setActionLoading(false);
    }
  };

  return (
    <div
      className={`task-card p-3 bg-gray-800 rounded-lg border ${borderColor} cursor-pointer`}
      onClick={() => onSelect(task)}
    >
      {/* Header row: title + priority */}
      <div className="flex items-start gap-2">
        <h3 className="text-sm font-medium text-gray-100 flex-1 line-clamp-2">
          {task.title}
        </h3>
        <span
          className={`text-[10px] px-1.5 py-0.5 rounded font-bold flex-shrink-0 ${priorityBadge.className}`}
        >
          {priorityBadge.label}
        </span>
      </div>

      {/* Labels */}
      {task.labels.length > 0 && (
        <div className="flex flex-wrap gap-1 mt-1.5">
          {task.labels.map((label) => (
            <span
              key={label}
              className="text-[10px] px-1.5 py-0.5 bg-gray-700 rounded-full text-gray-300"
            >
              {label}
            </span>
          ))}
        </div>
      )}

      {/* Subtask count */}
      {task.subtask_count > 0 && (
        <div className="mt-1.5 text-[10px] text-gray-400">
          {task.subtask_count} subtask{task.subtask_count !== 1 ? "s" : ""}
        </div>
      )}

      {/* Assigned agent */}
      {assignedAgent && (
        <div className="mt-1.5 flex items-center gap-1.5">
          <span
            className={`w-1.5 h-1.5 rounded-full ${
              assignedAgent.status === "working"
                ? "bg-green-400"
                : assignedAgent.status === "dead"
                  ? "bg-red-400"
                  : "bg-gray-400"
            }`}
          />
          <span className="text-[10px] text-gray-400">{assignedAgent.name}</span>
        </div>
      )}

      {/* Worktree branch */}
      {task.worktree_branch && !isHumanGate && (
        <div className="mt-1">
          <code className="text-[9px] font-mono text-gray-500">
            {task.worktree_branch}
          </code>
        </div>
      )}

      {/* Time in status */}
      <div className="mt-1.5 text-[9px] text-gray-500">
        {relativeTime(task.updated_at)} in {task.status.replace(/_/g, " ")}
      </div>

      {/* Planning spinner */}
      {task.status === "planning" && (
        <div className="mt-2 flex items-center gap-2 text-xs text-blue-300">
          <svg
            className="animate-spin h-3 w-3"
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
          AI decomposing...
        </div>
      )}

      {/* Plan Review card */}
      {task.status === "plan_review" && task.plan && (
        <div onClick={(e) => e.stopPropagation()}>
          <PlanSection plan={task.plan} planFeedback={task.plan_feedback} />

          <div className="mt-3 flex gap-2">
            <button
              disabled={actionLoading}
              onClick={() =>
                handleAction(() => onReviewPlan(task.id, true))
              }
              className="flex-1 text-xs py-1.5 rounded-md bg-green-600 hover:bg-green-500 text-white font-medium disabled:opacity-50 transition-colors"
            >
              Approve Plan
            </button>
            <button
              disabled={actionLoading}
              onClick={() => setShowRejectForm(!showRejectForm)}
              className="flex-1 text-xs py-1.5 rounded-md bg-red-600 hover:bg-red-500 text-white font-medium disabled:opacity-50 transition-colors"
            >
              Reject Plan
            </button>
          </div>

          {showRejectForm && (
            <div className="mt-2 space-y-2">
              <textarea
                value={rejectFeedback}
                onChange={(e) => setRejectFeedback(e.target.value)}
                placeholder="Explain what should change..."
                rows={2}
                className="w-full text-xs p-2 bg-gray-900 rounded border border-gray-600 text-gray-200 placeholder-gray-500 focus:outline-none focus:ring-1 focus:ring-red-500 resize-none"
                autoFocus
              />
              <button
                disabled={actionLoading || !rejectFeedback.trim()}
                onClick={() =>
                  handleAction(async () => {
                    await onReviewPlan(task.id, false, rejectFeedback.trim());
                    setRejectFeedback("");
                    setShowRejectForm(false);
                  })
                }
                className="w-full text-xs py-1.5 rounded-md bg-red-700 hover:bg-red-600 text-white font-medium disabled:opacity-50 transition-colors"
              >
                Submit Rejection
              </button>
            </div>
          )}
        </div>
      )}

      {/* Testing Ready card */}
      {task.status === "testing_ready" && (
        <div onClick={(e) => e.stopPropagation()}>
          <TestPlanSection
            testPlan={task.test_plan}
            testFeedback={null}
            worktreeBranch={task.worktree_branch}
          />
          <div className="mt-3">
            <button
              disabled={actionLoading}
              onClick={() =>
                handleAction(() => onSubmitTest(task.id, false, "started"))
              }
              className="w-full text-xs py-1.5 rounded-md bg-blue-600 hover:bg-blue-500 text-white font-medium disabled:opacity-50 transition-colors flex items-center justify-center gap-1.5"
            >
              <svg className="w-3 h-3" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M15 17h5l-1.405-1.405A2.032 2.032 0 0118 14.158V11a6.002 6.002 0 00-4-5.659V5a2 2 0 10-4 0v.341C7.67 6.165 6 8.388 6 11v3.159c0 .538-.214 1.055-.595 1.436L4 17h5m6 0v1a3 3 0 11-6 0v-1m6 0H9" />
              </svg>
              Start Testing
            </button>
          </div>
        </div>
      )}

      {/* Manual Testing card */}
      {task.status === "manual_testing" && (
        <div onClick={(e) => e.stopPropagation()}>
          <TestPlanSection
            testPlan={task.test_plan}
            testFeedback={task.test_feedback}
            worktreeBranch={task.worktree_branch}
          />

          <div className="mt-3 flex gap-2">
            <button
              disabled={actionLoading}
              onClick={() =>
                handleAction(() => onSubmitTest(task.id, true))
              }
              className="flex-1 text-xs py-1.5 rounded-md bg-green-600 hover:bg-green-500 text-white font-medium disabled:opacity-50 transition-colors"
            >
              Pass
            </button>
            <button
              disabled={actionLoading}
              onClick={() => setShowFailForm(!showFailForm)}
              className="flex-1 text-xs py-1.5 rounded-md bg-red-600 hover:bg-red-500 text-white font-medium disabled:opacity-50 transition-colors"
            >
              Fail
            </button>
          </div>

          {showFailForm && (
            <div className="mt-2 space-y-2">
              <textarea
                value={failFeedback}
                onChange={(e) => setFailFeedback(e.target.value)}
                placeholder="Describe what failed..."
                rows={2}
                className="w-full text-xs p-2 bg-gray-900 rounded border border-gray-600 text-gray-200 placeholder-gray-500 focus:outline-none focus:ring-1 focus:ring-red-500 resize-none"
                autoFocus
              />
              <button
                disabled={actionLoading || !failFeedback.trim()}
                onClick={() =>
                  handleAction(async () => {
                    await onSubmitTest(task.id, false, failFeedback.trim());
                    setFailFeedback("");
                    setShowFailForm(false);
                  })
                }
                className="w-full text-xs py-1.5 rounded-md bg-red-700 hover:bg-red-600 text-white font-medium disabled:opacity-50 transition-colors"
              >
                Submit Failure
              </button>
            </div>
          )}
        </div>
      )}

      {/* In Progress subtask progress */}
      {task.status === "in_progress" && task.subtask_count > 0 && (
        <div className="mt-2">
          <div className="flex items-center justify-between text-[10px] text-gray-400 mb-1">
            <span>Subtask progress</span>
            <span>
              {task.context?.completed_subtasks as number ?? 0}/{task.subtask_count}
            </span>
          </div>
          <div className="w-full h-1.5 bg-gray-700 rounded-full overflow-hidden">
            <div
              className="h-full bg-blue-500 rounded-full transition-all duration-500"
              style={{
                width: `${
                  ((task.context?.completed_subtasks as number ?? 0) /
                    task.subtask_count) *
                  100
                }%`,
              }}
            />
          </div>
        </div>
      )}

      {/* Merging progress */}
      {task.status === "merging" && (
        <div className="mt-2 flex items-center gap-2 text-xs text-purple-300">
          <svg
            className="animate-spin h-3 w-3"
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
          Merging to main...
          {task.pr_url && (
            <a
              href={task.pr_url}
              target="_blank"
              rel="noopener noreferrer"
              className="text-blue-400 hover:text-blue-300 underline"
              onClick={(e) => e.stopPropagation()}
            >
              PR
            </a>
          )}
        </div>
      )}

      {/* Failed — retry button */}
      {task.status === "failed" && (
        <div className="mt-2" onClick={(e) => e.stopPropagation()}>
          <button
            disabled={actionLoading}
            onClick={() =>
              handleAction(() => onRetryTask(task.id))
            }
            className="w-full text-xs py-1.5 rounded-md bg-orange-600 hover:bg-orange-500 text-white font-medium disabled:opacity-50 transition-colors flex items-center justify-center gap-1.5"
          >
            <svg className="w-3 h-3" fill="none" stroke="currentColor" viewBox="0 0 24 24">
              <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15" />
            </svg>
            Retry
          </button>
        </div>
      )}
    </div>
  );
}
