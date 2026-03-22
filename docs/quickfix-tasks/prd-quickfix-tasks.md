# PRD: Quick Fix Task Category

**Date**: 2026-03-21
**Status**: Draft
**Scope**: `internal/model/`, `internal/orchestrator/`, `internal/tui/`

---

## 1. Problem Statement

All tasks currently flow through the same heavyweight lifecycle: planner decomposition → plan review → TDD test writing → test review → implementation → merge review → merge. This pipeline is appropriate for multi-file features, but many changes are too trivial to warrant it — typo fixes, single-line constraint violations, simple config adjustments, and straightforward bug fixes.

The cost of this mismatch is twofold:

1. **Wasted time and agent capacity.** A one-line fix spawns a planner agent, waits for human plan review, spawns a test agent, waits for human test review, then finally spawns a coder — consuming multiple agent slots and multiple human review cycles for work that should take minutes.
2. **Bug reports go unaddressed.** The new bug report ingestion system surfaces actionable, often trivial fixes. Without a lightweight path, these pile up because routing them through the full pipeline isn't worth the overhead.

The orchestrator needs a "quick fix" category: tasks that skip planning and TDD gates, go straight to a coder agent, and merge with standard constraint validation. They must still flow through orchestration for visibility, tracking, and integration with the bug report system.

---

## 2. Solution

Introduce a `TaskCategory` enum (`standard`, `quickfix`) on the Task model. Quick fix tasks follow a minimal lifecycle:

```
BACKLOG → IN_PROGRESS → MERGING → DONE
```

They skip planning, plan review, TDD test writing, and test review. At merge time, they still run existing tests and constitution/constraint checks. If anything fails, they are flagged for human review rather than retried or escalated automatically.

Quick fixes enter the system through two paths:

1. **Human-created**: The user creates a task in the TUI and checks a "quick fix" checkbox. If the description is sparse, a classifier function enriches it with target file hints and context.
2. **Bug-report-derived**: After bug report ingestion, a classifier function (single LLM call within the orchestrator tick loop) examines each bug report and decides whether it warrants a quick fix or a full standard task. If quick fix, it produces a consolidated task with title, description, target files, and relevant bug report context.

Quick fixes are always top-level tasks (never subtasks of a feature). They are atomic — no decomposition into subtasks. They use regular coder agents and participate in the existing concurrency pool.

---

## 3. User Stories

1. As a developer using the orchestrator, I want to create a quick fix task that skips planning and TDD gates, so that trivial changes don't consume unnecessary time and review cycles.
2. As a developer, I want to mark a task as "quick fix" via a checkbox in the TUI, so that I can explicitly choose the lightweight pipeline at creation time.
3. As a developer, I want the orchestrator to automatically classify incoming bug reports as quick fix or standard, so that trivial bug fixes are routed through the lightweight pipeline without manual intervention.
4. As a developer, I want the classifier to consolidate bug report context (error messages, constraint details, affected files) into the quick fix task description, so that the coder agent has enough information to act without a planner.
5. As a developer, I want sparse quick fix descriptions (from human-created tasks) to be automatically enriched with target file hints, so that the coder agent knows where to look.
6. As a developer, I want quick fix tasks to appear in the TUI task board alongside standard tasks but visually distinguished, so that I can track their progress without needing a separate view.
7. As a developer, I want quick fix tasks to still run existing tests and constraint checks at merge time, so that quality gates are not bypassed for trivial changes.
8. As a developer, I want failed quick fix merges (test failures, constraint violations) to be flagged for human review, so that I can decide whether to escalate to a full task or manually intervene.
9. As a developer, I want quick fix tasks to use the same worktree-per-agent model as standard tasks, so that they don't introduce new concurrency or isolation concerns.
10. As a developer, I want quick fix tasks to participate in the existing agent concurrency pool, so that they don't require separate capacity management.
11. As a developer, I want the classifier function to run as part of the orchestrator tick loop (not a separate agent spawn), so that classification is fast and doesn't consume an agent slot.
12. As a developer, I want quick fixes to be top-level tasks only (never subtasks), so that the task hierarchy remains simple and the lightweight lifecycle doesn't interfere with feature decomposition.
13. As a developer, I want the classifier to provide a rationale for its classification decision, so that I can audit why a bug report was routed to the quick fix pipeline.
14. As a developer, I want to be able to convert a failed quick fix into a standard task, so that I can escalate when a fix turns out to be more complex than expected.

---

## 4. Implementation Decisions

### 4.1 Task Model Changes

- Add a `TaskCategory` enum to `internal/model/enums.go` with values `standard` and `quickfix`.
- Add a `Category` field of type `TaskCategory` to the `Task` struct.
- Default value is `standard` so that all existing tasks are unaffected.
- Add a database migration to add the `category` column to the tasks table.

### 4.2 Quick Fix Lifecycle

