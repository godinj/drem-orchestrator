# P0 Fix Plan: Docker Event Attempt Scoping And Role-Preserving Respawn

Findings: `RACE-SM-002`, `RACE-SM-003`.

## Summary

Fix Docker death handling by making events attempt-scoped rather than task-scoped:

- Match death events to `model.WorkerAttempt` by `task_id + container_id`, falling back to `task_id + drem.worker_id`.
- Mutate only the current assigned attempt. Stale old-container events are audited but do not clear current assignment or respawn.
- Respawn the same role as the dead attempt instead of hard-coded `coder`.
- Treat unmatched death events as non-mutating until a stronger policy exists.

## Current Evidence

- `internal/orchestrator/docker_events.go`
  - `dispatchEvent` extracts only `drem.task_id`.
  - `handleWorkerDeath` calls `markAssignedWorkerDead(task, ev, now)` and then `spawnCoder`.
  - `markAssignedWorkerDead` loads `task.AssignedAgentID`, so a delayed old event can kill the currently assigned replacement.
- `internal/workeridentity/workeridentity.go`
  - `RecordSpawn` creates `WorkerAttempt` rows with task, agent, worker, container, and agent type data.
- Spawner labels include `drem.worker_id`, `drem.agent_type`, `drem.project_id`, and caller-provided `drem.task_id`.

## Proposed Changes

### Add worker attempt lookup for death events

In `internal/orchestrator/docker_events.go`:

```go
func (o *Orchestrator) workerAttemptForDeathEvent(taskID uuid.UUID, ev container.Event) (*model.WorkerAttempt, bool) {
    var attempt model.WorkerAttempt
    if ev.ContainerID != "" {
        err := o.db.Where("task_id = ? AND container_id = ?", taskID, ev.ContainerID).
            Order("created_at DESC").First(&attempt).Error
        if err == nil { return &attempt, true }
    }
    if workerID := ev.Labels["drem.worker_id"]; workerID != "" {
        err := o.db.Where("task_id = ? AND worker_id = ?", taskID, workerID).
            Order("created_at DESC").First(&attempt).Error
        if err == nil { return &attempt, true }
    }
    return nil, false
}
```

### Ignore stale death events before mutation

```go
func currentAssignedAttempt(task *model.Task, attempt *model.WorkerAttempt) bool {
    if task == nil || attempt == nil || task.AssignedAgentID == nil || attempt.AgentID == nil {
        return false
    }
    return *task.AssignedAgentID == *attempt.AgentID
}
```

In `dispatchEvent`, after loading non-terminal task:

```go
attempt, ok := o.workerAttemptForDeathEvent(task.ID, ev)
if !ok {
    o.logger.Warn("docker death event has no matching worker attempt", ...)
    return
}
if !currentAssignedAttempt(&task, attempt) {
    o.logger.Info("ignoring stale docker death event for non-current attempt", ...)
    return
}
o.handleWorkerDeath(ctx, &task, attempt, ev, tracker)
```

### Make death handling role-aware

Change signature:

```go
func (o *Orchestrator) handleWorkerDeath(ctx context.Context, task *model.Task, attempt *model.WorkerAttempt, ev container.Event, tracker *replacementTracker)
```

Replace `spawnCoder` with:

```go
if err := o.respawnWorkerRole(ctx, task, attempt.AgentType); err != nil { ... }
```

Add:

```go
func (o *Orchestrator) respawnWorkerRole(ctx context.Context, task *model.Task, role string) error {
    switch role {
    case string(model.AgentCoder):
        return o.spawnCoder(ctx, task)
    case string(model.AgentReviewer):
        return o.spawnReviewer(ctx, task)
    case string(model.AgentFixer):
        return o.spawnFixer(ctx, task)
    case "supervisor":
        return o.spawnSupervisor(ctx, task)
    case "merger":
        return nil // leave merging task for merge retry/dispatch path
    default:
        return fmt.Errorf("unknown worker role %q for respawn", role)
    }
}
```

### Mark only matched attempt dead

Replace `markAssignedWorkerDead` with `markWorkerAttemptDead(task, attempt, ev, now)`:

- Require `attempt.AgentID != nil`.
- Require `task.AssignedAgentID == attempt.AgentID`.
- Load agent by `attempt.AgentID`.
- If `ev.ContainerID` and `ag.TmuxSession` are both set, require they match.
- Mark agent dead, clear `CurrentTaskID`, set exit metadata.
- Clear `task.AssignedAgentID` only when it still equals attempt agent ID.

### Improve lifecycle event details

Add labels to `recordContainerLifecycleEvent` details:

```go
detail["worker_id"] = ev.Labels["drem.worker_id"]
detail["agent_type"] = ev.Labels["drem.agent_type"]
```

## Tests

Add to `internal/orchestrator/docker_events_test.go`:

### Stale die does not kill current assigned worker

- Seed task assigned to current agent/container.
- Seed old worker attempt for old container.
- Deliver old container `die` event.
- Assert:
  - no spawn calls,
  - current assignment remains,
  - current agent remains working.

### Reviewer death respawns reviewer

- Seed current reviewer attempt/container.
- Deliver reviewer `die` event.
- Assert fake spawner sees `AgentType == reviewer`, not coder.

### Fixer death respawns fixer despite `in_progress` status

- Same as reviewer, but `AgentType == fixer` and task status `in_progress`.
- This guards against status-only role mapping.

### Unmatched death audits but does not respawn or clear assignment

- Seed current assigned worker.
- Deliver death for unknown container/worker ID.
- Assert assignment remains and no spawn happens.

### Update existing Docker event tests

Existing OOM/replacement-cap tests must seed a matching assigned agent and worker attempt before dispatching death events.

## Conflict Notes

- `handleWorkerDeath` signature changes should be localized to `docker_events.go` and tests.
- Do not reuse `respawnForTask` from container reconciliation for Docker events; it maps by task status and cannot distinguish coder/fixer in `in_progress`.
- Merger death is intentionally conservative: mark/clear only the current matched attempt, then leave redispatch to merge retry flow unless product policy says otherwise.
- This plan depends on reliable `WorkerAttempt` rows; it pairs with the spawn identity fix plan.

## Open Questions

- Should unmatched death events ever fail the task when no current assignment exists?
- Should `drem.agent_type` labels be trusted as fallback when no attempt exists? Current recommendation: no.
- Should `recordContainerLifecycleEvent` include `attempt_id` after lookup? Helpful, but optional.
