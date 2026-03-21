# Agent Bug Reports: Structured Bug Filing and Promotion Workflow

## Problem Statement

When agents encounter problems during their work — broken builds, flaky tests, unclear requirements, constraint violations, upstream code issues — they have no structured way to report these problems for human attention. Supervisors currently write journals about issues they encounter, but transitioning journal entries into actionable tasks requires a human to manually read through journals, identify issues, and create tasks. Other agent types (planners, coders, testers, fixers) have no reporting mechanism at all — their problems surface only through silent failures, retries, or degraded output.

The result is lost signal: real issues go unnoticed, patterns of recurring failures aren't visible, and the human operator lacks a centralized view of what's going wrong across the orchestration.

## Solution

Introduce **Bug Reports** as a first-class entity in the system. Any agent type can file a bug report by writing a structured JSON file to a shared directory. The orchestrator ingests these files on each tick, stores them in the database, and surfaces them in a dedicated TUI screen. Human operators can review, comment on, acknowledge, dismiss, or promote bug reports into full tasks. Promoted tasks are pre-populated from the bug report and opened in `$EDITOR` for refinement before creation.

This replaces the supervisor journal workflow with a unified, structured reporting mechanism available to all agent types.

## User Stories

1. As a human operator, I want to see all agent-reported problems in a dedicated screen, so that I have centralized visibility into what's going wrong across the orchestration.
2. As a human operator, I want to filter bug reports by category, severity, status, and project, so that I can focus on the most relevant issues.
3. As a human operator, I want to promote a bug report into a task with pre-populated title and description, so that I can quickly turn observed problems into actionable work.
4. As a human operator, I want to edit the title and description in `$EDITOR` before a promoted bug report becomes a task, so that I can add context or refine the scope.
5. As a human operator, I want to acknowledge a bug report to signal I've seen it without taking immediate action, so that I can triage without losing track.
6. As a human operator, I want to dismiss a bug report to hide it from the default view while keeping it in the database, so that I can reduce noise without losing history.
7. As a human operator, I want to hard-delete a bug report permanently, so that I can remove irrelevant or spam reports.
8. As a human operator, I want to add comments to bug reports, so that I can annotate them with notes like "known issue, waiting on upstream fix."
9. As a human operator, I want to see the severity of each bug report at a glance (blocking, degraded, informational), so that I can prioritize my attention.
10. As a human operator, I want to see which agent filed a bug report and what task they were working on, so that I can understand the context of the problem.
11. As a human operator, I want to see the reproduction context (file paths, failed commands, error output) in the bug report detail view, so that I can understand how to reproduce the problem.
12. As a human operator, I want to switch between the main dashboard and the bug report screen using the `b` key, so that navigation is fast and consistent with the TUI's keybinding model.
13. As a coder agent, I want to file a bug report when I encounter a broken build or failing dependency, so that the problem is visible to the operator even if I work around it.
14. As a tester agent, I want to file a bug report when I discover a flaky test or upstream code issue, so that recurring test failures are tracked rather than silently retried.
15. As a planner agent, I want to file a bug report when requirements are unclear or contradictory, so that ambiguities are surfaced before implementation begins.
16. As a fixer agent, I want to file a bug report when a constraint violation cannot be resolved within my scope, so that the operator knows the constraint system needs attention.
17. As any agent, I want to file a bug report mid-run without interrupting my work, so that I don't lose context by stopping to spawn a new agent.
18. As a supervisor agent, I want to file bug reports instead of writing journal entries, so that my observations feed into a structured workflow rather than an unstructured log.
19. As a human operator, I want promoted bug reports to automatically transition to `promoted` status with a link to the created task, so that I can trace the lineage from observation to fix.
20. As a human operator, I want the bug report list to show: severity icon, category tag, title, filing agent type, associated task title, status, and timestamp, so that I can scan and triage efficiently.
21. As a human operator, I want a confirmation prompt before hard-deleting a bug report, so that accidental deletions are prevented.
22. As a human operator, I want to view dismissed bug reports by toggling a filter, so that I can revisit archived issues if they become relevant again.

## Implementation Decisions

### New Entity: BugReport

A first-class GORM model with the following fields:
- **ID** (primary key)
- **Title** (string, required)
- **Description** (text, required — what went wrong)
- **Category** (enum: `tooling`, `merge_conflict`, `requirements`, `constraint_violation`, `upstream_code`, `test_failure`, `environment`, `other`)
- **Severity** (enum: `blocking` — agent cannot continue, `degraded` — agent worked around it, `informational` — observed but no immediate impact)
- **Status** (enum: `open`, `acknowledged`, `promoted`, `dismissed`)
- **ReproductionContext** (text — file paths, commands, error output)
- **AgentID** (foreign key to Agent)
- **TaskID** (foreign key to Task)
- **ProjectID** (foreign key to Project)
- **PromotedTaskID** (nullable foreign key to Task — set when promoted)
- **CreatedAt**, **UpdatedAt** (timestamps)

### New Entity: BugReportComment

