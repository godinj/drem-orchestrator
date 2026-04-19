# Agent: Orchestrator Integration (Spawner RPC + Docker Events)

You are working on the `master` branch of Drem Orchestrator, a Go-based multi-agent task orchestrator. Your task is Phase 3 integration work for the containerization initiative: replace the orchestrator's host-side worktree operations with spawner RPC calls and Docker event subscription. The task lifecycle state machine does NOT change; only the mechanism for spawning, observing, and replacing workers changes.

## Context

Read these specs before starting:

- `docs/prd-containerization.md` (sections: "Container filesystem model"; "Lifecycle and recovery"; "Orchestrator package" under "Modified modules"; user stories 19, 25, 49, 52, 53)
- `internal/orchestrator/orchestrator.go` (current main loop — preserve the state-machine semantics)
- `internal/orchestrator/task_processing.go` (current worktree-creation call sites you will rewire)
- `internal/orchestrator/reconcile.go` (reconcile-stale-subtasks logic — needs to consult the spawner's `ListWorkers` instead of the filesystem)
- `internal/orchestrator/session_spawning.go` (current spawn path for reviewer/fixer/supervisor — also rewired through the spawner)
- `internal/orchestrator/merge_execution.go` (current merge dispatch — rewired to spawn a merger container via spawner RPC)
- `internal/worktree/manager.go` (soon-to-be-deleted — understand the API surface being replaced)
- `internal/container/runtime.go` (prompt 01 — the Runtime interface used for Docker event subscription)
- `internal/spawner/client.go` (prompt 07 — the client library this orchestrator uses)
- `internal/gitref/` (prompt 03 — branch registry that replaces worktree records)
- `ARCHITECTURE.md` (import ceilings and state-machine invariants)

## Dependencies

- Prompt 01 (`internal/container/`) — Runtime for Docker event subscription
- Prompt 03 (`internal/gitref/`) — branch registry
- Prompt 07 (`internal/spawner/client.go`) — RPC client

## Deliverables

### New files

#### 1. `internal/orchestrator/worker_spawn.go`

Replaces the worktree-creation call sites in `task_processing.go` and `session_spawning.go`.

- `type WorkerSpawner interface { SpawnWorker(ctx, params) (*spawner.SpawnWorkerResult, error); DestroyWorker(ctx, id) error; InspectWorker(ctx, id) (*spawner.InspectWorkerResult, error) }` — the interface the orchestrator depends on; wired in `main` to a concrete `*spawner.Client`
- `func (o *Orchestrator) spawnCoder(ctx context.Context, task *model.Task) error` — builds `SpawnWorkerParams` from task metadata (project, agent type `"coder"`, generated worker ID, feature branch name from `gitref`), calls the spawner, records the resulting container ID in the DB, registers the branch in `gitref.Registry.Register`
- `func (o *Orchestrator) spawnReviewer/spawnFixer/spawnSupervisor` — parallel forms for each session type

Every spawn records the image identity (`SpawnWorkerParams.Image`) in the DB on the task/agent row so Kyle can attribute runs per PRD user story 49.

#### 2. `internal/orchestrator/docker_events.go`

- `func (o *Orchestrator) watchDockerEvents(ctx context.Context) error` — opens `container.Runtime.SubscribeEvents` with a filter on `drem.project=<this project>`, dispatches events:
  - `EventDie` with non-zero `ExitCode` or `OOMKilled=true` → record the exit, look up the task from `drem.task_id` label, call `handleWorkerDeath(task, containerID, state)`
  - `EventDie` with exit 0 → normal shutdown, record completion
  - `EventStart` → no-op (tasks start in spawn, not here)
- `func (o *Orchestrator) handleWorkerDeath(task *model.Task, containerID string, state container.State)` — inspects the task's state; if the task is still in an active phase, call `spawnCoder` again to launch a replacement; the replacement clones the same branch from the bare repo and picks up from the most recent pushed commit (thanks to the watchdog)
- Replacement policy: cap at 3 replacements per task per hour; beyond that, mark the task as failed and require human intervention

#### 3. `internal/orchestrator/merge_dispatch.go`

Rewire `merge_execution.go` so that instead of invoking `internal/merge/` in-process, it asks the spawner to launch a merger container with the appropriate flags.

- `func (o *Orchestrator) dispatchMerge(ctx context.Context, task *model.Task) (*MergeResult, error)` — calls `spawner.SpawnWorker` with `AgentType: "merger"` and env vars telling the merger which branches to operate on, waits for the container to exit (via Docker events), reads the merge result from the DB (populated by agentmon ingesting the merger's structured output)
- Preserve the retry policy in `retry_policy.go` and the `MergeAttemptState` in `merge_attempt_state.go` — only the dispatch mechanism changes

#### 4. `internal/orchestrator/reconcile_containers.go`

On orchestrator restart, reconstruct in-flight state:

- `func (o *Orchestrator) reconcileOnStartup(ctx context.Context) error` — calls `spawner.ListWorkers(project)`, reconciles against the tasks table; tasks whose recorded container ID is not in the live list are marked for respawn; tasks whose container is still running are re-attached to the event stream

### Migration

#### 5. `internal/orchestrator/orchestrator.go`

- Add fields: `Spawner WorkerSpawner`, `Runtime container.Runtime`, `GitrefRegistry *gitref.Registry`
- In `Run`, launch `watchDockerEvents` in a goroutine; gate shutdown on both the tick loop and the event watcher finishing
- In `New`/constructor, accept the new dependencies; keep existing dependencies intact

#### 6. `internal/orchestrator/task_processing.go`

Replace every call to `worktree.Manager.Create(...)` with `o.spawnCoder(...)`. Replace every call to `worktree.Manager.Remove(...)` with `o.Spawner.DestroyWorker(...)` + `o.GitrefRegistry.MarkDeleted(...)`.

#### 7. `internal/orchestrator/session_spawning.go`

Replace the existing subprocess-based spawns with `o.Spawner.SpawnWorker(...)` calls. The `agent_type` label distinguishes `reviewer`, `fixer`, `supervisor`. Image selection flows through the spawner's agent-type-to-image mapping (prompt 07).

#### 8. `internal/orchestrator/reconcile.go`

Replace the `os.Stat` / worktree-directory scans with calls to `spawner.ListWorkers(project)`. The reconcile contract stays the same; only the data source changes.

#### 9. `internal/orchestrator/merge_execution.go`

Replace the in-process merge call with `o.dispatchMerge(ctx, task)`. The merge attempt state, retry policy, and quick-fix-to-merging transition remain untouched.

### Tests

#### 10. `internal/orchestrator/worker_spawn_test.go`

Use a fake `WorkerSpawner` (implement `SpawnWorker`/`DestroyWorker`/`InspectWorker` that record calls and return canned responses). Assert:

- `spawnCoder` constructs the expected `SpawnWorkerParams` for a given task (project, agent type, worker ID, branch)
- On successful spawn, the task row records the container ID and image
- On spawn failure, the task stays in its prior state and an error is returned

#### 11. `internal/orchestrator/docker_events_test.go`

Use a fake `container.Runtime` from prompt 01. Emit synthetic events:

- `EventDie` with exit code 137 (OOM) → `handleWorkerDeath` is called with `OOMKilled=true`
- `EventDie` with exit 0 → no replacement spawned
- Replacement spawn attempted up to 3 times; 4th attempt marks the task failed

#### 12. `internal/orchestrator/reconcile_containers_test.go`

Seed the DB with several in-flight tasks. Wire a fake spawner that returns `ListWorkers` with a subset of their container IDs. Assert:

- Tasks whose container is still live are left untouched
- Tasks whose container is gone are re-queued for spawn
- Tasks with no recorded container ID (older rows) are left alone

## Scope Limitation

- **State machine.** Do not change the task status enum or the state transitions. Preserve `backlog → planning → plan_review → test_writing → test_review → in_progress → testing_ready → merging → done`. This prompt only changes spawn/observe/replace mechanics.
- **Supervisor loop.** Context window monitoring, stall detection, fixer spawning remain functional. Touch `supervisor/` only if necessary to swap spawn call sites.
- **Agent package.** Prompt 13 handles `internal/agent/`. Do not modify it here beyond incidental import adjustments.
- **TUI.** Prompt 15 handles `internal/tui/`. Do not modify it here.
- **Deletion.** Do not delete `internal/worktree/` or `internal/tmux/`. Prompt 17 coordinates those removals after prompts 12, 13, 15 land.
- **New state.** Any new DB columns needed (container ID, image reference) should be additive migrations. Do not remove existing columns here.

## Audit Trail

Every spawn and every container death must produce a row in the events table. Kyle's "what happened when" reports depend on this. Use the existing event-insertion helpers if they exist; if not, insert directly via GORM.

## Conventions

- Module path: `github.com/godinj/drem-orchestrator`
- Package name: `orchestrator` (extending existing)
- Per `ARCHITECTURE.md`: the orchestrator package already pushes against file count and import ceilings. Every new file here is additive; keep each under the file-length limit
- Existing GORM hook conventions — do not spread hooks across new files
- Tests: `testify/require`, `testutil.NewTestDBWithModels`, fake spawner and fake runtime from earlier prompts
- Build verification: `go build ./internal/orchestrator/... && go test ./internal/orchestrator/...`
- Full test suite: `go test ./...`
- Constitution check: `bash scripts/check_constitution.sh`
