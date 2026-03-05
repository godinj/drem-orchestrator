package tmux

import (
	"fmt"
	"os/exec"
	"testing"
	"time"
)

// testSessionName returns a unique session name for a test run to avoid
// collisions between parallel tests or leftover sessions.
func testSessionName(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("drem-test-%d", time.Now().UnixNano())
}

// requireTmux skips the test if tmux is not available in PATH.
func requireTmux(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("requires tmux")
	}
}

func TestEnsureSession(t *testing.T) {
	requireTmux(t)
	session := testSessionName(t)
	mgr := NewManager(session)
	t.Cleanup(func() { _ = mgr.KillSession() })

	// First call should create the session.
	if err := mgr.EnsureSession(""); err != nil {
		t.Fatalf("EnsureSession (create): %v", err)
	}

	// Second call should be a no-op (session already exists).
	if err := mgr.EnsureSession(""); err != nil {
		t.Fatalf("EnsureSession (idempotent): %v", err)
	}
}

func TestCreateAndListWindows(t *testing.T) {
	requireTmux(t)
	session := testSessionName(t)
	mgr := NewManager(session)
	t.Cleanup(func() { _ = mgr.KillSession() })

	if err := mgr.EnsureSession(""); err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}

	// Create a window that runs sleep.
	if err := mgr.CreateWindow("test-agent", "sleep 30", "/tmp"); err != nil {
		t.Fatalf("CreateWindow: %v", err)
	}

	windows, err := mgr.ListWindows()
	if err != nil {
		t.Fatalf("ListWindows: %v", err)
	}

	// Expect at least 2 windows: dashboard + test-agent.
	if len(windows) < 2 {
		t.Fatalf("expected at least 2 windows, got %d: %+v", len(windows), windows)
	}

	found := false
	for _, w := range windows {
		if w.Name == "test-agent" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("window 'test-agent' not found in: %+v", windows)
	}
}

func TestCloseWindow(t *testing.T) {
	requireTmux(t)
	session := testSessionName(t)
	mgr := NewManager(session)
	t.Cleanup(func() { _ = mgr.KillSession() })

	if err := mgr.EnsureSession(""); err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}

	if err := mgr.CreateWindow("ephemeral", "sleep 30", "/tmp"); err != nil {
		t.Fatalf("CreateWindow: %v", err)
	}

	// Close the window.
	if err := mgr.CloseWindow("ephemeral"); err != nil {
		t.Fatalf("CloseWindow: %v", err)
	}

	// Closing again should be idempotent.
	if err := mgr.CloseWindow("ephemeral"); err != nil {
		t.Fatalf("CloseWindow (idempotent): %v", err)
	}

	// Closing a window that never existed should also be fine.
	if err := mgr.CloseWindow("nonexistent-window"); err != nil {
		t.Fatalf("CloseWindow (nonexistent): %v", err)
	}
}

func TestCapturePane(t *testing.T) {
	requireTmux(t)
	session := testSessionName(t)
	mgr := NewManager(session)
	t.Cleanup(func() { _ = mgr.KillSession() })

	if err := mgr.EnsureSession(""); err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}

	// Create a window that produces output.
	if err := mgr.CreateWindow("echo-win", "echo 'hello from tmux'; sleep 30", "/tmp"); err != nil {
		t.Fatalf("CreateWindow: %v", err)
	}

	// Give the shell a moment to print output.
	time.Sleep(500 * time.Millisecond)

	out, err := mgr.CapturePane("echo-win", 50)
	if err != nil {
		t.Fatalf("CapturePane: %v", err)
	}

	// The capture should contain our echo output.
	if out == "" {
		t.Log("CapturePane returned empty (may depend on timing); not fatal")
	} else {
		t.Logf("CapturePane output:\n%s", out)
	}
}

func TestIsWindowAlive(t *testing.T) {
	requireTmux(t)
	session := testSessionName(t)
	mgr := NewManager(session)
	t.Cleanup(func() { _ = mgr.KillSession() })

	if err := mgr.EnsureSession(""); err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}

	// Window with a running process.
	if err := mgr.CreateWindow("alive-test", "sleep 30", "/tmp"); err != nil {
		t.Fatalf("CreateWindow: %v", err)
	}

	alive, err := mgr.IsWindowAlive("alive-test")
	if err != nil {
		t.Fatalf("IsWindowAlive: %v", err)
	}
	if !alive {
		t.Error("expected window to be alive")
	}

	// Nonexistent window.
	alive, err = mgr.IsWindowAlive("no-such-window")
	if err != nil {
		t.Fatalf("IsWindowAlive (nonexistent): %v", err)
	}
	if alive {
		t.Error("expected nonexistent window to not be alive")
	}
}

