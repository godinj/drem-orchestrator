# Reconciler × Stale Integration Worktree — Fix Plan

> Superseded by Phase 4 in `plans/orchestration-meta-analysis.md`. The
> reconciler no longer derives task completion from branch topology; do not
> implement the proposed control paths below.

Status: **proposed, 2026-04-20.** Plan only. No implementation
in this commit; this document is the scoping artefact so the
implementation can be dispatched as a separate worktree once the
approach is signed off.

Origin: Seth's CTO review flagged this as MAJOR 2 after the
bare-repo `denyCurrentBranch=ignore` flip landed (commits
`f664cd5`, `247335a`). The pivot from `updateInstead` to `ignore`
is correct — `updateInstead` was architecturally broken by the
worker-container path mismatch (see
`plans/bare-repo-denyCurrentBranch.md` §0b) — but it creates a
new latent interaction: host-side integration worktrees now go
stale on every successful worker push, and the reconciler's
orphan-recovery path assumes the worktree's working tree is a
faithful reflection of its HEAD. It isn't. When those two
assumptions collide, the reconciler manufactures a revert commit
on top of the worker's pushed work.

This plan also folds in a sibling reconciler correctness bug
("failed task's feature branch already merged to default,
transitioning to done" treats a missing branch ref as a
successful merge); justification for folding is in §8.

## 0. Goal

Two reconciler correctness fixes, landed as a sequence of small
commits on a single feature branch:

1. Orphan-recovery on a post-push feature worktree must never
   stage the post-push state as "leftover changes" and commit a
   revert on top of the feature branch.
2. Branch-absence must not be interpreted as "work was merged."
   A missing feature branch is one of the failure modes the
   reconciler has to recognise, not paper over.

Both bugs live in the reconciler because the reconciler is
designed to be a safety net for orchestrator-visible state
drifting out of sync with git reality, and both bugs amount to
the safety net making false assumptions about what git reality
looks like after a crash.

## 1. Symptom

### 1a. Revert-commit manufacture (MAJOR 2)

Sequence that reaches the bug:

1. Worker container is dispatched for subtask `S` under parent
   feature `F`. The worker clones / fetches, does work on the
   agent branch, and the watchdog runs the final
   `git push origin feature/<id>` to the bare repo at
   `/home/godinj/git/drem-orchestrator.git`.
2. The bare repo has
   `receive.denyCurrentBranch=ignore`. The push to
   `feature/<id>` succeeds. The bare ref now points at the
   worker's new tip. The receive-pack does **not** touch any
   worktree.
3. The host-side integration worktree at
   `/home/godinj/git/drem-orchestrator.git/feature/<id>/integration`
   is checked out on `feature/<id>`. Its working directory was
   populated when `processPlanning` called
   `worktreehost.Manager.CreateFeature` (see §2). The HEAD
   symbolic ref still points at `feature/<id>` — whose tip is
   now the new commit — but the working-dir files on disk are
   the pre-push content.
4. The worker container exits (clean or crashed) before the
   orchestrator's docker-events stream delivers the exit event.
   Common causes: container OOM, host docker restart, host
   reboot, watchdog segfault, or simply the event-stream
   reconnect window.
5. The reconciler tick fires (~60s cadence). Orphan-agent
   recovery at
   `internal/orchestrator/reconcile.go:270` runs
   `gitexec.CommitUnstagedChanges` against the integration
   worktree, because the reconciler believes the worktree might
   have uncommitted leftover state (plan.json, etc) that would
   block the subsequent merge.
6. `CommitUnstagedChanges` does `git add -u --` (stage all
   tracked modifications relative to HEAD), then `git commit`.
   Because the worktree's HEAD advanced when the worker pushed
   in step 2 **but the working-dir files did not**, every
   worker-modified file looks to `git status` like a
   modification of HEAD in the *reverse* direction — files the
   worker added look "deleted," files the worker deleted look
   "added back," files the worker modified look "modified back
   to the prior content." `git add -u` stages those reversions.
   `git commit` creates a revert commit on top of the worker's
   new tip.

What an operator sees:

