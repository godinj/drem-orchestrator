# Agent: Frontend — Clarification UI & Log Viewer

You are working on the `master` branch of Drem Orchestrator, a React + Vite task board frontend for an AI agent orchestrator.
Your task is implementing the UI for two features: (1) a clarification question/answer flow for the "Needs Clarification" task status, and (2) an agent log viewer modal.

## Context

Read these files before starting:
- `CLAUDE.md` (project conventions)
- `ui/src/types.ts` (TypeScript types, `TaskStatus`, `COLUMN_ORDER`, `COLUMN_LABELS`)
- `ui/src/api.ts` (API client functions — pattern: `submitPlanReview()`, `submitTestResult()`)
- `ui/src/hooks/useBoard.ts` (React Query mutations, WebSocket event handler)
- `ui/src/components/Board.tsx` (Kanban board — `BoardProps`, `COLUMN_BG`, `COLUMN_HEADER_COLORS`, `COLUMN_COUNT_BG`, `isHumanGate` check)
- `ui/src/components/TaskCard.tsx` (`TaskCardProps`, `PlanSection`, status-specific card sections)
- `ui/src/components/AgentSidebar.tsx` (`AgentSidebarProps`, agent detail expansion pattern)
- `ui/src/App.tsx` (prop wiring from `useBoard` → `Board` → `TaskCard`, `AgentSidebar`)

## Backend API Contract

The backend will provide these new endpoints (being built in parallel):

**POST `/api/tasks/{task_id}/clarifications`**
- Request body: `{ "answers": [{ "question_id": "q1", "answer": "Use JWT" }] }`
- Response: `Task` object (same as other task endpoints)
- Only valid when task status is `"needs_clarification"`

**GET `/api/agents/{agent_id}/log`**
- Response: `{ "log": "...agent log text..." }`
- Returns empty string if no log file exists

The task's `context` field will contain clarification data when in `needs_clarification` status:
```json
{
  "clarifications": [
    {
      "round": 1,
      "questions": [
        { "id": "q1", "question": "Should we use JWT or sessions?", "context": "Task mentions auth but not the mechanism" }
      ],
      "answers": null
    }
  ]
}
```

## Deliverables

### 1. `ui/src/types.ts`

**a)** Add `"needs_clarification"` to the `TaskStatus` union type (after `"planning"`):

```typescript
export type TaskStatus =
  | "backlog"
  | "planning"
  | "needs_clarification"
  | "plan_review"
  // ... rest unchanged
```

**b)** Add to `COLUMN_ORDER` (after `"planning"`):

```typescript
export const COLUMN_ORDER: TaskStatus[] = [
  "backlog",
  "planning",
  "needs_clarification",
  "plan_review",
  // ... rest unchanged
];
```

**c)** Add to `COLUMN_LABELS`:

```typescript
needs_clarification: "Needs Clarification",
```

**d)** Add `ClarificationQuestion` interface:

```typescript
export interface ClarificationQuestion {
  id: string;
  question: string;
  context?: string;
}

export interface ClarificationRound {
  round: number;
  questions: ClarificationQuestion[];
  answers: { question_id: string; answer: string }[] | null;
}
```

**e)** Add to `WSEvent` union:

```typescript
| { type: "clarification_needed"; task_id: string; questions: ClarificationQuestion[] }
```

### 2. `ui/src/api.ts`

Add two new functions:

```typescript
// Clarification answers
export async function submitClarificationAnswers(
  taskId: string,
  answers: { question_id: string; answer: string }[],
): Promise<Task> {
  return request<Task>(`/tasks/${taskId}/clarifications`, {
    method: "POST",
    body: JSON.stringify({ answers }),
  });
}

// Agent log
export async function getAgentLog(
  agentId: string,
): Promise<{ log: string }> {
  return request<{ log: string }>(`/agents/${agentId}/log`);
}
```

### 3. `ui/src/hooks/useBoard.ts`

**a)** Add clarification mutation (follow `reviewPlanMutation` pattern):

```typescript
const answerClarificationsMutation = useMutation({
  mutationFn: (data: {
    taskId: string;
    answers: { question_id: string; answer: string }[];
  }) => api.submitClarificationAnswers(data.taskId, data.answers),
  onSuccess: () => {
    queryClient.invalidateQueries({ queryKey: ["board", projectId] });
  },
});
```

**b)** Handle `clarification_needed` in `handleWSEvent` — add a case that invalidates the board query (same as `plan_submitted`/`testing_ready`).

**c)** Add to the return object:

```typescript
answerClarifications: async (
  taskId: string,
  answers: { question_id: string; answer: string }[],
) => {
  await answerClarificationsMutation.mutateAsync({ taskId, answers });
},
```

