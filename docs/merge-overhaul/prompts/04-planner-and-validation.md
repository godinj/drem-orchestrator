# Agent: Planner Prompt Improvements & Plan Validation

You are working on the `master` branch of Drem Orchestrator, a Go-based multi-agent orchestrator that manages Claude Code agents via tmux in git bare repos with worktrees.
Your task is to improve the planner agent's prompt with decomposition guidance that prevents observed failure modes, add plan validation code, and fix replanning with stale subtask cleanup.

## Context

Read these before starting:
- `docs/merge-overhaul/planner-decomposition-strategy.md` (all sections — P-1 through P-7)
- `internal/prompt/prompt.go` (plannerInstructions function, Generate function, Opts struct)
- `internal/orchestrator/orchestrator.go` (onPlannerCompleted function ~line 929, processBacklog function ~line 632 — read these to understand how plans are parsed and how replanning works)
- `internal/model/models.go` (Task model — understand the Plan field, PlanFeedback field, Context JSONField)

## Deliverables

### 1. Planner Prompt Improvements (`internal/prompt/prompt.go`)

Modify `plannerInstructions()` to add the following sections after the existing instructions. The current planner prompt is ~6 lines of instruction. Add ~40 lines across 5 new sections. Keep the existing instructions intact and append these.

#### Section: Coverage Verification (P-1)

```markdown
## Coverage Verification

Before finalizing your plan, verify:
1. List every acceptance criterion from the task description
2. For each criterion, identify which subtask(s) address it
3. If any criterion is not covered, add a subtask for it
4. If any subtask doesn't map to a criterion, justify it or remove it

Include this mapping in your plan.json:

"coverage": [
  {
    "criterion": "description of the acceptance criterion",
    "subtask_indices": [0, 2]
  }
]
```

#### Section: Integration Subtask (P-2)

```markdown
## Integration Subtask

Your plan MUST include a final integration subtask that:
- Wires together the components built by other subtasks
- Verifies end-to-end functionality (not just unit tests)
- Has dependencies on ALL other implementation subtasks
- Touches the files that connect subsystems (registries, routers, factories, main entry points)

This subtask runs last, after all other agent work is merged.
If the feature is simple enough to not need integration wiring, explicitly state why in the subtask description.
```

#### Section: Decomposition Rules (P-3)

```markdown
## Decomposition Rules

DO:
- Decompose along functional boundaries that minimize file overlap
- Make each subtask produce a testable, observable behavior change
- Include acceptance criteria from the parent task in subtask descriptions
- Prefer fewer, larger subtasks over many small ones (3-6 is typical)

DO NOT:
- Decompose by code layer (one subtask for models, one for handlers, one for UI) — this maximizes file overlap and merge conflicts
- Create subtasks for generic operations that already exist in the codebase — verify the operation doesn't exist before planning it
- Plan more than 8 subtasks — if you need more, the task should be split into multiple parent tasks
- Omit the files list — this is used for scheduling and must be accurate
```

#### Section: File Overlap (P-4)

```markdown
## File Overlap

Subtasks that modify the same files CANNOT run in parallel — they will be serialized, increasing total time. Minimize file overlap between subtasks. If overlap is unavoidable, use the `dependencies` field to specify the correct merge order.

When multiple subtasks must modify the same file (e.g., a registry or router), prefer having ONE subtask own that file and other subtasks depend on it, rather than having all subtasks append to it independently.
```

#### Section: Test Subtasks (P-7)

```markdown
## Test Subtasks

If you include a testing subtask, it MUST:
- Depend on ALL implementation subtasks (list all indices in `dependencies`)
- Be the last subtask (or second-to-last, before integration)
- Have agent_type "coder" (not "researcher")

Ordering: implementation subtasks -> test subtask -> integration subtask
```

### 2. Plan Validation (`internal/orchestrator/plan_validation.go` — new file)

Create a new file with plan validation logic. This is called after the planner produces plan.json, before transitioning to `plan_review`.

