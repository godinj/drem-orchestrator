// Package tmux provides a Go wrapper around the tmux CLI for managing
// sessions and windows used by the orchestrator to host Claude Code agents.
package tmux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ErrDashboardRespawned is returned by EnsureSession when the session already
// existed and the dashboard pane was respawned. Callers should exit instead of
// attaching, since the new binary is already running inside the pane.
var ErrDashboardRespawned = errors.New("dashboard respawned")

// WindowInfo holds metadata about a tmux window.
type WindowInfo struct {
	Index  int
	Name   string
	Active bool
}

// Manager manages a tmux session for the orchestrator.
type Manager struct {
	SessionName string // e.g., "drem-myproject"
}

// NewManager creates a Manager for the given session name. The name is
// sanitised to replace characters that tmux silently mangles (dots, colons)
// with hyphens, preventing has-session / new-session mismatches.
func NewManager(sessionName string) *Manager {
	s := strings.NewReplacer(".", "-", ":", "-").Replace(sessionName)
	return &Manager{SessionName: s}
}

// EnsureSession creates the tmux session if it doesn't exist.
// If the session already exists, this is a no-op.
// The first window is named "dashboard". When dashboardCmd is non-empty, it is
// used as the shell command for the initial window (e.g. the self-respawned
// drem binary that runs the TUI).
func (m *Manager) EnsureSession(dashboardCmd string) error {
	// Check if session already exists.
	_, err := runTmux("has-session", "-t", m.SessionName)
	if err == nil {
		// Session exists. Respawn dashboard if its process has exited
		// (e.g., user quit the TUI and rebuilt the binary).
		if dashboardCmd != "" {
			alive, aliveErr := m.IsWindowAlive("dashboard")
			if aliveErr == nil && !alive {
				_, _ = runTmux("respawn-pane", "-k", "-t", m.SessionName+":dashboard", dashboardCmd)
				return ErrDashboardRespawned
			}
		}
		return nil
	}

	// Session does not exist; create a detached session with a "dashboard" window.
	args := []string{"new-session", "-d", "-s", m.SessionName, "-n", "dashboard"}
	if dashboardCmd != "" {
		args = append(args, dashboardCmd)
	}
	_, err = runTmux(args...)
	if err != nil {
		return fmt.Errorf("ensure session %q: %w", m.SessionName, err)
	}

	// Install a hook that sets remain-on-exit on every new window's pane.
	// This ensures panes persist after commands exit, allowing WaitForExit
	// to read exit codes even for fast-exiting commands. We use set-hook
	// (after-new-window) because session-level remain-on-exit does not
	// reliably propagate to windows created with an explicit command.
	_, err = runTmux("set-hook", "-t", m.SessionName, "after-new-window", "set-option -p remain-on-exit on")
	if err != nil {
		return fmt.Errorf("set after-new-window hook for session %q: %w", m.SessionName, err)
	}

	return nil
}

// CreateWindow creates a new tmux window in the session and runs a command in it.
// The cmd string is passed as-is to tmux (it runs in a shell). The session's
// after-new-window hook (installed by EnsureSession) automatically sets
// remain-on-exit on the pane, so it stays around after the command exits,
// allowing WaitForExit to read the exit code.
func (m *Manager) CreateWindow(name, cmd, cwd string) error {
	_, err := runTmux("new-window", "-t", m.SessionName, "-n", name, "-c", cwd, cmd)
	if err != nil {
		return fmt.Errorf("create window %q: %w", name, err)
	}
	return nil
}

// CloseWindow kills a tmux window by name. If the window does not exist, the
// error is silently ignored (idempotent close).
func (m *Manager) CloseWindow(name string) error {
	target := fmt.Sprintf("%s:%s", m.SessionName, name)
	_, err := runTmux("kill-window", "-t", target)
	if err != nil {
		// Ignore "window not found" errors for idempotent behavior.
		if strings.Contains(err.Error(), "can't find window") ||
			strings.Contains(err.Error(), "window not found") ||
			strings.Contains(err.Error(), "no such window") {
			return nil
		}
		return fmt.Errorf("close window %q: %w", name, err)
	}
	return nil
}

