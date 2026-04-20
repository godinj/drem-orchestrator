# Container Subtask Branch Provisioning — Implementation Plan

Status: **implemented, 2026-04-20.** Landed as three commits on
worktree-agent-aa07eda4 (`c0956b3..64aa068`) plus this plan-status
update. Closes the T3 canary v5 subtask-spawn regression where the
worker container's `git clone --branch` hit a missing ref in the bare
repo.

Tests added:

- `internal/gitref/git_test.go` — 4 new
  (EnsureBranch creates-when-missing, idempotent-when-present with
  in-flight tip preserved, errors on missing source, rejects empty args).
- `internal/orchestrator/worker_spawn_test.go` — 3 new
  (subtask branches off parent, respawn preserves in-flight commits,
  missing-parent-branch fails closed with branch_missing reason).

Test rigs migrated to real bare repos (commit `53fb8cb`):
`workerSpawnTestRig`, `dockerEventsTestRig`, and `reconcileTestRig`
now call `testutil.SetupBareRepo(t)` instead of the `/tmp/fake-bare`
literal. `workerSpawnTestRig` returns the bare path as a third value
so tests that seed parent branches can use the new
`pushTestFeatureBranch` helper. `dispatchMergeTestRig` is left on the
literal: the merger path calls `Spawner.SpawnWorker` directly and
never reaches `spawnTypedWorker`, so branch-ensure does not fire.

All tests pass on `go test -count=1 ./...` in under 2 minutes;
`go vet ./...` is clean.

## 1. Problem

T3 canary v5 reached the point where orch dispatched a coder subtask
through the spawner, the worker container spun up with all three
bind mounts in place (prompt, creds, bare repo), passed
`worker-entrypoint.sh`'s required-env guard, then died with:

```
fatal: Remote branch feature/<subtask-uuid>-... not found in upstream origin
```

The root cause is that the pre-container `runner.SpawnAgent` path
created each subtask branch as a side effect of `git worktree add -b
<branch>`. The container migration dropped host-side worktrees, so
nothing now creates the subtask branch in the bare repo. When the
worker tries `git clone --branch "${DREM_BRANCH}" /bare /home/drem/work`,
the ref doesn't exist and the clone fails.

## 2. Fix shape

Pre-create the feature branch in the bare repo before the spawner
is asked to create the worker container. The new primitive is
idempotent, refuses to move the branch tip, and records an explicit
audit reason when the source branch the subtask is supposed to
fork off of can't be resolved.

### 2.1 `internal/gitref/git.go` — `EnsureBranch`

Add a fourth function next to `BranchExists`, `HeadCommit`, and
`DefaultBranch`:

```go
// EnsureBranch creates refs/heads/<branch> in the bare repo pointing at
// refs/heads/<fromBranch>, if <branch> does not already exist. It is a
// no-op when the branch exists (the tip is NOT force-reset — a live
// worker may have pushed commits already, and clobbering those is the
// exact failure mode branch provisioning exists to avoid).
//
// Returns an error when fromBranch does not exist (so callers cannot
// silently fork off a missing ref), or when any git invocation fails
// outside the "branch already exists" / "branch does not exist" sentinels.
func EnsureBranch(ctx context.Context, bareRepo, branch, fromBranch string) error
```

Implementation:

1. Validate every arg is non-empty; return a wrapped error on any zero value.
2. `BranchExists(ctx, bareRepo, branch)` — if true, return nil (idempotent no-op).
3. `BranchExists(ctx, bareRepo, fromBranch)` — if false, return an error
   identifying the missing source branch. Never create an empty branch.
4. `git --git-dir=<bareRepo> branch <branch> <fromBranch>` — plain
   `git branch` is exactly the verb we want: it refuses to overwrite
   an existing ref (guarded by step 2 anyway), takes a start point,
   and produces no working-tree side effects. `update-ref` also works
   but loses the start-point-validation path and would need an extra
   rev-parse.

Tests (`internal/gitref/git_test.go`):

- `TestEnsureBranch_CreatesWhenMissing` — seeds a bare repo with `main`,
  calls `EnsureBranch(..., "feature/x", "main")`, then asserts
  `BranchExists(feature/x) == true` and `HeadCommit(feature/x)` matches
  `HeadCommit(main)`.
