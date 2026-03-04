// Package tmux provides a Go wrapper around the tmux CLI for managing
// sessions and windows used by the orchestrator to host Claude Code agents.
package tmux

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

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

// NewManager creates a Manager for the given session name.
func NewManager(sessionName string) *Manager {
	return &Manager{SessionName: sessionName}
}

// EnsureSession creates the tmux session if it doesn't exist.
// If the session already exists, this is a no-op.
// The first window is named "dashboard" — this is where the TUI will run.
func (m *Manager) EnsureSession() error {
	// Check if session already exists.
	_, err := runTmux("has-session", "-t", m.SessionName)
	if err == nil {
		return nil
	}

	// Session does not exist; create a detached session with a "dashboard" window.
	_, err = runTmux("new-session", "-d", "-s", m.SessionName, "-n", "dashboard")
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

// FocusWindow switches the tmux client to a specific window.
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
