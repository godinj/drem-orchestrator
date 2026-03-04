# Agent: Task Board UI

You are working on **Drem Orchestrator**, a Python application that orchestrates Claude Code agents
working in parallel across git worktrees. Your task is to build the React task board UI — a
kanban-style board with real-time updates, human gate interactions (plan review and manual testing),
and agent status monitoring.

## Context

Read these before starting:
- `CLAUDE.md` (project conventions)
- `src/orchestrator/routers/tasks.py` (Task API endpoints — CRUD, transitions, plan-review, test-result)
- `src/orchestrator/routers/projects.py` (Project API — board endpoint)
- `src/orchestrator/routers/ws.py` (WebSocket endpoint for real-time events)
- `src/orchestrator/schemas.py` (Pydantic schemas — TaskResponse, PlanReview, TestResult, SubtaskPlan)
- `src/orchestrator/enums.py` (TaskStatus — all statuses including human gates)
- `ui/package.json` (existing skeleton)
- `ui/vite.config.ts` (proxy config)

## Dependencies

This agent depends on Agent 04 (API Server).
The UI consumes these API endpoints. If the backend isn't running,
develop against the schema types and mock data.

## Deliverables

### New files (`ui/src/`)

#### 1. `types.ts`

TypeScript types matching the backend Pydantic schemas:

```typescript
type TaskStatus =
  | "backlog"
  | "planning"
  | "plan_review"
  | "in_progress"
  | "testing_ready"
  | "manual_testing"
  | "merging"
  | "done"
  | "failed"

type AgentType = "orchestrator" | "planner" | "coder" | "researcher"
type AgentStatus = "idle" | "working" | "blocked" | "dead"

interface Task {
  id: string
  title: string
  description: string
  status: TaskStatus
  priority: number
  labels: string[]
  dependency_ids: string[]
  assigned_agent_id: string | null
  plan: SubtaskPlan[] | null
  plan_feedback: string | null
  test_plan: string | null
  test_feedback: string | null
  worktree_branch: string | null
  pr_url: string | null
  context: Record<string, unknown>
  parent_task_id: string | null
  subtask_count: number
  created_at: string
  updated_at: string
}

interface SubtaskPlan {
  title: string
  description: string
  agent_type: AgentType
  estimated_files: string[]
}

interface Agent {
  id: string
  name: string
  agent_type: AgentType
  status: AgentStatus
  current_task_id: string | null
  worktree_path: string | null
  worktree_branch: string | null
  heartbeat_at: string | null
  created_at: string
}

interface Project {
  id: string
  name: string
  bare_repo_path: string
  default_branch: string
  description: string | null
  task_counts: Record<TaskStatus, number>
  agent_count: number
  created_at: string
}

interface TaskEvent {
  id: string
  task_id: string
  event_type: string
  old_value: string | null
  new_value: string | null
  details: Record<string, unknown> | null
  actor: string
  created_at: string
}

// WebSocket event types
type WSEvent =
  | { type: "task_updated"; task: Task }
  | { type: "task_created"; task: Task }
  | { type: "agent_updated"; agent: Agent }
  | { type: "plan_submitted"; task_id: string; plan: SubtaskPlan[] }
  | { type: "testing_ready"; task_id: string; test_plan: string }
```

#### 2. `api.ts`

API client functions using `fetch`:

```typescript
const API = "/api"

// Projects
async function getProjects(): Promise<Project[]>
async function getProject(id: string): Promise<Project>
async function getBoard(projectId: string): Promise<Record<TaskStatus, Task[]>>

// Tasks
async function createTask(data: { title: string; description: string; project_id: string; priority?: number }): Promise<Task>
async function getTask(id: string): Promise<Task>
async function updateTask(id: string, data: Partial<Task>): Promise<Task>
async function getTaskEvents(id: string): Promise<TaskEvent[]>

// Human gate actions
async function submitPlanReview(taskId: string, approved: boolean, feedback?: string): Promise<Task>
async function submitTestResult(taskId: string, passed: boolean, feedback?: string): Promise<Task>

// Agents
async function getAgents(projectId: string): Promise<Agent[]>
```

#### 3. `hooks/useWebSocket.ts`

React hook for WebSocket connection with auto-reconnect:

```typescript
function useWebSocket(projectId: string, onEvent: (event: WSEvent) => void): {
  connected: boolean
  reconnecting: boolean
}
```

- Connects to `ws://localhost:8000/api/ws/{projectId}`
- Auto-reconnects with exponential backoff (1s, 2s, 4s, max 30s)
- Parses JSON messages into `WSEvent` type

#### 4. `hooks/useBoard.ts`

React Query hook that combines REST fetching with WebSocket updates:

```typescript
function useBoard(projectId: string): {
  columns: Record<TaskStatus, Task[]>
  agents: Agent[]
  isLoading: boolean
  error: Error | null
  createTask: (title: string, description: string) => Promise<void>
  reviewPlan: (taskId: string, approved: boolean, feedback?: string) => Promise<void>
  submitTest: (taskId: string, passed: boolean, feedback?: string) => Promise<void>
}
```

- Initial fetch via `getBoard()`
- WebSocket events optimistically update the local cache
- Mutations invalidate and refetch

#### 5. `components/Board.tsx`

Main kanban board component.

Columns in order:
1. **Backlog** — draggable task cards
2. **Planning** — shows spinner, "AI decomposing..."
3. **Plan Review** — highlighted column, cards have approve/reject buttons
4. **In Progress** — shows subtask progress bar (3/5 subtasks done)
5. **Testing Ready** — highlighted column with notification badge
6. **Manual Testing** — cards have pass/fail buttons
7. **Merging** — shows merge progress
8. **Done** — collapsed by default, expandable

Layout: horizontal scroll, each column ~280px wide, cards stacked vertically.

Header: project name, agent count badge, "New Task" button.

#### 6. `components/TaskCard.tsx`

Individual task card. Appearance varies by status:

**Default card:**
- Title, priority badge, label chips
- Subtask count if has children
- Assigned agent name + status dot (green=working, gray=idle, red=dead)
- Worktree branch name (monospace, small)
- Time in current status

**Plan Review card (PLAN_REVIEW status):**
- All of the above, plus:
- Expandable plan section showing list of proposed subtasks
- Each subtask: title, agent_type badge, estimated files list
- If plan was previously rejected: show plan_feedback in yellow callout
- **[Approve Plan]** button (green) — calls `submitPlanReview(id, true)`
- **[Reject Plan]** button (red) — opens textarea for feedback, calls `submitPlanReview(id, false, feedback)`

**Testing Ready card (TESTING_READY status):**
- All of the above, plus:
- Prominent notification styling (blue border, bell icon)
- Test plan section (rendered markdown or preformatted text):
  ```
  Test Plan:
  1. cd ~/git/drem-canvas.git/feature/auth
  2. cmake --build --preset release
  3. ./build/DremCanvas
  4. Click Login → verify redirect
  ```
- Worktree path (copyable)
- **[Start Testing]** button — transitions to MANUAL_TESTING

**Manual Testing card (MANUAL_TESTING status):**
- Test plan visible
- If previously failed: show test_feedback in yellow callout
- **[Pass]** button (green) — calls `submitTestResult(id, true)`
- **[Fail]** button (red) — opens textarea for feedback, calls `submitTestResult(id, false, feedback)`

#### 7. `components/TaskCreateDialog.tsx`

Modal dialog for creating a new task:
- Title input (required)
- Description textarea (required)
- Priority select (0-3)
- Submit button → calls `createTask()`

#### 8. `components/AgentSidebar.tsx`

Right sidebar showing agent status:
- List of agents with status dot, name, type badge
- Current task title (linked)
- Worktree branch
- Last heartbeat (relative time)
- Expandable: view agent log output

#### 9. `components/TaskDetail.tsx`

Slide-over panel when clicking a task card:
- Full description
- Subtask list with status badges
- Event timeline (from `/api/tasks/{id}/events`)
- Context JSON viewer (collapsible)
- Plan display with approve/reject (if PLAN_REVIEW)
- Test interface with pass/fail (if MANUAL_TESTING)

#### 10. `App.tsx`

Update the existing skeleton:
- React Query provider
- Project selector (dropdown if multiple projects)
- Board component
- Agent sidebar (collapsible)
- WebSocket connection

### Styling

Use Tailwind CSS. Design principles:
- Dark theme (gray-900 background, gray-800 cards)
- Human gate columns highlighted (blue-900/50 background for plan_review, green-900/50 for testing_ready)
- Status-appropriate colors: red for failed, green for done, blue for human gates, gray for in-progress
- Responsive: works at 1280px+ width
- Subtle animations: card transitions between columns

### Migration

#### 11. `ui/src/App.tsx`

Replace the placeholder with the full app shell.

#### 12. `ui/index.html`

Update title to "Drem Orchestrator".

### Tests

No unit tests required for the UI. Manual verification:

1. `cd ui && npm run dev` — verify app loads at http://localhost:5173
2. Verify kanban columns render (even with empty data)
3. Verify "New Task" dialog opens and submits
4. Verify WebSocket connects (check console)

## Build Verification

```bash
cd ui
npm install
npx tsc --noEmit
npm run build
```
