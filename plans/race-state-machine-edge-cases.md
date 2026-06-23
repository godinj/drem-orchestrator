# Race And State-Machine Edge Case Review

Generated: 2026-06-22

Scope: subagent-assisted static audit of race conditions, unintended state-machine transitions, and error-mapping gaps in the orchestrator. This is a review artifact, not a fix plan. The paired machine-readable artifact is `plans/race-state-machine-edge-cases.json`.

## Review Inputs

- Gate/API audit: task gate mutations, comments, inbox, client contracts, DTO/status surfaces.
- Reconciliation audit: Docker event handling, watchdog/agentmon ingest, stale assignment recovery, merge/retry loops.
- Container/control audit: worker spawning, spawner registry, project registry, git refs, websocket/eventbus/persona control, artifact registry.

## Severity Key

- P0: can corrupt active work, spawn duplicate live workers, kill the wrong worker, or write stale lifecycle results into current state.
- P1: can leave partial lifecycle state, misclassify client conflicts as server errors, violate recovery invariants, or cause durable operational confusion.
- P2: bounded or secondary consistency issue that still deserves an explicit guard, test, or documented invariant.

## Cross-Cutting Invariants To Add

- Transitions that check task status must persist with compare-and-swap semantics: `WHERE id = ? AND status = ?` or an equivalent transaction/row lock.
- Multi-row lifecycle mutations must be atomic where possible: task status, task event, assignment, child task changes, worker attempt, and comments should not be split across independent saves without compensation.
- Container events and ingested worker output must be scoped to the specific worker attempt/container that produced them, not only to `task_id`.
- Recovery classification and recovery mutation must be one guarded operation; the apply step must verify the same assignment, heartbeat window, worker status, and task status that were classified safe.
- Spawn must reserve durable ownership before or atomically with container creation; a live container without DB identity, attempt metadata, or gitref ownership is an orphan.
- Best-effort broadcast, websocket, and event-delivery paths must not block or duplicate authoritative state changes.

## P0 Findings

### RACE-SM-001: Double approve can duplicate subtasks

- Severity: P0
- Surface: `internal/orchhttp/gate_handlers.go:72`, `internal/orchestrator/handlers.go` plan approval/materialization.
- Scenario: Two concurrent `POST /approve` calls both observe `plan_review`, both enter plan approval, and both materialize subtasks before the final parent status write is visible.
- Expected invariant: Plan approval is exactly-once: one status transition, one task event, one subtask set.
- Evidence: Handler prechecks status before delegation; subagent audit found no transaction, row lock, conditional `WHERE status = plan_review`, or idempotency key around approval plus subtask creation.
- Acceptance guard: Concurrent approve integration test with a real plan containing subtasks; assert one success, one 409, one subtask set, and one parent status event.

### RACE-SM-002: Delayed container death can kill the current replacement worker

- Severity: P0
- Surface: `internal/orchestrator/docker_events.go:99`, `internal/orchestrator/docker_events.go:154`, `internal/orchestrator/docker_events.go:177`.
- Scenario: A delayed Docker `die`/OOM event for container A is routed by `drem.task_id` after the task has been reassigned to container B. `markAssignedWorkerDead` marks the current assigned agent dead without checking that the assigned agent's container ID matches the event container ID.
- Expected invariant: A container event may mutate only the agent/attempt/container it describes.
- Evidence: `dispatchEvent` loads by task ID; `markAssignedWorkerDead` loads `task.AssignedAgentID` and overwrites agent/task state with `ev.ContainerID`.
- Acceptance guard: Simulate reassignment from container A to B, then deliver A's death event. Assert B remains assigned/working and no respawn occurs for A unless A is still current.

### RACE-SM-003: Worker death recovery respawns the wrong lifecycle role

- Severity: P0
- Surface: `internal/orchestrator/docker_events.go:150`, `internal/orchestrator/docker_events.go:171`.
- Scenario: Any abnormal container death for a non-terminal task calls `spawnCoder`, even if the dead container was planner/reviewer/fixer/merger or the task is in planning, review, testing, or merging.
- Expected invariant: Recovery preserves the lifecycle role and current state; merger death re-enters merge recovery, reviewer death respawns reviewer, and so on.
- Evidence: `handleWorkerDeath` unconditionally calls `o.spawnCoder(ctx, task)` after cap checks.
- Acceptance guard: Parametrize dead container roles and statuses; assert replacement role matches the dead active attempt or recovery declines with a controlled status.

