# Planner Agent Decomposition Strategy — Design Notes

**Date**: 2026-03-07
**Status**: Draft (future follow-up to [merge reliability PRD](prd-merge-reliability.md))
**Scope**: `internal/prompt/prompt.go` — `plannerInstructions()`, plan validation in `internal/orchestrator/orchestrator.go`

---

## Current State

The planner prompt (`prompt.go:156-187`) gives the agent 6 lines of instruction:

1. "Decompose this task into implementable subtasks"
2. Plan JSON schema (title, description, agent_type, files, dependencies, priority)
3. "Each subtask should be independently implementable by one agent"
4. "List specific files each subtask will create or modify"
5. "Set dependencies between subtasks where order matters"
6. Agent type guidance (coder vs researcher)

There is no guidance on **what makes a good decomposition**, no validation that the plan covers the actual task requirements, and no instruction to account for integration concerns.

---

## Observed Failure Modes

### FM-1: Scope Drift — Subtasks Don't Match the Task

**MIDI Clip Preview**: The task description and acceptance criteria specified miniature piano-roll preview rendering, display modes, and interaction. The planner decomposed it into generic clip lifecycle operations (naming, split, join, delete, creation, tests) — none of which addressed the actual feature.

**Root cause**: The planner latched onto keywords in the codebase (existing clip operations) rather than the task's acceptance criteria. There is no instruction telling the planner to verify coverage.

### FM-2: Missing Integration Wiring

**Automation Lanes**: All 11 subtasks were implemented and merged successfully. Every individual piece worked. But the feature was completely non-functional because two cross-cutting wiring calls (connecting VimContext state to widget rendering) were never planned as subtasks.

**Root cause**: The instruction "each subtask should be independently implementable" encourages isolated vertical slices. Nobody is responsible for the horizontal glue.

### FM-3: Replanning Blocked by Stale Subtasks

**MIDI Clip Preview (replanning)**: After the misaligned plan was rejected and the task reset to `backlog`, the planner saw 6 existing `done` subtasks and auto-advanced without generating a new plan. The old subtasks had to be manually detached.

**Root cause**: `onPlannerCompleted()` doesn't clear stale subtasks. The planner prompt doesn't mention how to handle pre-existing subtask state.

---

## Proposed Improvements

### P-1: Acceptance Criteria Cross-Reference (Prompt)

Add an explicit instruction requiring the planner to verify that every acceptance criterion is covered by at least one subtask, and that every subtask maps back to at least one criterion.

```markdown
## Coverage Verification

Before finalizing your plan, verify:
1. List every acceptance criterion from the task description
2. For each criterion, identify which subtask(s) address it
3. If any criterion is not covered, add a subtask for it
4. If any subtask doesn't map to a criterion, justify it or remove it

Include this mapping in your plan.json:

"coverage": {
  "criteria": [
    {
      "criterion": "Piano-roll preview renders in clip widget",
      "subtasks": [0, 2]
    }
  ]
}
```

This forces the planner to read the acceptance criteria carefully and makes coverage gaps visible during plan review.

### P-2: Mandatory Integration Subtask (Prompt)

Add an instruction that the final subtask in every plan must be an integration/wiring subtask that connects the pieces.

```markdown
## Integration Subtask

Your plan MUST include a final integration subtask that:
- Wires together the components built by other subtasks
- Verifies end-to-end functionality (not just unit tests)
- Has dependencies on ALL other implementation subtasks
- Touches the files that connect subsystems (registries, routers,
  factories, main entry points)

This subtask runs last, after all other agent work is merged.
If the feature is simple enough to not need integration wiring,
explicitly state why in the subtask description.
```

### P-3: Decomposition Anti-Patterns (Prompt)

Add concrete guidance on what NOT to do, based on observed failures.

```markdown
## Decomposition Rules

DO:
- Decompose along functional boundaries that minimize file overlap
- Make each subtask produce a testable, observable behavior change
- Include acceptance criteria from the parent task in subtask descriptions
- Prefer fewer, larger subtasks over many small ones (3-6 is typical)

DO NOT:
- Decompose by code layer (one subtask for models, one for handlers,
  one for UI) — this maximizes file overlap and merge conflicts
- Create subtasks for generic operations that already exist in the
  codebase — verify the operation doesn't exist before planning it
- Plan more than 8 subtasks — if you need more, the task should be
  split into multiple parent tasks
- Omit the files list — this is used for scheduling and must be accurate
```

### P-4: File Overlap Awareness (Prompt)

Tell the planner that file overlap has real consequences and should be minimized.

```markdown
## File Overlap

Subtasks that modify the same files CANNOT run in parallel — they will
be serialized, increasing total time. Minimize file overlap between
subtasks. If overlap is unavoidable, use the `dependencies` field to
specify the correct merge order.

When multiple subtasks must modify the same file (e.g., a registry or
router), prefer having ONE subtask own that file and other subtasks
depend on it, rather than having all subtasks append to it independently.
```

### P-5: Plan Validation in the Orchestrator (Code)

After the planner produces `plan.json`, validate it before transitioning to `plan_review`. Currently `onPlannerCompleted()` only checks that the JSON parses and has >0 subtasks.

Add validation for:

