# Agent: Extract gitexec + WorktreeManager Interface + Orchestrator Test Migration

You are working on the `master` branch of Drem Orchestrator, a Go-based multi-agent task orchestrator. Your task is the first of three follow-up prompts that complete the containerization initiative: extract leaf git helpers into a new `internal/gitexec/` package, introduce a `WorktreeManager` interface inside `internal/orchestrator/` so production files stop importing `internal/worktree/` directly, and migrate the ~30 orchestrator test files that still import `internal/worktree/` onto a fake.

This prompt bundles remaining-work.md Steps 1, 2, and 6 into a single mechanical decoupling pass. Nothing deletes yet — that lives in prompt 21.

## Context

Read these specs before starting:

- `docs/containerization/remaining-work.md` (sections: Step 1, Step 2, Step 6; the "What's actually coupled" audit)
- `internal/worktree/` (source package — `Manager` plus package-level utilities like `RunGit`, `GetChangedFiles`, `IsClean`, `CommitInfo`, `BranchHasNewCommits`, `CommitUnstagedChanges`)
- `internal/orchestrator/` (the consumer being decoupled — ~15 production files, ~30 test files)
- `ARCHITECTURE.md` (the `[enforced]` ceilings — file length, function count)

## Dependencies

None beyond the landed Tier 1–3 work. Baseline confirmed by:

```bash
grep -rn "internal/tmux\|internal/worktree" cmd/ internal/ pkg/ --include="*.go" \
  | grep -v "internal/tmux/\|internal/worktree/" | wc -l
# → baseline ~61 (record the exact number before starting)
```

## Deliverables

### New package: `internal/gitexec/`

#### 1. `internal/gitexec/gitexec.go`

Lift the stateless, package-level utilities out of `internal/worktree/` so callers that only need to run git against an arbitrary directory don't have to depend on a `*worktree.Manager`:

```go
// Package gitexec runs git against an arbitrary working directory.
// It has no knowledge of Drem's worktree layout or branch conventions;
// that lives in internal/worktree.
package gitexec

func RunGit(ctx context.Context, dir string, args ...string) ([]byte, error)
func GetChangedFiles(ctx context.Context, dir, base, head string) ([]string, error)
func IsClean(ctx context.Context, dir string) (bool, error)
func CommitInfo(ctx context.Context, dir, sha string) (*Commit, error)
func BranchHasNewCommits(ctx context.Context, dir, base, head string) (bool, error)
func CommitUnstagedChanges(ctx context.Context, dir, msg string) (sha string, err error)
```

Types:

```go
type Commit struct {
    SHA       string
    Author    string
    Committed time.Time
    Subject   string
    Body      string
}
```

Audit the current `internal/worktree/` package-level functions before finalizing. If a function turns out to be stateful (e.g. reads `Manager.BareRepoPath` implicitly), leave it in `worktree` and do not move it.

#### 2. `internal/gitexec/gitexec_test.go`

Table-driven tests for each exported function against a scratch bare repo created via `internal/testutil.SetupBareRepo` + `internal/testutil.CommitFile`. Use `t.TempDir()` for the working-copy clone.

### Migration of utility call sites

#### 3. Update orchestrator production files

Every `internal/orchestrator/*.go` (non-test) file that currently imports `internal/worktree` only for `worktree.RunGit` / `worktree.GetChangedFiles` / etc. should be updated to import `internal/gitexec` instead. After this step, surviving `internal/worktree` imports in `internal/orchestrator/*.go` are only for `*worktree.Manager` receiver-method calls — those are handled below.

Expected diff size: under 400 lines across 3–5 orchestrator files.

### New interface: `WorktreeManager`

#### 4. `internal/orchestrator/worktree_manager.go`

```go
package orchestrator

// WorktreeManager is the orchestrator's view of worktree operations.
// Production is satisfied by *worktree.Manager (wired in cmd/drem/).
// Tests are satisfied by FakeWorktreeManager.
type WorktreeManager interface {
    BareRepoPath() string
    DefaultBranch() string
    MainWorktreePath() string
    FeatureWorktreePath(feature string) string

    CreateFeature(ctx context.Context, feature string) error
    RemoveFeature(ctx context.Context, feature string) error
    CreateAgentWorktree(ctx context.Context, agentID string) (string, error)
    RemoveAgentWorktree(ctx context.Context, agentID string) error
    ListAgentWorktrees(ctx context.Context) ([]string, error)

    // Extended — add as the audit reveals them.
    CommitUnstagedChanges(ctx context.Context, dir, msg string) (string, error)
    IsClean(ctx context.Context, dir string) (bool, error)
    BranchHasNewCommits(ctx context.Context, base, head string) (bool, error)
    CommitInfo(ctx context.Context, sha string) (*gitexec.Commit, error)
    MergeResult(ctx context.Context, feature string) error
    RebaseBranch(ctx context.Context, branch, onto string) error
    RebaseResult(ctx context.Context, branch string) error
    SyncResult(ctx context.Context, feature string) error
    UntrackEphemeralFiles(ctx context.Context, dir string) error
    GenerateRepoMapForMain(ctx context.Context) ([]byte, error)
    GetChangedFiles(ctx context.Context, base, head string) ([]string, error)
}
```