### RACE-SM-004: Late merge result can overwrite current merge attempt

- Severity: P0
- Surface: `internal/orchhttp/handlers_internal.go` ingest side effects, `internal/orchestrator/merge_dispatch.go` merge dispatch.
- Scenario: A stale `merge_result` from an old merger arrives after retry/reentry and writes `merge_commit` or `merge_conflicts` into task context. A later merge dispatch consumes stale context as if it belonged to the current attempt.
- Expected invariant: Ingested worker side effects are accepted only for the current worker attempt/container.
- Evidence: Subagent audit found `merge_result` handling keyed by event type and task ID, with no container/attempt/status match.
- Acceptance guard: Run two merger attempts for one task; deliver old success/failure after the new attempt starts; assert the old result is ignored.

### RACE-SM-005: Reconciler can manufacture revert commits from stale worktrees

- Severity: P0
- Surface: `internal/orchestrator/reconcile.go` orphaned subtask reconciliation; `plans/reconciler-stale-worktree-fix.md`.
- Scenario: Reconciler commits unstaged changes in a host feature worktree whose HEAD is stale relative to the worker-pushed bare ref, producing a revert commit.
- Expected invariant: Reconciliation must not commit from stale host worktrees; branch truth should come from bare refs or a fresh fetch-safe flow.
- Evidence: Subagent audit found active reconcile code still calling host-worktree auto-commit; existing plan documents this corruption sequence.
- Acceptance guard: Advance bare ref while host worktree is stale; run reconcile; assert no revert commit is created.

### RACE-SM-006: Spawn can leave a live container without durable DB identity

- Severity: P0
- Surface: `internal/orchestrator/worker_spawn.go:155`, `internal/orchestrator/worker_spawn.go:172`, `internal/workeridentity/workeridentity.go`.
- Scenario: `SpawnWorker` succeeds, then `RecordSpawn` fails while creating/updating the agent or worker attempt. Caller logs the error and continues to record spawn event data with an incomplete handle.
- Expected invariant: Container lifecycle and DB identity are reconciled: either both exist, or the container is destroyed/marked for recovery.
- Evidence: `spawnTypedWorker` logs `recordErr` and continues; subagent audit found `RecordSpawn` uses multiple DB writes without a transaction.
- Acceptance guard: Inject DB failure after spawner success; assert `DestroyWorker` is called or a durable recovery row/event is written and task assignment remains unambiguous.

### RACE-SM-007: Concurrent spawn can create two live workers for one task

- Severity: P0
- Surface: `internal/orchestrator/session_spawning.go`, `internal/orchestrator/worker_spawn.go:86`, `internal/workeridentity/workeridentity.go`.
- Scenario: Two spawn paths both observe no working agent or busy worktree, both spawn containers, then race assigning `Task.AssignedAgentID`; last writer wins and the losing container remains live.
- Expected invariant: At most one active worker per task and agent type unless explicitly replacing a known attempt.
- Evidence: Subagent audit found read-before-write checks, no task-level compare-and-swap, and no active-attempt uniqueness guard visible on the spawn path.
- Acceptance guard: Concurrently call a typed spawn twice with a fake spawner; assert only one container is started and the loser returns conflict.

## P1 Findings

### RACE-SM-008: Stale gate races are returned as 500 instead of 409

- Severity: P1
- Surface: `internal/orchhttp/gate_handlers.go:33`, `internal/orchhttp/gate_handlers.go:96`, `internal/orchhttp/gate_handlers.go:137`, `internal/orchhttp/gate_handlers.go:164`.
- Scenario: Request A prechecks a status, request B transitions first, then A's orchestrator method reloads and returns an expected-status error. Handler maps the delegated error to 500.
- Expected invariant: Stale gate data is a client conflict, not an internal error.
- Evidence: Handler comments document 409 for status mismatch, but delegated transition errors are written as 500.
- Acceptance guard: Race or fake stale transition test; assert stale delegated errors become typed 409 responses.

### RACE-SM-009: Gate mutations can leave partial multi-row lifecycle state