- Quick fix tasks follow: `BACKLOG → IN_PROGRESS → MERGING → DONE`.
- They skip: `PLANNING`, `PLAN_REVIEW`, `TEST_WRITING`, `TEST_REVIEW`, `TESTING_READY`.
- At merge time, existing tests and constraint/constitution checks still run.
- On failure at merge: task is flagged for human review (not auto-retried, not escalated to fixer agent).

### 4.3 Classifier Function

- Implemented as a function in the orchestrator package (e.g., `classifyBugReport()` in `internal/orchestrator/classifier.go`).
- Not a separate agent type — runs as a single LLM call within the orchestrator tick loop.
- Input: bug report details (category, severity, description, error output, affected files).
- Output: structured decision — `{category: "quickfix" | "standard", title, description, target_files, rationale}`.
- Also used to enrich sparse human-created quick fix tasks (add target file hints, expand description).
- Called during the bug report ingestion phase of the orchestrator tick.

### 4.4 Bug Report Integration

- After bug reports are ingested, each new bug report is passed to the classifier function.
- If classified as `quickfix`: a Task is created with `Category=quickfix` and the consolidated description.
- If classified as `standard`: a Task is created with `Category=standard` and follows the normal pipeline.
- The bug report's context (error messages, constraint violation details, referenced files) is included in the task description.

### 4.5 Human-Created Quick Fix Flow

- TUI task creation form includes a "quick fix" checkbox.
- When checked, the task is created with `Category=quickfix`.
- If the description is sparse (short description, no file references), the classifier function is called to enrich it before the task enters `IN_PROGRESS`.

### 4.6 Agent Assignment

- Quick fix tasks use the `coder` agent type.
- They participate in the existing concurrency pool (no separate quick fix pool).
- The coder agent is spawned with the quick fix task's description and target file hints in its prompt.
- Each quick fix agent gets its own worktree, same as standard tasks.

### 4.7 Scheduling

- Quick fix tasks are top-level tasks with no subtasks, so wave scheduling does not apply.
- They are scheduled by the existing priority-ordered query in `scheduleSubtasks()`.
- The scheduling logic needs to handle assigning coder agents directly to top-level quickfix tasks (currently it only assigns agents to subtasks).

### 4.8 TUI Changes

- Quick fix tasks are displayed in the same task board as standard tasks.
- They are visually distinguished (e.g., a label or icon indicating "quick fix").
- Task creation form includes a "quick fix" checkbox.

---

## 5. Testing Decisions

Good tests verify external behavior through the module's public interface, not implementation details. Tests should be resilient to refactoring — if the internal structure changes but the behavior is the same, tests should still pass.

### Modules to test:

**Classifier function** — Test that it returns well-structured classification decisions for various bug report inputs. Mock the LLM call and verify:
- Bug reports with trivial fixes (constraint violations, typos) are classified as `quickfix`.
- Bug reports with complex issues (multi-file changes, architectural problems) are classified as `standard`.
- The output includes all required fields (category, title, description, target_files, rationale).
- Sparse descriptions are enriched with target file hints.

**Lifecycle routing** — Test that quickfix tasks skip planning/TDD gates and go straight through the lightweight pipeline:
- A task with `Category=quickfix` in `BACKLOG` transitions directly to `IN_PROGRESS`.
- A quickfix task never enters `PLANNING`, `PLAN_REVIEW`, `TEST_WRITING`, `TEST_REVIEW`, or `TESTING_READY`.
- On merge failure, the task is flagged for human review.
- Constraint checks still run at merge time for quickfix tasks.

**Prior art**: The existing test files in `internal/orchestrator/` (e.g., `task_processing_test.go`, `scheduler_test.go`) provide patterns for testing lifecycle transitions and scheduling behavior.

---

## 6. Out of Scope

- **Automatic retry or fixer escalation for quick fixes.** Failed quick fixes go to human review. Retry/escalation may be added later.
- **Quick fixes as subtasks of features.** Quick fixes are always top-level tasks. Nesting within feature hierarchies is future work.
- **Separate concurrency pool for quick fixes.** They share the existing agent pool. Dedicated capacity may be added if quick fixes are starved by feature work.
- **Quick fix batching.** Grouping multiple quick fixes into a single agent session is not in scope.
- **TUI filtering or separate views for quick fixes.** They appear mixed in the existing board with visual distinction only.
- **Conversion of standard tasks to quick fixes.** Only the reverse (quick fix → standard on failure) is in scope.

---

## 7. Further Notes

- The `TaskCategory` enum is designed to be extensible. Future categories (e.g., `research`, `refactor`) could be added with their own lifecycle overrides.
- The classifier function could evolve into a more sophisticated triage system over time, potentially incorporating historical data about which types of changes succeed as quick fixes.
- Quick fixes that repeatedly fail and escalate to standard tasks may indicate that the classifier needs tuning — this is a signal to watch for operationally.
