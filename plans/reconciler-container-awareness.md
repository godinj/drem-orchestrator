# Reconciler container-awareness

Teach `reconcileStuckAgents` that container-mode workers are not registered
with the legacy runner, so presence in the spawner's `ListWorkers` result —
not `runner.GetRunningAgents()` — is the authoritative liveness signal for
that class of agent.

## Background

After T3's subtask-branch-provisioning fix (commits `c0956b3..2a246e5`) the
worker container spawns cleanly, clones the agent branch, starts the
watchdog, and execs `claude`. But the v6 canary observed an infinite
respawn loop: orch's stuck-agent sweeper treated every container worker as
dead ~60 s after spawn because the sweeper decides liveness by checking
`o.runner.GetRunningAgents()` — a map populated only by the host-spawn
path. Container workers are dispatched through `o.Spawner.SpawnWorker`
and never land in that map, so:

1. Every tick past the `agentSpawnGracePeriod` (60 s), the sweeper flags
   the container agent as dead.
2. The agent row transitions to `status=dead`, `current_task_id=NULL`.
3. `task.Context["retry_count"]` is bumped.
4. Under `MaxEmptyWorkRetries`, the task resets to backlog (or keeps its
   pre-dispatch status) and is picked up by dispatch on the next tick.
5. A fresh container (`nifty_cray`) is spawned against the same subtask
   branch while the original (`focused_wiles`) is still healthy and
   working, creating a multi-writer situation the system is not designed
   for.

## Fix

Spawner-poll approach. Every `reconcileStuckAgents` tick, also materialize
the set of container IDs the spawner reports as `Status == "running"` for
this project. An agent whose `TmuxSession` (repurposed as the container
handle in container mode — see `recordContainerOnAgent`) is in that set
is not stuck.

Concretely, in `internal/orchestrator/reconcile.go::reconcileStuckAgents`:

1. After building the legacy runner `runningSet`, build a
   `containerRunningSet := o.buildContainerRunningSet(ctx)` keyed by
   container ID. The helper calls
   `o.Spawner.ListWorkers(ctx, spawner.ListWorkersParams{Project: o.projectID.String()})`
   and keeps only workers with `Status == "running"`.
2. On spawner RPC error, log a Warn and return an empty set. The
   reconciler then behaves exactly as pre-fix code did for that tick
   (legacy agents still work, container agents may be flagged dead).
   This is a deliberate choice: a transient spawner outage must not
   become newly catastrophic for host-mode agents that genuinely are
   stuck.
3. In the per-task loop, after the existing
   `if runningSet[ag.ID] { continue }` gate, add:
   `if ag.TmuxSession != "" && containerRunningSet[ag.TmuxSession] { continue }`
   A Debug log line fires on skip so operators can diagnose without
   polluting the default log stream (it fires every tick for every live
   worker).
4. Gate the legacy `o.runner.GetRunningAgents()` call on `o.runner != nil`.
   The container-only test rig has no runner; without this gate the
   sweeper would panic the moment it ran against a container-mode orch.

## recoverStuckAgents

The other sweep loop (`internal/orchestrator/orchestrator.go::recoverStuckAgents`)
uses a filesystem heuristic: `os.Stat(ag.WorktreePath + "/.claude/agent-idle")`.
For container-mode agents, `WorktreePath` is empty (see
`recordContainerOnAgent` — only `TmuxSession`, `ModelID`, `HeartbeatAt`
are populated for the synthetic agent), so the stat fails and the loop
already continues. So the sweep is already a functional no-op for
container workers.

Even so, we add a belt-and-braces skip at the top of the loop for any
agent whose `TmuxSession` looks like a container ID (i.e. not a legacy
tmux session name — the `isLegacyTmuxSession` helper decides). This
future-proofs the sweep against any caller that decides to set
`WorktreePath` on a container agent, and documents the intent.

## HTTP heartbeat endpoint — deliberately NOT added