- Severity: P1
- Surface: `internal/orchestrator/handlers.go`, `internal/orchestrator/task_api.go`, `internal/state/machine.go:53`.
- Scenario: Transitions create children, update parent status, create events/comments, write files, or update agents through independent calls. Midway failures leave half-approved, half-rejected, or half-retried state.
- Expected invariant: Lifecycle DB mutations are transactional, or side effects are idempotent and repaired by explicit compensation.
- Evidence: `TransitionTask` explicitly leaves persistence to callers; subagent audit found multiple lifecycle handlers saving related rows separately.
- Acceptance guard: Failure-injection tests after child/event/comment creation; assert no partial DB mutation or assert a repair path runs.

### RACE-SM-010: Archive can cancel newly live work between precheck and update

- Severity: P1
- Surface: `internal/orchhttp/gate_handlers.go` archive handling and assignment check.
- Scenario: Archive checks assigned worker status, then a worker becomes working, is assigned, or heartbeats before archive's task update. The update gates only on task status and can cancel live work.
- Expected invariant: Archive never cancels a task currently assigned to a live worker.
- Evidence: Assignment classification is separate from update; subagent audit found update condition does not include current assignment/worker status.
- Acceptance guard: Change assignment/heartbeat between archive classification and update; assert 409 and unchanged task.

### RACE-SM-011: Stale-assignment recovery can clear a changed assignment

- Severity: P1
- Surface: `internal/orchhttp/health_recovery.go:188`, `internal/orchhttp/health_recovery.go:210`, `internal/orchhttp/health_recovery.go:223`.
- Scenario: Recovery classifies an assignment as stale, but before apply the task is reassigned or the worker heartbeats. Apply still clears by task ID.
- Expected invariant: Apply clears only the same assignment classified safe.
- Evidence: Handler classifies and applies in separate steps; subagent audit found task update is not conditional on `assigned_agent_id` or heartbeat/status snapshot.
- Acceptance guard: Reassign task or update heartbeat after classification; assert recovery returns conflict and does not clear the new assignment.

### RACE-SM-012: Heartbeat events do not refresh authoritative agent liveness

- Severity: P1
- Surface: `internal/extract/parse_heartbeat.go`, `internal/orchhttp/handlers_internal.go`, `internal/orchhttp/health_recovery.go:77`.
- Scenario: Watchdog emits `DREM-HEARTBEAT`; agentmon ingests it as a task event, but `model.Agent.HeartbeatAt` is not updated. Health recovery can classify a live worker as stale.
- Expected invariant: Accepted heartbeat records update the liveness field used by recovery, or recovery must not rely on that field.
- Evidence: Subagent audit found ingest side effects only handle merge results; health issue classification uses `Agent.HeartbeatAt`.
- Acceptance guard: Ingest heartbeat for assigned working agent with old `HeartbeatAt`; assert heartbeat advances and stale recovery refuses to clear it.

### RACE-SM-013: Retry cascade can reanimate parent and cancel or strand target child

- Severity: P1
- Surface: `internal/orchhttp/gate_handlers.go:245`, `internal/orchestrator/task_api.go` retry logic.
- Scenario: Retrying a failed child first retries the failed parent. Parent retry cancels/detaches stale children, including the requested child, then child retry fails or leaves parent backlog and child failed/cancelled.
- Expected invariant: Parent-child retry is one atomic semantic operation or a documented multi-step state with recovery.
- Evidence: Handler calls `RetryTask(parent)` then `RetryTask(child)` separately; subagents independently flagged partial/cancel behavior.
- Acceptance guard: Real HTTP retry for failed parent plus failed child; assert both end in intended schedulable states or one controlled conflict with no partial parent reanimation.

### RACE-SM-014: Agentmon tail cancellation can drop final lifecycle records

- Severity: P1
- Surface: `internal/agentmon/docker_source.go`, `internal/agentmon/docker_tail.go`.
- Scenario: Docker die/destroy cancels log tail context; tail reader exits on `ctx.Done` without draining buffered final lines such as heartbeat, crash, or merge result.
- Expected invariant: Container shutdown drains available logs before lifecycle handling considers the attempt complete.
- Evidence: Subagent audit found drain behavior only on scanner completion path, not cancellation path.
- Acceptance guard: Feed final `merge_result`, trigger die/cancel concurrently, assert final record is ingested.