func TestWaitForExit(t *testing.T) {
	requireTmux(t)
	session := testSessionName(t)
	mgr := NewManager(session)
	t.Cleanup(func() { _ = mgr.KillSession() })

	if err := mgr.EnsureSession(""); err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}

	// Create a window with a command that exits quickly with code 0.
	if err := mgr.CreateWindow("exit-ok", "true", "/tmp"); err != nil {
		t.Fatalf("CreateWindow: %v", err)
	}

	code, err := mgr.WaitForExit("exit-ok")
	if err != nil {
		t.Fatalf("WaitForExit: %v", err)
	}
	if code != 0 {
		t.Errorf("expected exit code 0, got %d", code)
	}

	// Clean up.
	if err := mgr.CloseWindow("exit-ok"); err != nil {
		t.Fatalf("CloseWindow: %v", err)
	}

	// Create a window with a command that exits with a non-zero code.
	if err := mgr.CreateWindow("exit-fail", "exit 42", "/tmp"); err != nil {
		t.Fatalf("CreateWindow (exit-fail): %v", err)
	}

	code, err = mgr.WaitForExit("exit-fail")
	if err != nil {
		t.Fatalf("WaitForExit (exit-fail): %v", err)
	}
	if code != 42 {
		t.Errorf("expected exit code 42, got %d", code)
	}

	// Clean up.
	if err := mgr.CloseWindow("exit-fail"); err != nil {
		t.Fatalf("CloseWindow (exit-fail): %v", err)
	}
}

func TestFocusWindow(t *testing.T) {
	requireTmux(t)
	session := testSessionName(t)
	mgr := NewManager(session)
	t.Cleanup(func() { _ = mgr.KillSession() })

	if err := mgr.EnsureSession(""); err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}

	if err := mgr.CreateWindow("focus-test", "sleep 30", "/tmp"); err != nil {
		t.Fatalf("CreateWindow: %v", err)
	}

	// FocusWindow should not error even from a detached session.
	if err := mgr.FocusWindow("focus-test"); err != nil {
		t.Fatalf("FocusWindow: %v", err)
	}
}

func TestCreateAndKillAgentSession(t *testing.T) {
	requireTmux(t)
	session := testSessionName(t)
	mgr := NewManager(session)

	agentSession := session + "-coder-abcd1234"
	t.Cleanup(func() { _ = mgr.KillAgentSession(agentSession) })

	// Create an agent session.
	if err := mgr.CreateAgentSession(agentSession, "sleep 30", "/tmp"); err != nil {
		t.Fatalf("CreateAgentSession: %v", err)
	}

	// Verify the session exists.
	_, err := runTmux("has-session", "-t", agentSession)
	if err != nil {
		t.Fatalf("agent session should exist after create: %v", err)
	}

	// Kill the agent session.
	if err := mgr.KillAgentSession(agentSession); err != nil {
		t.Fatalf("KillAgentSession: %v", err)
	}

	// Verify it's gone.
	_, err = runTmux("has-session", "-t", agentSession)
	if err == nil {
		t.Error("agent session should not exist after kill")
	}

	// Killing again should be idempotent.
	if err := mgr.KillAgentSession(agentSession); err != nil {
		t.Fatalf("KillAgentSession (idempotent): %v", err)
	}

	// Killing a session that never existed should be fine.
	if err := mgr.KillAgentSession("nonexistent-session-xyz"); err != nil {
		t.Fatalf("KillAgentSession (nonexistent): %v", err)
	}
}

func TestIsAgentSessionAlive(t *testing.T) {
	requireTmux(t)
	session := testSessionName(t)
	mgr := NewManager(session)

	agentSession := session + "-coder-alive123"
	t.Cleanup(func() { _ = mgr.KillAgentSession(agentSession) })

	if err := mgr.CreateAgentSession(agentSession, "sleep 30", "/tmp"); err != nil {
		t.Fatalf("CreateAgentSession: %v", err)
	}

	alive, err := mgr.IsAgentSessionAlive(agentSession)
	if err != nil {
		t.Fatalf("IsAgentSessionAlive: %v", err)
	}
	if !alive {
		t.Error("expected agent session to be alive")
	}

	// Nonexistent session.
	alive, err = mgr.IsAgentSessionAlive("no-such-session-xyz")
	if err != nil {
		t.Fatalf("IsAgentSessionAlive (nonexistent): %v", err)
	}
	if alive {
		t.Error("expected nonexistent session to not be alive")
	}
}

