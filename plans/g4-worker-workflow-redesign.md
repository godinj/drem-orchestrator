# G4 Worker Workflow Redesign

Status: tracer-bullet implementation started

## Problem

G4 direct-tool workers can currently fail in ways that are hard to recover:
runaway token loops, stale attempts, branch contamination, weak terminal failure
classification, retry dead ends, and missing runtime-tool evidence.

## Target Architecture

- Treat `worker_attempts` as the authoritative execution lease and event anchor.
- Fence worker ownership with attempt metadata before workers are allowed to
  heartbeat, complete, or finalize.
- Keep worker traces and diagnostics out of git; reject repo-local artifacts at
  branch acceptance.
- Classify terminal direct-tool failures with structured stop reasons.
- Surface failed tasks with active/stale attempts in health checks with dry-run
  recovery recommendations.
- Classify missing merge/test-gate tools as runtime failures instead of generic
  test failures.
- Avoid test-writing deadlocks when future test subtasks depend on
  implementation subtasks.

## Implemented Tracer Bullets

- Added durable attempt metadata fields and an `attempt_events` table with query
  indexes.
- Projected lease, failure, usage, and artifact metadata in attempt DTOs.
- Added structured direct-tool stop reasons for `max_iterations`,
  `no_progress`, `max_tokens`, `timeout`, and `context_limit`.
- Hardened branch policy rejection for `agent-trace-*`,
  `agent-push-diagnostic.json`, `.drem/attempt-*`, plan artifacts, and
  high-risk credential/config paths.
- Added health issues for failed tasks with active attempts, stale active
  attempts, and dead assigned agents, including recommended dry-run recovery
  commands.
- Added test-gate failure classification for missing runtime tools.
- Updated parent readiness so backlog test subtasks blocked by future
  implementation dependencies do not keep a parent stuck in `test_writing`.
- Added worker-attempt lease owners, default lease expiry, owner-fenced lease
  renewal, and owner-fenced terminal attempt completion primitives in
  `workeridentity.Store`.

## Remaining Phases

1. Expose attempt lease renewal/completion through the worker runtime API and
   wire container/direct-tool heartbeats through it.
2. Move commit/push/finalization out of direct-tool workers into an orchestrator
   finalizer.
3. Persist direct-tool model/tool-call attempt events during execution.
4. Store traces and diagnostics in attempt artifact storage outside the worktree.
5. Add retry preflight/apply endpoints for stale attempts and contaminated
   branches.
6. Add runtime preflight before spawning workers and before merge gates.
7. Add checkpointing and hard cumulative token/tool budgets.
8. Extend dashboards and `dremctl` around attempt event/artifact inspection.

## Verification

- `go test ./...`
- `go vet ./...`