The watchdog already emits `DREM-HEARTBEAT` lines to its own stdout from
`cmd/drem-watchdog/main.go`, but nothing relays those to orch. Adding an
HTTP heartbeat endpoint would require:

- designing the watchdog lifecycle (when to start/stop heartbeating, how
  to signal "agent done" vs "agent crashed"),
- opening a new ingress surface on orch with auth semantics,
- deciding how the endpoint interacts with the upcoming agentmon push
  path (`POST /internal/logs`),

…all of which are out of scope for a canary unblock. The spawner-poll
approach sidesteps the heartbeat question cleanly: orch already talks to
the spawner over the registry-synthesized ListWorkers RPC, so no new
network surface, no new auth boundary, no new code in the watchdog.

That said, a watchdog→orch HTTP heartbeat is still worth having as "Gap
N+1" because it would let orch detect a *non-exiting but silent* worker
(e.g. `claude` wedged on a long tool call that hasn't timed out). The
container-liveness check catches the crash-loop pattern but not the
silent-hang pattern. Filing that as follow-up work.

## Tests

Four new tests in `internal/orchestrator/reconcile_containers_test.go`
(same file as the other container-reconciler tests):

| Test | Setup | Assertion |
|---|---|---|
| `TestReconcileStuckAgents_ContainerAlive_Skips` | container in ListWorkers with `Status=running`; agent backdated past grace | fixes == 0, agent still `AgentWorking`, no `retry_count` written |
| `TestReconcileStuckAgents_ContainerExited_Kills` | container in ListWorkers with `Status=exited` | fixes == 1, agent `AgentDead`, task `retry_count=1` |
| `TestReconcileStuckAgents_ContainerMissing_Kills` | empty `Workers` slice | fixes == 1, agent `AgentDead` (container absent == not alive) |
| `TestReconcileStuckAgents_SpawnerListError_FallsBackToLegacy` | `fake.listErr` returns an RPC error | `reconcileStuckAgents` does not propagate error, behaves pre-fix (agent flagged) |
| `TestReconcileStuckAgents_LegacyRunnerPathIntact` | host-mode agent with empty `TmuxSession`, no runner, no container | fixes == 1, agent `AgentDead` (regression guard) |

All five tests use the existing `reconcileTestRig` fixture.
`fakeWorkerSpawner` already has `listResult` and `listErr` fields wired
into its `ListWorkers` implementation (added during phase-3.5
reconcileOnStartup work).

A small `backdateAgentPastGrace` test helper pushes `created_at` past
`2 * agentSpawnGracePeriod` so the sweep actually evaluates the agent
instead of swallowing it on the spawn-grace gate.

## Validation

- `go vet ./...` — clean.
- `go test -count=1 ./internal/orchestrator/...` — green, 42 s.
- `go test -count=1 ./...` — green. One package (`internal/agent`) has a
  pre-existing flake (`TestSpawnAgentInWorktree_Subprocess`) under full-repo
  parallel load; passes deterministically on targeted re-run against both
  the fix branch and master. Not introduced by this change.

## Files touched

- `internal/orchestrator/reconcile.go` — container-running set helper,
  nil-runner guard, skip gate, Debug log line.
- `internal/orchestrator/orchestrator.go` — belt-and-braces container
  skip in `recoverStuckAgents`.
- `internal/orchestrator/reconcile_containers_test.go` — five new tests.

## Containerization plan

Does NOT tick a Phase 2 / Phase 3 acceptance bullet. Closest analog is
Phase 6's "A worker can crash mid-task and be respawned" (user story 19,
line 275), but that criterion specifically requires Docker-event
subscription wiring (`o.Runtime`) for crash detection — which is
explicitly out of scope here and remains unimplemented. The fix in this
plan is a prerequisite for that criterion (we must not false-positive
respawn before we can correctly true-positive respawn) but does not
satisfy it.
