// merge_agent.go implements the agent-branch-into-feature-worktree merge
// path directly against gitexec + WorktreeManager, replacing the
// internal/merge/ package's MergeAgentIntoFeature. The containerization
// migration (docs/containerization/remaining-work.md Step 4) deletes
// internal/merge entirely — the feature-into-main path moves to the
// merger container via dispatchMerge, and this small in-process helper
// takes over the agent-into-feature path, which is still a local
// worktree operation and does not need a container.
//
// The logic mirrors the original mergeWithRebaseAndRetry flow:
//  1. Auto-commit artefacts in the feature worktree (e.g. plan.json).
//  2. Untrack ephemeral files so they don't surface as conflicts.
//  3. Rebase the agent worktree onto feature HEAD (skipped if the agent
//     worktree is missing — typical during reconcile).
//  4. Merge the agent branch, retrying transient failures up to
//     maxAgentMergeRetries.
//
// The package-level WorktreeMergeResult alias continues to carry the
// result shape, so handleAgentMergeFailure and the existing downstream
// consumers stay unchanged.
package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/godinj/drem-orchestrator/internal/gitexec"
)

const (
	// maxAgentMergeRetries is the retry budget for transient merge failures
	// of an agent branch into a feature worktree. Real conflicts (non-empty
	// conflict list from git) return immediately; only no-conflict failures
	// trigger retry.
	maxAgentMergeRetries = 3

	// agentMergeRetryBackoff is the base delay between retry attempts; it is
	// multiplied by the attempt number for linear backoff. Kept modest since
	// the retry target is a transient local-git failure, not a network issue.
	agentMergeRetryBackoff = 2 * time.Second
)

// mergeAgentWorktreeOps is the narrow subset of worktree operations needed by
// mergeAgentIntoFeature. It lets callers pass a full *worktree.Manager or a
// smaller wrapper without expanding the WorktreeManager interface with
// rarely-used merge plumbing.
type mergeAgentWorktreeOps interface {
	// FindWorktreeByBranch returns the directory of a worktree that has the
	// branch checked out. An error (typically "not found") means the agent
	// worktree has already been cleaned up; callers skip rebase in that case.
	FindWorktreeByBranch(branch string) (string, error)

	// MergeBranch attempts to merge sourceBranch into targetWorktree. It
	// returns a populated WorktreeMergeResult (never nil) on success and on
	// failure; a non-nil error indicates a structural problem (e.g. ref
	// resolution failure) rather than a merge conflict.
	MergeBranch(sourceBranch, targetWorktree string) (*WorktreeMergeResult, error)

	// RebaseBranchOnto rebases the source worktree's current branch onto
	// the tip of the target worktree's branch. Returns a RebaseResult whose
	// Success is false when the rebase hit conflicts.
	RebaseBranchOnto(sourceWorktree, targetWorktree string) (*WorktreeRebaseResult, error)
}

// mergeAgentBranchIntoFeature is the Orchestrator entry point that reuses
// o.worktree's merge/rebase primitives and the orchestrator's logger. It
// exists as a thin method so callers (agent_results.go, reconcile.go) can
// replace their old o.merger.MergeAgentIntoFeature call sites with a
// direct method call without re-plumbing the worktree dependency.
func (o *Orchestrator) mergeAgentBranchIntoFeature(ctx context.Context, agentBranch, featureWorktree string) (*WorktreeMergeResult, error) {
	if o.worktree == nil {
		return nil, fmt.Errorf("merge agent into feature: orchestrator has no worktree manager")
	}
	return mergeAgentIntoFeature(ctx, o.worktree, agentBranch, featureWorktree, o.logger)
}

// mergeAgentIntoFeature performs the full agent-branch-into-feature-worktree
// sequence using gitexec for the diagnostic / dirty-state operations and the
// provided mergeAgentWorktreeOps for the git-level merge/rebase primitives.
// The public entry point is the Orchestrator method above; this helper
// isolates the logic so it can be unit-tested with a fake worktree.
func mergeAgentIntoFeature(ctx context.Context, ops mergeAgentWorktreeOps, agentBranch, featureWorktree string, logger *slog.Logger) (*WorktreeMergeResult, error) {
	if ops == nil {
		return nil, fmt.Errorf("merge agent into feature: worktree ops are nil")
	}
	if featureWorktree == "" {
		return nil, fmt.Errorf("merge agent into feature: empty feature worktree")
	}
	if agentBranch == "" {
		return nil, fmt.Errorf("merge agent into feature: empty agent branch")
	}

	// Auto-commit any dirty artefacts (e.g. plan.json, .claude/) so the
	// merge command does not bail with "local changes would be overwritten".
	clean, err := gitexec.IsClean(ctx, featureWorktree)
	if err != nil {
		return nil, fmt.Errorf("merge agent into feature: check clean: %w", err)
	}
	if !clean {
		committed, commitErr := gitexec.CommitUnstagedChanges(ctx, featureWorktree,
			"chore: commit orchestrator artifacts before merge")
		if commitErr != nil {
			return nil, fmt.Errorf("merge agent into feature: auto-commit dirty worktree: %w", commitErr)
		}
		if committed && logger != nil {
			logger.Info("auto-committed orchestrator artifacts before merge",
				"feature_worktree", featureWorktree,
				"agent_branch", agentBranch)
		}
	}

	// Untrack ephemeral files (plan.json) so they do not become merge conflicts
	// in future operations. Best-effort.
	if _, rmErr := gitexec.UntrackEphemeralFiles(ctx, featureWorktree); rmErr != nil && logger != nil {
		logger.Warn("failed to untrack ephemeral files before agent merge",
			"feature_worktree", featureWorktree, "error", rmErr)
	}

	// Rebase the agent worktree onto feature HEAD, if the agent worktree still
	// exists. A missing worktree means the agent has already been reaped and
	// we go straight to merge.
	agentWorktree, findErr := ops.FindWorktreeByBranch(agentBranch)
	if findErr == nil {
		rebaseResult, rebaseErr := ops.RebaseBranchOnto(agentWorktree, featureWorktree)
		if rebaseErr != nil {
			return nil, fmt.Errorf("merge agent into feature: rebase: %w", rebaseErr)
		}
		if !rebaseResult.Success {
			return &WorktreeMergeResult{
				Success:      false,
				SourceBranch: agentBranch,
				Conflicts:    rebaseResult.Conflicts,
				GitStderr:    rebaseResult.GitStderr,
				GitCommand:   "git rebase",
			}, nil
		}
	}

	// Merge with retry for transient failures (e.g. racing against another
	// process briefly holding an index lock). Real file-level conflicts
	// return immediately.
	var lastResult *WorktreeMergeResult
	for attempt := 1; attempt <= maxAgentMergeRetries; attempt++ {
		result, err := ops.MergeBranch(agentBranch, featureWorktree)
		if err != nil {
			return nil, fmt.Errorf("merge agent into feature: attempt %d: %w", attempt, err)
		}
		if result.Success || len(result.Conflicts) > 0 {
			return result, nil
		}
		lastResult = result
		if attempt < maxAgentMergeRetries && logger != nil {
			logger.Warn("merge failed transiently, retrying",
				"attempt", attempt,
				"max_attempts", maxAgentMergeRetries,
				"agent_branch", agentBranch,
				"stderr", strings.TrimSpace(result.GitStderr))
		}
		if attempt < maxAgentMergeRetries {
			time.Sleep(time.Duration(attempt) * agentMergeRetryBackoff)
		}
	}
	return lastResult, nil
}
