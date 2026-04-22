# Container Agent Branch Persistence — Fix Plan

Status: **implemented, 2026-04-21.** Commits `456123a` (failing
regression tests) and `13aedaf` (fix) on branch
`worktree-agent-a82bd420`.

Dispatched as Bug B of a parallel two-subagent fix-up after the
v13 canary (task `a23ebaa2-157b-492b-83c1-2a199490268c`, subtask
`777f6507-5a8c-42d8-9d86-0e5dd8566573`) failed end-to-end this
morning. Bug A (agentmon 401s blocking heartbeat propagation) is
owned by the sibling subagent; scope boundary is strictly the
orch spawner plus the Agent-row persistence path.

## 1. Symptom

The v13 canary subtask produced a real coder run: the container
executed, the watchdog committed, `git push origin
feature/777f6507-...` succeeded against the bare repo, and the
feature branch tip ended up at `207fa17 [watchdog] wip
2026-04-21T20:35:44Z` carrying both files the coder was asked to
produce (`canary_v13.go` + `canary_v13_test.go`). The container's
Docker labels carried the correct metadata:

```
drem.agent_type=coder
drem.branch=feature/777f6507-write-tests-for-canaryv13marker-struct
drem.project=00db37cc-1cd3-412c-ae1e-f70e3e7921dc
drem.task_id=777f6507-5a8c-42d8-9d86-0e5dd8566573
drem.worker_id=coder-777f-2a39
```

But the orch HTTP API returned the matching Agent row with
`branch` and `current_task` empty, and `last_heartbeat ==
started_at` to microseconds:

```json
{"id":"2e875b27-...","container_id":"e328d07e110f...",
 "project":"drem-orchestrator","agent_type":"coder","branch":"",
 "status":"dead","started_at":"2026-04-21T20:34:43.967175524Z",
 "last_heartbeat":"2026-04-21T20:34:43.967071322Z",
 "current_task":""}
```

Because `WorktreeBranch` was empty on the Agent row, the
reconciler's commit-check guard in
`internal/orchestrator/reconcile_stuck.go:99`
(`if featureDir != "" && ag.WorktreeBranch != ""`) short-circuited
— `hasCommits` stayed `false` — and the task was failed with
"agent session died without producing commits" even though the
watchdog had pushed real work.

## 2. Root cause

`internal/orchestrator/worker_spawn.go:recordContainerOnAgent`
(pre-fix around line 482) and its sibling
`updateAgentContainer` (pre-fix around line 520) both silently
dropped `WorktreeBranch`. Two call paths, both broken:

- **Create-synthetic path** (lines 497-507 pre-fix): the Agent
  row was created with `AgentType`, `CurrentTaskID`,
  `TmuxSession`, `ModelID`, `HeartbeatAt`, but no
  `WorktreeBranch`. The field defaulted to the zero value of
  `string`, which is `""`.
- **Update-existing path** (`updateAgentContainer`, lines 520-
  530 pre-fix): only `TmuxSession`, `ModelID`, `HeartbeatAt`
  were written. `WorktreeBranch`, `CurrentTaskID`, and
  `AgentType` were all passed through unchanged — which, for a
  row that never had them set in the first place, meant empty
  forever.

The v13 incident hit the create-synthetic path (no prior
`AssignedAgentID` on the subtask), but the update path carried
the same latent bug for any scenario that recycled an Agent row
(retry after a dead agent, legacy host-path agents re-attached
to a container, prep-agent handoffs). Both needed to be fixed or
the bug would recur under a different trigger.

`buildSpawnContext` already derives the correct branch into
`swc.branch` and the spawner uses it to populate
`params.Branch`, `DREM_BRANCH`, and the `drem.branch` label.
The only gap was the final step: copying that string onto the
Agent row.

## 3. Fix

Commit `13aedaf`. Two surface changes, one call-site change.

### `recordContainerOnAgent` signature

Before:
```go
func (o *Orchestrator) recordContainerOnAgent(
    task *model.Task, containerID, image, agentType string,
) error
```

After:
```go
func (o *Orchestrator) recordContainerOnAgent(
    task *model.Task, containerID, image, agentType, branch string,
) error
```

The create-synthetic branch now sets `WorktreeBranch: branch`
alongside the existing fields. Status stays `AgentWorking` —
the spawner has just handed back a live container, so the row
should reflect that; subsequent transitions are owned by the
heartbeat / exit-event paths.

### `updateAgentContainer` signature

Before:
```go
func (o *Orchestrator) updateAgentContainer(
    ag *model.Agent, containerID, image string, now time.Time,
) error
```

