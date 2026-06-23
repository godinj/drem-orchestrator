# P1/P2 Fix Plan: Gate And Recovery Race Follow-Ups

Findings: `RACE-SM-008`, `RACE-SM-010`, `RACE-SM-011`, `RACE-SM-012`, `RACE-SM-015`, `RACE-SM-041`.

## Implemented Batch

- `RACE-SM-041`: align the `POST /fail` contract tests with production behavior. Production sends a failed test-writing result back to implementation, so tests should assert `testing_ready -> in_progress`, not terminal `failed`.

## Deferred Batch

- `RACE-SM-008`: stale gate race mapping should use one shared typed stale-transition error across gate handlers and orchestrator methods.
- `RACE-SM-010`: archive live-assignment TOCTOU needs a conditional task/assignment update with agent status and heartbeat predicates.
- `RACE-SM-011`: stale-assignment recovery TOCTOU needs classify-and-apply in one guarded transaction over the same assignment snapshot.
- `RACE-SM-012`: heartbeat ingest must update `Agent.HeartbeatAt` only for the current assigned attempt; requires end-to-end ingest plus recovery tests.
- `RACE-SM-015`: stuck-agent reconciliation must treat `ListWorkers` errors as unknown runtime liveness, not death evidence; requires spawner/runtime status tests.

## Acceptance Tests

- `internal/orchhttp/gate_handlers_test.go`: fail-handler fake contract matches production `in_progress` result.
- Deferred acceptance tests remain the source-review tests: stale gate errors return `409`, archive refuses newly live work, stale recovery refuses changed assignments, heartbeat ingest advances liveness, and list-worker errors do not false-kill seen containers.

## Target Files

- `internal/orchhttp/gate_handlers.go`
- `internal/orchhttp/gate_handlers_test.go`
- `internal/orchhttp/health_recovery.go`
- `internal/orchhttp/health_recovery_test.go`
- `internal/orchhttp/handlers_internal.go`
- `internal/orchestrator/reconcile_stuck.go`