- `TestEnsureBranch_IdempotentWhenPresent` — pre-creates `feature/x` with
  one commit on top of `main`, captures `HeadCommit(feature/x)`, calls
  `EnsureBranch(..., "feature/x", "main")` and asserts the tip did NOT
  move back to `main`. This is the guard that says "don't clobber a
  worker that already pushed."
- `TestEnsureBranch_ErrorsOnMissingSource` — asserts fork-from-ghost
  fails. Keeps accidental "branch off /dev/null" out of the code path.
- `TestEnsureBranch_RejectsEmptyArgs` — table-driven: empty `bareRepo`,
  `branch`, and `fromBranch` each return an error.

### 2.2 `internal/orchestrator/worker_spawn.go` — wire into `spawnTypedWorker`

Add two helpers + one policy-reason constant:

```go
// spawnPolicyReasonBranchMissing is the classifier attached to
// worker_spawn_failed events emitted when the subtask's parent carries
// no WorktreeBranch. Surfacing this separately from the generic spawn
// error makes the upstream planning gap visible in audit queries
// rather than masquerading as a git error.
const spawnPolicyReasonBranchMissing = "branch_missing"

// resolveBranchSource returns the branch that a to-be-created feature
// branch should fork off of. For parent tasks (no ParentTaskID), it
// returns the bare repo's default branch. For subtasks, it returns
// the parent's WorktreeBranch — so every subtask branches off the
// feature branch its parent carries. An empty parent WorktreeBranch
// is fail-closed (returns an error); "branch off main" for a subtask
// would silently mask a planning gap upstream.
func (o *Orchestrator) resolveBranchSource(ctx context.Context, task *model.Task, bareRepo string) (string, error)

// ensureWorkerBranch pre-creates the feature branch in the bare repo
// so the worker container's `git clone --branch <branch>` succeeds.
// Idempotent: if the branch already exists, the tip is not moved.
func (o *Orchestrator) ensureWorkerBranch(ctx context.Context, task *model.Task, swc spawnWorkerContext) error
```

In `spawnTypedWorker`, between `buildSpawnContext` and the
`rejectAPIKeyInEnv` policy check, call `ensureWorkerBranch`. On
failure, record a `worker_spawn_failed` event with the
`branch_missing` classifier (or an empty classifier for a generic
git failure) and return without calling the spawner.

Tests (new, in `worker_spawn_test.go`):

- `TestSpawnTypedWorker_SubtaskBranchesOffParent` — seeds a parent
  task with `WorktreeBranch: "feature/parent-x"` pre-created in the
  bare repo, then drives `spawnCoder` on a subtask whose
  `WorktreeBranch: "feature/sub-x"`. Asserts `BranchExists(feature/sub-x)`
  after spawn and that its tip matches `HeadCommit(feature/parent-x)`.
- `TestSpawnTypedWorker_IdempotentPreservesInFlightCommits` — creates
  `feature/x` with an extra commit on top of `main`, captures the tip,
  drives a respawn through `spawnCoder`, and asserts the tip did NOT
  move back to `main`. The scenario is a worker that pushed work,
  the container died, and the event-driven respawn path is about to
  create a fresh container against the same branch.
- `TestSpawnTypedWorker_SubtaskWithMissingParentBranchFailsClosed` —
  subtask whose parent has `WorktreeBranch == ""`. Asserts
  `spawnCoder` returns an error, the spawner is NOT called, and a
  `worker_spawn_failed` event was recorded with
  `details.reason = "branch_missing"`.

### 2.3 Test rig migration

The four orchestrator test files that wire `Spawner:` on the
`Orchestrator{...}` literal today use `FakeWorktreeManager{BarePath:
"/tmp/fake-bare"}` plus `project.BareRepoPath: "/tmp/fake-bare"`.
With `EnsureBranch` firing from `spawnTypedWorker`, every test that
drives a spawn needs a real bare repo so git can actually create
the ref. Migrate:

- `worker_spawn_test.go::workerSpawnTestRig` — replace the `/tmp/fake-bare`
  literals with `testutil.SetupBareRepo(t)`.
- `docker_events_test.go::dockerEventsTestRig` — same migration; the
  respawn tests invoke `spawnCoder` via `handleWorkerDeath`.
- `reconcile_containers_test.go::reconcileTestRig` — same migration;
  `TestReconcileOnStartup_GoneContainersRespawn` drives a `spawnCoder`.
- `merge_dispatch_test.go::dispatchMergeTestRig` — merger path calls
  `Spawner.SpawnWorker` **directly**, not through `spawnTypedWorker`,
  so `EnsureBranch` never fires here. Leaves the rig unchanged. The
  merger's branch assumption is that the feature branch already
  exists (it was created during coder/reviewer phases) — that
  assumption is reinforced by this plan, not weakened.

Small test-only helper for tests whose parent task carries a non-default
`WorktreeBranch`:

```go
// pushTestFeatureBranch creates the given feature branch in the bare
// repo with one seed commit, so subsequent EnsureBranch calls can fork
// off it. Mirrors the pattern used by gitref_test.go::pushBranch.
func pushTestFeatureBranch(t *testing.T, bareRepo, branch string)
```

Add to a shared test helper location inside `internal/orchestrator/`.
Callers: tests that seed a parent task whose `WorktreeBranch` is
non-empty and non-default (e.g. `feature/parent-x`).

## 3. What stays

- `runner.SpawnAgent` path (legacy tmux/worktree) is untouched; that
  path still creates its branch as a side effect of `git worktree add -b`.
  Only the container path needs this fix.
- The merger's `dispatchMerge` path is untouched; merger operates on
  an already-existing feature branch and explicitly does NOT go through
  `spawnTypedWorker`.
- `buildSpawnContext`, `rejectAPIKeyInEnv`, `recordContainerOnAgent`,
  and the events-table audit machinery are unchanged.

## 4. Why these alternatives were rejected

- **"Create the branch inside the worker via a pre-clone step."** The
  worker's bare-repo bind is read-only; letting it write to the bare
  would weaken the RO-by-default stance the spawner enforces today.
- **"Have the spawner create the branch."** The spawner's API surface
  is typed at "create container, destroy container"; expanding it to
  "create branch" blurs the ownership line. Branch provisioning is
  orch's job because orch knows the task graph.
- **"Fork subtasks off main when parent branch is missing."** Silently
  masks a planning gap upstream. If a subtask arrives without a parent
  branch, that's a bug — surface it as a `branch_missing` audit event
  so the operator can see it.
- **"Force-reset the branch on every ensure call."** Would clobber
  an in-flight worker's pushed commits on any respawn. The idempotent
  no-op is specifically the guarantee that's compatible with the
  event-driven respawn loop.

## 5. Commit plan

1. `feat(gitref): add EnsureBranch for idempotent bare-repo branch creation`
   — the gitref primitive + its 4 tests.
2. `feat(orch): pre-create subtask feature branch before container spawn`
   — `worker_spawn.go` wiring + its 3 tests + the
   `spawnPolicyReasonBranchMissing` constant.
3. `test(orch): migrate spawn-test rigs to real bare repos` — rig
   updates in the three spawn-driving test files, plus any downstream
   test adjustments that fall out of the migration.
4. `docs(plans): mark subtask-branch-provisioning plan implemented`
   — flip this file to **implemented** and add a reference line in
   `plans/containerization.md` Phase 2 acceptance so the plan trail
   is discoverable.

Each commit's pre-commit hooks must pass on its own; `go vet ./...`
and `go test -count=1 ./...` clean at every landed commit.

## 6. Validation

- Unit: `go test -count=1 ./internal/gitref/... ./internal/orchestrator/...` green.
- Full: `go test -count=1 ./...` green, under 2 minutes.
- Canary: operator re-runs T3 canary against the merged binary.
  `drem watch` should observe the subtask coder container staying alive
  past its `git clone` step and getting to work.

## 7. Open questions

None. The fix shape is intentionally narrow: one gitref primitive,
one orch hook, one test-rig sweep.
