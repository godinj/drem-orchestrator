# Bug Reports: End-to-End Walkthrough

This walkthrough traces a bug report from filing through triage to promotion, showing each step of the workflow.

## 1. Agent Files a Bug Report

During its work, a coder agent encounters a failing `go vet` check in a package it depends on. The agent cannot fix the issue (it is outside its subtask scope) but can work around it. It writes a JSON file to the shared bug report directory:

**File:** `.drem/bug-reports/f47ac10b-58cc-4372-a567-0e02b2c3d479.json`

```json
{
  "title": "go vet fails on internal/merge package",
  "description": "Running go vet ./internal/merge/... reports 'unreachable code' in merge.go line 245. The dead code was introduced by a recent refactor. Agent worked around it by skipping the vet step but the issue should be fixed.",
  "category": "tooling",
  "severity": "degraded",
  "reproduction_context": "cd /path/to/worktree && go vet ./internal/merge/...\n\nOutput:\ninternal/merge/merge.go:245:2: unreachable code",
  "agent_id": "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
  "task_id": "98765432-dcba-0987-6543-210fedcba987"
}
```

**Field breakdown:**

| Field | Value | Why |
|-------|-------|-----|
| `title` | Short summary | Appears in the TUI list view |
| `description` | Detailed explanation | Shown in the detail pane; should explain symptoms and impact |
| `category` | `tooling` | Classifies the problem type for filtering |
| `severity` | `degraded` | Agent worked around it but the problem remains |
| `reproduction_context` | Commands and output | Enough for a human to reproduce the issue |
| `agent_id` | Agent UUID | Read from `.claude/agent-metadata.json` in the worktree |
| `task_id` | Task UUID | The task the agent was working on when it hit the issue |

The agent continues its work after writing the file. Filing a bug report does not interrupt the agent's current task.

## 2. Orchestrator Ingests the Report

On the next tick (every 5 seconds by default), the orchestrator's ingestion loop scans `.drem/bug-reports/` for `.json` files.

**What happens:**

1. The orchestrator reads `f47ac10b-58cc-4372-a567-0e02b2c3d479.json`
2. Parses the JSON and validates required fields (`title`, `description`, `category`, `severity`)
3. Validates enum values: `category` must be one of `tooling`, `merge_conflict`, `requirements`, `constraint_violation`, `upstream_code`, `test_failure`, `environment`, `other`; `severity` must be `blocking`, `degraded`, or `informational`
4. Parses optional `agent_id` and `task_id` as UUIDs
5. Creates a `BugReport` record in the database with status `open`, associated with the current project
6. Deletes the source JSON file

**If validation fails** (missing required fields, unknown category/severity, malformed UUIDs), the file is moved to `.drem/bug-reports/failed/` and an error is logged. The orchestrator continues processing remaining files.

The database is the sole source of truth after ingestion. The JSON files are ephemeral transport.

## 3. Operator Sees It in the TUI

The operator presses `b` from the main dashboard to open the bug report screen.

The list view shows each report as a single row with these columns:

```
  ~ [tooling]        go vet fails on internal/merge package       degraded   5m ago
  ! [test_failure]   intermittent timeout in TestMergeConflict    blocking   12m ago
  · [requirements]   acceptance criteria ambiguous for auth flow  informational  1h ago
```

**Column layout:**

| Column | Description |
|--------|-------------|
| Severity icon | `!` (red, bold) = blocking, `~` (yellow) = degraded, `·` (gray) = informational |
| Category tag | Bracketed category value, e.g., `[tooling]` |
| Title | Truncated to fit available width |
| Status | Color-coded: white = open, cyan = acknowledged, green = promoted, gray = dismissed |
| Timestamp | Relative time since filing (e.g., `5m ago`, `2h ago`, `3d ago`) |

Press `Enter` on a report to open the detail pane, which shows the full description, reproduction context, agent/task IDs, comments, and available actions.

