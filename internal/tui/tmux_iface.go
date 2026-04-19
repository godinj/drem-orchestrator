// tmux_iface.go defines TmuxManager, the narrow session-management surface
// the TUI uses for supervisor/shell jumps. Keeping the interface inside the
// TUI package leaves the door open for a future in-container tmux session
// manager without the TUI caring which implementation it got. In the
// current containerized wiring cmd/drem/main.go passes nil — the TUI
// degrades gracefully when no tmux manager is configured.
package tui

// TmuxManager is the subset of tmux.Manager methods the TUI invokes for
// agent-session navigation and shell creation. Method shapes mirror the
// concrete type exactly so call sites continue to compile without a
// wrapper.
//
// GetSessionName exposes the dashboard session-name prefix used to derive
// per-task shell-session names. The concrete type carries the prefix on a
// public SessionName field; it also exposes GetSessionName() via a getter
// shim for interface satisfaction (Go forbids a method and field to share
// a name).
type TmuxManager interface {
	// IsAgentSessionAlive returns true when the named session exists and
	// its pane process is still running. (false, nil) is the contract for
	// a missing session.
	IsAgentSessionAlive(sessionName string) (bool, error)

	// FocusAgentSession switches the attached tmux client to sessionName.
	// The TUI uses it when the operator drills into a supervisor or shell
	// session from the agents panel.
	FocusAgentSession(sessionName string) error

	// CreateShellSession creates a plain interactive shell session named
	// sessionName rooted at cwd. The TUI uses it for the "open shell in
	// integration worktree" action.
	CreateShellSession(sessionName, cwd string) error

	// GetSessionName returns the dashboard session-name prefix. Used by
	// the TUI to namespace shell sessions under the dashboard session so
	// they show up together in tmux.
	GetSessionName() string
}
