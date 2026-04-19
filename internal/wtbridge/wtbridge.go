// Package wtbridge constructs a concrete WorktreeManager for the CLI.
//
// It exists only to localize the last remaining import of
// internal/worktree/ so the rest of the repo does not need to know about
// the package's existence. The bridge returns the
// internal/orchestrator.WorktreeManager interface rather than the concrete
// *worktree.Manager so callers stay off of internal/worktree entirely.
//
// Remove this package in prompt 21 alongside internal/worktree/.
package wtbridge

import (
	"github.com/godinj/drem-orchestrator/internal/orchestrator"
	"github.com/godinj/drem-orchestrator/internal/worktree"
)

// Options configures the worktree manager constructed by NewManager.
//
// The field set intentionally mirrors worktree.NewManager's current
// positional parameters so the bridge can be removed in prompt 21 without
// renaming fields at call sites.
type Options struct {
	// BareRepoPath is the path to the bare git repo that hosts every
	// feature and agent worktree for this project.
	BareRepoPath string
	// DefaultBranch is the branch name used as the base for all feature
	// branches (typically "main").
	DefaultBranch string
}

// NewManager builds a concrete worktree manager and returns it as the
// orchestrator.WorktreeManager interface. Returning the interface rather
// than the concrete *worktree.Manager keeps the worktree import contained
// inside this package.
func NewManager(opts Options) orchestrator.WorktreeManager {
	return worktree.NewManager(opts.BareRepoPath, opts.DefaultBranch)
}

// NewConcreteManager constructs a concrete *worktree.Manager for callers
// that need to access methods beyond the WorktreeManager interface
// (for example the one-off MigrateToGroupedLayout / MigrateAgentPaths
// helpers invoked once at boot). The return type is kept as a local type
// alias so callers do not import internal/worktree directly; see the
// Manager type alias below.
//
// Added solely to unblock the remaining cmd/drem callers that still need
// migration helpers during the containerization transition — prompt 21
// deletes this together with the rest of the package.
func NewConcreteManager(opts Options) *Manager {
	return worktree.NewManager(opts.BareRepoPath, opts.DefaultBranch)
}

// Manager is a local alias for *worktree.Manager so callers of this
// bridge do not need to import internal/worktree to declare a variable
// of the concrete type. The alias disappears when internal/worktree is
// removed in prompt 21.
type Manager = worktree.Manager
