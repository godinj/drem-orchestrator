// worktree_iface.go defines the narrow WorktreeManager interface the Runner
// uses for agent-worktree creation. Keeping the interface in a sibling file
// (rather than inlined in runner.go) prevents the growing runner from
// drifting further past the per-file line ceiling.
//
// The runner held a direct dependency on *worktree.Manager until prompt 21
// of the containerization migration deleted the host-mode worktree package
// surface from this file's reach. The interface is satisfied by the host-mode
// manager in internal/worktreehost via orchestrator.HostWorktreeManager's
// AsAgentWorktreeManager adapter.
package agent

// WorktreeManager is the narrow subset of worktree operations the Runner
// needs. It lets the runner accept either a host-mode concrete manager or a
// test fake without importing any particular worktree implementation.
type WorktreeManager interface {
	// CreateAgentWorktree creates a new agent worktree under the feature
	// group directory and returns its metadata.
	CreateAgentWorktree(featureName string) (*AgentWorktreeInfo, error)
	// GenerateRepoMapAsync kicks off repo-map generation in the given
	// worktree path; the implementation logs failures and never blocks.
	GenerateRepoMapAsync(worktreePath string)
}

// AgentWorktreeInfo describes an agent's nested worktree. Mirrors the shape
// returned by the host-mode worktree manager so Runner implementations can
// hand the struct back to the orchestrator without further conversion.
type AgentWorktreeInfo struct {
	Path          string
	Branch        string
	Head          string
	ParentFeature string
}
