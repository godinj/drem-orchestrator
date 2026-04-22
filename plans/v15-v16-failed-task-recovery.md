# v15/v16 failed-task recovery: `drem cli retry` verb

## Origin

2026-04-22 canary regression. Two synthetic canaries — v15
(`3ddba802-3326-4ca3-8992-5744e87dc4c1`, "Add CanaryV15Marker type")
and v16 (`4e1318b3-f579-47f9-8043-574e5990b999`, "Add CanaryV16Marker
type") — stalled at `status=failed` after a fixer-spawn bug that has
since been closed. Their IMPL subtasks
(`8a7616f1-49c2-4622-8f07-b8b423273ee7` for v15,
`724bed41-5037-42d1-9376-4d184d23a380` for v16) were the actual stuck
rows; TESTS subtasks were already `done`. Without a CLI surface the
only way to unstick them was the TUI's retry action, which doesn't
work against a headless containerized orch.

## Why the verb is `retry` and not `reset`

The Orchestrator already exposes `RetryTask(uuid.UUID) error` at
`internal/orchestrator/task_api.go:116`. That helper:

- Guards `task.Status == StatusFailed` (409 from HTTP handler otherwise).
- Clears `retry_count`, `last_error`, `failure_diagnosis`,
  `failure_category`, `prompt_adjustment`, `empty_work`,
  `constraint_violations` from task.Context.
- Unlinks any agent whose `current_task_id` still points at the task;
  flips `AgentDead → AgentIdle` in passing. Clears
  `task.AssignedAgentID`.
- Transitions `failed → backlog` via `state.TransitionTask`, so the
  state machine is respected and a `task_events` row lands with
  `actor="user"`, `details={"action":"retry"}`.
- Emits `task_updated` + `publishTaskTransition` so SSE listeners and
  the reconciler see the change immediately.

Naming the HTTP verb `retry` matches the existing helper — no
translation layer, no surprise for readers who already know
`RetryTask`. `reset` would have implied wider state clearing than
the helper actually performs.

## State-machine semantics

- `failed → backlog` via `RetryTask`.
- The scheduler's next tick auto-dispatches a fresh worker against the
  backlog row — no extra orchestrator call is needed.
- `internal/orchestrator/reconcile_parents.go` auto-clears a
  parent whose subtasks all reach `done`, so retrying the IMPL subtask
  is usually sufficient; the parent transitions itself.

## Subtask vs parent retry

Retrying an IMPL subtask (`8a7616f1…` / `724bed41…`) is what
re-dispatches the fixer. The parent (`3ddba802…` / `4e1318b3…`) then
advances automatically via `reconcile_parents.go` once the subtask
reaches `done`. If for any reason the parent stays at `failed` — for
example the reconciler is paused, or the parent failed for a reason
independent of its subtask — the parent itself is a legal retry target.
The status-guard only requires `task.Status == StatusFailed`; the
state machine doesn't care whether the row is a parent or leaf.

## Commits (five, TDD-ordered)

1. `test(orchhttp)`: RED regressions for `POST /tasks/{id}/retry`
   (happy, 404, 409, 400, 500, 503).
2. `feat(orchhttp)`: `handleRetryTask` + route wiring +
   `GateOrchestrator.RetryTask` interface extension.
3. `feat(orchclient)`: `Client.Retry(ctx, project, id)` + four
   typed-error regressions.
4. `feat(cli)`: `drem cli retry <id>` + four CLI-level regressions +
   help-string update.
5. `docs(plans)`: this file.

## Dogfood results

v15 recovery, live orch (image `5be243dd1e78`, redeployed post-merge):

1. `drem cli retry 8a7616f1` → `backlog`. Orch logs showed spawn
   activity within the next scheduler tick.
2. Worker completed the impl; subtask transitioned `backlog → in_progress
   → done`.
3. `reconcile_parents.go` auto-advanced parent `3ddba802…` to its
   normal next state without operator intervention.

v16 recovery: skipped (operator's call — v15 demonstrated the full
loop, re-proof would add no information).

## Open items

- Future work could add `drem cli retry --cascade <parent-id>` that
  retries a parent plus all its `failed` subtasks in one call. Useful
  when a coordinated multi-row failure needs a single kick rather than
  N separate retries. Out of scope here; noted for the backlog.
- `drem cli retry` today requires the task to already be at
  `status=failed`. A `--force` flag that also accepts stuck
  `in_progress` tasks (paired with a deliberate agent kill) is a
  separate feature and would bypass the state-machine guard — defer.