// ListWindows lists all windows in the session. Each entry includes the window
// index, name, and whether it is the currently active window.
func (m *Manager) ListWindows() ([]WindowInfo, error) {
	out, err := runTmux("list-windows", "-t", m.SessionName, "-F", "#{window_index}:#{window_name}:#{window_active}")
	if err != nil {
		return nil, fmt.Errorf("list windows: %w", err)
	}

	if out == "" {
		return nil, nil
	}

	var windows []WindowInfo
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			return nil, fmt.Errorf("unexpected list-windows output line: %q", line)
		}

		idx, err := strconv.Atoi(parts[0])
		if err != nil {
			return nil, fmt.Errorf("parse window index %q: %w", parts[0], err)
		}

		active := parts[2] == "1"

		windows = append(windows, WindowInfo{
			Index:  idx,
			Name:   parts[1],
			Active: active,
		})
	}

	return windows, nil
}

// FocusWindow selects a window by name within the managed session. Because the
// TUI now runs inside the same tmux session as agents, select-window is
// sufficient (no cross-session switch-client needed).
func (m *Manager) FocusWindow(name string) error {
	target := fmt.Sprintf("%s:%s", m.SessionName, name)
	_, err := runTmux("select-window", "-t", target)
	if err != nil {
		return fmt.Errorf("focus window %q: %w", name, err)
	}
	return nil
}

// CapturePane captures the visible content of a window's pane. Used by the TUI
// to preview agent output. The lines parameter controls how many lines of
// scrollback to capture.
func (m *Manager) CapturePane(name string, lines int) (string, error) {
	target := fmt.Sprintf("%s:%s", m.SessionName, name)
	out, err := runTmux("capture-pane", "-t", target, "-p", "-S", fmt.Sprintf("-%d", lines))
	if err != nil {
		return "", fmt.Errorf("capture pane %q: %w", name, err)
	}
	return out, nil
}

// IsWindowAlive checks if the process in a window is still running. Returns
// true if the process is alive (pane_dead is 0), false if it has exited
// (pane_dead is 1). Returns false, nil if the window doesn't exist.
func (m *Manager) IsWindowAlive(name string) (bool, error) {
	target := fmt.Sprintf("%s:%s", m.SessionName, name)
	out, err := runTmux("list-panes", "-t", target, "-F", "#{pane_dead}")
	if err != nil {
		// Window does not exist — not alive.
		if strings.Contains(err.Error(), "can't find window") ||
			strings.Contains(err.Error(), "window not found") ||
			strings.Contains(err.Error(), "no such window") {
			return false, nil
		}
		return false, fmt.Errorf("check window alive %q: %w", name, err)
	}

	return strings.TrimSpace(out) == "0", nil
}

// WaitForExit blocks until the command in a window's pane exits and returns its
// exit code. It polls pane_dead and pane_dead_status every 500ms. The caller is
// responsible for calling CloseWindow to clean up after this returns.
func (m *Manager) WaitForExit(name string) (int, error) {
	target := fmt.Sprintf("%s:%s", m.SessionName, name)

	for {
		out, err := runTmux("list-panes", "-t", target, "-F", "#{pane_dead}:#{pane_dead_status}")
		if err != nil {
			// If the window disappeared entirely, treat as an error.
			return -1, fmt.Errorf("wait for exit %q: %w", name, err)
		}

		line := strings.TrimSpace(out)
		parts := strings.SplitN(line, ":", 2)
		if len(parts) == 2 && parts[0] == "1" {
			exitCode, parseErr := strconv.Atoi(parts[1])
			if parseErr != nil {
				return -1, fmt.Errorf("parse exit code %q for window %q: %w", parts[1], name, parseErr)
			}
			return exitCode, nil
		}

		time.Sleep(500 * time.Millisecond)
	}
}

// KillSession destroys the entire tmux session. Used on shutdown.
func (m *Manager) KillSession() error {
	_, err := runTmux("kill-session", "-t", m.SessionName)
	if err != nil {
		return fmt.Errorf("kill session %q: %w", m.SessionName, err)
	}
	return nil
}

// Attach replaces the current process with `tmux attach-session -t <session>`.
// When already inside a tmux session ($TMUX is set), it uses switch-client
// instead to avoid the "sessions should be nested with care" error.
// It uses syscall.Exec so the calling process is fully replaced; this function
// only returns on error.
func (m *Manager) Attach() error {
	tmuxBin, err := exec.LookPath("tmux")
	if err != nil {
		return fmt.Errorf("find tmux binary: %w", err)
	}
	argv := []string{"tmux", "attach-session", "-t", m.SessionName}
	if os.Getenv("TMUX") != "" {
		argv = []string{"tmux", "switch-client", "-t", m.SessionName}
	}
	return syscall.Exec(tmuxBin, argv, syscall.Environ())
}

