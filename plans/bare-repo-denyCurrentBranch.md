# Bare-repo `receive.denyCurrentBranch=updateInstead` — Implementation Plan

Status: **implemented, 2026-04-20.** All 4 commits
(helper + tests, CLI wiring + tests, install doc, plan mark)
landed on `worktree-agent-a8c837a8`. Sibling plan to
`plans/drem-project-register-update.md`; mirrored its cadence
(plan first, TDD per layer, docs-as-acceptance-criteria). Closes
the canary-debugging gap where a worker's final `git push origin
<feature-branch>` into `/bare` was rejected because the
shared-workspace "bare" repo has host worktrees checked out.

Tests added:

- `internal/projects/bare_repo_test.go` — 5 new
  (happy path, idempotent, overwrites differing value, missing
  path, not a git repo).
- `cmd/drem/project_test.go` — 2 new
  (`TestProjectRegister_SetsBareRepoDenyCurrentBranch`,
  `TestProjectRegisterUpdate_IsIdempotentOnBareRepoConfig`).

All tests pass on `go test -count=1 ./...`; `go vet ./...` is clean.

Original design context preserved below.

---

## 0. Goal

Every `drem project register` (fresh or `--update`) leaves the
project's bare git repo with `receive.denyCurrentBranch=updateInstead`
set, so the worker watchdog's final push at end-of-task succeeds
against the shared-workspace bare repo without the operator
hand-running `git config` after registration.

## 1. Why now

The T3 canary surfaced this cascade:

1. Worker finishes its subtask, watchdog runs `git push origin
   feature/<id>` inside the container.
2. Push targets the bind-mounted bare repo at `/bare` (host path:
   `<Project.BareRepoPath>`).
3. Push is rejected:
   ```
   remote: error: refusing to update checked out branch: refs/heads/feature/...
   remote: error: By default, updating the current branch in a non-bare
   remote: error: repository is denied, because it will make the index
   remote: error: and work tree inconsistent with what you pushed, and
   remote: error: will require 'git reset --hard' to match the work tree to HEAD.
   ```
4. The "bare" repo isn't really bare — it's a shared-workspace git
   dir with host worktrees checked out (the integration worktrees
   `processPlanning` creates under `<bare>/<branch>`). Plain
   `git init --bare` doesn't hit this, but our setup does.
5. `receive.denyCurrentBranch=updateInstead` is git's
   purpose-built escape hatch: when a push targets a checked-out
   branch, git updates the worktree instead of rejecting. If the
   worktree is dirty, the push rejects safely (no data loss).
   Matches our data flow: the integration worktree SHOULD follow
   the feature branch tip after a successful worker merge.

Back-fill of the setting was run by hand on the existing
drem-orchestrator project:

```
git -C /home/godinj/git/drem-orchestrator.git config \
    receive.denyCurrentBranch updateInstead
