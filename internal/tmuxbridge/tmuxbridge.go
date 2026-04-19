// Package tmuxbridge constructs a concrete tmux session Manager for the
// CLI and the TUI.
//
// It exists only to localize the last remaining import of internal/tmux/
// so the rest of the repo does not need to know about the package's
// existence during the containerization transition. The bridge re-exports
// just enough of the tmux package surface (Manager type, option
// constructors, and the sentinel error) so callers stay off internal/tmux
// entirely.
//
// Remove this package in prompt 21 alongside internal/tmux/.
package tmuxbridge

import (
	"github.com/godinj/drem-orchestrator/internal/tmux"
)

// Manager is a local alias for *tmux.Manager so callers of this bridge
// do not need to import internal/tmux to declare a variable of the
// concrete type. The alias disappears when internal/tmux is removed in
// prompt 21.
type Manager = tmux.Manager

// ErrDashboardRespawned is re-exported from internal/tmux so callers can
// test the sentinel returned by EnsureSession without importing the
// soon-to-be-deleted package.
var ErrDashboardRespawned = tmux.ErrDashboardRespawned

// Options bundles the configuration the CLI passes to NewManager. The
// field set is intentionally narrow — prompt 21 removes the bridge and
// the handful of callers currently wiring these values go away with it.
type Options struct {
	// SessionName is the tmux session that hosts the dashboard TUI.
	SessionName string
	// Socket is the tmux server socket name (-L flag). Empty uses the
	// default tmux server.
	Socket string
	// ConfigFile is the tmux config file (-f flag). Empty uses tmux's
	// default config lookup.
	ConfigFile string
}

// NewManager builds a concrete tmux session manager from opts. The
// returned value is the concrete *tmux.Manager (aliased as *Manager)
// because the agent package's TmuxSessionManager interface, the TUI's
// session-manipulation call sites, and the dashboard self-respawn dance
// all share the same underlying implementation.
func NewManager(opts Options) *Manager {
	tmuxOpts := []tmux.Option{}
	if opts.Socket != "" {
		tmuxOpts = append(tmuxOpts, tmux.WithSocket(opts.Socket))
	}
	if opts.ConfigFile != "" {
		tmuxOpts = append(tmuxOpts, tmux.WithConfigFile(opts.ConfigFile))
	}
	return tmux.NewManager(opts.SessionName, tmuxOpts...)
}