### RACE-SM-015: Runtime list errors can false-kill seen containers

- Severity: P1
- Surface: `internal/orchestrator/reconcile_stuck.go` stuck-agent reconciliation.
- Scenario: `ListWorkers` transiently fails and returns an empty running set. If the agentmon sighting probe has seen the container, kill can proceed because the sighting only bypasses no-sighting cases.
- Expected invariant: Inability to confirm runtime liveness should not mark a container worker dead without reliable death/inspect evidence.
- Evidence: Subagent audit found fallback-to-empty behavior and kill logic that skips only when running set says running or sighting probe returns false.
- Acceptance guard: Working container, `ListWorkers` error, `HasSeen(container)=true`; assert no dead marking/respawn until explicit death or inspect confirms.

### RACE-SM-016: Merge retry backoff is computed but not enforced

- Severity: P1
- Surface: `internal/orchestrator/merge_execution.go`, `internal/orchestrator/retry_policy.go`.
- Scenario: Retry policy computes delay, but dispatch retries every tick because no `next_retry_at` guard prevents immediate reentry.
- Expected invariant: Transient merge retries obey backoff and cap across ticks and restarts.
- Evidence: Subagent audit found delay logged/saved only as attempt state, with no dispatch guard before re-executing.
- Acceptance guard: Transient merge failure followed by a tick before delay; assert no new merger is spawned.

### RACE-SM-017: Git branch provisioning is not race-idempotent

- Severity: P1
- Surface: `internal/gitref/git.go`, `internal/orchestrator/worker_spawn.go:113`.
- Scenario: Two spawns concurrently ensure the same branch. Both see branch absent; one creates it; the second `git branch` fails even though desired end state exists.
- Expected invariant: Concurrent branch provisioning is idempotent if the branch now exists and points to an acceptable ref.
- Evidence: Subagent audit found check-then-create behavior without recheck-on-already-exists.
- Acceptance guard: Race two `EnsureBranch` calls for the same bare ref; assert both succeed or one returns controlled idempotent success after rechecking.

### RACE-SM-018: Gitref ownership can fail after container spawn

- Severity: P1
- Surface: `internal/orchestrator/worker_spawn.go:191`, `internal/gitref/registry.go`.
- Scenario: Container is started, then gitref registration fails because another active row owns the branch. Code logs and continues, so a worker can run on a branch registry says belongs elsewhere.
- Expected invariant: A live worker branch has exactly one active owner matching its task/agent.
- Evidence: Comment says already-claimed is informational because the container has already been spawned.
- Acceptance guard: Fake registry returns already-active; assert spawn is rejected before container start or the spawned container is destroyed.

### RACE-SM-019: Project registry can allocate duplicate ports under concurrent register

- Severity: P1
- Surface: `internal/projects/registry.go` load/allocate/save.
- Scenario: Two `drem project register` processes load the same registry, both choose the same `OrchHostPort`, and last atomic rename wins or duplicate ports persist.
- Expected invariant: Concurrent registration serializes or detects stale base version; no duplicate ports and no lost projects.
- Evidence: Subagent audit found atomic write/rename but no file lock or reload-before-commit around allocation and save.
- Acceptance guard: Multi-process/goroutine registry save test; assert both projects survive with distinct ports or the second save conflicts.

### RACE-SM-020: Whole-file project registry saves can lose updates

- Severity: P1
- Surface: `internal/projects/registry.go` save/update/remove.
- Scenario: Separate CLI invocations load stale registry state and save whole TOML files; later save overwrites earlier add/remove/update.
- Expected invariant: Registry operations serialize, merge, or reject stale writes.
- Evidence: Save is whole-file encode plus rename with no lock/version check.
- Acceptance guard: Load same file twice, add distinct projects, save both; assert both survive or stale save fails.

### RACE-SM-021: Spawner registry is process-local after restart

- Severity: P1
- Surface: `internal/spawner/service.go`, `internal/spawner/methods.go`.
- Scenario: Spawner restarts while containers keep running. In-memory registry is empty, so `ListWorkers` does not report existing labeled workers; recovery code depending on list output misclassifies liveness.
- Expected invariant: Spawner list/inspect recovers from Docker labels after restart or callers treat list as process-local and unsafe for kill decisions.
- Evidence: Subagent audit found service registry is in memory and comments describe containers spawned by this service instance.
- Acceptance guard: Spawn, recreate service, assert `ListWorkers` can recover labeled running containers or exposes cold-start state that prevents destructive recovery.

