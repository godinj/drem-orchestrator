# Phase 3.5b: Quickfix dispatch migration

**Status**: implemented, 2026-04-20 (commit `46ab0e2`; plan-doc commit
tracked separately).

## Problem

The T3 canary regressed back to

    exec: "claude": executable file not found in $PATH

because the classifier picked `quickfix` for the canary task, and the
quickfix flow still shelled out through `o.runner.SpawnAgent`. Inside
the orch container there is no `claude` CLI on `$PATH` — claude runs
inside per-task worker containers (`drem-worker-<lang>`) that
bind-mount the operator's `~/.claude/.credentials.json`. So any
quickfix classification fails instantly, bounces to FAILED, and
unblocks the rest of the pipeline only when a human reclassifies the
task out of the quickfix lane.

Phase 3.5 (`plans/phase-3.5-subtask-dispatch-migration.md`, commits
`d8ae751..35da1f3`) migrated subtask / reviewer / fixer /
test-failure-fixer dispatch paths to the container spawner. It
explicitly deferred quickfix to this follow-up (Q3 in that doc).

## Affected sites

Two sites in `internal/orchestrator/quickfix_processing.go`:

1. **`processQuickFix` line 103** — initial quickfix dispatch
   (BACKLOG → IN_PROGRESS). Called from `doTick` when a fresh quickfix
   task lands in BACKLOG.
2. **`respawnQuickFixAgent` line 150** — empty-work retry path. Called
   from the IN_PROGRESS handler when `onAgentEmptyWork` clears the
   assigned agent and sets `empty_work=true`.

Both produce coder workers (quickfix is a coder-only flow; no planner,
no tester, no reviewer).

## Migration pattern

Follows the same recipe established by Phase 3.5
(`subtask_scheduling.go`, `session_spawning.go`, `test_execution.go`):

1. Add `if o.Spawner != nil` guard before the legacy call; dispatch
   via `o.spawnCoder(ctx, task)`. Context is
   `context.Background()` — no ctx in scope at these call sites,
   matching the other migration sites.
2. `spawnCoder` handles prompt rendering, creds mount, branch-ensure,
   audit events (`worker_spawned` / `worker_spawn_failed`), and
   Agent-row recording via `recordContainerOnAgent`. Caller does not
   re-render the prompt or duplicate any of that bookkeeping.
3. After `spawnCoder` returns, reload the task so `AssignedAgentID`
   (written by `recordContainerOnAgent`) is visible to the rest of
   the quickfix flow. Fail the task if the assignment is missing —
   mirrors `dispatchSubtaskViaSpawner`'s invariant.
4. Emit `quickfix_started` / `quickfix_retry` + `publishTaskTransition`
   + `publishAgentStatus` using the reloaded `task.AssignedAgentID`;
   there's no local `ag` in the container path.
5. State-machine transitions (`state.TransitionTask` BACKLOG →
   IN_PROGRESS in `processQuickFix`) stay in the caller because
   `spawnCoder` is a spawn primitive, not a state-machine driver.
6. `delete(task.Context, "empty_work")` in `respawnQuickFixAgent` runs
   in both paths — it's state-machine cleanup, not a spawn concern.
7. Legacy `runner.SpawnAgent` call stays as a fallback when
   `o.Spawner == nil` (host dev without a spawner socket).

### Capacity-gate semantics

The existing gate was `if o.runner == nil || !o.runner.CanSpawn() {
return nil }`. The migration narrows this to short-circuit only when
**both** surfaces are absent:

    if o.Spawner == nil && (o.runner == nil || !o.runner.CanSpawn()) {
        return nil
    }

Rationale matches the Phase 3.5 subtask-scheduling decision (§Q4):
per-container scheduling lives in docker, not in the in-process
subprocess limiter. Container mode ignores `CanSpawn`; legacy mode
keeps the gate so host-dev backpressures as before.

## Test strategy

Existing `quickfix_test.go` (705 lines, 14 tests) exercises the
legacy runner path with `o.Spawner == nil` and `o.runner` wired with
`maxConcurrent=0`. All 14 stay green unchanged — the migration only
adds a container branch above the pre-existing short-circuit.

New tests live in `internal/orchestrator/quickfix_container_test.go`
(new file rather than extending `quickfix_test.go` to stay under the
800-line ceiling on `quickfix_test.go`). Three tests:

1. **`TestProcessQuickFix_DispatchesViaSpawner`** — primary acceptance
   test. Wires a `fakeWorkerSpawner` (mirroring
   `workerSpawnTestRig`), drives a BACKLOG quickfix task through
   `processQuickFix`, asserts `AgentType="coder"` on the spawn call,
   task transitions to IN_PROGRESS, Agent row carries the container
   ID in `TmuxSession`, `worker_spawned` audit event exists,
   `quickfix_started` event lands on the events channel.
2. **`TestProcessQuickFix_SpawnerFailurePropagates`** — spawner
   returns an error; `processQuickFix` returns the error with
   `docker daemon refused` in the message. A `worker_spawn_failed`
   audit row is written by `spawnTypedWorker`'s internal path.
3. **`TestRespawnQuickFixAgent_DispatchesViaSpawner`** — retry path:
   task already in IN_PROGRESS with `empty_work=true` and
   `prompt_adjustment` in context. After `respawnQuickFixAgent`,
   `empty_work` is cleared, `prompt_adjustment` survives, Agent row
   carries the container ID, `quickfix_retry` event fires.

