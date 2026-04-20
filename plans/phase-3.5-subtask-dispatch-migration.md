# Phase 3.5 — Subtask Dispatch Migration (legacy `runner.SpawnAgent` → `o.Spawner`)

Status: in progress, 2026-04-20. Session deliverable: unblock the T3
containerization canary by routing the coder dispatch path in
`internal/orchestrator/subtask_scheduling.go` through the container-mode
spawner (`o.Spawner.SpawnWorker` via `spawnCoder` / `spawnTypedWorker`)
instead of the legacy host-runner dispatch path
(`o.runner.SpawnAgent`).

Sibling plans already landed:

- `plans/worker-subscription-auth.md` — creds mount plumbing
  (commits `66be7f0..4f3f9fc`).
- `plans/worker-prompt-delivery.md` — prompt render + atomic write +
  bind-mount (commits `4b6f1f3..17b2523`).
- `plans/drem-project-register-update.md` — compose/drem.toml
  reconciliation (commits `ee0ea90..3abda58`).

Everything downstream of `o.Spawner.SpawnWorker` — creds, prompt,
branch registration, Agent-row recording, spawn audit events — is in
place and regression-tested. What was missing: the subtask scheduler
never calls `o.Spawner.SpawnWorker`. It still calls the legacy
host-mode `o.runner.SpawnAgent`, which in a headless orch container
shells out to a `claude` binary that does not exist in the image.

## Why

The operator exercised the T3 canary today and hit:

    spawn agent for subtask failed
      error="spawn agent: start: start subprocess: start agent process:
      start: exec: \"claude\": executable file not found in $PATH"

Root cause: `internal/orchestrator/subtask_scheduling.go:267` calls
`o.runner.SpawnAgent(sub, featureName, agentType, agentPrompt)`. In
container mode, the runner is wired (non-nil) for legacy capacity
tracking, but its spawn path uses the tmux-era worktree +
`exec "claude"` harness that runs on a host with the claude CLI
installed. The orch image (`deploy/docker/orch.Dockerfile`) installs
Go, sqlite, and `drem` — **not** the claude CLI — because workers run
claude in per-task worker containers (reviewers/fixers/coders) that
bind-mount the host operator's `~/.claude/.credentials.json` and exec
claude themselves. The containerization PRD's §"Agent spawn routing"
assigns claude-execution to workers, not orch.

Today's state is:

    doTick → processInProgress → scheduleSubtasks → o.runner.SpawnAgent(...)
                                                    ^
                                                    legacy host-subprocess path;
                                                    fails instantly in the orch container
                                                    because claude binary is absent.

What we want:

    doTick → processInProgress → scheduleSubtasks → o.spawnCoder(ctx, sub)
                                                    → o.spawnTypedWorker(ctx, sub, "coder")
                                                      → o.buildSpawnContext(sub, "coder")
                                                        — renders + writes prompt file
                                                        — resolves creds mount
                                                        — builds env + labels + branch
                                                      → o.Spawner.SpawnWorker(ctx, params)
                                                      → o.recordContainerOnAgent(sub, ...)
                                                        — writes Agent row (TmuxSession = container id)
                                                        — wires task.AssignedAgentID ← ag.ID
                                                      → o.GitrefRegistry.Register(...)
                                                      → o.recordSpawnEvent(task, "coder", ...)

Everything after the `SpawnWorker` call is already built: tests in
`internal/orchestrator/worker_spawn_test.go` pin (a) CredsMount wired
from `DREM_WORKER_CREDS_PATH` env, (b) PromptMount written atomically
to host, (c) gitref registration, (d) agent-row creation carrying
container ID in `TmuxSession` (the repurposed column), and (e)
`worker_spawn_failed` audit on every failure mode. The T3 canary
needs exactly that spawn path — it just never reaches it from
subtask dispatch.

## Scope

### In scope — primary (must ship to unblock T3)

- `internal/orchestrator/subtask_scheduling.go:267` —
  `o.runner.SpawnAgent(sub, featureName, agentType, agentPrompt)` →
  `o.spawnCoder(ctx, sub)` (with corresponding removal of the
  prompt-generation and downstream-post-spawn code that now lives
  inside `spawnTypedWorker` / `recordContainerOnAgent`).

### In scope — secondary (best-effort this session, one commit per site)

- `internal/orchestrator/session_spawning.go:107` —
  `o.runner.SpawnAgentInWorktree(&task, worktreePath, model.AgentReviewer, reviewerPrompt)`
  (the `SpawnReviewerSession` public method) → `o.spawnReviewer(ctx, &task)`.
