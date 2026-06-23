# P0 Fix Plan: Spawn Reservation, Identity Finalization, And Double-Spawn Prevention

Findings: `RACE-SM-006`, `RACE-SM-007`.

## Summary

Make worker spawn a two-phase, DB-claimed operation:

1. Build spawn context and validate branch/policy preconditions.
2. Reserve a worker identity in DB and atomically claim `tasks.assigned_agent_id` only if currently `NULL`.
3. Call the external spawner.
4. Finalize the reservation with container ID.
5. If spawn or finalization fails, abort DB reservation; if container already exists and finalization fails, destroy it as compensation.

This prevents:

- live containers without durable DB identity,
- two concurrent spawn calls for the same task/role,
- silent continuation after identity recording failures.

## Current Evidence

- `internal/orchestrator/worker_spawn.go`
  - `spawnTypedWorker` calls `o.Spawner.SpawnWorker` before `workeridentity.RecordSpawn`.
  - `recordErr` is logged and the function continues.
- `internal/workeridentity/workeridentity.go`
  - `RecordSpawn` writes agent/task/attempt state through multiple operations.
  - No transaction and no compare-and-swap on `tasks.assigned_agent_id`.
- `internal/model/models.go`
  - `WorkerAttempt` currently lacks explicit active/reserved/running/completed lifecycle fields.
- `internal/spawner/methods.go`
  - `DestroyWorker` exists and can compensate for post-spawn finalize failures.

## Proposed Changes

## Add active attempt lifecycle fields

In `internal/model/models.go`, extend `WorkerAttempt`:

```go
type WorkerAttempt struct {
    // existing fields...
    State       string     `gorm:"not null;default:'reserved';index"`
    CompletedAt *time.Time `gorm:"index"`
}
```

Add constants:

```go
const (
    WorkerAttemptReserved = "reserved"
    WorkerAttemptRunning  = "running"
    WorkerAttemptFailed   = "failed"
)
```

Add unique partial index for active attempts:

```sql
CREATE UNIQUE INDEX IF NOT EXISTS idx_worker_attempt_active_task_role
ON worker_attempts(task_id, agent_type)
WHERE completed_at IS NULL;
```

If GORM tags cannot produce reliable SQLite DDL, add raw SQL in DB migration/setup and test helpers.

## Replace post-spawn recording with reserve/finalize APIs

In `internal/workeridentity/workeridentity.go`, add:

```go
type Reservation struct {
    TaskID uuid.UUID
    AgentID uuid.UUID
    AttemptID uuid.UUID
    WorkerID string
    AgentType string
    Branch string
    Image string
}

func (s *Store) ReserveSpawn(ctx context.Context, r SpawnRecord) (Reservation, error)
func (s *Store) FinalizeSpawn(ctx context.Context, res Reservation, containerID string) (Handle, error)
func (s *Store) AbortReservation(ctx context.Context, res Reservation, reason string) error
```

### Reserve strategy

Inside one transaction:

- Reload task.
- If `AssignedAgentID != nil`, return typed `ErrTaskAlreadyClaimed`.
- Create agent in working/reserved state with current task and branch metadata.
- Create `WorkerAttempt` with `State=reserved`, no container ID yet, `CompletedAt=nil`.
- Compare-and-swap task assignment:

```go
res := tx.Model(&model.Task{}).
    Where("id = ? AND assigned_agent_id IS NULL", task.ID).
    Update("assigned_agent_id", ag.ID)
if res.RowsAffected != 1 { return ErrTaskAlreadyClaimed }
```

### Finalize strategy

Inside one transaction:

- Load reserved agent and attempt.
- Set `Agent.TmuxSession = containerID`.
- Update attempt `container_id = containerID`, `state = running`, requiring `state = reserved`.
- Return handle.

### Abort strategy

Inside one transaction:

- Mark attempt `state=failed`, `completed_at=now`.
- Mark agent dead/completed.
- Clear `Task.AssignedAgentID` only if it still equals reservation agent ID.

Keep `RecordSpawn` as compatibility wrapper for merger or other paths that cannot immediately move to reserve/finalize.

## Update `spawnTypedWorker`

In `internal/orchestrator/worker_spawn.go`, replace order:

```go
res, spawnErr := o.Spawner.SpawnWorker(ctx, params)
handle, recordErr := workeridentity.NewStore(o.db).RecordSpawn(...)
```

with:

```go
store := workeridentity.NewStore(o.db)
reservation, reserveErr := store.ReserveSpawn(ctx, workeridentity.SpawnRecord{...})
if reserveErr != nil {
    o.recordSpawnFailureEventWithReason(task, agentType, "worker_already_active", reserveErr)
    return fmt.Errorf("spawn %s worker: reserve identity: %w", agentType, reserveErr)
}
task.AssignedAgentID = &reservation.AgentID

res, spawnErr := o.Spawner.SpawnWorker(ctx, params)
if spawnErr != nil {
    _ = store.AbortReservation(ctx, reservation, "spawn_failed")
    o.recordSpawnFailureEvent(task, agentType, spawnErr)
    return fmt.Errorf("spawn %s worker: %w", agentType, spawnErr)
}

handle, finalizeErr := store.FinalizeSpawn(ctx, reservation, res.ContainerID)
if finalizeErr != nil {
    destroyErr := o.Spawner.DestroyWorker(ctx, spawner.DestroyWorkerParams{ContainerID: res.ContainerID})
    _ = store.AbortReservation(ctx, reservation, "finalize_failed")
    o.recordSpawnFailureEventWithReason(task, agentType, "identity_finalize_failed", finalizeErr)
    if destroyErr != nil {
        return fmt.Errorf("spawn %s worker: finalize identity: %w; destroy %s failed: %v", agentType, finalizeErr, res.ContainerID, destroyErr)
    }
    return fmt.Errorf("spawn %s worker: finalize identity: %w", agentType, finalizeErr)
}
```

Run gitref registration and spawn event recording only after successful finalize.

## Tests

### Concurrent same-task spawn calls spawner once

Add to `internal/orchestrator/worker_spawn_test.go`:

- Create one in-progress task.
- Start two goroutines calling `o.spawnCoder`.
- Assert:
  - fake spawner called once,
  - one call succeeds and one returns already-claimed error,
  - only one active attempt exists,
  - task has one assigned agent.

Use file-backed WAL DB if needed for realistic writer concurrency.

### Finalize failure destroys spawned container

Add to `internal/orchestrator/worker_spawn_test.go`:

- Configure fake spawner to return container ID.
- Inject DB update failure during `FinalizeSpawn` using a test GORM callback or store hook.
- Assert:
  - error returned,
  - `DestroyWorker` called for the created container,
  - task assignment cleared,
  - attempt is failed/completed.

### Spawn RPC failure aborts reservation

- Existing spawn failure test should assert reservation cleanup:
  - no lingering assignment,
  - reserved attempt is failed/completed or removed,
  - no destroy call when no container exists.

### Workeridentity unit tests

Add to `internal/workeridentity/workeridentity_test.go`:

- `TestReserveSpawn_SecondReservationForSameTaskAndRoleFails`
- `TestReserveFinalizeSpawnCreatesRunningHandle`
- `TestWorkerAttempt_ActiveUniqueIndexPreventsDuplicateTaskRole`

### Update existing spawn tests

Adjust tests that currently assume identity is created only after spawner returns. With reservation, assignment exists before external spawn and attempt starts as `reserved`.

## Conflict Notes

- `RecordSpawn` is also used by `internal/orchestrator/merge_dispatch.go`. Keep compatibility initially; merger identity hardening is covered by the merge/reconcile plan.
- Gitref registration still happens after spawn/finalize. A stronger future patch can reserve gitref before spawn; do not mix that into this P0 unless necessary.
- Existing worker completion paths must eventually set `WorkerAttempt.CompletedAt`; otherwise active unique index can block legitimate later respawns. For this P0, ensure abort/failure paths set it and audit normal completion paths before enforcing the partial index globally.
- The Docker event fix depends on reliable `WorkerAttempt` rows, so this plan should land before or alongside Docker event attempt matching.

## Open Questions

- Should respawn be allowed while `AssignedAgentID` is still set, or must death handling clear it first?
- How should stale reserved attempts from process crash between reserve and spawn be reaped?
- Should gitref branch ownership become part of pre-spawn reservation?
- Should duplicate spawn errors be typed and treated as benign by schedulers?
