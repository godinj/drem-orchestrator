package orchestrator

// WorktreeManager is the orchestrator's view of worktree operations.
//
// Production is satisfied by a concrete manager wired in cmd/drem/. Tests are
// satisfied by FakeWorktreeManager. Moving these calls behind an interface
// lets every other internal/orchestrator/ source file drop its direct
// dependency on any particular worktree implementation.
//
// Method shapes are kept narrow — just what the orchestrator actually calls.
// The types below (WorktreeInfo, AgentWorktreeInfo, WorktreeMergeResult,
// WorktreeRebaseResult, WorktreeSyncResult) are owned by this package since
// the host worktree package was removed as part of the containerization
// cleanup.
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

	// Agent-into-feature merge primitives used by merge_agent.go to drive a
	// rebase + merge sequence. Tests substitute a FakeWorktreeManager.
	FindWorktreeByBranch(branch string) (string, error)
	MergeBranch(sourceBranch, targetWorktree string) (*WorktreeMergeResult, error)
	RebaseBranchOnto(sourceWorktree, targetWorktree string) (*WorktreeRebaseResult, error)
}

// WorktreeInfo describes a feature worktree.
type WorktreeInfo struct {
	Path   string
	Branch string
	Head   string // commit SHA
	IsBare bool
}

// AgentWorktreeInfo describes an agent's nested worktree.
type AgentWorktreeInfo struct {
	Path          string
	Branch        string
	Head          string
	ParentFeature string
}

// WorktreeMergeResult describes the outcome of a git merge.
type WorktreeMergeResult struct {
	Success           bool
	SourceBranch      string
	TargetBranch      string
	MergeCommit       string
	Conflicts         []string
	GitStderr         string // raw git stderr on failure
	GitCommand        string // the exact git command that failed
	ClassifiedDetails string // formatted conflict classification
	TrivialCount      int    // count of trivial conflicts
	NonTrivialCount   int    // count of non-trivial conflicts
}

// WorktreeRebaseResult describes the outcome of a git rebase.
type WorktreeRebaseResult struct {
	Success   bool
	Conflicts []string // conflicting file paths if rebase failed
	GitStderr string   // raw git stderr on failure
}

// WorktreeSyncResult describes the outcome of syncing a feature branch.
type WorktreeSyncResult struct {
	Branch  string
	Success bool
	Error   string
}
