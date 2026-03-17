# Agent: Merge Integration Tests

You are working on the `master` branch of drem-orchestrator, a Go TUI tool that orchestrates Claude Code agents via tmux.
Your task is to add integration tests for the merge orchestration functions: `PlanAgentMerge`, `MergeAllAgentsIntoFeature`, `SyncFeaturesAfterMerge`, and `GetMergeStatus`.

## Context

Read these before starting:
- `CLAUDE.md` (build commands, conventions)
- `docs/quality-gaps/prd-quality-gaps.md` (Gap T3 section)
- `internal/merge/merge.go` (full file — understand all public methods and their dependencies)
- `internal/merge/merge_test.go` (existing tests — append to this file, do NOT modify existing tests)
- `internal/testutil/testutil.go` (shared test helpers — use these for git setup)
- `internal/worktree/manager.go` (worktree manager API — `NewManager`, `MainWorktreePath`, `SyncAll`)
- `internal/model/models.go` (model types — `Task`, `Agent`, `TaskStatus` constants)

## Dependencies

This agent depends on the existing `testutil` package (already complete). Use `testutil.SetupBareRepo`, `testutil.AddWorktree`, `testutil.CommitFile`, `testutil.NewTestDB`, `testutil.CreateTask`, `testutil.CreateAgent` for all setup.

## Deliverables

### Modified file

#### 1. `internal/merge/merge_test.go`

Append new integration test functions. Do NOT modify existing tests.

**Test 1: `TestPlanAgentMerge_Clean`**

Setup:
- `testutil.SetupBareRepo(t)` → bare repo
- `testutil.AddWorktree(t, bare, "feature/plan-test", featureDir)`
- `testutil.AddWorktree(t, bare, "worktree-agent-plan", agentDir)`
- `testutil.CommitFile(t, agentDir, "new-file.go", "package main", "add file")`

Test:
- Call `orch.PlanAgentMerge("worktree-agent-plan", featureDir)`
- Assert no error
- Assert `plan.SourceBranch == "worktree-agent-plan"`
- Assert `plan.FilesChanged` contains `"new-file.go"`
- Assert `plan.PotentialConflicts` is empty (no overlapping files)

**Test 2: `TestPlanAgentMerge_WithConflicts`**

Setup: same as above but both feature and agent modify the same file.

Test:
- Assert `plan.PotentialConflicts` contains the shared file name

**Test 3: `TestMergeAllAgentsIntoFeature_TwoAgents`**

Setup:
- Bare repo + feature worktree + two agent worktrees, each with commits on different files
- Create a `testutil.NewTestDB(t)` with a parent task, two subtask records (status DONE), and two agent records with `WorktreeBranch` set
- Set `task.WorktreeBranch` and each subtask's `AssignedAgentID`

Test:
- `orch := NewOrchestrator(mgr, db)` — pass the DB
- Call `orch.MergeAllAgentsIntoFeature(task, featureDir)`
- Assert `report.AllSucceeded == true`
- Assert `len(report.AgentMerges) == 2`
- Assert both agent files exist in the feature worktree

**Test 4: `TestMergeAllAgentsIntoFeature_OneConflict`**

Setup: two agents, both modify the same file with different content.

Test:
- Assert `report.AllSucceeded == false`
- Assert at least one `AgentMerges` entry has `Success == false`

**Test 5: `TestSyncFeaturesAfterMerge`**

Setup:
- Bare repo with main worktree
- Two feature worktrees: `feature/sync-a` and `feature/sync-b`
- Commit a file in `feature/sync-a`, merge it into main manually with `git merge`

Test:
- Call `orch.SyncFeaturesAfterMerge("feature/sync-a")`
- This wraps `wt.SyncAll()` — assert no error
- The test verifies the function runs without error; full sync validation is in the worktree package

**Test 6: `TestGetMergeStatus`**

Setup:
- Bare repo with main worktree
- Feature worktree with a commit
- DB with a task record: `status = StatusTestingReady`, `WorktreeBranch = "feature/status-test"`, `ProjectID` set

Test:
- `orch := NewOrchestrator(mgr, db)`
- Call `orch.GetMergeStatus(projectID)`
- Assert no error
- Assert `status.ReadyToMerge` or `status.Conflicted` or `status.Behind` contains the branch (depending on git state)

## Important Notes

- `MergeAllAgentsIntoFeature` queries the DB for subtasks and agents, so you MUST provide a real `*gorm.DB` via `testutil.NewTestDB(t)` and create the necessary records
- `GetMergeStatus` also queries the DB, so same requirement
- `SyncFeaturesAfterMerge` only needs the worktree manager, no DB
- `PlanAgentMerge` only needs the worktree manager, no DB
- The `Orchestrator` is created with `NewOrchestrator(wt *worktree.Manager, db *gorm.DB)` — db can be nil for tests that don't query it

## Scope Limitation

- Do NOT modify any existing tests in `merge_test.go`
- Do NOT modify `merge.go`
- Do NOT add test helpers to `testutil`
- Do NOT test `MergeFeatureIntoMain` (it runs `go test ./...` via `VerifyBuild` which would be slow/recursive — the existing `TestMergeFeatureIntoMain_AutoCommitsDirtyMain` covers the core path)
- Only append new test functions at the end of the file

## Verification

```bash
go build ./internal/merge/
go test ./internal/merge/ -v -run "TestPlanAgentMerge|TestMergeAllAgents|TestSyncFeatures|TestGetMergeStatus"
go test ./internal/merge/ -v
go test ./...
```

## Conventions

- Go 1.22+ with standard library where possible
- `gofmt` for formatting
- Use `testutil` helpers for all git and DB setup
- Table-driven tests where appropriate
- Use `t.TempDir()` for filesystem operations
- Use `t.Helper()` in any local helper functions