All three reuse `fakeWorkerSpawner` + `spawnOutcome` from
`worker_spawn_test.go`. The new helper `newContainerQuickFixRig`
mirrors `newContainerSubtaskRig` (real bare repo for
`gitref.EnsureBranch`; `o.runner == nil` so any stray legacy path
would panic).

An explicit "fallback to runner" test was considered and rejected:
the existing `TestProcessQuickFix_NoCapacity_ReturnsNil` and
`TestProcessQuickFix_RetryExhaustion` already exercise the
`o.Spawner == nil, o.runner wired` path (they stay green after the
migration, proving the fallback gate behaves as before).

## Remaining legacy-runner sites

After this migration, `rg 'runner.SpawnAgent(InWorktree)?' internal/orchestrator/`
returns:

| Site | Status | Reason |
|------|--------|--------|
| `subtask_scheduling.go:301` | legacy fallback | Phase 3.5 migrated primary; host-dev fallback retained. |
| `session_spawning.go:142` (reviewer) | legacy fallback | Phase 3.5 migrated primary. |
| `session_spawning.go:326` (fixer) | legacy fallback | Phase 3.5 migrated primary. |
| `test_execution.go:212` | legacy fallback | Phase 3.5 migrated primary. |
| `quickfix_processing.go:117` (renumbered from :103) | legacy fallback | **This plan** migrated primary. |
| `quickfix_processing.go:213` (renumbered from :150) | legacy fallback | **This plan** migrated primary. |
| `task_processing.go:248` (planner fallback) | retained | Warm planner (`cmd/drem-planner`) handles prod. |
| `classifying.go:90` (classifier fallback) | retained | Warm classifier handles prod. |
| `task_prep.go:138` (prep agent) | punted | Phase 3.5 §Q1 flagged this — the prep-agent ingestion path needs refactoring before dispatch can migrate. |
| `context_monitor.go:237` (`spawnFixerForTestFailure`) | **deferred** | See below. |

### context_monitor.go:237 deferral

`spawnFixerForTestFailure` is called from `checkContextUsage` when an
implementation agent crosses `contextFixerPct` (85%). In container
mode, the check itself fires against supervisor data that depends on
host-side tmux file watching — a path that does not exist for
container agents. The upstream `runner.StopAgent(ag.ID)` call on line
116 would already panic/error for container-backed agents before
reaching the spawn call.

The surrounding `checkContextUsage` flow needs its own
container-awareness pass (mirroring the `reconcileStuckAgents`
container-aware work that just landed in commits `ec2135a` /
`924b751`) before a dispatch migration makes sense. Migrating just the
spawn call here leaves the caller broken for container agents.

Decision: **deferred.** Not in scope for this session. When the
container-awareness pass lands on `checkContextUsage`, migrating the
spawn call is a 10-minute follow-up that uses the same pattern as
`processTestingReady` in `test_execution.go` — `o.spawnFixer` under
`o.Spawner != nil` guard, preserve the prior AssignedAgentID clear
(because `recordContainerOnAgent` updates rather than replaces).

## Containerization acceptance-criteria impact

`plans/containerization.md` Phase 2 "Acceptance criteria":

- Line 137 `[x] Coder subtask dispatch routes through the container
  spawner` — **stays ticked.** Quickfix is distinct from the
  subtask-dispatch acceptance bullet, so this tick is untouched.
- Line 138 `[x] Reviewer-session, fixer-session, and test-failure
  fixer re-dispatch all route through the container spawner when
  wired` — **stays ticked.** This plan is orthogonal.

No existing bullet captures quickfix specifically; the
containerization plan's Phase 2 was drafted around the coder-subtask
and reviewer/fixer sessions that dominated canary traffic. The
quickfix lane is an auxiliary path that Phase 3.5 explicitly punted.

**Update decision**: the closest bullet is line 137 (coder dispatch).
Amend that bullet to name both dispatch primitives (subtask +
quickfix) so the single tick reflects the complete coder-side
migration, rather than adding a new bullet that splits the phase-2
acceptance surface.

## Commits

1. `46ab0e2` — `feat(orch): route quickfix dispatch through container
   spawner` (migration code + tests).
2. (this commit) — `docs(plans): phase 3.5b quickfix migration plan`
   (plan doc + `plans/containerization.md` line-137 amendment).

Both commits green on `go vet ./...` and `go test -count=1 ./...`
(full suite 53s, orchestrator package 47s — within noise of the
baseline documented in Phase 3.5's post-merge checklist).

## References

- `plans/phase-3.5-subtask-dispatch-migration.md` — parent plan,
  §"Q3: Follow-up migrations not in this session's scope" listed
  quickfix as the Phase 3.5b target.
- `internal/orchestrator/quickfix_processing.go` — target.
- `internal/orchestrator/subtask_scheduling.go:371-430` —
  `dispatchSubtaskViaSpawner` pattern that `dispatchQuickFixViaSpawner`
  mirrors.
- `internal/orchestrator/worker_spawn.go` — `spawnCoder` /
  `spawnTypedWorker` / `recordContainerOnAgent` that the migration
  consumes.