### RACE-SM-022: Spawner shutdown can leave ambiguous in-flight lifecycle operations

- Severity: P1
- Surface: `internal/spawner/service.go`, Docker runtime spawn/destroy calls.
- Scenario: Server context cancellation aborts an in-flight spawn/destroy. Caller may not know whether a container was created or destroyed.
- Expected invariant: Shutdown stops accepting new work but allows bounded in-flight lifecycle operations to finish or records unknown outcome for reconciliation.
- Evidence: Subagent audit found connection handlers pass the top-level server context into methods.
- Acceptance guard: Fake runtime blocks between create/start; cancel server; assert no orphan or a recoverable unknown-outcome record exists.

### RACE-SM-023: Websocket broadcast can block all clients and hub mutation

- Severity: P1
- Surface: `internal/serve/ws_hub.go`, `internal/serve/messages.go`.
- Scenario: Broadcast holds hub lock while synchronously writing to each websocket with `context.Background`; a slow/stuck client blocks delivery to all clients and register/unregister progress.
- Expected invariant: One bad websocket client cannot block hub mutation or message delivery to other clients.
- Evidence: Subagent audit found synchronous writes under lock and no per-client timeout/write pump.
- Acceptance guard: Fake blocked connection; assert broadcast returns within deadline and failed client is removed or isolated.

### RACE-SM-024: Eventbus poll/deliver can duplicate side effects

- Severity: P1
- Surface: `internal/eventbus/eventbus.go` poll/deliver.
- Scenario: Two watcher processes poll same agent concurrently, both see event undelivered, both deliver side effects, then one duplicate delivery insert fails or arrives too late.
- Expected invariant: Delivery claim is atomic; only the process that claims an event receives it for side effects.
- Evidence: Subagent audit found `Poll` and `Deliver` are separate operations; primary key prevents duplicate rows but not duplicate side effects between poll and insert.
- Acceptance guard: Concurrent poll/deliver for same agent; replace with transactional claim such as `INSERT OR IGNORE ... SELECT` and return only claimed events.

### RACE-SM-025: Persona control actions can race through Docker Compose

- Severity: P1
- Surface: `internal/personacontrol/control.go`, `internal/serve/persona_control.go`.
- Scenario: Concurrent `stop`, `start`, `recreate`, or `all recreate` requests run without per-service serialization; final service state depends on Compose interleaving.
- Expected invariant: Persona service transitions serialize per service or reject while a transition is in progress.
- Evidence: Subagent audit found no mutex/transition state; handler invokes controller directly.
- Acceptance guard: Fake executor blocks; send overlapping actions; assert second request receives 409 or queues deterministically.

### RACE-SM-026: Generic state transition helper is non-atomic by design

- Severity: P1
- Surface: `internal/state/machine.go:17`, `internal/state/machine.go:53` and all DB callers.
- Scenario: Two callers validate transitions from stale in-memory state and persist out of order; task save can succeed while event save fails, or event can be appended for a transition that loses the final task write.
- Expected invariant: Status transition and event append are one DB operation with current-status compare-and-swap.
- Evidence: `TransitionTask` mutates memory and returns event; comment says caller persists both.
- Acceptance guard: Add repository method for `TransitionTaskTx(id, from, to)`; race conflicting transitions and assert one success, one conflict, and exactly one event.

## P2 Findings

### RACE-SM-027: Cancelled status is outside the formal transition map

- Severity: P2
- Surface: `internal/state/machine.go:17`, `internal/model/enums.go`, archive endpoint.
- Scenario: `cancelled` exists as a status but is absent from `ValidTransitions`; archive bypasses `TransitionTask`.
- Expected invariant: Every status has an explicit state-machine entry, including terminal states with no outgoing transitions.
- Evidence: `StatusCancelled` is used elsewhere, while `ValidTransitions` includes `done`, `failed`, and `rejected`, but not `cancelled`.
- Acceptance guard: State-machine test that every known task status has an entry; decide whether archive is a first-class transition.

### RACE-SM-028: Comment/delete race can create orphan comments

