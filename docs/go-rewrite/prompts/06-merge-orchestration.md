# Agent: Merge Orchestration

You are working on the `feature/go-rewrite` branch of Drem Orchestrator, a multi-agent task orchestration system being rewritten from Python to Go.
Your task is to implement the merge orchestration layer — merging agent branches into features, features into main, build verification, and syncing.

## Context

Read these before starting:
- `docs/go-rewrite/DESIGN.md` (focus on "Merge Orchestration" section)
- `src/orchestrator/merge.py` (Python implementation to port — MergeOrchestrator class, MergePlan, FeatureMergeReport, verify_build, merge_all_agents, sync_features)
- `internal/worktree/manager.go` (WorktreeManager API — MergeBranch, SyncAll, RunGit)
- `internal/worktree/git.go` (git helpers — GetCommitLog, GetChangedFiles, IsClean, CommitInfo)
- `internal/model/models.go` (Go models — Task, Agent, MergeResult from worktree)
- `CLAUDE.md` (build commands, conventions)

## Dependencies

This agent depends on Agent 01 (Models/DB) and Agent 03 (Worktree Manager).
If those files don't exist yet, create minimal stubs with the interfaces you need.

## Deliverables

### New file: `internal/merge/merge.go`

Port `MergeOrchestrator` from Python:

```go
package merge

import (
    "fmt"
    "os/exec"
    "path/filepath"
    "strings"

    "github.com/godinj/drem-orchestrator/internal/model"
    "github.com/godinj/drem-orchestrator/internal/worktree"
    "gorm.io/gorm"
)

// MergePlan describes a planned merge (analysis only, no side effects).
type MergePlan struct {
    SourceBranch       string
    TargetWorktree     string
    TargetBranch       string
    CommitsToMerge     []worktree.CommitInfo
    FilesChanged       []string
    PotentialConflicts []string
}

// FeatureMergeReport summarizes merging all agent branches into a feature.
type FeatureMergeReport struct {
    FeatureBranch   string
    AgentMerges     []worktree.MergeResult
    AllSucceeded    bool
    BuildVerified   bool
    BuildOutput     string
    FilesChanged    []string
    CommitCount     int
}

// MergeStatus provides an overview of merge readiness for a project.
type MergeStatus struct {
    ReadyToMerge  []string // branches ready
    Conflicted    []string // branches with conflicts
    Behind        []string // branches behind main
}

// Orchestrator coordinates multi-step merges.
type Orchestrator struct {
    wt *worktree.Manager
    db *gorm.DB
}

// NewOrchestrator creates a MergeOrchestrator.
func NewOrchestrator(wt *worktree.Manager, db *gorm.DB) *Orchestrator
```

#### `PlanAgentMerge(agentBranch, featureWorktree string) (*MergePlan, error)`

Analyze a potential merge without executing it:

1. Get commits on agent branch since it diverged from feature: `GetCommitLog(featureWorktree, featureBranch, 50)`
2. Get changed files: `GetChangedFiles(featureWorktree, featureBranch)`
3. Check for potential conflicts: files changed on both agent and feature branches
   - Get files changed on feature since agent branched
   - Intersection = potential conflicts
4. Return the plan

This is read-only — no git state changes.

#### `MergeAgentIntoFeature(agentBranch, featureWorktree string) (*worktree.MergeResult, error)`

Execute the merge:

1. Check that the feature worktree is clean (`IsClean`)
2. Call `wt.MergeBranch(agentBranch, featureWorktree)`
3. Return the result

#### `MergeAllAgentsIntoFeature(task *model.Task, featureWorktree string) (*FeatureMergeReport, error)`

Merge all completed agent branches for a task's subtasks:

1. Query subtasks with status=DONE from DB
2. For each subtask that has an `AssignedAgentID`:
   - Look up the agent to get its `WorktreeBranch`
   - Call `MergeAgentIntoFeature(agent.WorktreeBranch, featureWorktree)`
   - Collect results
3. If all merges succeeded, run `VerifyBuild(featureWorktree)`
4. Build the report with total files changed and commit count
5. Clean up agent worktrees for successfully merged agents: `wt.RemoveAgentWorktree(branch)`

#### `MergeFeatureIntoMain(task *model.Task) (*worktree.MergeResult, error)`

Merge a completed feature into the default branch:

1. Get the main worktree path: `<bare-repo>/<default-branch>/`
   - Note: For non-bare repos, use the bare repo path directly
2. Ensure main worktree is clean
3. Merge: `wt.MergeBranch(task.WorktreeBranch, mainWorktree)`
4. If merge succeeds, verify build on main
5. If build fails:
   - Reset main: `RunGit(["reset", "--hard", "HEAD~1"], mainWorktree)`
   - Return failed result with build output
6. If all good, sync other features: `SyncFeaturesAfterMerge(task.WorktreeBranch)`
7. Clean up feature worktree: `wt.RemoveFeature(featureName)`

#### `VerifyBuild(worktreePath string) (bool, string, error)`

Try to run the project's build/test command. Check for these in order:

1. If `go.mod` exists: `go test ./...`
2. If `pyproject.toml` exists: `uv run pytest` (fallback: `pytest`)
3. If `Makefile` exists: `make test`
4. If `package.json` exists: `npm test`
5. If none found: return `(true, "no build system detected", nil)`

Run with a 5-minute timeout. Return `(success, output, error)`.

```go
func (o *Orchestrator) VerifyBuild(worktreePath string) (bool, string, error) {
    // Detect build system and run appropriate command
    cmd, args := detectBuildCommand(worktreePath)
    if cmd == "" {
        return true, "no build system detected", nil
    }

    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
    defer cancel()

    c := exec.CommandContext(ctx, cmd, args...)
    c.Dir = worktreePath
    out, err := c.CombinedOutput()
    if err != nil {
        return false, string(out), nil // build failed, not an error
    }
    return true, string(out), nil
}
```

#### `SyncFeaturesAfterMerge(mergedFeature string) ([]worktree.SyncResult, error)`

After merging a feature into main, sync all other features:

1. Call `wt.SyncAll()`
2. Return results

#### `GetMergeStatus(projectID uuid.UUID) (*MergeStatus, error)`

Overview of merge state:

1. Query tasks in MERGING or TESTING_READY status from DB
2. For each task with a WorktreeBranch:
   - Check if branch has conflicts with main (try merge --no-commit --no-ff, then abort)
   - Check if branch is behind main
3. Categorize into ready/conflicted/behind

## Conventions

- Go 1.22+, standard library where possible
- `gofmt` for formatting
- Exported functions have doc comments
- Error wrapping: `fmt.Errorf("context: %w", err)`
- Build verification: `go build ./... && go vet ./...`