### 4. `ui/src/components/Board.tsx`

**a)** Add `needs_clarification` to styling maps (use amber theme):

```typescript
const COLUMN_BG: Partial<Record<TaskStatus, string>> = {
  needs_clarification: "bg-amber-900/20",
  plan_review: "bg-blue-900/20",
  // ... rest unchanged
};

const COLUMN_HEADER_COLORS: Partial<Record<TaskStatus, string>> = {
  needs_clarification: "text-amber-300",
  // ... rest unchanged
};

const COLUMN_COUNT_BG: Partial<Record<TaskStatus, string>> = {
  needs_clarification: "bg-amber-600",
  // ... rest unchanged
};
```

**b)** Add `needs_clarification` to `isHumanGate` check (line 161-164):

```typescript
const isHumanGate =
  status === "needs_clarification" ||
  status === "plan_review" ||
  status === "testing_ready" ||
  status === "manual_testing";
```

Add emoji badge `"❓"` for the `needs_clarification` gate in the column header.

**c)** Add `onAnswerClarifications` to `BoardProps` interface:

```typescript
onAnswerClarifications: (
  taskId: string,
  answers: { question_id: string; answer: string }[],
) => Promise<void>;
```

**d)** Pass `onAnswerClarifications` through to `TaskCard`:

```tsx
<TaskCard
  key={task.id}
  task={task}
  agents={agents}
  onReviewPlan={onReviewPlan}
  onSubmitTest={onSubmitTest}
  onRetryTask={onRetryTask}
  onSelect={onSelectTask}
  onAnswerClarifications={onAnswerClarifications}
/>
```

### 5. `ui/src/components/TaskCard.tsx`

**a)** Add `onAnswerClarifications` to `TaskCardProps`:

```typescript
onAnswerClarifications: (
  taskId: string,
  answers: { question_id: string; answer: string }[],
) => Promise<void>;
```

**b)** Add `ClarificationSection` component (follow `PlanSection` pattern). Renders each question with a textarea, and a submit button:

```tsx
function ClarificationSection({
  clarifications,
  onAnswer,
  taskId,
}: {
  clarifications: ClarificationRound[];
  onAnswer: (taskId: string, answers: { question_id: string; answer: string }[]) => Promise<void>;
  taskId: string;
}) {
  const latestRound = clarifications[clarifications.length - 1];
  const questions = latestRound?.questions || [];
  const [answers, setAnswers] = useState<Record<string, string>>({});
  const [loading, setLoading] = useState(false);

  const allAnswered = questions.every((q) => answers[q.id]?.trim());

  const handleSubmit = async () => {
    setLoading(true);
    try {
      await onAnswer(
        taskId,
        questions.map((q) => ({ question_id: q.id, answer: answers[q.id] })),
      );
    } finally {
      setLoading(false);
    }
  };

  return (
    <div className="mt-2 space-y-2">
      <div className="text-xs font-medium text-amber-300">
        Agent has questions (Round {latestRound?.round || 1})
      </div>
      {questions.map((q) => (
        <div key={q.id} className="p-2 bg-gray-900/50 rounded border border-amber-700/50">
          <div className="text-xs text-gray-200 font-medium">{q.question}</div>
          {q.context && (
            <div className="text-[10px] text-gray-400 mt-0.5 italic">{q.context}</div>
          )}
          <textarea
            value={answers[q.id] || ""}
            onChange={(e) => setAnswers({ ...answers, [q.id]: e.target.value })}
            placeholder="Your answer..."
            rows={2}
            className="w-full mt-1.5 text-xs p-2 bg-gray-900 rounded border border-gray-600 text-gray-200 placeholder-gray-500 focus:outline-none focus:ring-1 focus:ring-amber-500 resize-none"
          />
        </div>
      ))}
      <button
        disabled={loading || !allAnswered}
        onClick={handleSubmit}
        className="w-full text-xs py-1.5 rounded-md bg-amber-600 hover:bg-amber-500 text-white font-medium disabled:opacity-50 transition-colors"
      >
        Submit Answers
      </button>
    </div>
  );
}
```

**c)** Add `needs_clarification` to the `isHumanGate` check in the card (line 206-209):

```typescript
const isHumanGate =
  task.status === "needs_clarification" ||
  task.status === "plan_review" ||
  // ... rest unchanged
```

And add amber border color for `needs_clarification`:

```typescript
const borderColor =
  task.status === "needs_clarification"
    ? "border-amber-500/50"
    : task.status === "plan_review"
    // ... rest unchanged
```

**d)** Add the clarification card section after the planning spinner and before the plan review card (between line 327 and 329):

