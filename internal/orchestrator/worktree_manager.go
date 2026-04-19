package orchestrator

import (
	"github.com/godinj/drem-orchestrator/internal/worktree"
)

// WorktreeManager is the orchestrator's view of worktree operations.
//
// Production is satisfied by *worktree.Manager (wired in cmd/drem/). Tests
// are satisfied by FakeWorktreeManager. Moving these calls behind an
// interface lets every other internal/orchestrator/ source file drop its
// direct dependency on internal/worktree — only this file needs the import.
//
// Method shapes match *worktree.Manager's current signatures as closely as
// possible so existing call sites are mechanical migrations, not rewrites.
// Accessor methods (BareRepo, DefaultBranchName) exist because Go forbids
// methods and public fields from sharing a name and we want *worktree.Manager
// to satisfy this interface without renaming its public fields.
type WorktreeManager interface {
	BareRepo() string
	DefaultBranchName() string
	FeatureWorktreePath(feature string) string
	MainWorktreePath() (string, error)

	CreateFeature(name string) (*WorktreeInfo, error)
	RemoveFeature(name string) error
	CreateAgentWorktree(featureName string) (*AgentWorktreeInfo, error)
	RemoveAgentWorktree(branch string) error
	ListAgentWorktrees(featureName string) ([]AgentWorktreeInfo, error)

	GenerateRepoMapForMain()
	GenerateRepoMapAsync(worktreePath string)

	// Agent-into-feature merge primitives. Added in the prompt-19 migration
	// so merge_agent.go can drive a rebase + merge sequence without importing
	// internal/worktree directly. *worktree.Manager implements these
	// verbatim; tests substitute a FakeWorktreeManager.
	FindWorktreeByBranch(branch string) (string, error)
	MergeBranch(sourceBranch, targetWorktree string) (*WorktreeMergeResult, error)
	RebaseBranchOnto(sourceWorktree, targetWorktree string) (*WorktreeRebaseResult, error)
}

// WorktreeInfo is re-exported from internal/worktree so orchestrator files
// never have to import internal/worktree directly. *worktree.Manager already
// returns *worktree.WorktreeInfo from CreateFeature, and a type alias keeps
// that call shape working without wrapper allocations.
type WorktreeInfo = worktree.WorktreeInfo

// AgentWorktreeInfo is re-exported from internal/worktree.
type AgentWorktreeInfo = worktree.AgentWorktreeInfo

// WorktreeMergeResult is the worktree package's merge-result shape, re-exported
// so callers don't need to import internal/worktree just to reference the
// return type of the in-process merger. Named distinct from orchestrator's
// own MergeResult (defined in merge_dispatch.go) to avoid collision.
type WorktreeMergeResult = worktree.MergeResult

// WorktreeRebaseResult re-exports internal/worktree.RebaseResult.
type WorktreeRebaseResult = worktree.RebaseResult

// WorktreeSyncResult re-exports internal/worktree.SyncResult.
type WorktreeSyncResult = worktree.SyncResult

// Compile-time assertion that *worktree.Manager satisfies WorktreeManager.
// When this assertion stops compiling, either the interface drifted or the
// Manager lost an accessor — both are cases the maintainer must resolve.
var _ WorktreeManager = (*worktree.Manager)(nil)
