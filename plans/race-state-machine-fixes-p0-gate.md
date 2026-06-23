# P0 Fix Plan: Gate Approval Exactly-Once Semantics

Findings: `RACE-SM-001`, related to `RACE-SM-008` and `RACE-SM-009`.

## Summary

Fix double plan approval by making `HandlePlanApproved` exactly-once:

- Atomically claim `plan_review` with compare-and-swap: `UPDATE tasks SET ... WHERE id = ? AND status = plan_review`.
- Materialize subtasks, update parent context/TDD metadata, and create the status event in the same DB transaction.
- Return a typed stale-state error for the concurrent loser; HTTP maps that to `409` rather than `500`.
- Move non-transactional side effects, especially `plan.json` write and pubsub emits, after commit.

## Current Evidence

- `internal/orchhttp/gate_handlers.go`
  - `handleApproveTask` loads task status before delegating.
  - Delegated errors are mapped to `500`.
- `internal/orchestrator/handlers.go`
  - `HandlePlanApproved` loads task, checks `StatusPlanReview`, materializes subtasks, and only later saves parent status/event.
  - `materializeSubtasks` uses `o.db` directly, so subtask creation is not transaction-bound to the parent transition.
- `internal/state/machine.go`
  - `TransitionTask` mutates memory and returns an event; persistence is caller-owned.

## Proposed Changes

### Add typed stale-transition error

Add to `internal/state/errors.go` or `internal/state/machine.go`:

```go
var ErrStaleTransition = errors.New("stale task state")
```

Wrap expected-state failures:

```go
return fmt.Errorf("%w: task %s is in %s, expected %s",
    state.ErrStaleTransition, taskID, task.Status, model.StatusPlanReview)
```

### Make subtask materialization transaction-aware

In `internal/orchestrator/handlers.go`:

```go
func (o *Orchestrator) materializeSubtasks(task *model.Task) (*parsePlanResult, []uuid.UUID, error) {
    return o.materializeSubtasksWithDB(o.db, task)
}

func (o *Orchestrator) materializeSubtasksWithDB(db *gorm.DB, task *model.Task) (*parsePlanResult, []uuid.UUID, error) {
    // existing body with o.db replaced by db
}
```

Avoid calling side-effecting helpers like `DeleteSubtask` from the transaction-bound helper.

### Rewrite `HandlePlanApproved` as CAS transaction

Expected shape:

```go
func (o *Orchestrator) HandlePlanApproved(taskID uuid.UUID) error {
    var committedTask model.Task
    var committedEvent *model.TaskEvent

    err := o.db.Transaction(func(tx *gorm.DB) error {
        var task model.Task
        if err := tx.First(&task, "id = ?", taskID).Error; err != nil {
            return fmt.Errorf("handle plan approved: load task: %w", err)
        }
        if task.Status != model.StatusPlanReview {
            return fmt.Errorf("%w: task %s is in %s, expected plan_review",
                state.ErrStaleTransition, taskID, task.Status)
        }

        targetStatus := chooseApprovedPlanTargetStatus(task.Plan)
        evt, err := state.TransitionTask(&task, targetStatus, "user", map[string]any{"action": "plan_approved"})
        if err != nil {
            return err
        }

        res := tx.Model(&model.Task{}).
            Where("id = ? AND status = ?", task.ID, model.StatusPlanReview).
            Updates(map[string]any{
                "status": task.Status,
                "updated_at": task.UpdatedAt,
                "assigned_agent_id": nil,
            })
        if res.Error != nil {
            return res.Error
        }
        if res.RowsAffected != 1 {
            return fmt.Errorf("%w: task %s approval already claimed", state.ErrStaleTransition, task.ID)
        }

        if _, _, err := o.materializeSubtasksWithDB(tx, &task); err != nil {
            return err
        }
        if err := tx.Create(evt).Error; err != nil {
            return err
        }
        committedEvent = evt
        return tx.First(&committedTask, "id = ?", task.ID).Error
    })
    if err != nil {
        return err
    }

    o.writeApprovedPlanJSON(&committedTask) // best effort after commit
    o.emit("task_updated", &committedTask)
    o.publishTaskTransition(committedTask.ID.String(), committedEvent.OldValue, committedEvent.NewValue, "plan approved")
    return nil
}
```

### Map stale approval errors to 409

In `internal/orchhttp/gate_handlers.go`:

```go
if err != nil {
    slog.Error("orchhttp: approve failed", "task_id", task.ID, "err", err)
    if errors.Is(err, state.ErrStaleTransition) {
        writeJSONError(w, http.StatusConflict, err.Error())
        return
    }
    writeJSONError(w, http.StatusInternalServerError, "internal: "+err.Error())
    return
}
```

## Tests

### Concurrent double approve creates subtasks once

Add to `internal/orchestrator/gate_approval_test.go`:

- Create a `plan_review` task with at least two subtasks in `Plan`.
- Run two goroutines calling `o.HandlePlanApproved(taskID)`.
- Assert:
  - one call succeeds,
  - one returns `errors.Is(err, state.ErrStaleTransition)`,
  - child count equals plan subtask count,
  - exactly one status-change event from `plan_review`,
  - parent status is expected target, e.g. `test_writing` when plan includes a test phase.

Use file-backed WAL DB if shared-memory SQLite hides the race.

### HTTP stale approval returns 409

Add to `internal/orchhttp/gate_handlers_test.go`:

- Fake orchestrator returns `fmt.Errorf("%w: already approved", state.ErrStaleTransition)`.
- POST approve against a `plan_review` task.
- Assert `409`, not `500`.

### Preserve non-stale errors

Keep or add test that generic orchestrator errors still return `500`.

## Conflict Notes

- This introduces `state.ErrStaleTransition`, which should be reused by broader gate error mapping in `RACE-SM-008`.
- This partially addresses `RACE-SM-009`, but only for plan approval. Do not expand the patch to every gate mutation yet.
- `materializeSubtasks` is used by defensive auto-materialization paths; preserve the existing wrapper.
- Filesystem writes cannot be rolled back; keep them post-commit and best-effort.

## Open Questions

- Should repeated approve ever be idempotent success if child set already matches? Current acceptance target says conflict.
- Should child uniqueness constraints be added as a second line of defense? Not required if approval claim is CAS-protected, but valuable later.