```go
package orchestrator

// PlanValidationResult contains the outcome of validating a plan.
type PlanValidationResult struct {
    Valid    bool
    Warnings []string
    Errors   []string
}

// ValidatePlan checks a parsed plan for structural issues.
// Returns warnings (surfaced at plan_review) and errors (block transition).
func ValidatePlan(subtasks []planSubtask) PlanValidationResult
```

Where `planSubtask` matches whatever struct the orchestrator uses to parse plan.json entries. Read `onPlannerCompleted()` to find the exact type.

Validation checks:

1. **Subtask count bounds**: If > 8 subtasks, add warning: `"Plan has N subtasks (recommended max: 8)"`
2. **File lists present**: Count subtasks with empty `Files` lists. If any, add warning: `"N subtask(s) have no files listed — scheduling will be degraded"`
3. **File overlap detection**: For each pair of subtasks, check if their `Files` lists overlap. If overlapping pairs exist and have no dependency between them, add warning: `"Subtasks A and B overlap on [files] but have no dependency — they will be serialized"`
4. **Dependency cycle detection**: Build a directed graph from `dependencies` and check for cycles. If found, add error: `"Dependency cycle detected in subtask dependencies"`
5. **Test subtask ordering**: If any subtask has "test" in its title (case-insensitive), check that it depends on all other non-test subtasks. If not, add warning: `"Test subtask 'X' does not depend on all implementation subtasks"`

Store the validation result in `task.Context["plan_validation"]` so the TUI can display it during plan_review.

Integrate the validation call into `onPlannerCompleted()` — after parsing plan.json and before transitioning to `plan_review`. Validation errors should prevent the transition and log the errors. Validation warnings should be stored but not block the transition.

### 3. Replanning Cleanup (`internal/orchestrator/orchestrator.go`)

Modify `processBacklog()` to handle replanning with stale subtasks. When a task transitions from `failed -> backlog` (or any state -> backlog) for replanning AND has `PlanFeedback` set, detach old subtasks before spawning a new planner.

```go
// In processBacklog(), before the existing planning transition:
if task.PlanFeedback != "" {
    var oldSubtasks []model.Task
    o.db.Where("parent_task_id = ?", task.ID).Find(&oldSubtasks)
    if len(oldSubtasks) > 0 {
        for i := range oldSubtasks {
            oldSubtasks[i].ParentTaskID = nil
            o.db.Save(&oldSubtasks[i])
        }
        slog.Info("detached old subtasks for replanning",
            "task_id", task.ID, "count", len(oldSubtasks))
    }
}
```

This prevents the planner from seeing stale `done` subtasks and auto-advancing without generating a new plan.

### 4. Tests

**Plan validation tests** (`internal/orchestrator/plan_validation_test.go` — new file):

- No overlap -> all in one group (no warnings about serialization)
- Full overlap -> warnings for each overlapping pair
- Empty files -> warning about scheduling degradation
- Subtask count > 8 -> warning
- Dependency cycle -> error, `Valid` is false
- Test subtask without full dependencies -> warning
- Valid plan with good structure -> no warnings, no errors

**Prompt tests**: Verify `plannerInstructions()` output contains the new section headers: "Coverage Verification", "Integration Subtask", "Decomposition Rules", "File Overlap", "Test Subtasks".

## Scope Limitation

ONLY modify `internal/prompt/prompt.go` and create `internal/orchestrator/plan_validation.go`. For the replanning cleanup, you will also modify `processBacklog()` in `internal/orchestrator/orchestrator.go` — limit your changes to ONLY that function. Do NOT modify any other functions in orchestrator.go. Do NOT touch `internal/worktree/`, `internal/merge/`, or `internal/agent/`.

## Conventions

- Go 1.22+ with standard library
- `gofmt` for formatting
- Exported functions have doc comments
- Error wrapping: `fmt.Errorf("context: %w", err)`
- Table-driven tests
- Build verification: `go build ./... && go test ./...`
