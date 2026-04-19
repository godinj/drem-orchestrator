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
	OnRemoveAgentWorktree func(branch string) error
	OnListAgentWorktrees  func(featureName string) ([]AgentWorktreeInfo, error)
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
