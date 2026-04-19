package orchestrator

import (
	"fmt"
	"path/filepath"
)

// FakeWorktreeManager is an in-memory implementation of WorktreeManager for
// orchestrator unit tests. It lives in production source (rather than
// _test.go) so tests in sibling packages can reuse it without duplicating
// the surface.
//
// Callers parameterise behaviour through the exported fields and function
// hooks. Nil hooks fall back to sensible defaults driven by the in-memory
// maps; test-specific error injection goes through the OnXxx hooks.
//
// Zero value is usable (returns empty paths, empty lists, no errors) but
// most tests will set BarePath + Default at minimum.
type FakeWorktreeManager struct {
	// BarePath is returned by BareRepo(). Tests that compose feature dirs
	// (bare + feature name) typically set this to a t.TempDir().
	BarePath string

	// Default is returned by DefaultBranchName(). Defaults to "main" if empty.
	Default string

	// MainPath is returned by MainWorktreePath() when non-empty. Otherwise a
	// synthetic path (BarePath/Default) is returned unless MainErr is set.
	MainPath string

	// MainErr is returned by MainWorktreePath when non-nil. Overrides MainPath.
	MainErr error

	// Features maps feature name → integration worktree path. Populated by
	// CreateFeature and consulted by FeatureWorktreePath so tests that want
	// to simulate "feature already exists" can pre-populate the map.
	Features map[string]string

	// AgentWorktrees maps feature name → list of agent worktree info. Tests
	// that exercise ListAgentWorktrees should populate this.
	AgentWorktrees map[string][]AgentWorktreeInfo

	// RemovedFeatures and RemovedAgents record cleanup calls so tests can
	// assert on them without needing OnXxx hooks.
	RemovedFeatures []string
	RemovedAgents   []string

	// RepoMapsGenerated tracks paths passed to GenerateRepoMapAsync /
	// GenerateRepoMapForMain (the latter uses MainPath).
	RepoMapsGenerated []string

	// Hooks for finer-grained test control. When non-nil, the hook runs
	// instead of the default implementation; tests wire these in to
	// simulate errors, capture call arguments, or override return values.
	OnFeatureWorktreePath func(name string) string
	OnCreateFeature       func(name string) (*WorktreeInfo, error)
	OnRemoveFeature       func(name string) error
	OnCreateAgentWorktree func(featureName string) (*AgentWorktreeInfo, error)
	OnRemoveAgentWorktree func(branch string) error
	OnListAgentWorktrees  func(featureName string) ([]AgentWorktreeInfo, error)

	// Agent-into-feature merge hooks. The default implementations return
	// a zero-value successful merge / rebase / "not found" for the branch
	// lookup, matching the shape tests expect when the fake is only used
	// for structural assertions (paths, counts). Tests exercising merge
	// decision-making wire these explicitly.
	OnFindWorktreeByBranch func(branch string) (string, error)
	OnMergeBranch          func(sourceBranch, targetWorktree string) (*WorktreeMergeResult, error)
	OnRebaseBranchOnto     func(sourceWorktree, targetWorktree string) (*WorktreeRebaseResult, error)
}

// compile-time assertion.
var _ WorktreeManager = (*FakeWorktreeManager)(nil)

// BareRepo returns the bare repo path configured via BarePath.
func (f *FakeWorktreeManager) BareRepo() string {
	return f.BarePath
}

// DefaultBranchName returns Default, falling back to "main" if unset so
// tests that don't care about branch-name edge cases still behave sensibly.
func (f *FakeWorktreeManager) DefaultBranchName() string {
	if f.Default == "" {
		return "main"
	}
	return f.Default
}

// FeatureWorktreePath returns the integration worktree path for the given
// feature. Uses OnFeatureWorktreePath if set, then Features map lookup, then
// falls back to <BarePath>/feature/<name>/integration (matching the real
// Manager's layout convention) so tests that only care about path shape can
// stay implicit.
func (f *FakeWorktreeManager) FeatureWorktreePath(name string) string {
	if f.OnFeatureWorktreePath != nil {
		return f.OnFeatureWorktreePath(name)
	}
	if f.Features != nil {
		if p, ok := f.Features[name]; ok {
			return p
		}
	}
	if f.BarePath == "" {
		return ""
	}
	return filepath.Join(f.BarePath, "feature", name, "integration")
}

// MainWorktreePath returns MainPath if set, otherwise <BarePath>/<Default>.
// Returns MainErr if set regardless of the other fields.
func (f *FakeWorktreeManager) MainWorktreePath() (string, error) {
	if f.MainErr != nil {
		return "", f.MainErr
	}
	if f.MainPath != "" {
		return f.MainPath, nil
	}
	if f.BarePath == "" {
		return "", fmt.Errorf("fake worktree manager: no bare path configured")
	}
	return filepath.Join(f.BarePath, f.DefaultBranchName()), nil
}