- `drem tasks` shows subtask `S` as done or failed (depending on
  the next merge step). The feature branch log shows the
  worker's commits followed by one extra orchestrator-authored
  commit whose message is "Auto-commit uncommitted feature
  worktree changes (reconcile)".
- Running `git show` on that commit reveals a reversion of
  exactly the worker's diff.
- Outcome is one of:
  - **(a)** The reconciler subsequently merges the (now
    revert-polluted) feature branch into integration/default,
    and the work appears to have no effect. The operator has to
    inspect `git log` to discover the revert; the default branch
    compiles but the feature payload is silently absent.
  - **(b)** The revert commit conflicts with something else on
    the merge target — another subtask's work, a concurrent
    merger, the default branch's drift — and the merge fails
    with conflicts that don't correspond to any human-authored
    change, producing a confusing quickfix dispatch or a
    failed-task record with conflicting-hunk output that
    references files the worker already handled.

In both cases the corruption is written to the feature branch
ref, which means re-running the reconciler won't unstick it —
the bad commit has to be removed by hand or the branch
rewritten.

### 1b. Missing-branch false-positive (sibling bug)

Sequence:

1. Task `T` fails. Some failure-path cleanup (or a subsequent
   failed merge retry) deletes the feature branch ref.
2. Reconciler's `reconcileAlreadyMergedFeatures`
   (`internal/orchestrator/reconcile_parents.go:20`) queries
   failed tasks whose feature branch is an ancestor of the
   default branch's HEAD. When `git merge-base --is-ancestor
   <missing-branch> HEAD` is invoked against a missing branch,
   the command errors and `continue` skips the task — so **this
   specific call path does not corrupt state on missing
   branch**. The one that does corrupt is the orphan-recovery
   path in `reconcile.go:277-280`, where
   `BranchHasNewCommits` erroring on a missing agent branch is
   caught with `if err != nil { merged = true }` — a literal
   "assume merge happened" short-circuit. That comment is the
   bug.
3. The subtask transitions to done; the parent task's
   completion check fires; the parent transitions to done.

Operator sees: `v10` and `v11` T3 canary tasks both reached
`status=done` in the database despite never producing a
successful merge, and their feature branches were absent from
the bare repo. Inspecting the task-event timeline shows a
`reconcile-already-merged` or analogous reason, even though no
integration merge was ever run for those tasks.

## 2. Evidence

Four file:line references, quoted. All paths rooted at the repo
root.

### 2a. Worktree is checked out on the feature branch

`internal/worktreehost/manager.go:134-162` (function
`CreateFeature`):

```go
// CreateFeature creates a feature worktree at
// <bare-repo>/feature/<name>/integration/ with branch feature/<name>.
func (m *Manager) CreateFeature(name string) (*WorktreeInfo, error) {
	branch := ensurePrefix(name)
	featureName := strings.TrimPrefix(branch, featurePrefix)
	groupDir := m.FeatureGroupDir(featureName)
	integrationDir := m.FeatureWorktreePath(featureName)
	...
	_, err := RunGit([]string{
		"worktree", "add", "-b", branch, integrationDir,
	}, m.BareRepoPath)
```

`git worktree add -b feature/<name> <integrationDir>` creates a
host worktree whose HEAD symbolic ref is `feature/<name>`. That
is the same ref the worker container pushes to. No code runs
after a worker push to refresh the host worktree's working dir.

### 2b. Reconciler calls CommitUnstagedChanges on the stale worktree

`internal/orchestrator/reconcile.go:268-275`:

```go
// Ensure the feature worktree is clean before merge attempts.
// Leftover changes (e.g. plan.json) block MergeAgentIntoFeature.
if committed, cErr := gitexec.CommitUnstagedChanges(
	context.Background(), featureDir, "Auto-commit uncommitted feature worktree changes (reconcile)",
); cErr != nil {
	o.logger.Warn("reconcile: failed to clean feature worktree", "feature", featureBranch, "error", cErr)
} else if committed {
	o.logger.Info("reconcile: committed leftover changes in feature worktree", "feature", featureBranch)
```

The comment reveals the mental model: the reconciler thinks the
only thing it could be staging here is stray plan.json /
`.claude/settings.json`. It has no concept of "the branch ref
advanced underneath the worktree." It cannot distinguish a
genuine uncommitted plan.json from a complete worker diff
inverted by a stale working tree.

### 2c. CommitUnstagedChanges stages `git add -u` blind

`internal/gitexec/gitexec.go:112-141`:

```go
func CommitUnstagedChanges(ctx context.Context, dir, message string) (bool, error) {
	clean, err := IsClean(ctx, dir)
	if err != nil {
		return false, fmt.Errorf("commit unstaged: check clean: %w", err)
	}
	if clean {
		return false, nil
	}
	// Stage only tracked (modified/deleted) files. Using -u instead of --all
	// avoids staging untracked files such as build artifacts, editor configs,
	// and other files not yet in the index, which would cause merge conflicts
	// when multiple agents commit overlapping untracked files.
	if _, err := RunGit(ctx, dir, "add", "-u", "--", "."); err != nil {
		return false, fmt.Errorf("commit unstaged: add: %w", err)
	}
	...
	if _, err := RunGit(ctx, dir, "commit", "-m", message); err != nil {
		return false, fmt.Errorf("commit unstaged: commit: %w", err)
	}
```

`git add -u` stages the diff between the working tree and the
index. After a background ref-advance, the index still reflects
the old tip but HEAD points at the new tip. Staging the working
tree's diff and committing produces a commit that reverts HEAD
to whatever the working tree happens to contain. The function
has no guard that compares branch tip to worktree HEAD before
running — its contract is "best-effort clean-up" but it gives
back zero signal about what it actually committed.

### 2d. Missing-branch false-positive in orphan recovery

`internal/orchestrator/reconcile.go:277-284`:

```go
hasCommits, err := gitexec.BranchHasNewCommits(context.Background(), featureDir, ag.WorktreeBranch)
if err != nil {
	// Branch likely already cleaned up — assume merge happened.
	merged = true
} else if hasCommits {
	result, mergeErr := o.mergeAgentBranchIntoFeature(context.Background(), ag.WorktreeBranch, featureDir)
	if mergeErr != nil {
		o.logger.Error("reconcile: merge agent into feature failed",
```

The `// Branch likely already cleaned up — assume merge
happened.` comment is load-bearing. `BranchHasNewCommits`
internally runs `git rev-list --count HEAD..<branch>`; that
command errors on many failure modes beyond "branch cleaned up
because merge happened" — malformed ref, permission error,
corrupt index, *and* "branch was deleted by a failed-merge
rollback that should instead be a task failure." Treating all of
those as "merge happened" is exactly the false-positive Seth
flagged for v10/v11.

## 3. Reproducer

Deterministic local reproduction of the revert-commit bug. This
is roughly what a regression test needs to mechanise.

```bash
set -euo pipefail

# Setup: a bare repo with denyCurrentBranch=ignore and a feature
# worktree checked out on feature/repro.
TMP=$(mktemp -d)
BARE="$TMP/bare.git"
git init --bare "$BARE"
git -C "$BARE" config receive.denyCurrentBranch ignore
# Seed an initial commit so HEAD resolves.
SEED=$(mktemp -d)
git clone "$BARE" "$SEED"
cd "$SEED"
git checkout -b main
echo "seed" > README.md
git add README.md && git commit -m "seed"
git push origin main
cd -

# Create the integration worktree (mimics processPlanning).
git -C "$BARE" worktree add -b feature/repro "$BARE/feature/repro/integration" main

# A separate clone stands in for the worker container.
WORKER=$(mktemp -d)
git clone "$BARE" "$WORKER"
cd "$WORKER"
git fetch origin
git checkout -b feature/repro origin/feature/repro
echo "worker change" >> README.md
echo "new file" > NEW.txt
git add -A && git commit -m "worker: do work"
# This push succeeds because of ignore. The integration worktree's
# working-dir does NOT update.
git push origin feature/repro
cd -

# Simulate the reconciler's CommitUnstagedChanges against the
# now-stale integration worktree.
INT="$BARE/feature/repro/integration"
# HEAD is the worker's tip, but working-dir is the seed state.
git -C "$INT" status --short
# The bug: add -u + commit produces a revert.
git -C "$INT" add -u -- .
git -C "$INT" diff --cached --stat
git -C "$INT" commit -m "Auto-commit uncommitted feature worktree changes (reconcile)"

# Confirm the bug.
git -C "$INT" log --oneline feature/repro | head -5
git -C "$INT" show --stat HEAD
# The HEAD commit is a revert of the worker's diff.
```

The regression test in Go will:

1. Create a throwaway bare repo with
   `receive.denyCurrentBranch=ignore` under `t.TempDir()`.
2. Call `worktreehost.Manager.CreateFeature` to create the
   integration worktree exactly as the orchestrator would.
3. In a separate clone, commit + push to the feature branch to
   advance the bare ref.
4. Call whatever function the fix introduces (or the current
   orphan-recovery code path in a no-fix baseline) and assert
   that no new commit is created on the feature branch ref.

The sibling missing-branch bug has a simpler reproducer: fake a
failed task record with `worktree_branch = "feature/does-not-
exist"`, tick the reconciler, assert the task stays `failed` and
does not transition to `done`.

## 4. Fix options

### (a) Detect ref-advance and refresh the worktree

Before calling `CommitUnstagedChanges`, compare the worktree's
working-dir HEAD against the bare repo's branch tip for the same
branch. If they differ, run `git reset --hard <branch>` on the
integration worktree to refresh working-dir to match the branch
tip, *then* run `CommitUnstagedChanges` (which will now see a
clean tree and no-op).

Pros:
- Preserves the "clean up leftover plan.json" intent of the
  original reconcile step.
- Works whether the worker pushed, did not push, or pushed
  partial state.

Cons:
- `git reset --hard` is destructive to any genuine leftover
  state in the working dir (plan.json, `.claude/settings.json`,
  editor scratch files). If any of those ever mattered — say,
  a plan.json that the merger reads for audit — they're gone.
- Adds a git round-trip on every reconciler tick regardless of
  whether the worktree is actually stale.
- Still relies on the host worktree's working dir for
  downstream steps that could instead go through plumbing.

### (b) Skip CommitUnstagedChanges when branch ref is ahead of worktree HEAD

Same detection as (a) — read branch tip and worktree HEAD —
but on mismatch, *skip* the `CommitUnstagedChanges` call
entirely and log a telemetry event. The downstream merge code
already resolves branches via plumbing, so the stale working dir
is a non-issue for correctness; the only risk is if something
else in the reconciler path assumes the working dir matches
HEAD.

Pros:
- Non-destructive. Preserves plan.json / settings if the
  working dir was in fact intentional.
- Lower-latency (one `rev-parse` vs a full reset).
- Correctly signals "something weird happened here" in logs.

Cons:
- Keeps a code path alive that is only useful in a narrowing
  set of cases (pre-push crashes where the worker wrote but
  didn't commit). Those cases are increasingly niche now that
  workers commit locally before push.
- Still has the `CommitUnstagedChanges` function sitting in the
  hot path for some subset of reconciler ticks.

### (c) Remove CommitUnstagedChanges from the orphan-recovery path

Drop the call entirely. The orphan-recovery path's job is to
reconcile orchestrator state with git reality when a worker dies
between commit and exit-event. If the worker pushed, the branch
ref is advanced and the merge-forward code can handle it. If the
worker didn't push, there is no recoverable state in the
host-side integration worktree the orchestrator can rely on:
the worker's work lives inside the container's own worktree,
which the orchestrator cannot reach. Staging the host
worktree's working-dir state after a push is pure noise; staging
it before a push is fishing for plan.json-level leftovers that
the next fresh `git fetch --all && git reset --hard` on the
merger side wipes anyway.

Pros:
- Eliminates the bug by removing the offending step. No
  working-dir state can be committed if no code is trying to
  commit it.
- Simplifies the reconciler: fewer steps, clearer contract
  ("orphan recovery reconciles branch refs, never creates
  commits").
- Aligns with the invariant that the merger is the only code
  authorised to create commits on feature branches during
  integration. A reconciler that also creates commits is a
  second author on the same ref, which breaks the audit trail.

Cons:
- Loses the "clean up leftover plan.json" behaviour for
  pre-push crashes. Need to confirm via grep that nothing
  downstream actually depends on those files being committed
  vs being cleaned up another way (e.g., `gitexec.UntrackEphemeralFiles`
  already exists and targets plan.json specifically).
- Any existing test that asserts "leftover plan.json gets
  committed during reconcile" needs to change its assertion.

## 5. Chosen option: (c) Remove CommitUnstagedChanges from orphan recovery

Justification:

1. **The original intent is better served elsewhere.**
   `UntrackEphemeralFiles` already exists
   (`internal/gitexec/gitexec.go:143+`) and specifically handles
   the plan.json / `.claude/settings.json` case the
   `CommitUnstagedChanges` call was compensating for.
   `UntrackEphemeralFiles` removes those files from the index
   without committing any working-tree content — it solves the
   "plan.json blocks merge" symptom without the "any stale
   working tree becomes a commit" bug. If pre-merge cleanup is
   wanted, swap `CommitUnstagedChanges` for `UntrackEphemeralFiles`
   as the pre-merge step. Either removing outright or replacing
   with `UntrackEphemeralFiles` is fine; the core commit of this
   plan will remove, and a follow-up commit will add the
   targeted cleanup if testing proves it's needed.

2. **Option (a) is destructive in the wrong direction.** A
   `git reset --hard` silently eats any genuine local state,
   which is the opposite of the safety-net posture a reconciler
   should have. When the reconciler is unsure about the state
   of the world, its job is to log loudly and bail, not to
   nuke and proceed.

3. **Option (b) keeps the function call alive for a narrowing
   case.** The cases where `CommitUnstagedChanges` does
   something useful (pre-push crash with unstaged but valuable
   work on the integration worktree) require the worker to
   have written files directly into the host integration
   worktree rather than into its own container worktree.
   Post-containerization, workers write into their own
   per-container worktree, never into the host integration
   worktree. So (b)'s preserved code path covers zero real
   cases in the current architecture.

4. **Seth's framing lines up with (c).** His gut call was "the
   orphan-recovery path is supposed to be a safety net, not a
   work-salvage mechanism." Option (c) is that framing in code
   form.

Sibling bug (missing-branch false-positive) fix: replace the
`if err != nil { merged = true }` short-circuit with an
explicit branch-existence check. If the branch ref is absent,
the subtask goes to `failed`, not `done`, and a telemetry
event is emitted with the reason `missing-branch-after-orphan`.
The same principle applies to `reconcileAlreadyMergedFeatures`
even though its current `continue`-on-error behaviour is
already correct — we'll replace the implicit "err means not
merged" with an explicit `EnsureBranchExists` +
`merge-base --is-ancestor` pair so the intent is readable and
the edge cases documented.

## 6. Commit sequence

Eight commits on a single feature branch. Order is chosen so
each commit is individually revertable and the tree stays green
at every step.

1. **`test(orchestrator): failing regression for reconciler revert-commit on stale worktree`**

   Adds the Go equivalent of §3's reproducer as a test in
   `internal/orchestrator/reconcile_containers_test.go` (or a
   new `reconcile_orphan_test.go` if the table scale warrants
   it). Test fails on master at HEAD `247335a`. Asserts the
   feature branch ref has exactly one commit (the worker's)
   after the reconciler runs, not two.

2. **`test(orchestrator): failing regression for missing-branch false-positive`**

   Adds a sibling test that seeds a failed subtask with a
   worktree_branch that doesn't exist as a ref, runs the
   reconciler, and asserts the subtask stays `failed`. Also
   fails on master.

3. **`fix(orchestrator): drop CommitUnstagedChanges from orphan recovery`**

   Removes the `CommitUnstagedChanges` call at
   `reconcile.go:270` and the surrounding log lines. Updates
   any existing test that expected the call to occur. Commit
   (1)'s regression test flips from red to green.

4. **`fix(orchestrator): fail subtask on missing branch in orphan recovery`**

   Replaces `if err != nil { merged = true }` at
   `reconcile.go:278` with an explicit
   `gitref.BranchExists(bare, ag.WorktreeBranch)` check. When
   the branch is missing, transition the subtask to `failed`
   with reason `reconcile-agent-branch-missing`. When the
   branch exists but the command errored for another reason,
   log and skip (do not assume merge). Commit (2)'s regression
   test flips from red to green.

5. **`fix(orchestrator): explicit branch-existence in reconcileAlreadyMergedFeatures`**

   Even though the current `continue`-on-error behaviour in
   `reconcile_parents.go:40-45` is coincidentally correct, the
   code reads "skip on error" without documenting what the
   error means. Replace with an explicit
   `BranchExists` check up front, so a missing branch is
   clearly a skip (and logged), and the
   `merge-base --is-ancestor` call only runs on branches that
   provably exist. Pure refactor; no behaviour change in
   isolation.

6. **`feat(orchestrator): replace reconciler plan.json cleanup with UntrackEphemeralFiles`**

   Optional follow-on if (3) broke any test that actually
   cared about plan.json cleanup during reconcile. Calls
   `UntrackEphemeralFiles` instead of `CommitUnstagedChanges`,
   which removes plan.json / `.claude/settings.json` from the
   index without ever staging working-tree content. Skipped
   entirely if commit (3)'s diff caused no test regressions.

7. **`docs(reconcile): document orphan-recovery contract`**

   Updates `internal/orchestrator/reconcile.go` package
   comment or adds a new `docs/reconciler.md` (style-matched
   to existing `docs/containerization/install.md`) stating the
   new invariant: the reconciler reconciles *state*, not
   *contents*. It transitions tasks, removes worktrees, and
   resets DB fields; it never creates commits on feature
   branches. Commits on feature branches are the merger's
   sole responsibility.

8. **`docs(plans): mark reconciler-stale-worktree-fix plan implemented`**

   Flips this document's status header from "proposed" to
   "implemented, YYYY-MM-DD." References the commit range.

## 7. Test coverage

Unit:

- `internal/orchestrator/reconcile_orphan_test.go`
  (new file, or append to `reconcile_containers_test.go`):
  - `TestReconcileOrphan_DoesNotCommitOnStaleWorktree` — §3
    regression, post-push stale worktree produces zero new
    commits on the feature ref.
  - `TestReconcileOrphan_MissingBranchMarksSubtaskFailed` —
    subtask with missing `worktree_branch` does not transition
    to `done`, goes to `failed` with expected reason event.
  - `TestReconcileOrphan_BranchExists_WithNewCommits_Merges` —
    happy path regression: when the branch exists and has new
    commits, the orphan-recovery merge still runs (the fix
    should not break the case it used to handle correctly).
  - `TestReconcileOrphan_BranchExists_NoNewCommits_MarksMerged`
    — branch exists, rev-list count is 0, path that used to
    hit the `else` arm at `reconcile.go:296` still flags
    `merged = true`.
- `internal/orchestrator/reconcile_parents_test.go`:
  - `TestReconcileAlreadyMerged_MissingBranch_Skips` — explicit
    coverage for the refactor in commit (5). Missing branch ref
    → task stays `failed`.
  - `TestReconcileAlreadyMerged_IsAncestor_TransitionsToDone` —
    existing happy-path assertion preserved.
- `internal/gitref/git_test.go`:
  - `TestBranchExists_Present` / `TestBranchExists_Absent` —
    if the fix adds a new `BranchExists` helper in
    `internal/gitref/`, cover it directly rather than only
    through reconciler tests.

Integration:

- `internal/orchestrator/reconcile_containers_test.go`:
  - Full reconcile-tick test with a real bare repo + host
    worktree + separate clone pushing to the feature branch.
    Ticks the reconciler and asserts:
    - feature branch ref advanced exactly once (worker's push)
    - no orchestrator-authored commit on the feature branch
    - subtask transitions follow the expected
      testing_ready → merging → done path through the merger,
      not through the reconciler's direct transition helpers.

Regression posture:

- Both failing tests (commits 1 and 2) must be committed
  *before* the fix commits (3 and 4), so the commit log records
  red → green for each fix. Anything less and a future
  reviewer cannot verify the fix actually addressed the
  described bug.

Existing tests that may need updating:

- Anything in `reconcile_containers_test.go` that asserts the
  reconciler logs "committed leftover changes in feature
  worktree" or writes a specific commit message. Those
  assertions become wrong after commit (3) and must be removed
  or inverted.
- `gitexec` tests that exercise `CommitUnstagedChanges` remain
  valid — the function still exists, it's just no longer called
  from the reconciler.

## 8. Missing-branch false-positive: fold vs sibling

**Fold into this plan.** Both bugs are reconciler correctness
issues, they share the same file and adjacent lines
(`reconcile.go:270` and `reconcile.go:278`), the fix for (1b)
requires touching the same function the fix for (1a) is
already modifying, and the test scaffolding for one is a
natural home for the other.

Splitting them would mean:

- two worktree branches that both diverge at the same spot
  in the same file, creating a merge-order dependency;
- duplicated test fixtures (both tests need a bare repo with
  `denyCurrentBranch=ignore` and a seeded failed task);
- two rounds of CTO review for two bugs that share a single
  architectural root cause ("the reconciler treats git
  command errors as semantic facts").

Folding also makes the §7 "document orphan-recovery contract"
doc commit coherent — there's one contract to explain, not two
ad-hoc patches.

Scope boundary: this plan covers orphan-agent recovery and
`reconcileAlreadyMergedFeatures`. It does **not** cover:

- `reconcileStuckAgents` (`internal/orchestrator/reconcile_stuck.go`)
  — separate concern, separate plan if needed.
- The merger's own stale-worktree handling — the merger
  already starts from `git fetch --all && git reset --hard`,
  so it is not vulnerable to the §1a class of bug. If review
  surfaces a merger-side variant we should sibling-plan it.
- Changes to `receive.denyCurrentBranch` — already decided,
  already landed, outside this plan's scope.

## 9. Risks and open questions

- **Risk: a test in the orchestrator suite secretly depends on
  `CommitUnstagedChanges` cleaning up plan.json.** Mitigation:
  commit (3) runs `go test ./...` before landing; any failure
  points at the test, not the fix. If a test fails, commit (6)
  (swap to `UntrackEphemeralFiles`) becomes non-optional.
- **Risk: `BranchExists` helper doesn't exist yet.** Check
  `internal/gitref/git.go` — we added `EnsureBranch` recently
  (commit `c0956b3`). A `BranchExists` reader is a 6-line
  helper: `git show-ref --verify --quiet refs/heads/<name>`.
  If absent, commit (4) adds it.
- **Open question: should the fix also clear `AgentWorking`
  state on the affected agent when the orphan-recovery path
  flags the subtask as failed due to missing branch?** Leaning
  yes, but needs one grep of the existing "mark agent idle"
  helpers before committing. Flag for review.
- **Open question: what telemetry do we emit?** At minimum a
  `reconcile.orphan_recovery.missing_branch` counter and a
  task-event with reason `reconcile-agent-branch-missing`.
  Larger observability surface (spans, structured logs for the
  stale-worktree detection) can wait for a follow-up.

## 10. Acceptance criteria

Required for this plan to be marked implemented:

- All eight commits in §6 land (6 is optional per §9 first
  risk); commit messages match the §6 wording or a close
  paraphrase.
- `go test -count=1 ./...` passes. `go vet ./...` clean.
- The two regression tests from §7 exist, failed before the
  fix commits and pass after.
- `docs/reconciler.md` (or the in-package contract comment,
  whichever commit 7 chooses) exists and states the "reconciler
  reconciles state, not contents" invariant.
- `plans/reconciler-stale-worktree-fix.md` status flipped to
  implemented with a date and commit-range reference.
- Full repo audit (per constitution: repo-wide, not
  change-scoped) shows no new audit findings introduced.

Not required, explicitly out of scope:

- Merger-side stale-worktree hardening.
- Any change to `reconcileStuckAgents`.
- Any change to `receive.denyCurrentBranch`.
- A migration tool for already-corrupted feature branches on
  existing deployments. A one-liner ops note in the commit
  message of commit (3) covers the manual recovery: for any
  feature branch whose tip commit is "Auto-commit uncommitted
  feature worktree changes (reconcile)", run `git reset --hard
  HEAD~1` on the bare ref to drop the revert commit.