- `internal/orchestrator/session_spawning.go:248` —
  `o.runner.SpawnAgentInWorktree(&task, worktreePath, model.AgentFixer, fixerPrompt)`
  (the `SpawnFixerSession` public method) → `o.spawnFixer(ctx, &task)`.
- `internal/orchestrator/test_execution.go:139` — the test-failure
  fixer re-dispatch inside `processTestingReady` — →
  `o.spawnFixer(ctx, parent)`.
- `internal/orchestrator/task_prep.go:138` — the prep-agent spawn in
  `spawnPrepAgent` when the direct-SGLang prep pipeline is not
  configured — →  `o.spawnTypedWorker(ctx, sub, string(model.AgentPrep))`.
  Note: prep is currently NOT in `credsMountRequired`'s table (it is
  neither a claude-backed role nor the merger). That is deliberate —
  prep today runs via the direct SGLang pipeline, not as a
  claude-harness worker. But if we migrate the subprocess fallback
  to the container path, it will need a decision: either (a) add
  `"prep"` to `credsMountRequired` and `promptRequired` (claude-based
  prep, same auth/prompt story as coder), or (b) route prep through
  a new non-claude agent type that skips both mounts. Option (a) is
  what the legacy host path does today — it runs the same claude CLI
  as a coder but with the prep prompt template. See "Open questions"
  §Q1. If the answer is non-trivial, this site is punted with a
  note in its commit body and the task_prep.md plan gets a follow-up
  entry.

### Out of scope (per prompt)

- `internal/orchestrator/task_processing.go:248` — legacy planner
  fallback. Warm planner (`DREM_PLANNER_URL`) handles prod traffic;
  the subprocess call only fires when the HTTP endpoint is unset.
  Leaving this for a future phase that wires the planner to the
  spawner instead of HTTP (if that ever matters — current view is
  that the warm planner is the end state).
- `internal/orchestrator/classifying.go:90` — legacy classifier
  fallback. Same shape: warm `drem-classifier` handles prod; the
  subprocess call is a rollback safety net.
- Every test under `internal/agent/*` — this migration does not
  touch the `internal/agent` package at all. The package keeps its
  legacy subprocess implementation; the orchestrator simply stops
  calling it for container-mode dispatch. Tests in that package
  continue to exercise it directly and must not regress.
- `quickfix_processing.go:103` and `:150`, `context_monitor.go:237` —
  not in the prompt's scope list. They still call `o.runner.SpawnAgent`
  / `SpawnAgentInWorktree`. Flagged as follow-ups in §"Open questions"
  Q3; not blocked on T3 canary.
- The `o.runner` field itself. It stays non-nil in container mode
  (legacy capacity tracker + interactive-supervisor shim use it).
  Removing the field is a separate Phase 3.6 cleanup.
- The state machine. Task statuses, event shapes, transition
  validity, auto-schedule fast-track — unchanged.
- `flag.FlagSet.Parse` gotcha — no CLI work in this plan.
- RPC surface of the spawner (`SpawnWorkerParams` / result shape) —
  stable; no wire changes.
- Phase 4+ items on the containerization PRD.

## Call-site inventory

Current producers of `runner.SpawnAgent` / `runner.SpawnAgentInWorktree`
that flow through `o.Spawner` in this plan:

| Site | Current call | Proposed target | Agent type | Commit |
|------|---|---|---|---|
| `subtask_scheduling.go:267` | `o.runner.SpawnAgent(sub, featureName, agentType, prompt)` | `o.spawnCoder(ctx, sub)` (or `spawnTypedWorker(ctx, sub, string(agentType))` if agent_type varies) | coder (mostly); occasionally reviewer/fixer when subtask context sets agent_type | Commit 2 (primary) |
| `session_spawning.go:107` (`SpawnReviewerSession`) | `o.runner.SpawnAgentInWorktree(&task, worktreePath, model.AgentReviewer, prompt)` | `o.spawnReviewer(ctx, &task)` | reviewer | Commit 3 |
| `session_spawning.go:248` (`SpawnFixerSession`) | `o.runner.SpawnAgentInWorktree(&task, worktreePath, model.AgentFixer, prompt)` | `o.spawnFixer(ctx, &task)` | fixer | Commit 4 |
| `test_execution.go:139` (`processTestingReady`) | `o.runner.SpawnAgentInWorktree(parent, worktreePath, model.AgentFixer, prompt)` | `o.spawnFixer(ctx, parent)` | fixer | Commit 5 |
| `task_prep.go:138` (`spawnPrepAgent`) | `o.runner.SpawnAgent(sub, featureName, model.AgentPrep, prompt)` | decision in Q1; most likely `o.spawnTypedWorker(ctx, sub, string(model.AgentPrep))` after an entry is added to `credsMountRequired` and `promptRequired` for "prep" | prep | Commit 6 (or punted) |