// CreateAgentSession creates a new tmux session for an agent and runs a command
// in it. The session is independent of the dashboard session, so it persists
// across dashboard restarts. remain-on-exit is set so the pane stays around
// after the command exits, allowing WaitForAgentExit to read the exit code.
//
// The command is wrapped so that remain-on-exit is set from within the pane
// before the real command runs. This avoids race conditions with fast-exiting
// commands and the reliability issues of respawn-pane.
func (m *Manager) CreateAgentSession(sessionName, cmd, cwd string) error {
	// Wrap: set remain-on-exit on this pane first, then run the real command.
	// Both execute in the same shell, so there is no race — even commands
	// that exit instantly (like "true") will have remain-on-exit set before
	// the pane dies.
	wrapped := fmt.Sprintf("tmux set-option -p remain-on-exit on; %s", cmd)
	_, err := runTmux("new-session", "-d", "-s", sessionName, "-c", cwd, wrapped)
	if err != nil {
		return fmt.Errorf("create agent session %q: %w", sessionName, err)
	}

	// Set large scrollback so CaptureAgentPane can retrieve sufficient
	// context for memory extraction after long-running agents.
	_, err = runTmux("set-option", "-t", sessionName, "history-limit", "50000")
	if err != nil {
		return fmt.Errorf("set history-limit for agent session %q: %w", sessionName, err)
	}

	return nil
}

// KillAgentSession destroys an agent's tmux session. If the session does not
// exist, the error is silently ignored (idempotent).
func (m *Manager) KillAgentSession(sessionName string) error {
	_, err := runTmux("kill-session", "-t", sessionName)
	if err != nil {
		if strings.Contains(err.Error(), "can't find session") ||
			strings.Contains(err.Error(), "session not found") ||
			strings.Contains(err.Error(), "no such session") ||
			strings.Contains(err.Error(), "no server running") {
			return nil
		}
		return fmt.Errorf("kill agent session %q: %w", sessionName, err)
	}
	return nil
}

// IsAgentSessionAlive checks if the process in an agent session is still
// running. Returns true if the process is alive (pane_dead is 0), false if it
// has exited. Returns false, nil if the session doesn't exist.
func (m *Manager) IsAgentSessionAlive(sessionName string) (bool, error) {
	out, err := runTmux("list-panes", "-t", sessionName, "-F", "#{pane_dead}")
	if err != nil {
		if strings.Contains(err.Error(), "can't find session") ||
			strings.Contains(err.Error(), "session not found") ||
			strings.Contains(err.Error(), "no such session") ||
			strings.Contains(err.Error(), "can't find window") ||
			strings.Contains(err.Error(), "no server running") {
			return false, nil
		}
		return false, fmt.Errorf("check agent session alive %q: %w", sessionName, err)
	}
	return strings.TrimSpace(out) == "0", nil
}

// WaitForAgentExit blocks until the command in an agent session's pane exits
// and returns its exit code. It polls pane_dead and pane_dead_status every
// 500ms. The caller is responsible for calling KillAgentSession to clean up.
func (m *Manager) WaitForAgentExit(sessionName string) (int, error) {
	for {
		exitCode, dead, err := m.checkPaneDead(sessionName)
		if err != nil {
			return -1, fmt.Errorf("wait for agent exit %q: %w", sessionName, err)
		}
		if dead {
			return exitCode, nil
		}

		time.Sleep(500 * time.Millisecond)
	}
}

// checkPaneDead checks whether a tmux pane's process has exited. Returns the
// exit code, whether the pane is dead, and any error from the tmux query.
func (m *Manager) checkPaneDead(sessionName string) (exitCode int, dead bool, err error) {
	out, err := runTmux("list-panes", "-t", sessionName, "-F", "#{pane_dead}:#{pane_dead_status}")
	if err != nil {
		return -1, false, err
	}

	line := strings.TrimSpace(out)
	parts := strings.SplitN(line, ":", 2)
	if len(parts) == 2 && parts[0] == "1" {
		code, parseErr := strconv.Atoi(parts[1])
		if parseErr != nil {
			return -1, false, fmt.Errorf("parse exit code %q: %w", parts[1], parseErr)
		}
		return code, true, nil
	}

	return 0, false, nil
}

// CaptureAgentPane captures the visible content of an agent session's pane.
// The lines parameter controls how many lines of scrollback to capture.
func (m *Manager) CaptureAgentPane(sessionName string, lines int) (string, error) {
	out, err := runTmux("capture-pane", "-t", sessionName, "-p", "-S", fmt.Sprintf("-%d", lines))
	if err != nil {
		return "", fmt.Errorf("capture agent pane %q: %w", sessionName, err)
	}
	return out, nil
}

