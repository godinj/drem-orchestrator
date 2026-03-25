package merge

import (
	"sync"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/worktree"
)

// Merger abstracts the merge operations that MergeQueue delegates to.
// *Orchestrator satisfies this interface.
type Merger interface {
	MergeFeatureIntoMain(task *model.Task) (*worktree.MergeResult, error)
	MergeAgentIntoFeature(agentBranch, featureWorktree string) (*worktree.MergeResult, error)
}

// Rebaser abstracts the rebase + worktree-lookup operations needed by
// MergeQueue.  *worktree.Manager satisfies this via its exported helpers.
type Rebaser interface {
	FindWorktreeByBranch(branch string) (string, error)
	MainWorktreePath() (string, error)
	RebaseBranchOnto(featureWorktree, mainWorktree string) (*worktree.RebaseResult, error)
}

// MergeQueue wraps a Merger and adds two behaviors:
//   - Serialization: MergeFeatureIntoMain calls are mutually exclusive.
//   - Rebase-before-merge: the feature branch is rebased onto current main
//     HEAD before MergeFeatureIntoMain delegates to the underlying merger.
type MergeQueue struct {
	mu      sync.Mutex
	merger  Merger
	rebaser Rebaser
}

// NewMergeQueue creates a MergeQueue that delegates merges to merger and
// uses rebaser to rebase feature branches before merge.
func NewMergeQueue(merger Merger, rebaser Rebaser) *MergeQueue {
	return &MergeQueue{merger: merger, rebaser: rebaser}
}

// MergeFeatureIntoMain serialises access, rebases the feature branch onto
// the current main HEAD, then delegates to the underlying merger.
func (q *MergeQueue) MergeFeatureIntoMain(task *model.Task) (*worktree.MergeResult, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	featureWt, err := q.rebaser.FindWorktreeByBranch(task.WorktreeBranch)
	if err == nil {
		mainWt, mainErr := q.rebaser.MainWorktreePath()
		if mainErr != nil {
			return nil, mainErr
		}
		rebaseResult, rebaseErr := q.rebaser.RebaseBranchOnto(featureWt, mainWt)
		if rebaseErr != nil {
			return nil, rebaseErr
		}
		if !rebaseResult.Success {
			result := &worktree.MergeResult{
				Success:   false,
				Conflicts: rebaseResult.Conflicts,
				GitStderr: rebaseResult.GitStderr,
			}
			attachClassification(result)
			return result, nil
		}
	}

	result, mergeErr := q.merger.MergeFeatureIntoMain(task)
	if mergeErr != nil {
		return result, mergeErr
	}
	if !result.Success {
		attachClassification(result)
	}
	return result, nil
}

// attachClassification classifies conflicts in a failed MergeResult and
// populates the ClassifiedDetails, TrivialCount, and NonTrivialCount fields.
func attachClassification(result *worktree.MergeResult) {
	if len(result.Conflicts) == 0 {
		return
	}
	classified := ClassifyConflicts(result.Conflicts)
	result.ClassifiedDetails = FormatClassifiedConflicts(classified)
	for _, cc := range classified {
		if cc.Class == Trivial {
			result.TrivialCount++
		} else {
			result.NonTrivialCount++
		}
	}
}

// MergeAgentIntoFeature passes through to the underlying merger without
// serialization.
func (q *MergeQueue) MergeAgentIntoFeature(agentBranch, featureWorktree string) (*worktree.MergeResult, error) {
	return q.merger.MergeAgentIntoFeature(agentBranch, featureWorktree)
}
