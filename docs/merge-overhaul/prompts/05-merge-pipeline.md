# Agent: Merge Pipeline — Rebase-Before-Merge & Retry

You are working on the `master` branch of Drem Orchestrator, a Go-based multi-agent orchestrator that manages Claude Code agents via tmux in git bare repos with worktrees.
Your task is to update the merge orchestrator to rebase agent branches before merging and add retry logic for transient failures.

## Context

Read these before starting:
- `docs/merge-overhaul/prd-merge-reliability.md` (sections 4.2, 4.5)
- `internal/merge/merge.go` (MergeAgentIntoFeature, MergeAllAgentsIntoFeature, Orchestrator struct)
- `internal/worktree/manager.go` (MergeResult struct — now has GitStderr and GitCommand fields, MergeBranch method)
- `internal/worktree/git.go` (RebaseBranch function, RebaseResult struct — these are new, added by a prior agent)

## Dependencies

This agent depends on Agent 01 (worktree-merge-improvements). The following should already exist:
- `RebaseBranch(sourceWorktree, targetWorktree string) (*RebaseResult, error)` in `internal/worktree/git.go`
- `RebaseResult` struct with `Success`, `Conflicts`, `GitStderr` fields
- `MergeResult` struct with `GitStderr` and `GitCommand` fields
- `FindWorktreeByBranch(branch string) (string, error)` on the worktree Manager

If these don't exist yet, create minimal stubs in `internal/worktree/` with the signatures above and implement against them. Mark stubs with `// TODO: implemented by agent 01` comments.

## Deliverables

### 1. Rebase-Before-Merge (`internal/merge/merge.go`)

Modify `MergeAgentIntoFeature()` to rebase the agent branch onto the feature HEAD before merging. The current implementation directly calls `o.wt.MergeBranch()`.

New flow:

```go
func (o *Orchestrator) MergeAgentIntoFeature(agentBranch, featureWorktree string) (*worktree.MergeResult, error) {
    // 1. Find the agent's worktree by branch name
    agentWorktree, err := o.wt.FindWorktreeByBranch(agentBranch)
    if err != nil {
        // Agent worktree may already be cleaned up — fall back to direct merge
        return o.wt.MergeBranch(agentBranch, featureWorktree)
    }

    // 2. Rebase agent branch onto feature HEAD
    rebaseResult, err := worktree.RebaseBranch(agentWorktree, featureWorktree)
    if err != nil {
        return nil, fmt.Errorf("rebase %s onto feature: %w", agentBranch, err)
    }

    if !rebaseResult.Success {
        // Real conflict — return as merge failure without attempting merge
        return &worktree.MergeResult{
            Success:      false,
            SourceBranch: agentBranch,
            Conflicts:    rebaseResult.Conflicts,
            GitStderr:    rebaseResult.GitStderr,
            GitCommand:   "git rebase",
        }, nil
    }

    // 3. After successful rebase, merge is guaranteed clean (fast-forward-able)
    return o.wt.MergeBranch(agentBranch, featureWorktree)
}
```

### 2. Merge Retry with Backoff (`internal/merge/merge.go`)

Wrap the rebase+merge flow in a retry loop for transient failures. Add a constant for max retries.

```go
const maxMergeRetries = 3
```

Create a new method that wraps the retry logic, or modify `MergeAgentIntoFeature` directly:

```go
func (o *Orchestrator) MergeAgentIntoFeature(agentBranch, featureWorktree string) (*worktree.MergeResult, error) {
    var lastResult *worktree.MergeResult

    for attempt := 1; attempt <= maxMergeRetries; attempt++ {
        // Find agent worktree (may not exist if cleaned up)
        agentWorktree, findErr := o.wt.FindWorktreeByBranch(agentBranch)

        // Rebase if worktree exists
        if findErr == nil {
            rebaseResult, err := worktree.RebaseBranch(agentWorktree, featureWorktree)
            if err != nil {
                return nil, fmt.Errorf("rebase attempt %d: %w", attempt, err)
            }
            if !rebaseResult.Success {
                // Real conflict — don't retry
                return &worktree.MergeResult{
                    Success:      false,
                    SourceBranch: agentBranch,
                    Conflicts:    rebaseResult.Conflicts,
                    GitStderr:    rebaseResult.GitStderr,
                    GitCommand:   "git rebase",
                }, nil
            }
        }

        // Merge
        var err error
        lastResult, err = o.wt.MergeBranch(agentBranch, featureWorktree)
        if err != nil {
            return nil, fmt.Errorf("merge attempt %d: %w", attempt, err)
        }

        if lastResult.Success {
            return lastResult, nil
        }

        // If conflicts are real file conflicts, don't retry
        if len(lastResult.Conflicts) > 0 {
            return lastResult, nil
        }

        // Transient failure — retry after backoff
        slog.Warn("merge failed transiently, retrying",
            "attempt", attempt,
            "max_attempts", maxMergeRetries,
            "agent_branch", agentBranch,
            "stderr", lastResult.GitStderr,
        )
        time.Sleep(time.Duration(attempt) * 2 * time.Second)
    }

    return lastResult, nil
}
```

Key retry rules:
- **Real conflicts** (rebase fails or merge returns conflicts): return immediately, no retry
- **Hard errors** (RunGit returns a non-GitError): return immediately, no retry
- **Transient failures** (merge fails with no file conflicts — e.g., lock contention, ref issues): retry with exponential backoff (2s, 4s, 6s)

### 3. Update MergeAllAgentsIntoFeature

Review `MergeAllAgentsIntoFeature()` to ensure it works with the updated `MergeAgentIntoFeature()`. It iterates over agent branches and merges them one by one. The retry logic is now internal to `MergeAgentIntoFeature()`, so `MergeAllAgentsIntoFeature()` should not need changes — but verify that error handling is correct with the enriched `MergeResult` (new `GitStderr`/`GitCommand` fields).

### 4. Tests

Add tests to `internal/merge/merge_test.go` (create if it doesn't exist):

- **Clean rebase + merge**: Agent branch has non-overlapping changes. Rebase succeeds, merge succeeds. Verify `Success: true`.
- **Rebase conflict**: Agent and feature touch same file same line. Rebase fails with conflicts. Verify `Success: false`, `Conflicts` is populated, no merge attempted.
- **Agent worktree missing**: Agent worktree already cleaned up. Falls back to direct merge. Verify it still works.
- **Transient failure + retry succeeds**: Mock/arrange for first merge to fail with no conflicts (transient), second to succeed. Verify final result is success and retry was attempted.
- **Real conflict — no retry**: Merge fails with file conflicts. Verify only 1 attempt (no retry for real conflicts).
- **Max retries exhausted**: All attempts fail transiently. Verify the last result is returned after 3 attempts.

Tests should set up real git repos using `t.TempDir()` for the rebase/merge scenarios. For retry tests, you may need to arrange the git state to cause transient-like failures, or use a test helper that wraps the worktree manager.

## Scope Limitation

ONLY modify files in `internal/merge/`. Do NOT touch `internal/worktree/`, `internal/orchestrator/`, or `internal/agent/`. If you need worktree functions that don't exist yet, add minimal stubs to `internal/worktree/` with `// TODO` markers.

## Conventions

- Go 1.22+ with standard library
- `gofmt` for formatting
- Exported functions have doc comments
- Error wrapping: `fmt.Errorf("context: %w", err)`
- Table-driven tests
- Build verification: `go build ./... && go test ./...`
