# P1/P2 Fix Plan: Infra State, Client, Eventbus, And Gitref Guards

Findings: `RACE-SM-016`, `RACE-SM-017`, `RACE-SM-018`, `RACE-SM-023`, `RACE-SM-024`, `RACE-SM-027`, `RACE-SM-032`, `RACE-SM-036`.

## Implemented Batch

- `RACE-SM-017`: make `gitref.EnsureBranch` race-idempotent by rechecking the target branch when create fails.
- `RACE-SM-027`: model `cancelled` as an explicit terminal state in `state.ValidTransitions` and test status-map coverage.
- `RACE-SM-032`: allow `orchclient` gate success responses with `204` or empty `200` bodies to return the zero DTO instead of surfacing JSON EOF.
- `RACE-SM-036`: make eventbus `Ack` preserve the first acknowledgement timestamp.

## Deferred Batch

- `RACE-SM-016`: enforce merge retry backoff with durable `next_retry_at`; requires merge dispatch clock injection and restart-safe persistence tests.
- `RACE-SM-018`: fail/cleanup gitref ownership registration failures after spawn; requires coordinated container destroy and worker identity rollback tests.
- `RACE-SM-023`: isolate websocket broadcast from slow clients; requires hub write-pump/interface refactor for deterministic blocked-client tests.
- `RACE-SM-024`: replace poll/deliver with atomic eventbus claim; requires watcher API migration so only the claimer runs side effects.

## Acceptance Tests

- `internal/gitref/git_test.go`: concurrent or simulated-create-race `EnsureBranch` calls both succeed when the branch now exists.
- `internal/state/machine_test.go`: every known task status has a state-machine entry; `cancelled` has no outgoing transitions.
- `pkg/orchclient/gate_test.go`: `204` and empty `200` success bodies do not fail decoding.
- `internal/eventbus/bus_test.go`: a second ack does not change `acked_at`.

## Target Files

- `internal/gitref/git.go`
- `internal/gitref/git_test.go`
- `internal/state/machine.go`
- `internal/state/machine_test.go`
- `pkg/orchclient/gate.go`
- `pkg/orchclient/gate_test.go`
- `internal/eventbus/eventbus.go`
- `internal/eventbus/bus_test.go`