```

This plan makes that the default for every new operator
registration and for every `--update` refresh so migrators from
old installs (who never had the setting) pick it up
automatically.

## 2. What changes, what stays

Keep:
- Fresh `drem project register` happy-path (registry add →
  drem.toml + compose.yml render). The bare-repo config step
  is a leaf call appended after the render succeeds.
- `drem project register --update` behavior. The config step is
  idempotent, so running it on every `--update` is safe — `git
  config <key> <value>` overwrites with the same value (no-op)
  or sets a differing value.
- The registry file format. `receive.denyCurrentBranch` lives on
  the bare repo's `config`, not in `~/.drem/projects.toml`.

Add:
- `internal/projects/bare_repo.go` — one exported function:
  `ConfigureBareRepo(barePath string) error`. Shells out to
  `git --git-dir=<barePath> config receive.denyCurrentBranch
  updateInstead`. Idempotent (that's how `git config <key> <value>`
  works). Returns an error when git is missing, the path doesn't
  exist, or the path isn't a git repo.
- `internal/projects/bare_repo_test.go` — TDD suite (see §4).
- Two call sites in `cmd/drem/project.go`:
  - `cmdProjectRegister` — after `WriteProjectComposeAt` succeeds
    (around line 247), before the success log line.
  - `cmdProjectRegisterUpdate` — after the compose + drem.toml
    writes succeed (around line 386), before the summary log.
- Two call-site tests in `cmd/drem/project_test.go`.
- Install-doc sentence under Step 6 noting the auto-configured
  setting + a verification command.

Remove:
- Nothing.

## 3. Design options considered

### (A) `receive.denyCurrentBranch=updateInstead` — **chosen**

Matches the data flow. The integration worktree under
`<bare>/<branch>` is *supposed* to follow the feature branch tip
so merger integration reads the freshest code. `updateInstead`
gives us that for free; dirty-worktree safety built in.

### (B) `receive.denyCurrentBranch=ignore`

Simpler — push always succeeds, never touches the worktree.
Rejected: leaves the host worktree stale, which means any
subsequent merger/integration read of
`<bare>/<branch>/path/to/file` sees the pre-push contents until
someone manually runs `git checkout` or `git reset --hard` in
the worktree. Silent staleness is worse than the current loud
rejection.

### (C) Make the bare repo actually bare

Delete the host worktrees, run push against a genuine bare repo,
have merger clone on demand for integration reads. Biggest
refactor — touches merger, watchdog, worktreehost, and
every test that relies on the shared-workspace layout. Out of
scope for this canary fix; revisit if other friction shows up.

### (D) Shadow bare repo via `post-receive` hook

Install a hook that fast-forwards the host worktree when the push
arrives. Works, but is duplicative — `updateInstead` does exactly
the same job with no moving parts. Rejected as overkill.

## 4. Files touched

### New files

- `plans/bare-repo-denyCurrentBranch.md` — this file.
- `internal/projects/bare_repo.go` — `ConfigureBareRepo`.
- `internal/projects/bare_repo_test.go` — 5 tests (see §5).

### Modified files

- `cmd/drem/project.go` — two call sites gain one line each
  (call `projects.ConfigureBareRepo(bareRepoPath)` with wrapped
  error).
- `cmd/drem/project_test.go` — 2 new tests
  (`TestProjectRegister_SetsBareRepoDenyCurrentBranch`,
  `TestProjectRegisterUpdate_IsIdempotentOnBareRepoConfig`).
- `docs/containerization/install.md` — one sentence + a
  verification command under Step 6.

## 5. Tests (TDD)

### `internal/projects/bare_repo_test.go`

1. **Happy path.** Init a bare repo in a temp dir with
   `git init --bare`, call `ConfigureBareRepo`, assert
   `git --git-dir=<bare> config --get receive.denyCurrentBranch`
   returns `updateInstead`.
2. **Idempotent.** Call twice. Assert both return nil and the
   value is still `updateInstead`.
3. **Overwrites differing value.** Set
   `receive.denyCurrentBranch=ignore` first, call helper, assert
   value becomes `updateInstead` (we are authoritative).
4. **Missing path.** Call against a non-existent path, assert
   a clear error mentioning the path.
5. **Not a git repo.** Call against an empty temp dir, assert
   a clear error.

### `cmd/drem/project_test.go`

6. **`TestProjectRegister_SetsBareRepoDenyCurrentBranch`** —
   fresh register against a temp bare. Assert
   `receive.denyCurrentBranch` reads `updateInstead` after the
   register completes.
7. **`TestProjectRegisterUpdate_IsIdempotentOnBareRepoConfig`** —
   register fresh, then `--update --force`; assert no error and
   value is still `updateInstead`.

## 6. Commit sequence

1. **`feat(projects): helper to configure receive.denyCurrentBranch on bare repo`**
   — `internal/projects/bare_repo.go` + `bare_repo_test.go`.
   Pure addition, no caller yet. TDD: tests first.
2. **`feat(cli): set receive.denyCurrentBranch on project register`**
   — wire both `cmdProjectRegister` and `cmdProjectRegisterUpdate`
   call sites; add the two call-site tests.
3. **`docs(containerization): document bare-repo denyCurrentBranch setting`**
   — one-paragraph addition to Step 6 of install.md plus a
   verification `git config --get` one-liner.
4. **`docs(plans): mark bare-repo-denyCurrentBranch plan implemented`**
   — flip this plan's status header to `implemented, <date>` with
   the usual test-count summary.

~4 commits, ~150 LoC total (helper + tests + docs). Half a day
focused.

## 7. Rollout

For new operators: nothing to do. Fresh `drem project register`
configures the setting.

For migrators with an existing project who skipped this fix
cycle:

```bash
# One-shot (the back-fill we already ran on drem-orchestrator):
git -C <Project.BareRepoPath> config \
    receive.denyCurrentBranch updateInstead

# Or re-run the update path to pick it up alongside any other
# template drift:
drem project register --update <project-name> --force
```

The `--update` path call-site means every `--update` invocation
reapplies the setting, so operators who habitually regenerate
on plan rollouts get it for free.

## 8. Non-goals

- Do NOT touch `internal/watchdog/`, `internal/merger/`,
  `internal/orchestrator/`, `internal/spawner/`, or any worker
  entrypoint. The fix is purely host-side (CLI + bare-repo
  config).
- Do NOT rebuild docker images. The orch binary is unchanged.
- Do NOT push to origin.
- Do NOT modify the registry file format or `Project` struct.
- Do NOT add a plan-wide hook infrastructure (post-receive etc.).
  `git config` key is the right primitive.

## 9. Related plans

- `plans/drem-project-register-update.md` — `--update` path that
  this plan extends.
- `plans/worker-bare-mount-rw.md` — the commit 069d967 fix that
  made `/bare` writable from the worker; this plan is the
  complementary fix that makes the write itself succeed.
- `docs/containerization/install.md` — Step 6 gains a sentence
  about the auto-configured setting.