- Severity: P2
- Surface: `internal/orchhttp/gate_handlers.go` comments, lifecycle delete paths.
- Scenario: Comment endpoint loads task; concurrent delete removes the task/comments/events; comment insert succeeds without an enforced FK.
- Expected invariant: Comments always belong to existing tasks.
- Evidence: Subagent audit found load-then-create pattern and could not confirm enforced foreign-key constraints.
- Acceptance guard: Concurrent delete/comment test; assert comment fails 404/409 or FK prevents orphan.

### RACE-SM-029: Inbox archive/ignore race returns 500 instead of controlled conflict

- Severity: P2
- Surface: `internal/serve/inbox.go`, `internal/csuite/diskstore/diskstore.go`.
- Scenario: Two clients move the same inbox item; one rename succeeds, the other sees missing source and returns raw filesystem error mapped to 500.
- Expected invariant: Concurrent processing of one inbox item is idempotent or returns 404/409, not 500.
- Evidence: Subagent audit found raw `os.Rename` errors and limited error mapping.
- Acceptance guard: Parallel archive/ignore same item; assert one success and one 404/409.

### RACE-SM-030: Inbox archive can overwrite destination collision

- Severity: P2
- Surface: `internal/csuite/diskstore/diskstore.go` inbox item move.
- Scenario: `os.Rename(src, dest)` can overwrite existing archive/ignored file with same basename on Unix.
- Expected invariant: Archiving/ignoring never destroys an existing inbox record.
- Evidence: Destination is constructed from basename with no exclusive create or collision check.
- Acceptance guard: Pre-create destination basename, then archive live item; assert collision is refused or unique suffix is used.

### RACE-SM-031: Message pagination can skip same-second items

- Severity: P2
- Surface: `internal/csuite/diskstore/diskstore.go` message pagination.
- Scenario: Sorting uses `(CreatedAt desc, ID desc)`, but cursor filtering keeps only messages before cursor timestamp, skipping same-timestamp lower IDs.
- Expected invariant: Cursor pagination visits every message exactly once under timestamp ties.
- Evidence: Subagent audit found created timestamps truncated to seconds and cursor filtering ignoring ID.
- Acceptance guard: Create more than one page of messages with identical `CreatedAt`; assert all IDs appear.

### RACE-SM-032: Success responses with empty bodies break orchclient mutations

- Severity: P2
- Surface: `pkg/orchclient/gate.go`.
- Scenario: Client decodes every 2xx response into output; future 204 or empty 200 returns JSON EOF as an error.
- Expected invariant: Client success handling matches the server contract, either body-required or empty-success accepted.
- Evidence: Subagent audit found unconditional JSON decode for all 2xx statuses.
- Acceptance guard: Client test for 204/empty 200; either accept zero DTO or enforce server never returns empty body.

### RACE-SM-033: Health recovery can clear terminal or wrong-status assignments

- Severity: P2
- Surface: `internal/orchhttp/health_recovery.go:76`, `internal/orchhttp/health_recovery.go:93`.
- Scenario: Stale-assignment issue is raised for any task with `AssignedAgentID`, including terminal or gate statuses; apply has no visible status guard.
- Expected invariant: Recovery is scoped to actionable statuses or terminal cleanup is explicit and safe.
- Evidence: Health issue generation checks assignment before active-status check.
- Acceptance guard: Done/failed task with historical assigned worker; recovery should no-op, reject, or document safe cleanup behavior.

### RACE-SM-034: Replacement cap is not durable across orchestrator restarts

- Severity: P2
- Surface: `internal/orchestrator/docker_events.go:26`, `internal/orchestrator/docker_events.go:79`.
- Scenario: Respawn cap lives in memory; orchestrator restart resets the one-hour replacement count and permits extra crash-loop replacements.
- Expected invariant: Replacement cap survives restart or is explicitly best-effort.
- Evidence: `replacementTracker` is constructed inside `watchDockerEvents` and stores timestamps in memory only.
- Acceptance guard: Record cap deaths, restart watcher, deliver another death; assert task fails or documented best-effort behavior is accepted.

### RACE-SM-035: Websocket fanout can hang HTTP message handlers after response

