package orchestrator

import (
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/worktree"
)

// mergerClient abstracts the merge operations used by the orchestrator.
// Defined at the consumption site (per architecture rule) so tests can
// provide stubs without importing the merge package.
type mergerClient interface {
	MergeFeatureIntoMain(task *model.Task) (*worktree.MergeResult, error)
	MergeAgentIntoFeature(agentBranch, featureWorktree string) (*worktree.MergeResult, error)
}