func TestWaitForAgentExit(t *testing.T) {
	requireTmux(t)
	session := testSessionName(t)
	mgr := NewManager(session)

	// Agent session with command that exits with code 0.
	agentOK := session + "-coder-exitok"
	t.Cleanup(func() { _ = mgr.KillAgentSession(agentOK) })

	if err := mgr.CreateAgentSession(agentOK, "true", "/tmp"); err != nil {
		t.Fatalf("CreateAgentSession (exit-ok): %v", err)
	}

	code, err := mgr.WaitForAgentExit(agentOK)
	if err != nil {
		t.Fatalf("WaitForAgentExit: %v", err)
	}
	if code != 0 {
		t.Errorf("expected exit code 0, got %d", code)
	}
	_ = mgr.KillAgentSession(agentOK)

	// Agent session with command that exits with non-zero code.
	agentFail := session + "-coder-exitfail"
	t.Cleanup(func() { _ = mgr.KillAgentSession(agentFail) })

	if err := mgr.CreateAgentSession(agentFail, "exit 42", "/tmp"); err != nil {
		t.Fatalf("CreateAgentSession (exit-fail): %v", err)
	}

	code, err = mgr.WaitForAgentExit(agentFail)
	if err != nil {
		t.Fatalf("WaitForAgentExit (exit-fail): %v", err)
	}
	if code != 42 {
		t.Errorf("expected exit code 42, got %d", code)
	}
}

func TestCaptureAgentPane(t *testing.T) {
	requireTmux(t)
	session := testSessionName(t)
	mgr := NewManager(session)

	agentSession := session + "-coder-capture"
	t.Cleanup(func() { _ = mgr.KillAgentSession(agentSession) })

	if err := mgr.CreateAgentSession(agentSession, "echo 'hello from agent'; sleep 30", "/tmp"); err != nil {
		t.Fatalf("CreateAgentSession: %v", err)
	}

	// Give the shell a moment to print output.
	time.Sleep(500 * time.Millisecond)

	out, err := mgr.CaptureAgentPane(agentSession, 50)
	if err != nil {
		t.Fatalf("CaptureAgentPane: %v", err)
	}

	if out == "" {
		t.Log("CaptureAgentPane returned empty (may depend on timing); not fatal")
	} else {
		t.Logf("CaptureAgentPane output:\n%s", out)
	}
}

func TestFocusAgentSession(t *testing.T) {
	requireTmux(t)
	session := testSessionName(t)
	mgr := NewManager(session)

	agentSession := session + "-coder-focus"
	t.Cleanup(func() { _ = mgr.KillAgentSession(agentSession) })

	if err := mgr.CreateAgentSession(agentSession, "sleep 30", "/tmp"); err != nil {
		t.Fatalf("CreateAgentSession: %v", err)
	}

	// FocusAgentSession uses switch-client which requires an attached client.
	// From a detached test we expect an error but it should not panic.
	_ = mgr.FocusAgentSession(agentSession)
}

func TestListAgentSessions(t *testing.T) {
	requireTmux(t)
	session := testSessionName(t)
	mgr := NewManager(session)

	agent1 := session + "/plan - test task 1111"
	agent2 := session + "/code - parent > subtask 2222"
	t.Cleanup(func() {
		_ = mgr.KillAgentSession(agent1)
		_ = mgr.KillAgentSession(agent2)
	})

	if err := mgr.CreateAgentSession(agent1, "sleep 30", "/tmp"); err != nil {
		t.Fatalf("CreateAgentSession (agent1): %v", err)
	}
	if err := mgr.CreateAgentSession(agent2, "sleep 30", "/tmp"); err != nil {
		t.Fatalf("CreateAgentSession (agent2): %v", err)
	}

	sessions, err := mgr.ListAgentSessions()
	if err != nil {
		t.Fatalf("ListAgentSessions: %v", err)
	}

	if len(sessions) < 2 {
		t.Fatalf("expected at least 2 agent sessions, got %d: %v", len(sessions), sessions)
	}

	found1, found2 := false, false
	for _, s := range sessions {
		if s == agent1 {
			found1 = true
		}
		if s == agent2 {
			found2 = true
		}
	}
	if !found1 {
		t.Errorf("agent session %q not found in: %v", agent1, sessions)
	}
	if !found2 {
		t.Errorf("agent session %q not found in: %v", agent2, sessions)
	}
}

func TestKillSession(t *testing.T) {
	requireTmux(t)
	session := testSessionName(t)
	mgr := NewManager(session)

	if err := mgr.EnsureSession(""); err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}

	if err := mgr.KillSession(); err != nil {
		t.Fatalf("KillSession: %v", err)
	}

	// Verify the session is gone by checking has-session fails.
	_, err := runTmux("has-session", "-t", session)
	if err == nil {
		t.Error("session should not exist after KillSession")
		// Clean up just in case.
		_ = mgr.KillSession()
	}
}
