// tmux_adapter.go defines TmuxSessionManager, the narrow surface the Runner
// uses to manage long-lived supervisor/shell tmux sessions. Keeping this
// interface inside internal/agent/ (rather than importing internal/tmux)
// lets the runner compile without a direct internal/tmux dependency — a
// required invariant on the path to prompt 21's tmux package deletion.
//
// *tmux.Manager satisfies this interface verbatim; orchestrator tests can
// substitute a fake without importing internal/tmux.
package agent

// TmuxSessionManager is the subset of *tmux.Manager methods the agent Runner
// and the orchestrator's supervisor-session code exercise. Method shapes
// mirror tmux.Manager exactly so callers passing a *tmux.Manager satisfy
// this interface without a wrapper. The dashboard-session-name lookup is
// exposed via the separate dashboardSessionNamer / sessionFieldGetter
// extensions in runner.go so this core interface stays small.
type TmuxSessionManager interface {
	// ListAgentSessions returns the names of tmux sessions that belong to
	// agents of this manager's project. Used by ReapOrphanedSessions to find
	// sessions whose process has exited.
	ListAgentSessions() ([]string, error)

	// IsAgentSessionAlive checks whether the process in the named session is
	// still running. Returns (false, nil) when the session does not exist.
	IsAgentSessionAlive(sessionName string) (bool, error)

	// KillAgentSession destroys the named tmux session. Idempotent: a missing
	// session is not an error.
	KillAgentSession(sessionName string) error

	// CreateAgentSession creates a detached tmux session named sessionName
	// running cmd in cwd. Used by orchestrator-level supervisor spawning.
	CreateAgentSession(sessionName, cmd, cwd string) error
}
