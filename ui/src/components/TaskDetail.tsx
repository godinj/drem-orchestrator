import { useState, useEffect } from "react";
import type { Task, Agent, TaskEvent, SubtaskPlan } from "../types";
import { getTaskEvents } from "../api";

interface TaskDetailProps {
  task: Task | null;
  agents: Agent[];
  onClose: () => void;
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
}

const AGENT_TYPE_BADGE_COLORS: Record<string, string> = {
  orchestrator: "bg-purple-900/50 text-purple-300",
  planner: "bg-blue-900/50 text-blue-300",
  coder: "bg-green-900/50 text-green-300",
  researcher: "bg-orange-900/50 text-orange-300",
};

const STATUS_COLORS: Record<string, string> = {
  backlog: "bg-gray-600",
  planning: "bg-blue-600",
  plan_review: "bg-blue-500",
  in_progress: "bg-indigo-600",
  testing_ready: "bg-teal-600",
  manual_testing: "bg-purple-600",
  merging: "bg-yellow-600",
  done: "bg-green-600",
  failed: "bg-red-600",
};

function relativeTime(dateStr: string): string {
  const date = new Date(dateStr);
  const now = new Date();
  const diffMs = now.getTime() - date.getTime();
  const diffSec = Math.floor(diffMs / 1000);

  if (diffSec < 60) return "just now";
  const diffMin = Math.floor(diffSec / 60);
  if (diffMin < 60) return `${diffMin}m ago`;
  const diffHr = Math.floor(diffMin / 60);
  if (diffHr < 24) return `${diffHr}h ago`;
  return `${Math.floor(diffHr / 24)}d ago`;
}