```tsx
{/* Needs Clarification card */}
{task.status === "needs_clarification" && task.context?.clarifications && (
  <div onClick={(e) => e.stopPropagation()}>
    <ClarificationSection
      clarifications={task.context.clarifications as ClarificationRound[]}
      onAnswer={onAnswerClarifications}
      taskId={task.id}
    />
  </div>
)}
```

Import `ClarificationRound` from `../types`.

### 6. `ui/src/components/LogModal.tsx` (new file)

Create a modal component for viewing agent logs:

```tsx
import { useState, useEffect, useRef } from "react";
import { getAgentLog } from "../api";

interface LogModalProps {
  agentId: string;
  agentName: string;
  onClose: () => void;
}

export function LogModal({ agentId, agentName, onClose }: LogModalProps) {
  const [log, setLog] = useState<string>("");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const logRef = useRef<HTMLPreElement>(null);

  const fetchLog = async () => {
    try {
      setLoading(true);
      setError(null);
      const data = await getAgentLog(agentId);
      setLog(data.log);
    } catch (err) {
      setError(String(err));
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    fetchLog();
  }, [agentId]);

  // Auto-scroll to bottom when log updates
  useEffect(() => {
    if (logRef.current) {
      logRef.current.scrollTop = logRef.current.scrollHeight;
    }
  }, [log]);

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/60"
      onClick={onClose}
    >
      <div
        className="bg-gray-900 border border-gray-700 rounded-xl w-[80vw] max-w-4xl h-[70vh] flex flex-col shadow-2xl"
        onClick={(e) => e.stopPropagation()}
      >
        {/* Header */}
        <div className="flex items-center justify-between px-4 py-3 border-b border-gray-700">
          <h3 className="text-sm font-semibold text-gray-200">
            Agent Log: {agentName}
          </h3>
          <div className="flex items-center gap-2">
            <button
              onClick={fetchLog}
              className="text-xs px-2 py-1 rounded bg-gray-700 hover:bg-gray-600 text-gray-300 transition-colors"
            >
              Refresh
            </button>
            <button
              onClick={onClose}
              className="text-gray-400 hover:text-gray-200 transition-colors"
            >
              <svg className="w-5 h-5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M6 18L18 6M6 6l12 12" />
              </svg>
            </button>
          </div>
        </div>

        {/* Log content */}
        <div className="flex-1 overflow-hidden p-4">
          {loading ? (
            <div className="flex items-center justify-center h-full text-gray-400 text-sm">
              Loading log...
            </div>
          ) : error ? (
            <div className="flex items-center justify-center h-full text-red-400 text-sm">
              {error}
            </div>
          ) : log ? (
            <pre
              ref={logRef}
              className="h-full overflow-auto text-[11px] font-mono text-gray-300 whitespace-pre-wrap leading-relaxed"
            >
              {log}
            </pre>
          ) : (
            <div className="flex items-center justify-center h-full text-gray-500 text-sm">
              No log output yet
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
```

### 7. `ui/src/components/AgentSidebar.tsx`

Add a "View Log" button for each agent that has a `worktree_path`. When clicked, show `LogModal`.

**a)** Import `LogModal`:

```typescript
import { LogModal } from "./LogModal";
```

**b)** Add state for the selected agent log:

```typescript
const [logAgentId, setLogAgentId] = useState<string | null>(null);
const logAgent = logAgentId ? agents.find((a) => a.id === logAgentId) : null;
```

**c)** Add "View Log" button next to "Show details" (after line 188):

```tsx
{agent.worktree_path && (
  <button
    onClick={() => setLogAgentId(agent.id)}
    className="ml-2 mt-1 text-[10px] text-amber-400 hover:text-amber-300"
  >
    View Log
  </button>
)}
```

**d)** Render `LogModal` at the bottom of the component (before the closing `</>`):

```tsx
{logAgent && (
  <LogModal
    agentId={logAgent.id}
    agentName={logAgent.name}
    onClose={() => setLogAgentId(null)}
  />
)}
```

### 8. `ui/src/App.tsx`

Wire the new `answerClarifications` callback from `useBoard` through `Board`:

```tsx
<Board
  // ... existing props ...
  onAnswerClarifications={board.answerClarifications}
/>
```

## Conventions

- React functional components with hooks
- TypeScript strict types
- Tailwind CSS utility classes (dark theme — gray-900 backgrounds)
- `text-[10px]` for smallest text, `text-xs` for small, `text-sm` for normal
- Amber color palette for the clarification feature (amber-300, amber-500, amber-600, amber-700, amber-900)
- Follow existing component patterns exactly (stop propagation on interactive elements, `handleAction` for loading states)

## Build Verification

```bash
cd ui && npm run build
```
