package orchestrator

import (
	"github.com/godinj/drem-orchestrator/internal/agent"
	"github.com/godinj/drem-orchestrator/internal/worktreehost"
	"gorm.io/gorm"
)

// NewHostWorktreeManager constructs a host-mode WorktreeManager implementation
// suitable for production wiring in cmd/drem/main.go. It wraps a
// worktreehost.Manager with an adapter that converts the host package's
// concrete result types into the orchestrator's interface types.
//
// This constructor exists so cmd/drem/ does not need to know about the
// worktreehost type aliases — it only sees the orchestrator interface and
// the *HostWorktreeManager boot-helper handle.
func NewHostWorktreeManager(bareRepoPath, defaultBranch string) WorktreeManager {
	return &hostWorktreeAdapter{
		mgr: worktreehost.NewManager(bareRepoPath, defaultBranch),
	}
}

// HostWorktreeManager is the concrete host-mode manager, exported for callers
// that need access to boot-time helpers (MigrateToGroupedLayout,
// MigrateAgentPaths) that are not part of the core WorktreeManager interface.
type HostWorktreeManager struct {
	adapter *hostWorktreeAdapter
}

// NewHostManager constructs a HostWorktreeManager for use at boot time.
// The returned value satisfies WorktreeManager via AsInterface() and also
// exposes the boot-time migration helpers.
func NewHostManager(bareRepoPath, defaultBranch string) *HostWorktreeManager {
	return &HostWorktreeManager{
		adapter: &hostWorktreeAdapter{
			mgr: worktreehost.NewManager(bareRepoPath, defaultBranch),
		},
	}
}

// AsInterface returns the host manager as the WorktreeManager interface.
func (h *HostWorktreeManager) AsInterface() WorktreeManager {
	return h.adapter
}

// MigrateToGroupedLayout runs the one-off boot migration that relocates
// old-layout worktrees into the grouped-feature layout.
func (h *HostWorktreeManager) MigrateToGroupedLayout() error {
	return h.adapter.mgr.MigrateToGroupedLayout()
}

// MigrateAgentPaths rewrites Agent.WorktreePath values in the database from
// the old nested (.claude/worktrees/agent-...) layout to the new sibling
// layout (feature/<name>/agent-...).
func (h *HostWorktreeManager) MigrateAgentPaths(db *gorm.DB) {
	h.adapter.mgr.MigrateAgentPaths(db)
}

// CreateAgentWorktree is exposed so callers that need direct agent-worktree
// creation (agent.Runner.SpawnAgent) can call it without going through the
// interface. Returns the host package's concrete type shape.
func (h *HostWorktreeManager) CreateAgentWorktree(featureName string) (*AgentWorktreeInfo, error) {
	return h.adapter.CreateAgentWorktree(featureName)
}

// AsAgentWorktreeManager returns an agent.WorktreeManager that wraps this
// host manager. The returned value is used to wire agent.NewRunner at boot.
func (h *HostWorktreeManager) AsAgentWorktreeManager() agent.WorktreeManager {
	return &hostAgentAdapter{mgr: h.adapter.mgr}
}

// hostAgentAdapter wraps *worktreehost.Manager to satisfy agent.WorktreeManager.
// Separate from hostWorktreeAdapter because agent.WorktreeManager returns
// agent.AgentWorktreeInfo, not the orchestrator's AgentWorktreeInfo.
type hostAgentAdapter struct {
	mgr *worktreehost.Manager
}

// compile-time assertion.
var _ agent.WorktreeManager = (*hostAgentAdapter)(nil)

func (a *hostAgentAdapter) CreateAgentWorktree(featureName string) (*agent.AgentWorktreeInfo, error) {
	info, err := a.mgr.CreateAgentWorktree(featureName)
	if err != nil {
		return nil, err
	}
	return &agent.AgentWorktreeInfo{
		Path:          info.Path,
		Branch:        info.Branch,
		Head:          info.Head,
		ParentFeature: info.ParentFeature,
	}, nil
}

func (a *hostAgentAdapter) GenerateRepoMapAsync(worktreePath string) {
	a.mgr.GenerateRepoMapAsync(worktreePath)
}

// hostWorktreeAdapter wraps *worktreehost.Manager so its concrete result
// types (worktreehost.WorktreeInfo, MergeResult, RebaseResult,
// AgentWorktreeInfo) are converted to the orchestrator interface types.
type hostWorktreeAdapter struct {
	mgr *worktreehost.Manager
}

// compile-time assertion.
var _ WorktreeManager = (*hostWorktreeAdapter)(nil)