## 4. Operator Triages

The operator navigates to the `go vet` report using `j/k` and begins triage.

### Step 1: Acknowledge

Press `a` to acknowledge the report. This transitions it from `open` to `acknowledged`, signaling "I've seen this" without committing to immediate action. The status badge updates to cyan.

### Step 2: Add a Comment

Press `c` to open the feedback overlay. Type a comment and press Enter:

```
Known issue from the merge refactor last week. Will fix after current feature lands.
```

Comments are stored with author `user` and a timestamp. They appear in the detail pane in chronological order. System-generated comments (e.g., from promotion) use author `system`.

### Step 3: Decide -- Promote or Dismiss

The operator decides this report warrants a task. They press `p` to promote.

## 5. Promotion Workflow

When the operator presses `p`, the promotion workflow begins:

1. **Temp file created.** A temporary Markdown file is created with the bug report's title on the first line and description below:

   ```
   go vet fails on internal/merge package

   Running go vet ./internal/merge/... reports 'unreachable code' in merge.go line 245. The dead code was introduced by a recent refactor. Agent worked around it by skipping the vet step but the issue should be fixed.
   ```

2. **`$EDITOR` opens.** The operator's configured editor (or `vi` if `$EDITOR` is not set) opens with the pre-populated content. The operator can refine the title, expand the description, add acceptance criteria, or adjust scope.

3. **Save and exit.** When the editor closes:
   - The first line of the file becomes the task title
   - Everything after the first line becomes the task description
   - If the title is empty, promotion is cancelled

4. **Task created.** A new task is inserted into the database in `backlog` status with the edited title and description, associated with the current project.

5. **Bug report updated.** The report's status transitions to `promoted` and its `PromotedTaskID` field is set to the new task's ID. The detail pane shows a green "Promoted to task: <uuid>" line.

Both the task creation and bug report update happen in a single database transaction -- if either fails, neither is committed.

The promoted task appears in the main dashboard's task board and follows the normal task lifecycle (planning, implementation, testing, merging).

## State Machine

The complete state machine for bug report lifecycle:

```
open ──→ acknowledged ──→ promoted
  │          │
  │          └──→ dismissed
  │
  ├──→ promoted
  └──→ dismissed
```

**Valid transitions:**

| From | To | Trigger |
|------|----|---------|
| `open` | `acknowledged` | Operator presses `a` |
| `open` | `promoted` | Operator presses `p` and completes editor workflow |
| `open` | `dismissed` | Operator presses `D` |
| `acknowledged` | `promoted` | Operator presses `p` and completes editor workflow |
| `acknowledged` | `dismissed` | Operator presses `D` |

`promoted` and `dismissed` are terminal states with no outbound transitions.

**Hard-delete** (`x` with `y` confirmation) is not a status transition. It permanently removes the bug report and all its comments from the database.

## Filtering

Press `/` to enter filter mode. The filter bar appears below the header with four dimensions:

```
> Category: all   Severity: all   Status: all   Dismissed: no
```

- `Tab` / `Shift+Tab` -- cycle between dimensions
- `j/k` -- cycle values within the selected dimension
- `Enter` -- apply filters and return to list
- `Esc` -- cancel without applying

**Filter dimensions:**

| Dimension | Values | Default |
|-----------|--------|---------|
| Category | `all`, `tooling`, `merge_conflict`, `requirements`, `constraint_violation`, `upstream_code`, `test_failure`, `environment`, `other` | `all` |
| Severity | `all`, `blocking`, `degraded`, `informational` | `all` |
| Status | `all`, `open`, `acknowledged`, `promoted`, `dismissed` | `all` |
| Dismissed | `yes`, `no` | `no` |

By default, dismissed reports are hidden from the list. Toggle the Dismissed filter to `yes` to include them.

Active filters are displayed as badges in the header, e.g., `[cat:tooling sev:blocking]`. Filters persist within the TUI session but reset on restart.
