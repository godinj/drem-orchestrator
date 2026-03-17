# Agent: Test Helper Consolidation

You are working on the `master` branch of Drem Orchestrator, a Go-based agent orchestration system.
Your task is to consolidate all duplicated test helpers into `internal/testutil/testutil.go` and migrate every caller.

## Context

Read these before starting:
- `ARCHITECTURE.md` (Duplication section — testutil rules; Test Infrastructure section — factory functions)
- `internal/testutil/testutil.go` (existing shared helpers)
- Each test file listed in the violations below

## Current Violations

### DB initialization duplicates (3 files)

These files define their own DB init instead of using `testutil.NewTestDB`:

1. **`internal/orchestrator/lifecycle_test.go:51`** — `lifecycleTestDB()` uses private cache mode
2. **`internal/agent/runner_mock_test.go:118`** — `mockTestDB()` uses shared cache mode
3. **`internal/model/models_test.go:15`** — inline `gorm.Open(sqlite.Open("file::memory:")...)`

### Git helper duplicates (3 files)

These files reimplement helpers that exist in testutil:

1. **`internal/merge/merge_test.go:23,56,67`** — `setupBareRepo()`, `addWorktree()`, `commitFile()` (byte-for-byte identical to testutil except for casing)
2. **`internal/orchestrator/orchestrator_test.go:39,115`** — `initBareRepo()`, `runGitCmd()` (similar to testutil with minor differences)
3. **`internal/worktree/manager_test.go:12`** — `initBareRepo()` (duplicate)

### Test factory duplicates (5 files)

These files define their own entity creation helpers:

1. **`internal/agent/runner_mock_test.go:162,176`** — `createTestProject()`, `createTestTask()`
2. **`internal/memory/memory_test.go:15`** — `createTestProject()` (returns projectID, taskID, agentID)
3. **`internal/taskimport/import_test.go:44`** — `createTestProject()` (returns uuid, model.Project)
4. **`internal/orchestrator/lifecycle_test.go:75`** — `createLifecycleTask()`
5. **`internal/orchestrator/failure_recovery_test.go:17`** — `newTestOrch()`

### DB init used by newer orchestrator test files (3 files)

These use `lifecycleTestDB()` from lifecycle_test.go — once that's migrated to testutil, these automatically benefit:

1. `internal/orchestrator/agent_result_test.go`
2. `internal/orchestrator/reconcile_test.go`
3. `internal/orchestrator/scheduling_test.go`

## Deliverables

### Modified: `internal/testutil/testutil.go`

Add the following exported helpers. Read existing helpers first to match style.

#### New DB helper

If `lifecycleTestDB()` uses `cache=private` for isolation (different from the existing `NewTestDB` which uses a UUID-based name), add an option or a new function:

```go
// NewPrivateCacheTestDB creates an in-memory SQLite database with cache=private
// for test isolation when multiple DBs are needed in the same test.
func NewPrivateCacheTestDB(t *testing.T) *gorm.DB
```

Or, if the existing `NewTestDB` already provides sufficient isolation (it uses UUID-based cache names), then `lifecycleTestDB()` callers can simply use `NewTestDB`.

Investigate which approach is correct by reading the existing `NewTestDB` and `lifecycleTestDB` implementations.

#### New factory helpers

```go
// CreateProject creates a test project in the database and returns it.
func CreateProject(t *testing.T, db *gorm.DB, name, bareRepoPath, defaultBranch string) model.Project

// CreateTask creates a test task in the database and returns it.
func CreateTask(t *testing.T, db *gorm.DB, projectID uuid.UUID, title string, status model.TaskStatus) model.Task

// CreateAgent creates a test agent in the database and returns it.
func CreateAgent(t *testing.T, db *gorm.DB, taskID uuid.UUID, agentType model.AgentType, status model.AgentStatus) model.Agent
```

Each factory should:
- Create the record with `db.Create()`
- Call `t.Helper()`
- Call `t.Fatalf()` on error
- Return the created model (with ID populated)

### Migration: Delete local helpers and replace with testutil calls

For each violation above:

1. Delete the local helper function
2. Replace all call sites with the corresponding `testutil.X()` call
3. Add `"github.com/godinj/drem-orchestrator/internal/testutil"` to imports if not already present
4. Remove unused imports after migration

Be careful with signature differences:
- `memory_test.go`'s `createTestProject` returns `(projectID, taskID, agentID)` — this creates 3 entities. Replace with 3 separate `testutil.CreateProject/CreateTask/CreateAgent` calls at the call site.
- `taskimport/import_test.go`'s `createTestProject` returns `(uuid.UUID, model.Project)` — replace with `testutil.CreateProject` and use `project.ID`.
- `merge_test.go`'s `newTestDB` uses `db.Init()` — check if `testutil.NewTestDB` is equivalent. If `db.Init` does something extra (e.g., PRAGMA settings), add that to testutil.

### Special case: `internal/merge/merge_test.go:461` — `newTestDB()`

This function calls `db.Init()` rather than `gorm.Open` directly. Read `internal/db/db.go` to understand if `db.Init()` does anything beyond `gorm.Open + AutoMigrate`. If it does, either:
- Add a `testutil.NewTestDBViaInit()` that calls `db.Init()`, or
- Add the extra logic to `testutil.NewTestDB`

## Scope Limitation

- Do NOT modify any production (non-test) code.
- Do NOT change test logic or assertions — only change how test fixtures are created.
- Do NOT add new test cases.
- If a local helper does something genuinely unique (not duplicated elsewhere), leave it in place.

## Verification

```bash
# Compliance check: no DB init outside testutil
grep -rn 'gorm.Open(sqlite' internal/ --include='*_test.go' | grep -v testutil/
# Should return no results

# Compliance check: no git helpers outside testutil
grep -rn 'func setupBareRepo\|func initBareRepo\|func addWorktree\|func commitFile' \
  internal/ --include='*_test.go' | grep -v testutil/
# Should return no results

# Compliance check: no factory functions outside testutil
grep -rn 'func createTest\|func mockTestDB\|func lifecycleTestDB' \
  internal/ --include='*_test.go' | grep -v testutil/
# Should return no results

# All tests must pass
go test ./...
```

## Conventions

- `t.Helper()` on all test helper functions
- `t.Fatalf()` for setup failures (not `t.Errorf`)
- Exported functions have doc comments
- Error wrapping with `fmt.Errorf("context: %w", err)`
- Build verification: `go test ./...`