// CreateFeature records the feature in the Features map and returns a
// WorktreeInfo pointing at FeatureWorktreePath(name). Tests that want to
// inject errors should wire OnCreateFeature.
func (f *FakeWorktreeManager) CreateFeature(name string) (*WorktreeInfo, error) {
	if f.OnCreateFeature != nil {
		return f.OnCreateFeature(name)
	}
	path := f.FeatureWorktreePath(name)
	if f.Features == nil {
		f.Features = make(map[string]string)
	}
	f.Features[name] = path
	return &WorktreeInfo{
		Path:   path,
		Branch: "feature/" + name,
		Head:   "fake-head",
	}, nil
}

// RemoveFeature records the removal in RemovedFeatures and deletes the
// entry from the Features map. Uses OnRemoveFeature if set.
func (f *FakeWorktreeManager) RemoveFeature(name string) error {
	if f.OnRemoveFeature != nil {
		return f.OnRemoveFeature(name)
	}
	f.RemovedFeatures = append(f.RemovedFeatures, name)
	delete(f.Features, name)
	return nil
}

// CreateAgentWorktree appends a synthetic AgentWorktreeInfo into the map
// for the feature and returns it. Uses OnCreateAgentWorktree if set.
func (f *FakeWorktreeManager) CreateAgentWorktree(featureName string) (*AgentWorktreeInfo, error) {
	if f.OnCreateAgentWorktree != nil {
		return f.OnCreateAgentWorktree(featureName)
	}
	if f.AgentWorktrees == nil {
		f.AgentWorktrees = make(map[string][]AgentWorktreeInfo)
	}
	idx := len(f.AgentWorktrees[featureName])
	branch := fmt.Sprintf("worktree-agent-fake-%d", idx)
	info := AgentWorktreeInfo{
		Path:          filepath.Join(f.BarePath, "feature", featureName, fmt.Sprintf("agent-fake-%d", idx)),
		Branch:        branch,
		Head:          "fake-head",
		ParentFeature: "feature/" + featureName,
	}
	f.AgentWorktrees[featureName] = append(f.AgentWorktrees[featureName], info)
	return &info, nil
}

// RemoveAgentWorktree records the branch in RemovedAgents. Uses
// OnRemoveAgentWorktree if set.
func (f *FakeWorktreeManager) RemoveAgentWorktree(branch string) error {
	if f.OnRemoveAgentWorktree != nil {
		return f.OnRemoveAgentWorktree(branch)
	}
	f.RemovedAgents = append(f.RemovedAgents, branch)
	return nil
}

// ListAgentWorktrees returns the stored list for the feature. Uses
// OnListAgentWorktrees if set, otherwise AgentWorktrees map lookup.
func (f *FakeWorktreeManager) ListAgentWorktrees(featureName string) ([]AgentWorktreeInfo, error) {
	if f.OnListAgentWorktrees != nil {
		return f.OnListAgentWorktrees(featureName)
	}
	if f.AgentWorktrees == nil {
		return nil, nil
	}
	return f.AgentWorktrees[featureName], nil
}

// GenerateRepoMapForMain records a call with MainPath (or the synthetic
// bare/default path) so tests can assert that repo map generation was
// triggered without actually running any scripts.
func (f *FakeWorktreeManager) GenerateRepoMapForMain() {
	path, err := f.MainWorktreePath()
	if err != nil {
		return
	}
	f.RepoMapsGenerated = append(f.RepoMapsGenerated, path)
}

// GenerateRepoMapAsync records the path passed in. Synchronous in the fake
// — there is no goroutine — which matches how tests want to observe the
// call.
func (f *FakeWorktreeManager) GenerateRepoMapAsync(worktreePath string) {
	f.RepoMapsGenerated = append(f.RepoMapsGenerated, worktreePath)
}

// FindWorktreeByBranch returns a synthetic agent worktree path by default
// so tests that don't care about rebase behaviour still get through the
// rebase step. Tests asserting "worktree missing" should wire
// OnFindWorktreeByBranch to return an error.
func (f *FakeWorktreeManager) FindWorktreeByBranch(branch string) (string, error) {
	if f.OnFindWorktreeByBranch != nil {
		return f.OnFindWorktreeByBranch(branch)
	}
	if f.BarePath == "" {
		return "", fmt.Errorf("fake worktree manager: no bare path configured")
	}
	return filepath.Join(f.BarePath, "worktree-by-branch", branch), nil
}

// MergeBranch returns a successful WorktreeMergeResult by default. Tests
// that want to exercise conflict handling should wire OnMergeBranch to
// return a result with Success=false and a non-empty Conflicts slice.
func (f *FakeWorktreeManager) MergeBranch(sourceBranch, targetWorktree string) (*WorktreeMergeResult, error) {
	if f.OnMergeBranch != nil {
		return f.OnMergeBranch(sourceBranch, targetWorktree)
	}
	return &WorktreeMergeResult{
		Success:      true,
		SourceBranch: sourceBranch,
		MergeCommit:  "fake-merge-commit",
	}, nil
}

// RebaseBranchOnto returns a successful WorktreeRebaseResult by default.
func (f *FakeWorktreeManager) RebaseBranchOnto(sourceWorktree, targetWorktree string) (*WorktreeRebaseResult, error) {
	if f.OnRebaseBranchOnto != nil {
		return f.OnRebaseBranchOnto(sourceWorktree, targetWorktree)
	}
	return &WorktreeRebaseResult{Success: true}, nil
}
