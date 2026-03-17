# Agent: Merge Pipeline Integration Tests

You are working on the `master` branch of drem-orchestrator, a Go TUI tool that orchestrates Claude Code agents via tmux.
Your task is to add integration tests for the untested merge pipeline functions, raising coverage from ~30% (after Phase 1) toward ~55%.

## Context

Read these before starting:
- `CLAUDE.md` (build commands, conventions)
- `docs/test-coverage-overhaul/prd-test-coverage.md` (Phase 3a section)
- `internal/merge/merge.go` (source — 512 LOC, all functions)
- `internal/merge/merge_test.go` (existing tests — retry logic, basic merge)
- `internal/worktree/git.go` (RunGit function used for git operations)
- `internal/model/models.go` (model types: Task, Agent, Project)

**Already tested (do NOT duplicate):**
- `MergeAgentIntoFeature` — 1 integration test (clean rebase)
- `mergeWithRebaseAndRetry` — 6 mock tests (retry logic, transient failures, max retries)
- Rebase conflict detection — 2 tests

**NOT tested:**
- `PlanAgentMerge` — analyzes merge without executing
- `MergeAllAgentsIntoFeature` — orchestrates multiple agent merges
- `MergeFeatureIntoMain` — merges feature branch into main
- `SyncFeaturesAfterMerge` — rebases other features after a merge
- `GetMergeStatus` — dry-run merge readiness check

## Dependencies

This agent depends on Agent 01 (testutil). Use `testutil.SetupBareRepo()`, `testutil.AddWorktree()`, `testutil.CommitFile()`, and `testutil.NewTestDB()` for test setup.

If `internal/testutil/testutil.go` doesn't exist yet, create stub versions of these functions following the patterns currently in `merge_test.go` (the local `setupBareRepo`, `addWorktree`, `commitFile` functions).

Also depends on Agent 04 (merge-helper-tests) for exported `Intersect`, `DetectBuildCommand`, `FileExists`. If those aren't exported yet, test only the public merge pipeline functions.

## Deliverables

### Modified files

#### 1. `internal/merge/merge_test.go` (append ~200–250 LOC)

All tests use real git operations via `testutil` helpers. Each test creates its own isolated bare repo.

```go
func TestPlanAgentMerge_Clean(t *testing.T)
```
- Set up bare repo with feature worktree and agent branch
- Agent branch has non-conflicting changes
- Call `PlanAgentMerge` → verify it reports no conflicts
- Verify returned plan contains commit count and changed files

```go
func TestPlanAgentMerge_Conflicting(t *testing.T)
```
- Set up bare repo with feature worktree and agent branch
- Both branches modify the same file differently
- Call `PlanAgentMerge` → verify it identifies the conflicting files
- Verify the plan indicates conflict

```go
func TestMergeAllAgentsIntoFeature(t *testing.T)
```
- Set up bare repo with feature worktree and 2 agent branches (non-conflicting)
- Create corresponding Agent records in test DB
- Create a Task record linking the agents
- Call `MergeAllAgentsIntoFeature` → verify both agents merged
- Verify feature worktree contains changes from both agents
- Verify returned report shows success for both

```go
func TestMergeAllAgentsIntoFeature_PartialFailure(t *testing.T)
```
- Set up 2 agent branches: one clean, one conflicting with feature
- Call `MergeAllAgentsIntoFeature` → verify the clean one merged
- Verify report shows which succeeded and which failed
- Verify feature worktree is in a clean state (not left mid-rebase)

```go
func TestMergeFeatureIntoMain_Clean(t *testing.T)
```
- Set up bare repo with feature worktree containing changes
- Create Task record in DB
- Call `MergeFeatureIntoMain` → verify feature merged into main
- Verify main branch contains the feature's changes (check with `git log` on bare repo)

```go
func TestMergeFeatureIntoMain_BuildFailure(t *testing.T)
```
- This test depends on how `VerifyBuild` works
- Read `MergeFeatureIntoMain` to understand what happens on build failure
- If it rolls back the merge, verify the rollback
- If build verification is skippable (no build file detected), this test may need a go.mod with intentionally broken code

```go
func TestSyncFeaturesAfterMerge(t *testing.T)
```
- Set up bare repo with 2 feature worktrees: feature-A and feature-B
- Merge feature-A into main
- Call `SyncFeaturesAfterMerge("feature-A")` → verify feature-B is rebased onto updated main
- Verify feature-B contains the changes from feature-A's merge

```go
func TestGetMergeStatus(t *testing.T)
```
- Set up a project with tasks at various merge stages
- Call `GetMergeStatus` → verify it reports correct status for each
- Read the function to understand the returned struct fields

## Scope Limitation

- Only modify `internal/merge/merge_test.go`
- Do NOT modify `merge.go`
- All tests use real git operations (integration tests, not mocks)
- Each test must clean up after itself (use `t.TempDir()` via testutil helpers)
- If a test requires DB records, use `testutil.NewTestDB(t)`

## Verification

```bash
go test ./internal/merge/ -v -cover -timeout 60s
```

All existing and new tests must pass. Coverage should reach ~55%.

## Conventions

- `gofmt` for formatting
- Table-driven tests where multiple cases test the same function
- `t.Helper()` on test helpers
- Follow the patterns established in existing merge tests
