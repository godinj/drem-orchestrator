# Bug: Plan rejection does not trigger re-planning

## Symptom

When a task's plan is rejected (status set back to `planning` with `plan_feedback`), the orchestrator does not regenerate the plan. It immediately transitions the task back to `plan_review` with the same stale plan, ignoring the feedback.

## Root Cause

In `internal/orchestrator/task_processing.go`, `processPlanning()` (line ~57) checks:

```go
if task.Plan != nil {
    // transition to plan_review immediately
}
```

This short-circuits before checking whether `plan_feedback` is set. When a plan is rejected, the task goes back to `planning` with both `Plan` (the old plan) and `PlanFeedback` populated. On the next tick, `processPlanning` sees `Plan != nil` and immediately promotes to `plan_review` without invoking the planner agent.

## Contrast with processBacklog

`processBacklog()` (line ~25) already handles the re-plan case correctly — it checks `task.PlanFeedback != ""` and detaches old subtasks. But that logic only runs in the `backlog` → `planning` transition. Once the task is in `planning`, `processPlanning` never considers feedback.

## Fix

`processPlanning()` should check if `plan_feedback` is set. If so, it should clear the existing `Plan` (set to nil) and spawn a new planner agent, passing the feedback so the planner can address it. Something like:

```go
func (o *Orchestrator) processPlanning(task *model.Task) error {
    // If plan exists BUT feedback requests a re-plan, clear the stale plan
    // and fall through to spawn a new planner agent.
    if task.Plan != nil && task.PlanFeedback != "" {
        task.Plan = nil
        // save task so the cleared plan persists
    }

    // existing logic: if plan exists (and no feedback), transition to plan_review
    if task.Plan != nil {
        // ... existing code ...
    }

    // ... rest of existing code (check agent, spawn planner) ...
}
```

The planner agent prompt should include `plan_feedback` so it knows what to change.

## Files to examine

- `internal/orchestrator/task_processing.go` — `processPlanning()` and `processBacklog()`
- `internal/prompt/planner.go` (or wherever the planner agent prompt is built) — ensure feedback is passed to the planner
- `internal/orchestrator/handlers.go` — check how plan rejection sets state (the TUI `handleReject` path)

## Test plan

1. Write a test where a task has both `Plan` (non-nil) and `PlanFeedback` (non-empty) in `planning` state
2. Call `processPlanning()` and assert it does NOT transition to `plan_review`
3. Assert it clears the plan and spawns a planner agent (or equivalent)
