# Agent: Worktree Merge Improvements

You are working on the `master` branch of Drem Orchestrator, a Go-based multi-agent orchestrator that manages Claude Code agents via tmux in git bare repos with worktrees.
Your task is to improve the worktree layer's merge infrastructure: enrich MergeResult with diagnostic fields, add pre-merge ref verification, and add a RebaseBranch method.

## Context

Read these before starting:
- `docs/merge-overhaul/prd-merge-reliability.md` (sections 4.2, 4.3, 4.4)
- `internal/worktree/manager.go` (MergeResult struct, MergeBranch function, Manager struct)
- `internal/worktree/git.go` (RunGit helper, GitError type, existing git helpers)

## Deliverables

### 1. Enrich MergeResult (`internal/worktree/manager.go`)

The current `MergeResult` struct:

```go
type MergeResult struct {
    Success      bool
    SourceBranch string
    TargetBranch string
    MergeCommit  string
    Conflicts    []string
}
```

Add two fields:

```go
type MergeResult struct {
    Success      bool
    SourceBranch string
    TargetBranch string
    MergeCommit  string
    Conflicts    []string
    GitStderr    string   // raw git stderr on failure
    GitCommand   string   // the exact git command that failed
}
```

Update `MergeBranch()` to populate `GitStderr` and `GitCommand` on failure. Currently it runs `git merge --no-ff <sourceBranch>` and parses the error, but discards stderr detail. Capture the full stderr from `RunGit` (which wraps errors as `GitError` containing stderr) and store it in the result.

### 2. Pre-merge ref verification (`internal/worktree/manager.go`)

Modify `MergeBranch()` to verify the source branch ref is resolvable in the target worktree before attempting the merge. In a bare repo with multiple worktrees, branch refs created in one worktree may not be immediately visible in another.

Add this logic at the start of `MergeBranch()`:

```go
// Verify the branch ref is resolvable in the target worktree
_, err := RunGit([]string{"rev-parse", "--verify", sourceBranch}, targetWorktree)
if err != nil {
    slog.Warn("branch ref not visible, fetching",
        "branch", sourceBranch,
        "worktree", targetWorktree,
    )
    // In a bare repo, fetch from local to refresh refs
    RunGit([]string{"fetch", ".", sourceBranch + ":" + sourceBranch}, targetWorktree)

    // Re-verify
    _, err = RunGit([]string{"rev-parse", "--verify", sourceBranch}, targetWorktree)
    if err != nil {
        return nil, fmt.Errorf("branch %s not resolvable after fetch: %w", sourceBranch, err)
    }
}
```

### 3. Add RebaseBranch method (`internal/worktree/git.go`)

Add a new exported type and function:

```go
// RebaseResult describes the outcome of a rebase operation.
type RebaseResult struct {
    Success   bool
    Conflicts []string  // conflicting file paths if rebase failed
    GitStderr string    // raw git stderr on failure
}

// RebaseBranch rebases the branch checked out in sourceWorktree onto
// the HEAD of targetWorktree. On conflict, the rebase is aborted and
// the source worktree is left unchanged.
func RebaseBranch(sourceWorktree, targetWorktree string) (*RebaseResult, error)
```

Implementation:
1. Resolve target HEAD: `RunGit(["rev-parse", "HEAD"], targetWorktree)`
2. Attempt rebase: `RunGit(["rebase", targetHEAD], sourceWorktree)`
3. On success: return `&RebaseResult{Success: true}`
4. On failure: run `RunGit(["rebase", "--abort"], sourceWorktree)`, parse conflicting files from stderr, return `&RebaseResult{Success: false, Conflicts: ..., GitStderr: ...}`

To parse conflicts from rebase stderr, look for lines matching `CONFLICT (content): Merge conflict in <file>`. Extract the file paths.

### 4. Add helper: FindWorktreeByBranch (`internal/worktree/manager.go`)

Add a method to locate a worktree path given a branch name:

```go
// FindWorktreeByBranch returns the worktree path for the given branch,
// or an error if no worktree has that branch checked out.
func (m *Manager) FindWorktreeByBranch(branch string) (string, error)
```

Use `ListWorktrees()` to scan and match on the `Branch` field.

### 5. Tests

Add table-driven tests for:

- **MergeBranch with enriched result**: Verify `GitStderr` and `GitCommand` are populated on a failed merge. Set up two branches with conflicting changes, attempt merge, assert fields are non-empty.
- **MergeBranch pre-merge fetch**: This is harder to unit test (requires ref visibility issues). At minimum, test the happy path where the ref IS visible and the fetch is skipped.
- **RebaseBranch clean rebase**: Create a branch with non-overlapping changes, rebase onto updated target, verify success.
- **RebaseBranch conflict**: Create two branches modifying the same line, attempt rebase, verify `Success: false`, `Conflicts` contains the file, and the source worktree is clean (rebase was aborted).
- **FindWorktreeByBranch**: Verify it returns the correct path for a known branch and an error for an unknown branch.

All tests should use `t.TempDir()` to create temporary bare repos with worktrees for isolation.

## Scope Limitation

ONLY modify files in `internal/worktree/`. Do NOT touch `internal/merge/`, `internal/orchestrator/`, or any other package. The merge pipeline integration (using RebaseBranch in MergeAgentIntoFeature) is handled by a separate agent.

## Conventions

- Go 1.22+ with standard library
- `gofmt` for formatting
- Exported functions have doc comments
- Error wrapping: `fmt.Errorf("context: %w", err)`
- Table-driven tests
- Build verification: `go build ./... && go test ./...`