// CaptureAgentPaneFull captures the entire scrollback of an agent session's
// pane. Used by the orchestrator for memory extraction on completion.
func (m *Manager) CaptureAgentPaneFull(sessionName string) (string, error) {
	out, err := runTmux("capture-pane", "-t", sessionName, "-p", "-S", "-")
	if err != nil {
		return "", fmt.Errorf("capture agent pane full %q: %w", sessionName, err)
	}
	return out, nil
}

// SendKeys sends a sequence of keystrokes to an agent session's pane.
// Used to gracefully exit the Claude TUI after detecting completion.
func (m *Manager) SendKeys(sessionName string, keys ...string) error {
	args := append([]string{"send-keys", "-t", sessionName}, keys...)
	_, err := runTmux(args...)
	if err != nil {
		return fmt.Errorf("send keys to %q: %w", sessionName, err)
	}
	return nil
}

// WaitForAgentIdle polls for an idle signal file and then gracefully exits
// the Claude TUI. It checks for the signal file every 500ms. Once detected,
// it sends "/exit" + Enter to the tmux pane, then falls through to
// WaitForAgentExit to collect the exit code from pane_dead. The ctx parameter
// allows the caller to cancel the wait (e.g. on StopAgent).
func (m *Manager) WaitForAgentIdle(ctx context.Context, sessionName, idleSignalPath string) (int, error) {
	// Phase 1: poll for the idle signal file OR early process exit.
	// If the user (or something else) already exited the agent, the pane
	// will be dead and we should skip straight to collecting the exit code
	// instead of polling forever for a signal file that will never appear.
	alreadyDead := false
	for {
		select {
		case <-ctx.Done():
			return -1, ctx.Err()
		default:
		}

		if _, err := os.Stat(idleSignalPath); err == nil {
			break // signal file exists — agent is idle
		}

		// Check if the pane process already exited.
		exitCode, dead, err := m.checkPaneDead(sessionName)
		if err == nil && dead {
			alreadyDead = true
			_ = exitCode // will be re-read below for consistency
			break
		}

		time.Sleep(500 * time.Millisecond)
	}

	if alreadyDead {
		// Process already exited — just collect the exit code.
		return m.WaitForAgentExit(sessionName)
	}

	// Phase 2: send /exit to gracefully close the Claude TUI.
	// The /exit command opens a confirmation prompt, so we send Enter
	// again after a short delay to confirm it.
	if err := m.SendKeys(sessionName, "/exit", "Enter"); err != nil {
		// If send-keys fails (session already gone), fall through to
		// WaitForAgentExit which will handle the error.
		_ = err
	}
	time.Sleep(500 * time.Millisecond)
	if err := m.SendKeys(sessionName, "Enter"); err != nil {
		_ = err
	}

	// Phase 3: wait for the process to actually exit and get the exit code.
	return m.WaitForAgentExit(sessionName)
}

// FocusAgentSession switches the tmux client to an agent's session. This
// performs a cross-session switch-client, allowing the user to jump from the
// dashboard session to any agent session.
func (m *Manager) FocusAgentSession(sessionName string) error {
	_, err := runTmux("switch-client", "-t", sessionName)
	if err != nil {
		return fmt.Errorf("focus agent session %q: %w", sessionName, err)
	}
	return nil
}

// ListAgentSessions returns the names of all tmux sessions that belong to
// agents of this manager's project. Agent sessions are identified by having
// the manager's session name as a prefix (e.g. "drem-myproject-coder-a1b2c3d4").
func (m *Manager) ListAgentSessions() ([]string, error) {
	out, err := runTmux("list-sessions", "-F", "#{session_name}")
	if err != nil {
		if strings.Contains(err.Error(), "no server running") ||
			strings.Contains(err.Error(), "no sessions") {
			return nil, nil
		}
		return nil, fmt.Errorf("list agent sessions: %w", err)
	}

	prefix := m.SessionName + "-"
	var sessions []string
	for _, line := range strings.Split(out, "\n") {
		name := strings.TrimSpace(line)
		if name != "" && strings.HasPrefix(name, prefix) {
			sessions = append(sessions, name)
		}
	}
	return sessions, nil
}

// runTmux executes a tmux command and returns stdout. On failure, the error
// message includes the tmux arguments and combined output for debugging.
func runTmux(args ...string) (string, error) {
	cmd := exec.Command("tmux", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("tmux %s: %w\n%s", strings.Join(args, " "), err, string(out))
	}
	return strings.TrimSpace(string(out)), nil
}