After:
```go
func (o *Orchestrator) updateAgentContainer(
    ag *model.Agent, containerID, image, agentType, branch string,
    taskID uuid.UUID, now time.Time,
) error
```

The update path now writes `WorktreeBranch = branch`,
`CurrentTaskID = &taskID`, and (when non-empty) `AgentType`
unconditionally. A pre-existing Agent row cannot be trusted to
carry the right branch for the current spawn — row recycling on
retry is a documented scenario — so the write is non-conditional.
An empty `branch` argument would be a spawn-context bug that
`buildSpawnContext` already catches upstream (`branch` always
resolves to either `task.WorktreeBranch` or the synthetic
`"feature/" + taskFeatureName(task)` fallback).

### Call site in `spawnTypedWorker`

Pass `params.Branch` (not `task.WorktreeBranch`) to
`recordContainerOnAgent`:

```go
if err := o.recordContainerOnAgent(
    task, res.ContainerID, params.Image, agentType, params.Branch,
); err != nil { ... }
```

`params.Branch` is the canonical branch the spawner actually
used for the container's env/labels. Using it keeps the Agent
row's `WorktreeBranch` in lockstep with `DREM_BRANCH` and
`drem.branch` regardless of whether `buildSpawnContext` took the
task-provided branch or the synthetic fallback — so a future
caller that dispatches with an empty `task.WorktreeBranch`
(which, per the subtask branch-missing sentinel, would be
rejected at `ensureWorkerBranch`; but for robustness) can still
produce a well-formed Agent row if it somehow gets past the
gates.

## 4. Verification

Regression tests (commit `456123a`, red before fix, green after):

- `TestRecordContainerOnAgent_CreatePathPopulatesBranchAndTask`
  — drives `spawnCoder` against a task with
  `WorktreeBranch="feature/abc"` and no `AssignedAgentID`,
  reloads the Agent row, asserts `WorktreeBranch`,
  `CurrentTaskID`, and `AgentType` are all populated on the
  synthetic agent.
- `TestRecordContainerOnAgent_UpdatePathPopulatesBranchAndTask`
  — pre-creates an Agent row with empty `WorktreeBranch` and
  nil `CurrentTaskID`, assigns it to a task, drives
  `spawnCoder`, reloads the same Agent row, asserts the branch
  and task fields are now populated (without creating a second
  row).

Full test run: `go test ./internal/...` green across all 37
packages. `go vet ./...` clean.

## 5. Linkage

This commit closes the reconciler-side gap left open after the
`plans/reconciler-stale-worktree-fix.md` work:

- `resolveFeatureWorktree` in `orchestrator.go:628` (commit
  `90a4ea2`) already handles the subtask case by falling back
  to the parent task's `WorktreeBranch` via the task row, so
  the reconciler can resolve a feature directory regardless of
  whether the Agent row carries the branch.
- But `reconcile_stuck.go:99` still requires
  `ag.WorktreeBranch != ""` before running
  `BranchHasNewCommits`. Without this guard being satisfied,
  the reconciler skips the commit-check and treats the worker
  as dead-with-no-output.
- This commit makes the spawner produce Agent rows that satisfy
  that guard, without touching the reconciler itself.

No change to `reconcile_stuck.go` was needed. The spawner fix
alone unblocks the downstream path: with
`ag.WorktreeBranch="feature/777f6507-..."`,
`resolveFeatureWorktree` returns the integration worktree path,
`BranchHasNewCommits` returns `true` for the watchdog's push,
and `synthesizeCompletion` takes over — the exact happy-path
the reconciler was designed for.

## 6. Out of scope

- **Agentmon 401s (Bug A)**: sibling subagent's scope.
- **The reconciler itself**: its contract is correct; the gap
  was upstream in the spawner.
- **Migrations for already-failed v13-shaped tasks**: operator
  can re-dispatch the parent task after Kyle's v14 canary
  confirms the fix.

## 7. Acceptance criteria

- [x] `go test ./internal/orchestrator/... ./internal/...` green.
- [x] Two `TestRecordContainerOnAgent_*` regression tests added
      to `internal/orchestrator/worker_spawn_test.go`, failing
      at HEAD before the fix, passing after.
- [x] Both the create-synthetic and update-existing paths write
      `WorktreeBranch`, `CurrentTaskID`, `AgentType`, and
      `TmuxSession` on the Agent row.
- [x] Call site passes `params.Branch` (the canonical branch)
      rather than reading `task.WorktreeBranch` directly.
- [x] This plan doc committed alongside the fix, per the repo's
      docs-as-acceptance-criteria rule.
