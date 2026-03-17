package agent

import "context"

// SessionManager abstracts tmux session operations for testing.
// It is satisfied by *tmux.Manager without any modifications to the tmux package.
type SessionManager interface {
	// CreateAgentSession creates a new tmux session for an agent.
	CreateAgentSession(sessionName, cmd, cwd string) error

	// IsAgentSessionAlive checks if the process in an agent session is alive.
	IsAgentSessionAlive(sessionName string) (bool, error)

	// KillAgentSession destroys an agent's tmux session (idempotent).
	KillAgentSession(sessionName string) error

	// CaptureAgentPane captures the content of an agent session's pane.
	CaptureAgentPane(sessionName string, lines int) (string, error)

	// ListAgentSessions returns the names of all agent tmux sessions.
	ListAgentSessions() ([]string, error)

	// WaitForAgentIdle polls for an idle signal and gracefully exits the agent.
	WaitForAgentIdle(ctx context.Context, sessionName, idleSignalPath string) (int, error)
}
