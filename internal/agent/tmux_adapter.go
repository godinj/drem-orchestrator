// tmux_adapter.go defines TmuxSessionManager, the narrow surface the Runner
// uses to manage long-lived supervisor/shell tmux sessions. Keeping the
// interface inside this package lets the runner compile without a direct
// dependency on the host-mode tmux wrapper — a required invariant on the
// path to prompt 21's tmux package deletion.
//
// *tmux.Manager satisfies this interface verbatim; orchestrator tests can
// substitute a fake.
package agent

// TmuxSessionManager is the subset of *tmux.Manager methods the agent Runner
// and the orchestrator's supervisor-session code exercise. Method shapes
// mirror tmux.Manager exactly so callers passing a *tmux.Manager satisfy
// this interface without a wrapper. The dashboard-session-name lookup is
// exposed via the separate dashboardSessionNamer / sessionFieldGetter
// extensions below so this core interface stays small.
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

// dashboardSessionNamer is an optional extension implemented by tmux session
// managers that expose a dashboard-session-name prefix via a plain
// SessionName() method. Custom managers (fakes in tests) can opt in without
// disturbing the core TmuxSessionManager surface.
type dashboardSessionNamer interface {
	SessionName() string
}

// sessionFieldGetter is a second extension point for tmux managers that
// expose the dashboard session name through a differently-named method.
// *tmux.Manager satisfies this via GetSessionName — Go forbids a method
// and field sharing a name, and the concrete type already has a public
// SessionName field, so a Get-prefix shim is the tidiest way across.
type sessionFieldGetter interface {
	GetSessionName() string
}

// dashboardSessionName returns the dashboard prefix encoded in tm. Returns
// an empty string when tm is nil or does not implement either extension
// interface; callers that depend on a non-empty prefix (supervisor-session
// spawning) should guard against the empty case.
func dashboardSessionName(tm TmuxSessionManager) string {
	if tm == nil {
		return ""
	}
	if namer, ok := tm.(dashboardSessionNamer); ok {
		return namer.SessionName()
	}
	if getter, ok := tm.(sessionFieldGetter); ok {
		return getter.GetSessionName()
	}
	return ""
}