Left alone explicitly:

| Site | Call | Reason |
|------|---|---|
| `task_processing.go:248` | `o.runner.SpawnAgent(task, featureName, model.AgentPlanner, prompt)` | Warm planner handles prod traffic; this is the subprocess fallback for when `DREM_PLANNER_URL` is unset. Operator excluded. |
| `classifying.go:90` | `o.runner.SpawnAgentInWorktree(task, mainWT, model.AgentClassifier, prompt)` | Warm classifier handles prod traffic; subprocess fallback. Operator excluded. |
| `quickfix_processing.go:103,150` | `o.runner.SpawnAgent(task, featureName, model.AgentCoder, prompt)` | Not in this plan's scope. Follow-up tracked in §Open-questions Q3. |
| `context_monitor.go:237` | `o.runner.SpawnAgentInWorktree(subtask, ag.WorktreePath, model.AgentFixer, prompt)` | Not in this plan's scope. Same follow-up as above. |

## Migration recipe (per call site)

For the coder dispatch in `subtask_scheduling.go` (and mirror for each
secondary site with agent-type-appropriate renaming):

**Before** (current, line numbers from `3abda58`):

```go
// Load project for prompt generation.
var project model.Project
if err := o.db.First(&project, "id = ?", o.projectID).Error; err != nil {
    return fmt.Errorf("schedule subtasks: load project: %w", err)
}

// Build parent context for the prompt.
parentCtx := map[string]any{
    "parent_title":       parent.Title,
    "parent_description": parent.Description,
    "feature_branch":     parent.WorktreeBranch,
}

// Build prompt.
subComments, _ := o.GetComments(parent.ID)
agentPrompt := prompt.Generate(prompt.Opts{
    Task:         sub,
    Project:      &project,
    AgentType:    agentType,
    WorktreePath: featureDir,
    Comments:     subComments,
    ParentCtx:    parentCtx,
})

// Spawn agent (creates worktree internally).
ag, err := o.runner.SpawnAgent(sub, featureName, agentType, agentPrompt)
if err != nil {
    o.logger.Error("spawn agent for subtask failed", "subtask_id", sub.ID, "error", err)
    continue
}
```

**After:**

```go
// Container-mode dispatch: build the spawn context + call
// Spawner.SpawnWorker via spawnTypedWorker. The prompt is rendered
// (and written atomically to the per-project prompts volume) inside
// buildSpawnContext; the ParentCtx block and standalone
// prompt.Generate call in this path are no longer needed because
// spawnTypedWorker owns prompt rendering end-to-end.
//
// NOTE: subtask_scheduling previously fed ParentCtx into
// prompt.Generate; buildSpawnContext today does not. See
// "Open questions" Q2 — if the parent-context fields are load-bearing
// for coder subtasks the right fix is to plumb them through
// spawnTypedWorker, not to retain the legacy path.
if err := o.spawnCoder(dispatchCtx, sub); err != nil {
    o.logger.Error("spawn agent for subtask failed",
        "subtask_id", sub.ID, "error", err)
    // spawnCoder/spawnTypedWorker already emits a worker_spawn_failed
    // event on every internal failure branch; no extra event writes here.
    continue
}

// spawnCoder set sub.AssignedAgentID via recordContainerOnAgent
// (it creates an Agent row when none exists, or updates an existing
// row's TmuxSession to the container ID). Reload the subtask so the
// downstream fast-track transitions and publishAgentStatus see the
// populated assignment.
if err := o.db.First(sub, "id = ?", sub.ID).Error; err != nil {
    return fmt.Errorf("schedule subtasks: reload sub after spawn: %w", err)
}
if sub.AssignedAgentID == nil {
    o.logger.Error("agent assignment missing after spawnCoder",
        "subtask", sub.Title)
    if err := o.failTask(sub, "agent record not found after container spawn"); err != nil {
        o.logger.Error("schedule: fail subtask after missing agent",
            "subtask_id", sub.ID, "error", err)
    }
    continue
}
```

Key semantic preservations the caller still needs after the migration:

- **`AssignedAgentID` is populated.** `recordContainerOnAgent` creates
  the Agent row and writes `task.AssignedAgentID = &ag.ID` when none
  exists; if one exists, it updates `TmuxSession` / `ModelID` /
  `HeartbeatAt` on the existing row. `spawnCoder` calls it
  unconditionally after `SpawnWorker` succeeds.
- **`publishAgentStatus` still fires.** The downstream emit block
  (`o.publishAgentStatus(..., ag.ID.String(), string(agentType),
  string(model.AgentWorking))`) is unchanged; it just reads
  `sub.AssignedAgentID` after the reload instead of using a stack
  variable returned from `SpawnAgent`.
- **`subtask_scheduled` event fires.** Unchanged — emitted after
  the fast-track transition block.
- **Fast-track transitions (`backlog → planning → plan_review →
  in_progress`) still run.** Unchanged — they depend on
  `sub.Status` and `sub.AssignedAgentID`, both of which are
  populated by the spawn + reload sequence.
- **Runner-capacity gate.** The legacy path had
  `if o.runner == nil || !o.runner.CanSpawn() { break }` above the
  spawn call. Container mode does not need the runner's capacity
  limit (the spawner runs per-container under docker's scheduler).
  We can either (a) drop the guard when `o.Spawner != nil`, or (b)
  keep the guard because the runner is still wired and still tracks
  ratelimits the orchestrator uses elsewhere. We go with (b) for the
  primary migration — the guard stays; it is a conservative upper
  bound that never fires in container mode because `runner` is the
  same instance regardless. This decision can be revisited when the
  `runner` field itself is retired in Phase 3.6.

### Context plumbing to `spawnTypedWorker`

Today `buildSpawnContext` builds the prompt without parent context.
For parent tasks and single-agent-type coder subtasks this is fine —
`prompt.Generate` reads the task description and project metadata.
For subtasks where the parent title + plan + feature branch matter
for the rendered prompt, we have two options:

- **O1 (simplest):** accept that parent context is dropped from the
  container-mode prompt and rely on the task description carrying
  enough context. This is the lowest-risk change for the primary
  (ship-T3) commit and is easy to audit.
- **O2:** extend `buildSpawnContext` to optionally take a
  `parentCtx` argument (or a `prompt.Opts` builder) so callers can
  override the default. This is bigger — it touches every
  `spawnCoder/spawnReviewer/spawnFixer/spawnSupervisor` signature
  and it risks regressing the worker-prompt-delivery tests.

The primary commit ships O1. The plan doc here is explicit that O1
ships first; an O2 follow-up is tracked in §Open questions Q2 and
can land independently. The T3 canary does not need O2 — the test
task passed at O1-equivalent prompt quality in the legacy path for
single-agent tasks (parent context was a nice-to-have, not
load-bearing).

## Test strategy

### Existing tests that must continue to pass

- `internal/agent/*` — untouched; every test in this package still
  exercises the subprocess path via `Runner.SpawnAgent` directly.
- `internal/orchestrator/subtask_scheduling_test.go` — exercises
  `scheduleSubtasks` on a parent with no candidates and
  `dispatchPendingSubtasks` with a terminal parent. Neither calls
  the spawn path. Both must continue to pass unchanged.
- `internal/orchestrator/worker_spawn_test.go` — the container-mode
  spawn path tests (creds, prompt, gitref, agent row, policy checks).
  All must continue to pass; this migration builds ON TOP of the
  invariants they already cover.
- `internal/orchestrator/merge_dispatch_test.go` — merger spawn via
  `o.Spawner.SpawnWorker` with argv; unchanged.
- `internal/orchestrator/task_prep_test.go` →
  `TestSpawnPrepAgent_FallsBackToSubprocessWhenNilConfig` — currently
  asserts that with nil runner AND nil directPrepCfg, `spawnPrepAgent`
  panics on `o.runner.SpawnAgent`. After Commit 6 the panic goes
  away (the container path replaces the subprocess path). The test
  body must change to assert the new expected behavior: when
  `o.Spawner` is also nil, `spawnPrepAgent` returns an error ("no
  WorkerSpawner configured"); when `o.Spawner` is a fake, it calls
  `SpawnWorker` with `AgentType="prep"` and the mounts dictated by
  Q1's decision. The assertion style mirrors the
  `TestSpawnCoder_WithoutSpawnerReturnsError` test in
  `worker_spawn_test.go`.

### New tests

Added in the commit matching the site under migration (i.e., the
test change rides with the code change — no separate test commits,
one commit per behavioral change):

1. **`TestScheduleSubtasks_DispatchesCoderViaSpawner`**
   (new in `subtask_scheduling_test.go`)
   - Rig: fresh DB, `FakeWorktreeManager`, `fakeWorkerSpawner` (reused
     from `worker_spawn_test.go`). Parent task in `in_progress`,
     one backlog subtask with `agent_type=coder` and dependencies
     met. Creds env set via `setWorkerCredsPathEnv`.
   - Call `scheduleSubtasks(parent)`.
   - Assert: `fake.spawnCalls` length is 1; `AgentType == "coder"`;
     `WorkerID` is `coder-<taskIDprefix>-<nonce>`-shaped;
     `Branch == "feature/<featureName>"`; `PromptMount` is a
     non-empty path that exists on disk and contains the subtask
     title; `CredsMount` is the env value; `Env["DREM_TASK_ID"]`
     matches the subtask ID.
   - Assert: subtask was reloaded and `AssignedAgentID` is set; the
     corresponding Agent row's `TmuxSession` equals the fake
     container ID; the agent's `AgentType` equals `coder`.
   - Assert: fast-track transitions happened (`sub.Status ==
     StatusInProgress` on reload).
   - Assert: an `worker_spawned` TaskEvent exists for the subtask.
   - Assert: `subtask_scheduled` event landed on `o.events` channel.

2. **`TestScheduleSubtasks_SpawnerFailureFailsFast`**
   (new in `subtask_scheduling_test.go`)
   - Rig: same as above; `fake.spawnResults` returns an error.
   - Call `scheduleSubtasks(parent)`.
   - Assert: no fast-track transitions occurred (subtask still in
     `backlog`); `AssignedAgentID` is nil; a `worker_spawn_failed`
     event exists on the subtask carrying the error message; the
     top-level `scheduleSubtasks` call returned no error (container
     errors are per-subtask, not hoisted).

3. **`TestScheduleSubtasks_WithoutSpawnerSkipsDispatch`**
   (new in `subtask_scheduling_test.go`)
   - Rig: `o.Spawner == nil`, `o.runner == nil` (headless container
     with no spawner wired — shouldn't happen in prod but is the
     safest fail-mode to test). Parent + backlog subtask, creds env
     set.
   - Call `scheduleSubtasks(parent)`.
   - Assert: `scheduleSubtasks` returns nil (no panic; behaves like
     a capacity gate); subtask still in `backlog`; no events.
   - This test documents the contract: scheduleSubtasks tolerates a
     nil Spawner, just as the old path tolerated a nil runner. This
     matters for any future decomposition where the orchestrator
     loads without a Spawner wired during startup.

4. **`TestSpawnReviewerSession_DispatchesViaSpawner`** (commit 3),
   **`TestSpawnFixerSession_DispatchesViaSpawner`** (commit 4),
   **`TestProcessTestingReady_DispatchesFixerViaSpawner`**
   (commit 5). Each mirrors 1's pattern for the appropriate public
   method / processing routine, with the appropriate agent_type
   assertion. These tests live in the existing
   `session_spawning_test.go` / `test_execution_test.go` files.

5. **`TestSpawnPrepAgent_DispatchesViaSpawner`** (commit 6) —
   see Q1 decision; may be simpler or skipped entirely if prep
   migration is punted.

### Contract tests that do NOT change

- `TestCredsMountRequired_Table` — new agent types (e.g., prep) get
  new entries, and the test gets the entry to match. But the table's
  contract — "claude-backed = true, everything else = false" —
  is stable.
- `TestRejectAPIKeyInEnv_Table` — no change.
- `TestBuildSpawnContext_MergerOmitsCredsMount` — no change.

## Rollout order (commits on `worktree-agent-abb68b90`)

1. **Commit 1:** `docs(plans): phase-3.5 subtask dispatch migration plan`
   - This file. No code changes.
   - Must match prior plan density (≥ 400 lines).

2. **Commit 2 (PRIMARY — unblocks T3):**
   `feat(orch): route coder subtask dispatch through container spawner`
   - `internal/orchestrator/subtask_scheduling.go` — swap
     `o.runner.SpawnAgent` for `o.spawnCoder` (or `spawnTypedWorker`
     when `agentType != coder`). Adjust imports and downstream
     assignment/agent-ID semantics.
   - Add 3 new tests to `subtask_scheduling_test.go`:
     `TestScheduleSubtasks_DispatchesCoderViaSpawner`,
     `_SpawnerFailureFailsFast`, `_WithoutSpawnerSkipsDispatch`.
   - Commit body quotes the `"exec claude: executable file not found"`
     reproducer; references this plan.

3. **Commit 3:** `feat(orch): route reviewer-session dispatch through container spawner`
   - `session_spawning.go:107` — `SpawnReviewerSession` feature-review
     fork still uses `o.runner.SpawnAgentInWorktree`; swap to
     `o.spawnReviewer(ctx, &task)`. The plan-review direct-SGLang
     fork is untouched.
   - New test: `TestSpawnReviewerSession_DispatchesViaSpawner`.

4. **Commit 4:** `feat(orch): route fixer-session dispatch through container spawner`
   - `session_spawning.go:248` — `SpawnFixerSession` swaps to
     `o.spawnFixer(ctx, &task)`.
   - New test: `TestSpawnFixerSession_DispatchesViaSpawner`.

5. **Commit 5:** `feat(orch): route test-failure fixer re-dispatch through container spawner`
   - `test_execution.go:139` — `processTestingReady` swaps to
     `o.spawnFixer(ctx, parent)`.
   - New test: `TestProcessTestingReady_DispatchesFixerViaSpawner`.

6. **Commit 6 (conditional):** `feat(orch): route prep-agent fallback through container spawner`
   - `task_prep.go:138` — subject to Q1's decision. If it requires
     adding "prep" to `credsMountRequired` and `promptRequired`,
     that is included in this commit alongside a new row in
     `TestCredsMountRequired_Table`. If the decision is non-trivial
     (e.g., prep needs a different image/contract), this commit is
     SKIPPED and a follow-up plan doc is referenced in the final
     docs commit.

7. **Final commit:** `docs(plans): mark phase-3.5 subtask dispatch migration done + tick containerization`
   - Tick the relevant entries in `docs/prd-containerization.md`.
   - Update any doc paragraphs that say
     `"runner.SpawnAgent is the coder dispatch path"` (session
     inventory will surface them during Commit 2's grep pass).
   - Add a "subtask dispatch migrated to container spawner" section
     to the PRD.
   - Mark this plan done at the top (status line + tests summary).

Each commit is expected to be green on `go vet ./... && go test
-count=1 ./...` on its own. If the `internal/orchestrator` suite
(which baseline runs in ~42.8s) regresses, the failing commit is
the last one to change.

## Validation — T3 canary dry-run

Reproducing on-host after this plan lands requires the running orch
container to be restarted (the new binary picks up the new dispatch
path). Expected behavior post-merge:

    # Inside the orch container:
    drem cli create-task --title="..." --description="... real multi-file work ..."
    # wait: classifier (seconds) → planner (~48s warm) → plan_review
    drem cli approve <task-id-prefix>

After approve, `processInProgress` fires on the next tick, which
calls `scheduleSubtasks`. Each backlog subtask with dependencies met
runs through the migration path. Expected observable effects:

- Host: a file appears at
  `~/.drem/projects/drem-orchestrator/prompts/<subtask-uuid>.md`
  with the rendered coder prompt. (Written by the container-path
  prompt pipeline inside `buildSpawnContext`, atomic tmp+rename.)
- Docker: `docker ps --filter name=drem-worker` shows a new
  container per dispatched subtask, labeled `drem.project=...`,
  `drem.agent_type=coder`, `drem.task_id=<uuid>`.
- Worker logs: `docker logs <container>` shows "execing claude with
  prompt from /home/drem/.drem/prompt.md" as the last line before
  claude takes over.
- DB: the subtask row has `AssignedAgentID` set. The referenced
  Agent row's `TmuxSession` column carries the Docker container ID.
  An `worker_spawned` TaskEvent is recorded.
- Orch logs (in-container, at `/var/lib/drem/drem.log` — NOT docker
  logs stdout; slog-formatted): emit a `subtask_scheduled` line per
  subtask.
- TUI: the subtask's status chip transitions
  `backlog → planning → plan_review → in_progress`, matching the
  fast-track (unchanged from legacy).

Failure modes to check after dry-run:

- If `DREM_WORKER_CREDS_PATH` is unset (or host creds are absent):
  `worker_spawn_failed` event with `reason=policy_violation_api_key`
  or a missing-file error; no container spawned. Correct behavior.
- If `DREM_PROMPT_ROOT_HOST` is unset and `$HOME` is unresolvable:
  `worker_spawn_failed` event with
  `reason=prompt_render_failed`. Correct behavior.
- If the bare repo is unmounted: spawner emits a mount-error; the
  Docker event watcher sees the container never transition to
  running and subsequently dies. Agent row is still created but
  will be picked up by `reconcileOnStartup` on next orch restart
  and re-spawned via `respawnForTask`.

Agent-row schema: NO migration required. `TmuxSession` is reused as
the container handle (documented in `reconcile_containers.go:96-115`
as `taskContainerID`). No other columns are repurposed.

## Known gotchas

- **Orch logs land at `/var/lib/drem/drem.log` inside the container
  (slog-formatted), NOT `docker logs stdout`.** Operator must
  `docker exec orch tail -f /var/lib/drem/drem.log` or mount a bind
  at `~/.drem/projects/<project>/logs/drem.log`.
- **`o.runner` stays non-nil in container mode.** The runner is
  wired at startup for dispatch-limiter + legacy interactive-supervisor
  tmux paths. The migration is about replacing the CALL; not about
  removing or nil-ing the field. Tests that assume
  `o.runner == nil ⇒ container mode` are wrong in both directions
  and should be rewritten.
- **Agent-row shape preservation.** The legacy `runner.SpawnAgent`
  creates an Agent row as part of its flow. The container-path
  `spawnTypedWorker` also creates an Agent row, but later and via
  `recordContainerOnAgent`. The caller can no longer use the return
  value of the spawn call to read `ag.ID`; it must reload the task
  to pick up the `AssignedAgentID` that `recordContainerOnAgent`
  wrote. The migration recipe (§"Migration recipe") lays this out.
- **`featureName` / `agent-<hash>` branch naming.** The legacy
  subtask scheduler derives branch names via `featureName` +
  `agent-<hash>` (see `runner.go`'s spawn path). The container path
  uses `task.WorktreeBranch` directly, and when empty falls back to
  `"feature/" + taskFeatureName(task)`. In practice, subtasks at
  this point in the scheduler have `WorktreeBranch == ""` (they
  inherit the parent's branch context). `buildSpawnContext`'s
  fallback produces `"feature/<taskFeatureName(task)>"`. The
  feature-integration worktree the coder checks out inside the
  container is clone-from-bare, so the branch name is the only
  thing that needs to roundtrip. Verified compatible.
- **`flag.FlagSet.Parse` gotcha — does NOT apply here.** No CLI
  flag work in this plan.
- **Worktree-stale gotcha — DID apply here.** The worktree base
  lagged master (was at `307dbc7`, master at `3abda58`). Rebased
  onto current master before the first commit. Same recipe the
  last subagent documented self-correcting against.
- **`o.runner.CanSpawn()` guard.** The legacy guard stays above the
  spawn call; dropping it is NOT part of this plan. Container
  mode's spawner does not enforce a per-orch capacity cap — that is
  a known gap and a pre-existing Phase-3.6 cleanup item.

## Open questions

### Q1: Do we migrate `spawnPrepAgent`?

The prep-agent call at `task_prep.go:138` runs when
`directPrepCfg == nil` — the subprocess-fallback branch. Prep agents
in the legacy path run the same claude CLI as coders but with a
prep prompt template; they write `task-prep-<id>.json` to the
worktree and exit.

Options:

- **(a)** Migrate. Add `"prep"` to both `credsMountRequired` and
  `promptRequired`. Update the `TestCredsMountRequired_Table` test
  to add the new row. Route through `spawnTypedWorker(ctx, sub,
  string(model.AgentPrep))`. Worker entrypoint already execs claude
  from `/home/drem/.drem/prompt.md`, so the prep prompt flows
  through the same delivery pipeline as the coder prompt.
- **(b)** Punt. Leave the subprocess call in place; add a comment
  that the subprocess path is legacy-only and production should
  always set `directPrepCfg`.

Preferred answer: (a). The parity argument is strong — prep has
always been "coder with a different prompt" in the legacy path, and
the container path already supports arbitrary prompt content. The
commit-body risk is that prep's `.json` output file needs to round-trip
through the container's worktree, but since prep currently writes
`task-prep-<id>.json` into the worktree (which the worker clones
from `/bare`), and agentmon already ingests per-task artifacts from
that worktree, the round-trip is already covered.

Commit 6 implements (a) unless a second look at prep's output
ingestion contract shows a bigger problem. If so, Commit 6 is
skipped and this plan gets a follow-up entry.

### Q2: Parent context in container-path prompts

Today `buildSpawnContext` does not pass `ParentCtx` to
`prompt.Generate`. The legacy subtask scheduler passes
`{parent_title, parent_description, feature_branch}`. The rendered
markdown loses these fields under container mode.

Options:

- **(o1)** Ship the primary commit without ParentCtx; let the task
  description carry enough context. Lowest-risk for T3.
- **(o2)** Extend `buildSpawnContext` to accept a variadic
  `prompt.Opts`-builder or a `parentCtx map[string]any`. Touches
  the signatures of every `spawnCoder/spawnReviewer/spawnFixer/
  spawnSupervisor`. Bigger blast radius.

We go with **o1** for the primary commit. A follow-up issue/plan
can implement o2 if the T3 canary surfaces parent-context as
load-bearing.

### Q3: Follow-up migrations not in this session's scope

- `quickfix_processing.go:103,150` — `o.runner.SpawnAgent(task,
  featureName, model.AgentCoder, prompt)` for quick-fix coder
  spawns. Same shape as the primary migration but runs on a
  different trigger (failed coder re-dispatch). Recommend a Phase
  3.5b plan covering these two + `context_monitor.go:237` in a
  single follow-up session.
- `classifying.go:90` — explicitly excluded. Warm classifier is
  the prod path.
- `task_processing.go:248` — explicitly excluded. Warm planner is
  the prod path.

These are tracked but not landed this session.

### Q4: runner-capacity-gate semantics in container mode

Today the scheduler has `if o.runner == nil || !o.runner.CanSpawn()
{ break }`. In container mode, `runner` is non-nil (wired for
legacy tmux-supervisor shim) and its `CanSpawn` reports a cap the
container-spawner doesn't respect. Two outcomes:

- If `CanSpawn == true`: the scheduler dispatches via
  `spawnCoder`. Container spawned. Fine.
- If `CanSpawn == false`: the scheduler breaks out of the loop
  before dispatch. But container mode has no actual capacity limit
  — the spawn would have succeeded. We lose throughput, but we do
  not regress: a subsequent tick re-enters the same loop and the
  runner's dispatch accounting is stale-but-bounded because no
  subprocess dispatches are now happening.

The cleanup is to drop the `CanSpawn` guard when `o.Spawner !=
nil`. That is a separate change — Phase 3.6 — and does not belong
in this plan. We accept the minor throughput regression for now
(the dispatch limiter's cap is high enough that the regression is
theoretical in a lightly-loaded canary).

## References

- `docs/prd-containerization.md` — overall scope + phase plan.
- `docs/containerization/prompts/12-orchestrator-integration.md` —
  the original orch-migration plan (§6-7 migrated task_processing
  and session_spawning in intent, but the actual code migration for
  subtask dispatch never landed; this plan completes that).
- `docs/containerization/prompts/13-agent-spawn-routing.md` — the
  agent-spawn-routing design; explains why `internal/agent` is the
  narrow port and why the orchestrator's responsibility is the
  caller-side migration.
- `ARCHITECTURE.md` — import ceilings + state-machine invariants.
- `plans/worker-prompt-delivery.md` — prompt pipeline this
  migration builds on.
- `plans/worker-subscription-auth.md` — creds pipeline this
  migration builds on.
- `plans/drem-project-register-update.md` — per-project compose
  reconciliation (the compose file that sets
  `DREM_WORKER_CREDS_PATH` + `DREM_PROMPT_ROOT_HOST` is generated
  by `drem project register --update`).
- `internal/orchestrator/worker_spawn.go` — the target of the
  migration.
- `internal/orchestrator/reconcile_containers.go:133-155` — already
  demonstrates the post-spawn recovery pattern (`respawnForTask`).
- `internal/orchestrator/merge_dispatch.go` — already-migrated
  merger path, used as a reference.

## Post-merge verification checklist

- [ ] `go vet ./...` clean on every commit.
- [ ] `go test -count=1 ./...` green on every commit.
  Baseline run time for `internal/orchestrator`: ~42.8s.
- [ ] All new tests exercise the `fakeWorkerSpawner` (or reuse
      helpers from `worker_spawn_test.go`); no new tests reach for
      the real `internal/agent` runner.
- [ ] `grep -rn 'runner.SpawnAgent' internal/orchestrator` after
      migration shows exactly: `classifying.go:90`,
      `task_processing.go:248`, and the two sites in
      `quickfix_processing.go` (+ any unchanged references in
      `orchestrator.go` doc comments). No new appearances in
      `subtask_scheduling.go`, `session_spawning.go`,
      `test_execution.go`, or (per Q1) `task_prep.go`.
- [ ] `docs/prd-containerization.md` has an updated "Modified
      modules" entry covering the new call sites, and the
      subtask-dispatch tick is moved to "done".
- [ ] Plan doc at top is marked "implemented, YYYY-MM-DD" with
      concrete commit range.