func (a *hostWorktreeAdapter) BareRepo() string {
	return a.mgr.BareRepo()
}

func (a *hostWorktreeAdapter) DefaultBranchName() string {
	return a.mgr.DefaultBranchName()
}

func (a *hostWorktreeAdapter) FeatureWorktreePath(name string) string {
	return a.mgr.FeatureWorktreePath(name)
}

func (a *hostWorktreeAdapter) MainWorktreePath() (string, error) {
	return a.mgr.MainWorktreePath()
}

func (a *hostWorktreeAdapter) CreateFeature(name string) (*WorktreeInfo, error) {
	info, err := a.mgr.CreateFeature(name)
	if err != nil {
		return nil, err
	}
	return hostWorktreeInfoTo(info), nil
}

func (a *hostWorktreeAdapter) RemoveFeature(name string) error {
	return a.mgr.RemoveFeature(name)
}

func (a *hostWorktreeAdapter) CreateAgentWorktree(featureName string) (*AgentWorktreeInfo, error) {
	info, err := a.mgr.CreateAgentWorktree(featureName)
	if err != nil {
		return nil, err
	}
	return hostAgentWorktreeInfoTo(info), nil
}

func (a *hostWorktreeAdapter) RemoveAgentWorktree(branch string) error {
	return a.mgr.RemoveAgentWorktree(branch)
}

func (a *hostWorktreeAdapter) ListAgentWorktrees(featureName string) ([]AgentWorktreeInfo, error) {
	infos, err := a.mgr.ListAgentWorktrees(featureName)
	if err != nil {
		return nil, err
	}
	out := make([]AgentWorktreeInfo, len(infos))
	for i := range infos {
		out[i] = *hostAgentWorktreeInfoTo(&infos[i])
	}
	return out, nil
}

func (a *hostWorktreeAdapter) GenerateRepoMapForMain() {
	a.mgr.GenerateRepoMapForMain()
}

func (a *hostWorktreeAdapter) GenerateRepoMapAsync(worktreePath string) {
	a.mgr.GenerateRepoMapAsync(worktreePath)
}

func (a *hostWorktreeAdapter) FindWorktreeByBranch(branch string) (string, error) {
	return a.mgr.FindWorktreeByBranch(branch)
}

func (a *hostWorktreeAdapter) MergeBranch(sourceBranch, targetWorktree string) (*WorktreeMergeResult, error) {
	result, err := a.mgr.MergeBranch(sourceBranch, targetWorktree)
	if err != nil {
		return nil, err
	}
	return hostMergeResultTo(result), nil
}

func (a *hostWorktreeAdapter) RebaseBranchOnto(sourceWorktree, targetWorktree string) (*WorktreeRebaseResult, error) {
	result, err := a.mgr.RebaseBranchOnto(sourceWorktree, targetWorktree)
	if err != nil {
		return nil, err
	}
	return hostRebaseResultTo(result), nil
}

// --- conversion helpers ---

func hostWorktreeInfoTo(w *worktreehost.WorktreeInfo) *WorktreeInfo {
	if w == nil {
		return nil
	}
	return &WorktreeInfo{
		Path:   w.Path,
		Branch: w.Branch,
		Head:   w.Head,
		IsBare: w.IsBare,
	}
}

func hostAgentWorktreeInfoTo(w *worktreehost.AgentWorktreeInfo) *AgentWorktreeInfo {
	if w == nil {
		return nil
	}
	return &AgentWorktreeInfo{
		Path:          w.Path,
		Branch:        w.Branch,
		Head:          w.Head,
		ParentFeature: w.ParentFeature,
	}
}

func hostMergeResultTo(r *worktreehost.MergeResult) *WorktreeMergeResult {
	if r == nil {
		return nil
	}
	return &WorktreeMergeResult{
		Success:           r.Success,
		SourceBranch:      r.SourceBranch,
		TargetBranch:      r.TargetBranch,
		MergeCommit:       r.MergeCommit,
		Conflicts:         r.Conflicts,
		GitStderr:         r.GitStderr,
		GitCommand:        r.GitCommand,
		ClassifiedDetails: r.ClassifiedDetails,
		TrivialCount:      r.TrivialCount,
		NonTrivialCount:   r.NonTrivialCount,
	}
}

func hostRebaseResultTo(r *worktreehost.RebaseResult) *WorktreeRebaseResult {
	if r == nil {
		return nil
	}
	return &WorktreeRebaseResult{
		Success:   r.Success,
		Conflicts: r.Conflicts,
		GitStderr: r.GitStderr,
	}
}