**Audit first.** Grep every method called on `o.worktree` and enumerate the real surface. The list above is a starting point taken from remaining-work.md; reconcile with actual call sites before declaring the interface final. If a method can be satisfied by `gitexec` directly at the call site, drop it from the interface.

Interface goals:
- No dependency from `internal/orchestrator/` on `internal/worktree/` at the source-import level after this prompt lands.
- Method shapes stay as close to `*worktree.Manager`'s current signatures as possible so callers are mechanical migrations, not rewrites.

#### 5. Retype `Orchestrator.worktree`

Change the field type in `internal/orchestrator/orchestrator.go` (or wherever `Orchestrator` is defined):

```go
// before
worktree *worktree.Manager

// after
worktree WorktreeManager
```

Update the orchestrator constructor to accept a `WorktreeManager`. `cmd/drem/main.go` still constructs `*worktree.Manager` and passes it in — prompt 20 cleans up the cmd/ import.

#### 6. `internal/orchestrator/fake_worktree_manager.go`

Test-only fake implementing `WorktreeManager`. In-memory state keyed by feature/agent ID. Parameterizable via exported fields so individual tests can stub specific behaviors:

```go
type FakeWorktreeManager struct {
    Bare     string
    Default  string
    Features map[string]string        // feature → worktree path
    Agents   map[string]string        // agentID → worktree path
    // plus hooks for methods tests want to intercept
    OnCreateFeature func(context.Context, string) error
}
```

Reusable across all orchestrator tests. Place in production source so tests in other packages (if any) can reuse it; gate behind build tag if it bloats production binaries.

### Test migration

#### 7. `internal/orchestrator/*_test.go`

Migrate every test file that currently imports `internal/worktree` to use `FakeWorktreeManager` instead. Roughly 30 files. Preserve the tests' existing assertions — do not weaken behavior under the guise of "migration."

Split into logical commits so reviewers can follow. Suggested split:
- One commit per test-file cluster (e.g. all `reconcile_*_test.go` together, all `task_*_test.go` together)
- Head of branch must build and test cleanly between commits

## Verification

Every step of this prompt should confirm progress:

```bash
go build ./...
go test ./...
```

Both must be green (pre-existing failures in `internal/agent/direct_tool_agent_compaction_test.go` and `internal/orchestrator/fasttrack_atomicity_test.go` remain out of scope — leave them failing if baseline).

Import audit — must decrease substantially from the ~61 baseline:

```bash
grep -rn "internal/worktree" internal/orchestrator/ --include="*.go" | wc -l
# → should be 0 after this prompt lands

grep -rn "internal/tmux\|internal/worktree" cmd/ internal/ pkg/ --include="*.go" \
  | grep -v "internal/tmux/\|internal/worktree/" | wc -l
# → should drop by roughly 45 (all orchestrator prod + test files)
```

Constitution:

```bash
bash scripts/check_constitution.sh
```

Must pass with no new violations.

## Scope Limitation

- **No changes to `internal/agent/`, `internal/merge/`, `internal/tui/`, `cmd/drem/`.** Those belong to prompts 19 and 20.
- **Do not delete `internal/worktree/`.** `*worktree.Manager` is still wired in `cmd/drem/main.go` as the concrete implementation. Deletion is prompt 21.
- **No behavioral changes.** Every existing test must continue to exercise the same scenario — the only change is who satisfies `WorktreeManager`.
- **No cross-package refactors.** If a method on the proposed interface turns out to be used outside `internal/orchestrator/`, flag it in the prompt output and leave the existing import in place; do not chase the refactor into neighboring packages.
- **No premature abstraction.** If a `*worktree.Manager` method is called on `o.worktree` only once across orchestrator and can be inlined via `gitexec` or a direct helper, prefer that over adding it to the interface.

## Commit Hygiene

Suggested commit sequence:

1. `add internal/gitexec package with leaf git helpers`
2. `migrate orchestrator utility call sites to gitexec`
3. `introduce WorktreeManager interface in orchestrator`
4. `add FakeWorktreeManager`
5. `migrate orchestrator test files to FakeWorktreeManager (batch 1)`
6. `migrate orchestrator test files to FakeWorktreeManager (batch 2)`
7. `migrate orchestrator test files to FakeWorktreeManager (batch 3)`

If any single commit breaks `go build`, fold into the next. Head of branch must build and pass tests.

## Conventions

- Module path: `github.com/godinj/drem-orchestrator`
- Package names: `gitexec`, `orchestrator` (extending existing)
- Testing: `testify/require`; reuse `internal/testutil.SetupBareRepo` / `CommitFile`
- File-length + function-count ceilings per `ARCHITECTURE.md`
- Build verification: `go build ./... && go test ./...`
- Constitution check: `bash scripts/check_constitution.sh`
