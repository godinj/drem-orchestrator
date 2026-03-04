# Agent: Git Worktree Manager

You are working on the `feature/go-rewrite` branch of Drem Orchestrator, a multi-agent task orchestration system being rewritten from Python to Go.
Your task is to implement the git worktree management layer — creating feature worktrees, agent worktrees, merging branches, and syncing.

## Context

Read these before starting:
- `docs/go-rewrite/DESIGN.md` (focus on "Git Worktree Management" section and the 3-tier worktree hierarchy)
- `src/orchestrator/worktree.py` (Python implementation to port — full WorktreeManager class)
- `src/orchestrator/git_utils.py` (Python git helpers to port — run_git, CommitInfo, get_commit_log, get_changed_files, is_clean)
- `CLAUDE.md` (build commands, conventions)

## Deliverables

### New file: `internal/worktree/git.go`

Low-level git helpers (port from `git_utils.py`):

```go
package worktree

import (
    "fmt"
    "os/exec"
    "strings"
    "time"
)

// GitError represents a git command failure.
type GitError struct {
    Command    string
    ReturnCode int
    Stderr     string
}

func (e *GitError) Error() string

// CommitInfo holds parsed git log data.
type CommitInfo struct {
    SHA        string
    ShortSHA   string
    Author     string
    Date       time.Time
    Message    string
}

// RunGit executes a git command in the given directory and returns stdout.
// Returns GitError on non-zero exit.
func RunGit(args []string, cwd string) (string, error)

// GetCommitLog returns commits since baseRef (up to maxCount).
// Uses: git log --format="%H|%h|%an|%aI|%s" <baseRef>..HEAD
func GetCommitLog(worktreePath, baseRef string, maxCount int) ([]CommitInfo, error)

// GetChangedFiles returns files changed since baseRef.
// Uses: git diff --name-only <baseRef>..HEAD
func GetChangedFiles(worktreePath, baseRef string) ([]string, error)

// IsClean returns true if the worktree has no uncommitted changes.
// Uses: git status --porcelain
// Ignores .claude/ directory entries in the output.
func IsClean(worktreePath string) (bool, error)

// GetDefaultBranch detects the default branch name (main or master).
// Uses: git symbolic-ref refs/remotes/origin/HEAD, falls back to checking if "main" branch exists.
func GetDefaultBranch(repoPath string) (string, error)
```

### New file: `internal/worktree/manager.go`

Port the Python `WorktreeManager` class:

```go
package worktree

import (
    "fmt"
    "path/filepath"
    "github.com/google/uuid"
)

// WorktreeInfo describes a feature worktree.
type WorktreeInfo struct {
    Path      string
    Branch    string
    Head      string // commit SHA
    IsBare    bool
}

// AgentWorktreeInfo describes an agent's nested worktree.
type AgentWorktreeInfo struct {
    Path          string
    Branch        string
    Head          string
    ParentFeature string
}

// MergeResult describes the outcome of a git merge.
type MergeResult struct {
    Success      bool
    SourceBranch string
    TargetBranch string
    MergeCommit  string
    Conflicts    []string
}

// SyncResult describes the outcome of syncing a feature branch.
type SyncResult struct {
    Branch  string
    Success bool
    Error   string
}

// BranchStatus describes the state of a branch relative to its upstream.
type BranchStatus struct {
    Branch           string
    Ahead            int
    Behind           int
    DirtyFiles       int
    LastCommitSHA    string
    LastCommitMessage string
}

// Manager manages git worktrees in a 3-tier hierarchy:
// bare-repo.git/ → feature/X/ → .claude/worktrees/agent-<uuid>/
type Manager struct {
    BareRepoPath  string
    DefaultBranch string
}

// NewManager creates a WorktreeManager for the given bare repo.
func NewManager(bareRepoPath, defaultBranch string) *Manager
```

Implement these methods:

#### `CreateFeature(name string) (*WorktreeInfo, error)`

Creates a feature worktree at `<bare-repo>/feature/<name>` with branch `feature/<name>`.

```bash
git worktree add <bare-repo>/feature/<name> -b feature/<name>
# cwd: bare repo path
```

If the worktree already exists (directory exists), return its info without error.

#### `RemoveFeature(name string) error`

Removes a feature worktree and its branch.

```bash
git worktree remove feature/<name> --force
git branch -D feature/<name>
```

#### `ListWorktrees() ([]WorktreeInfo, error)`

Lists all worktrees using:

```bash
git worktree list --porcelain
```

Parse the output: each worktree block has `worktree <path>`, `HEAD <sha>`, `branch refs/heads/<branch>` lines separated by blank lines.

#### `CreateAgentWorktree(featureName string) (*AgentWorktreeInfo, error)`

Creates a nested agent worktree inside a feature worktree:

```
feature/<featureName>/.claude/worktrees/agent-<uuid>/
```

With branch name `worktree-agent-<uuid>`. Base it on the feature branch.

```bash
# First, ensure .claude/worktrees/ directory exists
mkdir -p feature/<featureName>/.claude/worktrees/

# Create worktree
git worktree add feature/<featureName>/.claude/worktrees/agent-<uuid> -b worktree-agent-<uuid> feature/<featureName>
```

Generate a new UUID for each agent worktree.

#### `RemoveAgentWorktree(branch string) error`

Removes an agent worktree by its branch name.

```bash
git worktree remove <path-that-has-this-branch> --force
git branch -D <branch>
```

Use `ListWorktrees()` to find the path for the given branch.

#### `ListAgentWorktrees(featureName string) ([]AgentWorktreeInfo, error)`

Lists agent worktrees inside a feature by scanning `feature/<name>/.claude/worktrees/`.

#### `MergeBranch(sourceBranch, targetWorktree string) (*MergeResult, error)`

Merges a source branch into the target worktree:

```bash
# In the target worktree
git merge <sourceBranch> --no-edit
```

If the merge fails with conflicts:
1. Run `git diff --name-only --diff-filter=U` to get conflict file list
2. Run `git merge --abort`
3. Return `MergeResult{Success: false, Conflicts: [...]}`

If merge succeeds, get the merge commit SHA from `git rev-parse HEAD`.

#### `SyncAll() ([]SyncResult, error)`

Rebases all feature branches onto the default branch:

```bash
# For each feature worktree:
git -C <feature-path> rebase <default-branch>
```

Collect results. If a rebase fails, abort it (`git rebase --abort`) and record the error.

#### `GetBranchStatus(worktreePath string) (*BranchStatus, error)`

Returns ahead/behind counts and dirty file count:

```bash
git -C <path> rev-list --left-right --count HEAD...origin/<branch>
git -C <path> status --porcelain
git -C <path> log -1 --format="%H|%s"
```

### New file: `internal/worktree/manager_test.go`

Write tests using a temporary bare git repo:

1. `TestCreateAndListFeature` — create feature, verify it appears in list
2. `TestCreateAgentWorktree` — create agent worktree inside feature, verify nested path
3. `TestRemoveFeature` — create then remove, verify gone
4. `TestMergeBranch` — create feature, make a commit, merge into another worktree
5. `TestMergeBranchConflict` — create conflicting changes, verify conflict detection and abort

Use `t.TempDir()` for the bare repo. Initialize it with:

```bash
git init --bare <tmpdir>/test.git
git -C <tmpdir>/test.git commit --allow-empty -m "init"
```

## Conventions

- Go 1.22+, standard library where possible
- `gofmt` for formatting
- Exported functions have doc comments
- Error wrapping: `fmt.Errorf("context: %w", err)`
- Build verification: `go build ./... && go vet ./...`