- Severity: P2
- Surface: `internal/serve/messages.go`, `internal/serve/ws_hub.go`.
- Scenario: REST message POST writes 201, then broadcasts synchronously; slow websocket writes keep handler goroutine around after response.
- Expected invariant: Persisted message response is not coupled to best-effort websocket fanout latency.
- Evidence: Subagent audit found inline broadcast with background context.
- Acceptance guard: Blocked websocket test; assert POST handler returns promptly.

### RACE-SM-036: Eventbus ack overwrites original ack timestamp

- Severity: P2
- Surface: `internal/eventbus/eventbus.go` ack handling.
- Scenario: Repeated ack updates `acked_at`, losing first acknowledgement time despite idempotent-noop expectations.
- Expected invariant: Ack is idempotent and preserves first acknowledgement timestamp.
- Evidence: Subagent audit found update does not require `acked_at IS NULL`.
- Acceptance guard: Ack same event twice with controlled time; assert timestamp unchanged.

### RACE-SM-037: Persona `start` may recreate instead of only starting

- Severity: P2
- Surface: `internal/personacontrol/control.go`.
- Scenario: Action `start` maps to `docker compose up -d --no-deps`; if service was removed or config changed, Compose may create/recreate rather than start existing stopped container.
- Expected invariant: `start` is a safe stopped-to-running transition unless explicitly named/documented as `up`.
- Evidence: Subagent audit found `stop` uses Compose stop while start uses Compose up.
- Acceptance guard: Command-building test requiring `docker compose start <service>` for start, or rename action semantics.

### RACE-SM-038: Gitref merged state is immediately overwritten by deleted state

- Severity: P2
- Surface: `internal/merger/merger.go`, `internal/gitref/registry.go`.
- Scenario: Successful merge calls `MarkMerged`, then `MarkDeleted` on a single status field; final row loses whether deletion followed merge or discard.
- Expected invariant: Registry can answer whether a deleted branch was merged vs discarded.
- Evidence: Subagent audit found one enum field and immediate transition to deleted after merged.
- Acceptance guard: After successful merge, assert registry preserves both merge outcome and deletion outcome or terminal `merged_deleted`.

### RACE-SM-039: Artifact register lookup/create is not concurrency-idempotent

- Severity: P2
- Surface: `internal/artifactregistry/registry.go`.
- Scenario: Two callers register same `ContentURI`; both miss existing row, one create succeeds, the other returns unique error instead of idempotent success.
- Expected invariant: Registering the same artifact URI concurrently succeeds with one final row.
- Evidence: Subagent audit found manual lookup-then-create inside transaction, without conflict clause/retry.
- Acceptance guard: Concurrent `RegisterArtifact` with same URI; assert both succeed and one row exists.

### RACE-SM-040: Artifact supersede can race to conflicting current superseders

- Severity: P2
- Surface: `internal/artifactregistry/registry.go`.
- Scenario: Two supersede decisions for the same artifact race; last writer changes `superseded_by_id`, while both may create links/history.
- Expected invariant: An artifact has one current superseder unless conflict is modeled explicitly.
- Evidence: Subagent audit found update by ID only, not conditional on unsuperseded/current status.
- Acceptance guard: Concurrent supersede to two targets; assert one conflict via conditional update.

## Contract Drift And Documentation Risks

### RACE-SM-041: Test fail contract drift

- Severity: P1
- Surface: `internal/orchhttp/gate_handlers_test.go`, `internal/orchestrator/handlers.go`, `plans/orch-api-gate-mutations.md`.
- Scenario: Fake/test expectation for `POST /fail` says `failed`, while real orchestrator/docs say `testing_ready -> in_progress`.
- Expected invariant: Handler tests and fakes match production transition contract.
- Evidence: Gate subagent found real `HandleTestFailed` transitions to `in_progress` while fake test asserts `failed`.
- Acceptance guard: Replace fake-only assertion with real orchestrator test or align fake with production status.

## Open Verification Gaps

- Dynamic race tests were not run; this artifact is static analysis plus subagent review.
- Production DB constraints and SQLite/GORM transaction settings were not fully verified.
- Foreign-key enforcement for task comments was not confirmed.
- Lifecycle-engine mode may bypass some legacy reconciliation paths; equivalent protections need a separate pass.
- Spawner `InspectWorker` and Docker status semantics for restarting/paused/removing/dead containers need runtime validation.
- Whether production wires container sighting probes consistently was not confirmed.