function PlanDisplay({
  plan,
  planFeedback,
  taskId,
  showActions,
  onReview,
}: {
  plan: SubtaskPlan[];
  planFeedback: string | null;
  taskId: string;
  showActions: boolean;
  onReview: (taskId: string, approved: boolean, feedback?: string) => Promise<void>;
}) {
  const [rejectFeedback, setRejectFeedback] = useState("");
  const [showRejectForm, setShowRejectForm] = useState(false);
  const [loading, setLoading] = useState(false);

  const handleAction = async (fn: () => Promise<void>) => {
    setLoading(true);
    try {
      await fn();
    } finally {
      setLoading(false);
    }
  };

  return (
    <div className="space-y-3">
      <h3 className="text-sm font-semibold text-gray-200">
        Proposed Plan ({plan.length} subtasks)
      </h3>

      {planFeedback && (
        <div className="p-3 bg-yellow-900/30 border border-yellow-700/50 rounded-lg text-sm text-yellow-200">
          <span className="font-medium">Previous feedback:</span> {planFeedback}
        </div>
      )}

      <div className="space-y-2">
        {plan.map((subtask, i) => (
          <div
            key={i}
            className="p-3 bg-gray-900/50 rounded-lg border border-gray-700/50"
          >
            <div className="flex items-center gap-2 mb-1">
              <span className="text-xs font-medium text-gray-200">
                {i + 1}. {subtask.title}
              </span>
              <span
                className={`text-[10px] px-1.5 py-0.5 rounded font-medium ${
                  AGENT_TYPE_BADGE_COLORS[subtask.agent_type] || "bg-gray-700 text-gray-300"
                }`}
              >
                {subtask.agent_type}
              </span>
            </div>
            <p className="text-xs text-gray-400">{subtask.description}</p>
            {subtask.estimated_files.length > 0 && (
              <div className="mt-2 flex flex-wrap gap-1">
                {subtask.estimated_files.map((file, j) => (
                  <code
                    key={j}
                    className="text-[10px] px-1.5 py-0.5 bg-gray-800 rounded text-gray-400 font-mono"
                  >
                    {file}
                  </code>
                ))}
              </div>
            )}
          </div>
        ))}
      </div>

      {showActions && (
        <div className="space-y-2 pt-2">
          <div className="flex gap-2">
            <button
              disabled={loading}
              onClick={() => handleAction(() => onReview(taskId, true))}
              className="flex-1 py-2 rounded-lg bg-green-600 hover:bg-green-500 text-white text-sm font-medium disabled:opacity-50 transition-colors"
            >
              Approve Plan
            </button>
            <button
              disabled={loading}
              onClick={() => setShowRejectForm(!showRejectForm)}
              className="flex-1 py-2 rounded-lg bg-red-600 hover:bg-red-500 text-white text-sm font-medium disabled:opacity-50 transition-colors"
            >
              Reject Plan
            </button>
          </div>

          {showRejectForm && (
            <div className="space-y-2">
              <textarea
                value={rejectFeedback}
                onChange={(e) => setRejectFeedback(e.target.value)}
                placeholder="Explain what should change..."
                rows={3}
                className="w-full text-sm p-3 bg-gray-900 rounded-lg border border-gray-600 text-gray-200 placeholder-gray-500 focus:outline-none focus:ring-2 focus:ring-red-500 resize-none"
                autoFocus
              />
              <button
                disabled={loading || !rejectFeedback.trim()}
                onClick={() =>
                  handleAction(async () => {
                    await onReview(taskId, false, rejectFeedback.trim());
                    setRejectFeedback("");
                    setShowRejectForm(false);
                  })
                }
                className="w-full py-2 rounded-lg bg-red-700 hover:bg-red-600 text-white text-sm font-medium disabled:opacity-50 transition-colors"
              >
                Submit Rejection
              </button>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

function TestInterface({
  task,
  onSubmitTest,
}: {
  task: Task;
  onSubmitTest: (taskId: string, passed: boolean, feedback?: string) => Promise<void>;
}) {
  const [failFeedback, setFailFeedback] = useState("");
  const [showFailForm, setShowFailForm] = useState(false);
  const [loading, setLoading] = useState(false);

  const handleAction = async (fn: () => Promise<void>) => {
    setLoading(true);
    try {
      await fn();
    } finally {
      setLoading(false);
    }
  };

  return (
    <div className="space-y-3">
      <h3 className="text-sm font-semibold text-gray-200">Testing</h3>

      {task.test_feedback && (
        <div className="p-3 bg-yellow-900/30 border border-yellow-700/50 rounded-lg text-sm text-yellow-200">
          <span className="font-medium">Previous feedback:</span>{" "}
          {task.test_feedback}
        </div>
      )}

      {task.test_plan && (
        <div>
          <div className="text-xs font-medium text-gray-400 mb-1">
            Test Plan:
          </div>
          <pre className="p-3 bg-gray-900 rounded-lg border border-gray-700/50 text-sm text-gray-300 whitespace-pre-wrap font-mono overflow-x-auto">
            {task.test_plan}
          </pre>
        </div>
      )}

      {task.worktree_branch && (
        <div className="flex items-center gap-2">
          <span className="text-xs text-gray-400">Branch:</span>
          <code className="text-xs font-mono text-gray-300 bg-gray-900 px-2 py-1 rounded">
            {task.worktree_branch}
          </code>
          <button
            onClick={() =>
              navigator.clipboard.writeText(task.worktree_branch || "")
            }
            className="text-xs text-blue-400 hover:text-blue-300"
          >
            Copy
          </button>
        </div>
      )}

      <div className="space-y-2 pt-2">
        <div className="flex gap-2">
          <button
            disabled={loading}
            onClick={() => handleAction(() => onSubmitTest(task.id, true))}
            className="flex-1 py-2 rounded-lg bg-green-600 hover:bg-green-500 text-white text-sm font-medium disabled:opacity-50 transition-colors"
          >
            Pass
          </button>
          <button
            disabled={loading}
            onClick={() => setShowFailForm(!showFailForm)}
            className="flex-1 py-2 rounded-lg bg-red-600 hover:bg-red-500 text-white text-sm font-medium disabled:opacity-50 transition-colors"
          >
            Fail
          </button>
        </div>

        {showFailForm && (
          <div className="space-y-2">
            <textarea
              value={failFeedback}
              onChange={(e) => setFailFeedback(e.target.value)}
              placeholder="Describe what failed..."
              rows={3}
              className="w-full text-sm p-3 bg-gray-900 rounded-lg border border-gray-600 text-gray-200 placeholder-gray-500 focus:outline-none focus:ring-2 focus:ring-red-500 resize-none"
              autoFocus
            />
            <button
              disabled={loading || !failFeedback.trim()}
              onClick={() =>
                handleAction(async () => {
                  await onSubmitTest(task.id, false, failFeedback.trim());
                  setFailFeedback("");
                  setShowFailForm(false);
                })
              }
              className="w-full py-2 rounded-lg bg-red-700 hover:bg-red-600 text-white text-sm font-medium disabled:opacity-50 transition-colors"
            >
              Submit Failure
            </button>
          </div>
        )}
      </div>
    </div>
  );
}

export function TaskDetail({
  task,
  agents,
  onClose,
  onReviewPlan,
  onSubmitTest,
}: TaskDetailProps) {
  const [events, setEvents] = useState<TaskEvent[]>([]);
  const [eventsLoading, setEventsLoading] = useState(false);
  const [showContext, setShowContext] = useState(false);

  useEffect(() => {
    if (!task) return;
    setEventsLoading(true);
    getTaskEvents(task.id)
      .then(setEvents)
      .catch((err) => console.error("Failed to load events:", err))
      .finally(() => setEventsLoading(false));
  }, [task?.id, task?.status]);

  if (!task) return null;

  const assignedAgent = task.assigned_agent_id
    ? agents.find((a) => a.id === task.assigned_agent_id)
    : null;

  const statusColor = STATUS_COLORS[task.status] || "bg-gray-600";

  return (
    <>
      {/* Backdrop */}
      <div
        className="fixed inset-0 z-40 bg-black/40 backdrop-blur-sm"
        onClick={onClose}
      />

      {/* Slide-over panel */}
      <div className="fixed right-0 top-0 h-full w-full max-w-lg z-50 bg-gray-800 border-l border-gray-700 shadow-2xl overflow-y-auto">
        {/* Header */}
        <div className="sticky top-0 z-10 bg-gray-800 border-b border-gray-700 px-6 py-4 flex items-start gap-3">
          <button
            onClick={onClose}
            className="mt-1 text-gray-400 hover:text-gray-200 transition-colors flex-shrink-0"
          >
            <svg
              className="w-5 h-5"
              fill="none"
              stroke="currentColor"
              viewBox="0 0 24 24"
            >
              <path
                strokeLinecap="round"
                strokeLinejoin="round"
                strokeWidth={2}
                d="M6 18L18 6M6 6l12 12"
              />
            </svg>
          </button>
          <div className="flex-1 min-w-0">
            <h2 className="text-lg font-semibold text-gray-100 break-words">
              {task.title}
            </h2>
            <div className="flex items-center gap-2 mt-1">
              <span
                className={`text-[10px] px-2 py-0.5 rounded-full text-white font-medium ${statusColor}`}
              >
                {task.status.replace(/_/g, " ")}
              </span>
              <span className="text-xs text-gray-400">
                P{task.priority}
              </span>
              {assignedAgent && (
                <span className="text-xs text-gray-400">
                  Agent: {assignedAgent.name}
                </span>
              )}
            </div>
          </div>
        </div>

        <div className="px-6 py-4 space-y-6">
          {/* Description */}
          <div>
            <h3 className="text-sm font-semibold text-gray-300 mb-2">
              Description
            </h3>
            <p className="text-sm text-gray-400 whitespace-pre-wrap">
              {task.description}
            </p>
          </div>

          {/* Labels */}
          {task.labels.length > 0 && (
            <div>
              <h3 className="text-sm font-semibold text-gray-300 mb-2">
                Labels
              </h3>
              <div className="flex flex-wrap gap-1.5">
                {task.labels.map((label) => (
                  <span
                    key={label}
                    className="text-xs px-2 py-0.5 bg-gray-700 rounded-full text-gray-300"
                  >
                    {label}
                  </span>
                ))}
              </div>
            </div>
          )}

          {/* Metadata */}
          <div className="grid grid-cols-2 gap-3 text-xs">
            {task.worktree_branch && (
              <div>
                <span className="text-gray-500">Branch</span>
                <div className="font-mono text-gray-300 mt-0.5">
                  {task.worktree_branch}
                </div>
              </div>
            )}
            {task.pr_url && (
              <div>
                <span className="text-gray-500">PR</span>
                <div className="mt-0.5">
                  <a
                    href={task.pr_url}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="text-blue-400 hover:text-blue-300 underline"
                  >
                    View PR
                  </a>
                </div>
              </div>
            )}
            <div>
              <span className="text-gray-500">Created</span>
              <div className="text-gray-300 mt-0.5">
                {new Date(task.created_at).toLocaleString()}
              </div>
            </div>
            <div>
              <span className="text-gray-500">Updated</span>
              <div className="text-gray-300 mt-0.5">
                {new Date(task.updated_at).toLocaleString()}
              </div>
            </div>
          </div>

          {/* Plan Review section */}
          {task.plan && task.plan.length > 0 && (
            <div className="border-t border-gray-700 pt-4">
              <PlanDisplay
                plan={task.plan}
                planFeedback={task.plan_feedback}
                taskId={task.id}
                showActions={task.status === "plan_review"}
                onReview={onReviewPlan}
              />
            </div>
          )}

          {/* Test section */}
          {(task.status === "manual_testing" ||
            task.status === "testing_ready") && (
            <div className="border-t border-gray-700 pt-4">
              <TestInterface task={task} onSubmitTest={onSubmitTest} />
            </div>
          )}

          {/* Context JSON viewer */}
          {task.context && Object.keys(task.context).length > 0 && (
            <div className="border-t border-gray-700 pt-4">
              <button
                onClick={() => setShowContext(!showContext)}
                className="flex items-center gap-1.5 text-sm font-semibold text-gray-300 hover:text-gray-100"
              >
                <svg
                  className={`w-3 h-3 transition-transform ${showContext ? "rotate-90" : ""}`}
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
                Context Data
              </button>
              {showContext && (
                <pre className="mt-2 p-3 bg-gray-900 rounded-lg border border-gray-700/50 text-xs text-gray-400 font-mono overflow-x-auto whitespace-pre-wrap">
                  {JSON.stringify(task.context, null, 2)}
                </pre>
              )}
            </div>
          )}

          {/* Event timeline */}
          <div className="border-t border-gray-700 pt-4">
            <h3 className="text-sm font-semibold text-gray-300 mb-3">
              Event Timeline
            </h3>
            {eventsLoading ? (
              <div className="text-xs text-gray-500">Loading events...</div>
            ) : events.length === 0 ? (
              <div className="text-xs text-gray-500">No events recorded</div>
            ) : (
              <div className="space-y-2">
                {events.map((event) => (
                  <div
                    key={event.id}
                    className="flex gap-3 text-xs"
                  >
                    <div className="flex-shrink-0 mt-1">
                      <div className="w-2 h-2 rounded-full bg-gray-600" />
                    </div>
                    <div className="flex-1 min-w-0">
                      <div className="flex items-center gap-2">
                        <span className="font-medium text-gray-300">
                          {event.event_type}
                        </span>
                        <span className="text-gray-500">
                          {relativeTime(event.created_at)}
                        </span>
                      </div>
                      {(event.old_value || event.new_value) && (
                        <div className="text-gray-400 mt-0.5">
                          {event.old_value && (
                            <span className="line-through text-gray-500 mr-1">
                              {event.old_value}
                            </span>
                          )}
                          {event.new_value && (
                            <span>{event.new_value}</span>
                          )}
                        </div>
                      )}
                      <div className="text-gray-500 mt-0.5">
                        by {event.actor}
                      </div>
                    </div>
                  </div>
                ))}
              </div>
            )}
          </div>
        </div>
      </div>
    </>
  );
}