```go
// internal/orchestrator/plan_validation.go (new file)

type PlanValidationResult struct {
    Valid    bool
    Warnings []string  // shown during plan_review
    Errors   []string  // block transition to plan_review, trigger retry
}

func ValidatePlan(plan []planEntry, task *model.Task) PlanValidationResult {
    var result PlanValidationResult

    // 1. Subtask count bounds
    if len(plan) > 8 {
        result.Warnings = append(result.Warnings,
            fmt.Sprintf("Plan has %d subtasks (recommended max: 8)", len(plan)))
    }

    // 2. File lists present
    emptyFiles := 0
    for _, p := range plan {
        if len(p.Files) == 0 {
            emptyFiles++
        }
    }
    if emptyFiles > 0 {
        result.Warnings = append(result.Warnings,
            fmt.Sprintf("%d subtask(s) have no files listed — scheduling will be degraded", emptyFiles))
    }

    // 3. File overlap report
    overlaps := computeFileOverlaps(plan)
    for _, overlap := range overlaps {
        if !hasDependency(plan, overlap.SubtaskA, overlap.SubtaskB) {
            result.Warnings = append(result.Warnings,
                fmt.Sprintf("Subtasks %d and %d overlap on %s but have no dependency — they will be serialized",
                    overlap.SubtaskA, overlap.SubtaskB, strings.Join(overlap.Files, ", ")))
        }
    }

    // 4. Dependency cycle detection
    if hasCycle(plan) {
        result.Errors = append(result.Errors, "Dependency cycle detected")
    }

    // 5. Coverage mapping present (if acceptance criteria exist)
    // This check depends on P-1 being implemented in the prompt

    result.Valid = len(result.Errors) == 0
    return result
}
```

Validation warnings are surfaced in the TUI during `plan_review` so the human reviewer can make an informed decision. Validation errors trigger a planner retry with feedback.

### P-6: Replanning with Stale Subtask Cleanup (Code)

When a task transitions from `failed → backlog` for replanning and `plan_feedback` is set, automatically detach old subtasks.

```go
// internal/orchestrator/orchestrator.go — processBacklog() changes

func (o *Orchestrator) processBacklog(task *model.Task) {
    // If replanning with feedback, detach old subtasks first
    if task.PlanFeedback != "" {
        var oldSubtasks []model.Task
        o.db.Where("parent_task_id = ?", task.ID).Find(&oldSubtasks)
        for _, st := range oldSubtasks {
            st.ParentTaskID = nil
            st.Labels = append(st.Labels, "detached-from:"+task.ID.String())
            o.db.Save(&st)
        }
        o.logger.Info("detached old subtasks for replanning",
            "task_id", task.ID, "count", len(oldSubtasks))
    }

    // ... existing backlog→planning transition ...
}
```

### P-7: Test Subtask Ordering (Prompt + Code)

Tests should run after all implementation subtasks are merged, not in parallel with them.

**Prompt addition**:
```markdown
## Test Subtasks

If you include a testing subtask, it MUST:
- Depend on ALL implementation subtasks (list all indices in `dependencies`)
- Be the last subtask (or second-to-last, before integration)
- Have agent_type "coder" (not "researcher")

Ordering: implementation subtasks → test subtask → integration subtask
```

**Code enforcement**: In `ValidatePlan()`, detect subtasks with "test" in the title and warn if they don't depend on all other implementation subtasks.

---

## Impact on Merge Reliability PRD

These improvements directly support several items in the merge reliability PRD:

| Planner Improvement | PRD Section Supported |
|---------------------|----------------------|
| P-1 (Coverage verification) | Reduces scope drift → fewer replanning cycles |
| P-2 (Integration subtask) | Catches wiring gaps before user testing |
| P-3 (Anti-patterns) | Layer-based decomposition is the #1 cause of file overlap |
| P-4 (File overlap awareness) | Makes §4.1 (dependency-aware scheduling) more effective |
| P-5 (Plan validation) | Catches problems at plan time instead of merge time |
| P-6 (Replanning cleanup) | Fixes the stale subtask blocking issue |
| P-7 (Test ordering) | Prevents test agents from running against incomplete code |

---

## Prompt Delta

The full prompt change for `plannerInstructions()` adds ~40 lines to the current 6. The sections are:

1. **Existing**: Schema and basic rules (keep as-is)
2. **New §Coverage Verification** (P-1): ~10 lines
3. **New §Integration Subtask** (P-2): ~8 lines
4. **New §Decomposition Rules** (P-3): ~12 lines
5. **New §File Overlap** (P-4): ~6 lines
6. **New §Test Subtasks** (P-7): ~6 lines

Total prompt grows from ~30 lines to ~72 lines. This is well within typical prompt budgets and every line addresses a specific observed failure.

---

## Open Questions

1. **Coverage mapping enforcement**: Should a missing `coverage` field in plan.json be a validation error (retry) or a warning (shown at review)? Starting with warning is safer — the planner may not produce valid JSON for a new field on first attempt.

2. **Subtask count cap**: The doc suggests 8 as a hard warning. Some features may legitimately need more. Should this be configurable per-project?

3. **Planner codebase access**: The planner currently runs in the feature worktree with full codebase access. It could be given read access to merged subtask diffs when replanning, so it understands what has already been done. This would help with partial replanning (only add subtasks for missing criteria).

4. **Plan diffing on replan**: When a task goes through `plan_review → planning` (rejection), the planner starts from scratch. Should the rejected plan be included in the prompt as context? This could help the planner iterate rather than reinvent.

5. **Automated acceptance criteria extraction**: Not all tasks have structured acceptance criteria. Should the planner be instructed to *extract* criteria from the task description as a first step, then decompose? This adds a validation step that could catch scope drift earlier.
