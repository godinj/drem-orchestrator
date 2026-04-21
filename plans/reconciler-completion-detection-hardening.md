# Reconciler completion-detection hardening

Status: planning — opened 2026-04-20 alongside
`fix(orchestrator): resolve feature worktree for top-level tasks`
(commit `be3a363`). That commit fixes the primary bug exposed by
canary v12 (top-level task wrongly reported "agent session died
without producing commits" even though the watchdog had pushed the
agent branch). Investigation flagged four adjacent concerns in the
same code path; each deserves its own commit. This doc names them and
sketches why they matter.

## Context

`internal/orchestrator/reconcile.go`'s `reconcileStuckAgents` loop is
the last line of defence when an agent's tmux session dies without
emitting the usual completion signal. Its job is to decide, on behalf
of the missing completion event, whether the agent left real work
behind (so the orchestrator should route through
`synthesizeCompletion`) or produced nothing (so retry/fail).

That decision currently rests on one boolean, `hasCommits`, derived
from `gitexec.BranchHasNewCommits(featureDir, ag.WorktreeBranch)`.
The v12 canary showed that the inputs to this check are fragile: any
failure to resolve `featureDir`, any missing `ag.WorktreeBranch`, any
git-level error while running `rev-list` — all silently collapse into
`hasCommits = false` and the task gets mis-routed to the empty-work
path. The primary fix repaired `featureDir` resolution for top-level
tasks. The remaining four issues below each turn a different one of
these fragile inputs into a false-failure on a different code path.

## The four adjacent bugs

### 1. `BranchHasNewCommits` error is fail-closed

`reconcile.go:537-542` — when `BranchHasNewCommits` returns a non-nil
error, the reconciler logs a warning and leaves `hasCommits = false`.
That means a transient git error (lock contention, fs readback, a
worktree that just got recreated) mis-routes a perfectly good
completion to the empty-work retry path. Compare with
`agent_results.go:107-111`, which fails *open* on the same call:
"assume there are commits on error". The two code paths should agree.
Proposed fix: on error, log and set `hasCommits = true` so we err
toward treating the agent as productive.

### 2. Empty `ag.WorktreeBranch` also hits the false-failure path

`reconcile.go:535` — the `featureDir != "" && ag.WorktreeBranch != ""`
guard is correct for classifier agents (no branch, genuinely nothing
to check). But coder/planner agents with an unexpectedly empty
`ag.WorktreeBranch` (e.g. the watchdog committed on the feature
branch directly) also skip the commit check and fall through to the
empty-work path. We should either (a) fall back to checking the
feature branch itself for post-default-branch commits, or (b) pull
`ag.WorktreeBranch` from the agent-worktree filesystem when the DB
field is empty. Needs a short design note before implementation.

### 3. Polling-via-host-worktree is not containerization-safe

The reconciler runs `rev-list` against a host-side feature worktree
path (`o.worktree.FeatureWorktreePath(fn)`). Post-containerization
(`plans/containerization.md`), agents commit into container-mounted
worktrees and the watchdog pushes the branch to the bare repo; the
host worktree is not guaranteed to reflect the push until the next
host-side `git fetch`. A more robust check queries the bare repo
directly with `git -C <bareRepo> rev-list --count default..<branch>`,
which always sees what was just pushed. This is likely the right
long-term answer — the host worktree becomes just a convenience for
human inspection.

### 4. Watchdog does not emit a positive completion signal

Even with all three fixes above, detection is still *reactive*: the
reconciler only runs after the agent's tmux session dies, after the
grace period, after the next tick. That's a multi-minute lag between
"work is done and pushed" and "task transitions out of
`in_progress`". The watchdog (which already commits and pushes) is
the natural place to emit an explicit completion signal — either by
touching `.claude/agent-idle`, writing a watchdog-done marker the
orchestrator can poll, or calling a local orchestrator endpoint
directly. This removes the entire stuck-agent-reconciler code path
from the happy case; it becomes purely a safety net.

## Why these aren't in the primary fix

Each is a separate failure mode, each needs its own regression test,
and fixing them together would make the primary change (one-function
`resolveFeatureWorktree` correction) much harder to review and to
bisect against. They share a theme (reconciler fails closed where it
should fail open, or polls the wrong source of truth) but the
remediations differ. Land the primary fix, then pick these up
individually on follow-up worktree branches.

## Ordering

1. Bug 1 (fail-open on `BranchHasNewCommits` error) — smallest,
   one-line change plus a test, highest safety value.
2. Bug 4 (watchdog completion signal) — biggest architectural win,
   removes the reactive-reconciler class entirely for the happy path.
   Needs design discussion first.
3. Bug 3 (bare-repo polling) — depends on how bug 4 shakes out; if
   the watchdog signals completion directly, the polling path matters
   only for true failures and can stay host-worktree-based.
4. Bug 2 (empty `ag.WorktreeBranch` fallback) — needs a case-by-case
   inventory of *why* it's ever empty before we decide on a fallback.
