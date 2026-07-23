# Agent: Orchestrator Reconciliation and Lifecycle Tests

You are working on the `master` branch of drem-orchestrator, a Go-based agent
orchestration system using GORM+SQLite, typed worker attempts, a spawner API,
and Git references.

Your task is to improve tests for the current recovery-only reconciliation
contract. Do not reintroduce reconciliation as an alternate workflow engine.

## Context

Read these before starting:

- `AGENTS.md` and `CLAUDE.md`
- `internal/orchestrator/reconcile.go`
- `internal/orchestrator/reconcile_containers.go`
- `internal/orchestrator/reconcile_stuck.go`
- `internal/orchestrator/reconcile_parents.go`
- `internal/orchestrator/docker_events.go`
- existing `reconcile*_test.go`, `docker_events_test.go`, and
  `standard_parent_failure_path_test.go` files
- `internal/model/models.go` and `internal/model/delivery.go`

## Contract to preserve

Normal task transitions come from typed worker attempts, exact-SHA gate
results, verification records, integration authorization, and merge evidence.
Agentmon `TaskEvent` rows are telemetry only. Reconciliation may repair a
missed durable edge or clean resources, but may not infer success or death from
Git topology, worker inventory absence, an idle file, a heartbeat, or logs.

Add or strengthen table-driven tests for these behaviors:

1. A terminal spawner observation is recorded before its task effect and
   finalizes the matching `WorkerAttempt` exactly once when replayed.
2. A zero-exit replay after the task effect but before attempt finalization
   closes the attempt without applying the task transition twice.
3. A worker missing from `ListWorkers` leaves its task, agent, and active
   attempt unchanged at startup and during periodic reconciliation.
4. A temporary spawner error is fail-closed and causes no retry or respawn.
5. Container-backed agents are ignored by legacy stale-agent recovery; a
   disappeared legacy host agent is routed through the ordinary typed failure
   handler and is never synthesized as success.
6. Orphaned task assignments are cleared only when neither the runner nor a
   typed active attempt owns the assignment.
7. Completed-parent reconciliation repairs only an `in_progress` parent whose
   durable children are all `done`; it never revives a failed parent.
8. Stale completed-child annotation and orphan worktree cleanup preserve
   terminal task state and never turn Git evidence into task success.
9. Merger attempts finalize from typed terminal status without depending on a
   `merge_result` telemetry event.
10. Duplicate Docker-event and polling observations cannot respawn, complete,
    or fail a task more than once.

## Test infrastructure

- Use `testutil.NewTestDB(t)` for isolated SQLite state.
- Use `testutil.SetupBareRepo(t)` only when real Git topology is part of the
  resource-cleanup behavior under test.
- Prefer the existing fake spawner and launch-service helpers.
- Assert persisted `WorkerAttempt`, `AttemptEvent`, task, agent, and event rows
  after each observation.
- Exercise external behavior rather than private helper call counts.
- Keep time and inventory inputs deterministic.

## Verification

```bash
go test ./internal/orchestrator -run '(DispatchEvent|Reconcile|WorkerAttempt)' -count=1
go test -race ./internal/orchestrator ./internal/orchhttp
go test ./...
```

Run `gofmt` on changed Go files and `git diff --check` before handoff.