- **ID** (primary key)
- **BugReportID** (foreign key to BugReport)
- **Author** (string — "user" or "system")
- **Body** (text)
- **CreatedAt** (timestamp)

### Filing Mechanism: File-Drop with Tick-Based Ingestion

- Agents write a JSON file to `.drem/bug-reports/<uuid>.json` containing: title, description, category, severity, reproduction_context
- The orchestrator checks this directory on each tick (every 5 seconds)
- Valid files are parsed, inserted into the database (associating with the filing agent's ID, current task, and project), and deleted
- Invalid files are logged and moved to `.drem/bug-reports/failed/` for debugging
- Files are ephemeral transport — the database is the sole source of truth

### State Machine

```
open → acknowledged → promoted
open → acknowledged → dismissed
open → promoted
open → dismissed
```

Hard-delete is a separate action (not a status transition) that removes the record from the database entirely.

### Promotion Workflow

1. Human presses `p` on a bug report in the TUI
2. A temporary file is created pre-populated with the bug report's title and description
3. `$EDITOR` is spawned for the human to refine
4. On save/exit, a new Task is created in `backlog` status with the edited content
5. The bug report's status transitions to `promoted` and `PromotedTaskID` is set to the new task's ID

### TUI: Dedicated Bug Report Screen

- Accessed via `b` keybinding from the main dashboard; `Esc` or `b` returns to dashboard
- List view columns: severity icon, category tag, title (truncated), agent type, associated task title, status, timestamp
- Selecting a report shows a detail pane with full description, reproduction context, comments, and available actions
- Filter bar supporting: category, severity, status, project
- Keybindings: `a` acknowledge, `p` promote, `D` dismiss, `x` delete (with confirmation), `c` add comment, `j/k` navigate, `/` filter

### Prompt Changes

- All agent type prompts (planner, coder, tester, fixer, reviewer, supervisor) are updated to include bug report filing instructions
- Instructions specify the JSON schema and the file path convention
- Supervisor journal instructions are removed and replaced with bug report filing instructions

### No Suggested Fix Field

Bug reports intentionally omit a "suggested fix" field. When a bug report is promoted to a task, the agent assigned to that task should determine the correct fix based on the bug report's description and reproduction context.

### No Automated Actions

Bug reports do not trigger automated actions (e.g., spawning fixer agents). They are purely an observability and workflow tool for human operators. Automated responses may be added in a future iteration.

## Testing Decisions

Tests should verify external behavior through the module's public interface, not implementation details. Each test should set up state, call the public API, and assert on the observable outcome.

### Modules to test:

**`internal/bugreport/`** (primary test target):
- Ingestion: valid JSON files are parsed and inserted; invalid files are rejected; files are cleaned up after ingestion; agent/task/project associations are set correctly
- State transitions: valid transitions succeed (open→acknowledged, acknowledged→promoted, etc.); invalid transitions are rejected
- Promotion: creates a task with correct pre-populated fields; sets PromotedTaskID on the bug report; transitions status to promoted
- Filtering: queries return correct results when filtering by category, severity, status, project; combinations of filters work correctly
- Comments: adding comments associates them with the correct bug report; comments are returned in chronological order
- Hard-delete: record is removed from DB; associated comments are cascade-deleted

**`internal/model/bugreport.go`**:
- Enum validation: invalid category/severity/status values are rejected
- Association integrity: foreign keys reference valid records

### Prior art:
- State transition tests in `internal/state/` follow the pattern of testing valid and invalid transitions
- Model tests follow GORM patterns established in `internal/model/`
- The `internal/constraints/` package demonstrates testing a module with file-based input (TOML loading → evaluation → report), which parallels the bug report ingestion flow (JSON loading → validation → DB insertion)

## Out of Scope

- **Automated deduplication**: If multiple agents report the same problem, all reports are shown individually. Automated grouping/merging may be added later if volume becomes a problem.
- **Automated actions from bug reports**: Bug reports do not trigger fixer agents, re-planning, or any automated remediation. This is a future consideration.
- **GitHub issue integration**: Bug reports live in the local database only. Syncing to external issue trackers is out of scope.
- **Agent-to-agent bug report visibility**: Agents do not read other agents' bug reports. Bug reports are for human consumption.
- **Suggested fix field**: Intentionally excluded — the fixing agent determines the approach.
- **fsnotify / filesystem watching**: Tick-based directory scanning (every 5s) is sufficient. Event-driven file watching may be added if latency becomes a concern.

## Further Notes

- This feature **replaces the supervisor journal workflow**. Once bug reports are implemented, supervisor prompts should be updated to file bug reports instead of writing journals. Journal-related code can be removed.
- The `.drem/bug-reports/` directory should be created automatically by the orchestrator on startup if it doesn't exist, and added to `.gitignore` if not already present.
- Bug report JSON files should include the agent's ID and current task ID so the orchestrator can associate them correctly during ingestion, since the orchestrator knows which agents are running and can validate these references.
- The bug report screen's filter state should persist within a TUI session but does not need to persist across restarts.
- Documentation for this feature (README walkthrough, configuration guide) is part of the acceptance criteria.
