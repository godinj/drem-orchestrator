# Agent: Shared Test Utilities Package

You are working on the `master` branch of drem-orchestrator, a Go TUI tool that orchestrates Claude Code agents via tmux.
Your task is to create a shared `internal/testutil` package that consolidates duplicated test helpers, then migrate all existing tests to use it.

## Context

Read these before starting:
- `CLAUDE.md` (build commands, conventions)
- `docs/test-coverage-overhaul/prd-test-coverage.md` (Phase 0 section)

Examine the current duplicated helpers:
- `internal/worktree/worktree_test.go` (setupBareRepo, addWorktree, commitFile — lines 12–76)
- `internal/merge/merge_test.go` (setupBareRepo, addWorktree, commitFile — lines 19–75, identical copies)
- `internal/model/models_test.go` (testDB — uses `file::memory:`, no cache)
- `internal/orchestrator/orchestrator_test.go` (testDB — uses `file::memory:?cache=shared`)
- `internal/orchestrator/failure_recovery_test.go` (newTestDB — uses UUID-isolated `file:{UUID}?mode=memory&cache=private`)

## Deliverables

### New files

#### 1. `internal/testutil/testutil.go`

Shared test helper package. Consolidate these functions:

**Database helpers:**

```go
// NewTestDB creates a UUID-isolated in-memory SQLite database with auto-migration.
// Each call returns a fully independent database instance.
func NewTestDB(t *testing.T) *gorm.DB
```

- DSN: `file:{uuid}?mode=memory&cache=private` (the failure_recovery pattern — best isolation)
- Auto-migrate all model types: `model.Project`, `model.Task`, `model.Agent`, `model.TaskEvent`, `model.Memory`, `model.TaskComment`
- Logger: `logger.Silent`
- Call `t.Helper()`

```go
// NewSharedTestDB creates a shared in-memory SQLite database.
// Multiple connections can access the same data (needed for multi-goroutine tests).
func NewSharedTestDB(t *testing.T) *gorm.DB
```

- DSN: `file::memory:?cache=shared`
- Same migration and logger setup as NewTestDB

**Git helpers:**

```go
// SetupBareRepo creates a bare git repo with an initial commit in a temp dir.
// Returns the bare repo path.
func SetupBareRepo(t *testing.T) string
```

- Use `worktree.RunGit` for all git operations
- Create bare repo, clone it, configure user, make initial commit with README.md, push

```go
// AddWorktree creates a worktree from the bare repo with a new branch.
// Returns the worktree path.
func AddWorktree(t *testing.T, bareRepo, branch, dir string) string
```

- Configure git user.email and user.name in the worktree

```go
// CommitFile creates or overwrites a file and commits it in the given worktree.
func CommitFile(t *testing.T, worktree, filename, content, message string)
```

```go
// RunGitCmd runs a git command in the given directory and returns stdout.
// Fails the test on error.
func RunGitCmd(t *testing.T, dir string, args ...string) string
```

- Wraps `worktree.RunGit`

### Migration

#### 2. `internal/worktree/worktree_test.go`

- Delete the local `setupBareRepo`, `addWorktree`, `commitFile` functions
- Add import for `"github.com/godinj/drem-orchestrator/internal/testutil"`
- Replace all calls: `setupBareRepo(t)` → `testutil.SetupBareRepo(t)`, `addWorktree(t, ...)` → `testutil.AddWorktree(t, ...)`, `commitFile(t, ...)` → `testutil.CommitFile(t, ...)`

#### 3. `internal/merge/merge_test.go`

- Delete the local `setupBareRepo`, `addWorktree`, `commitFile` functions
- Add import for `"github.com/godinj/drem-orchestrator/internal/testutil"`
- Replace all calls with `testutil.` prefix versions

#### 4. `internal/model/models_test.go`

- Delete the local `testDB` function
- Add import for `"github.com/godinj/drem-orchestrator/internal/testutil"`
- Replace all `testDB(t)` calls with `testutil.NewTestDB(t)`

#### 5. `internal/orchestrator/orchestrator_test.go`

- Delete the local `testDB` function
- Add import for `"github.com/godinj/drem-orchestrator/internal/testutil"`
- Replace `testDB(t)` calls with `testutil.NewSharedTestDB(t)` (this file uses `cache=shared`)

#### 6. `internal/orchestrator/failure_recovery_test.go`

- Delete the local `newTestDB` function
- Add import for `"github.com/godinj/drem-orchestrator/internal/testutil"`
- Replace `newTestDB(t)` calls with `testutil.NewTestDB(t)`

#### 7. Any other `_test.go` files in `internal/orchestrator/` that reference `testDB`

- Check `test_gate_test.go`, `test_review_test.go`, `test_writing_test.go`, `scheduler_test.go`, `plan_parse_test.go`, `plan_validation_test.go`
- These are in the same package as `orchestrator_test.go` so they share its `testDB` — they'll automatically pick up the change

## Verification

After all changes:

```bash
go build ./...
go test ./...
```

Every existing test must still pass. Zero duplicated `testDB`, `setupBareRepo`, `addWorktree`, or `commitFile` functions should remain in any `_test.go` file.

## Conventions

- `gofmt` for formatting
- Exported functions have doc comments
- Error wrapping with `fmt.Errorf("context: %w", err)`
- Table-driven tests where applicable
