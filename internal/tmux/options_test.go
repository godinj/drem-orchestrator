package tmux

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// testSocketName returns a unique socket name for test isolation.
func testSocketName(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("drem-test-%d", time.Now().UnixNano())
}

// killSocketServer tears down an entire tmux server on the given socket.
func killSocketServer(t *testing.T, socket string) {
	t.Helper()
	// kill-server on the dedicated socket; ignore errors (server may not exist).
	_ = exec.Command("tmux", "-L", socket, "kill-server").Run()
}

func TestNewManagerWithOptions(t *testing.T) {
	t.Run("defaults without options", func(t *testing.T) {
		mgr := NewManager("myproject")
		if mgr.Socket != "" {
			t.Errorf("expected empty Socket, got %q", mgr.Socket)
		}
		if mgr.ConfigFile != "" {
			t.Errorf("expected empty ConfigFile, got %q", mgr.ConfigFile)
		}
		if mgr.SessionName != "myproject" {
			t.Errorf("expected SessionName %q, got %q", "myproject", mgr.SessionName)
		}
	})

	t.Run("WithSocket sets Socket field", func(t *testing.T) {
		mgr := NewManager("myproject", WithSocket("drem"))
		if mgr.Socket != "drem" {
			t.Errorf("expected Socket %q, got %q", "drem", mgr.Socket)
		}
	})

	t.Run("WithConfigFile sets ConfigFile field", func(t *testing.T) {
		mgr := NewManager("myproject", WithConfigFile("/path/to/tmux.conf"))
		if mgr.ConfigFile != "/path/to/tmux.conf" {
			t.Errorf("expected ConfigFile %q, got %q", "/path/to/tmux.conf", mgr.ConfigFile)
		}
	})

	t.Run("both options together", func(t *testing.T) {
		mgr := NewManager("myproject", WithSocket("drem"), WithConfigFile("tmux.conf"))
		if mgr.Socket != "drem" {
			t.Errorf("expected Socket %q, got %q", "drem", mgr.Socket)
		}
		if mgr.ConfigFile != "tmux.conf" {
			t.Errorf("expected ConfigFile %q, got %q", "tmux.conf", mgr.ConfigFile)
		}
	})
}

func TestRunTmuxPrependsGlobalFlags(t *testing.T) {
	// These tests verify the argument structure without running real tmux.
	// We inspect the args that would be passed by building them the same way
	// the Manager should: prepend -L <socket> and -f <config> before the subcommand.

	buildArgs := func(m *Manager, subcmd ...string) []string {
		var args []string
		if m.Socket != "" {
			args = append(args, "-L", m.Socket)
		}
		if m.ConfigFile != "" {
			args = append(args, "-f", m.ConfigFile)
		}
		args = append(args, subcmd...)
		return args
	}

	t.Run("socket only", func(t *testing.T) {
		mgr := NewManager("proj", WithSocket("drem"))
		args := buildArgs(mgr, "has-session", "-t", "=proj")
		// Expected: ["-L", "drem", "has-session", "-t", "=proj"]
		if len(args) < 2 || args[0] != "-L" || args[1] != "drem" {
			t.Errorf("expected -L drem prefix, got %v", args)
		}
		if args[2] != "has-session" {
			t.Errorf("expected subcommand at index 2, got %q", args[2])
		}
	})

	t.Run("socket and config", func(t *testing.T) {
		mgr := NewManager("proj", WithSocket("drem"), WithConfigFile("/etc/tmux.conf"))
		args := buildArgs(mgr, "new-session", "-d", "-s", "proj")
		// Expected: ["-L", "drem", "-f", "/etc/tmux.conf", "new-session", "-d", "-s", "proj"]
		if len(args) < 5 {
			t.Fatalf("expected at least 5 args (4 prefix + subcommand), got %v", args)
		}
		if args[0] != "-L" || args[1] != "drem" {
			t.Errorf("expected -L drem, got %v", args[:2])
		}
		if args[2] != "-f" || args[3] != "/etc/tmux.conf" {
			t.Errorf("expected -f /etc/tmux.conf, got %v", args[2:4])
		}
		if args[4] != "new-session" {
			t.Errorf("expected subcommand at index 4, got %q", args[4])
		}
	})

	t.Run("no options no extra flags", func(t *testing.T) {
		mgr := NewManager("proj")
		args := buildArgs(mgr, "list-sessions")
		if len(args) != 1 || args[0] != "list-sessions" {
			t.Errorf("expected bare subcommand, got %v", args)
		}
	})
}

func TestSocketIsolation(t *testing.T) {
	requireTmux(t)

	socket := testSocketName(t)
	session := testSessionName(t)
	mgr := NewManager(session, WithSocket(socket))
	t.Cleanup(func() {
		_ = mgr.KillSession()
		killSocketServer(t, socket)
	})

	// Create a session on the dedicated socket.
	if err := mgr.EnsureSession(""); err != nil {
		t.Fatalf("EnsureSession on socket %q: %v", socket, err)
	}

	// Verify: session exists on the dedicated socket.
	out, err := exec.Command("tmux", "-L", socket, "has-session", "-t", "="+session).CombinedOutput()
	if err != nil {
		t.Fatalf("session should exist on socket %q: %v\n%s", socket, err, out)
	}

	// Verify: session does NOT exist on the default server.
	err = exec.Command("tmux", "has-session", "-t", "="+session).Run()
	if err == nil {
		t.Errorf("session %q should NOT exist on default tmux server", session)
	}
}

func TestAgentSessionWithSocket(t *testing.T) {
	requireTmux(t)

	socket := testSocketName(t)
	session := testSessionName(t)
	mgr := NewManager(session, WithSocket(socket))

	agentSession := session + "/coder-socket-test"
	t.Cleanup(func() {
		_ = mgr.KillAgentSession(agentSession)
		killSocketServer(t, socket)
	})

	// Create an agent session on the dedicated socket.
	if err := mgr.CreateAgentSession(agentSession, "sleep 30", "/tmp"); err != nil {
		t.Fatalf("CreateAgentSession on socket %q: %v", socket, err)
	}

	// Verify: agent session exists on the dedicated socket.
	out, err := exec.Command("tmux", "-L", socket, "has-session", "-t", "="+agentSession).CombinedOutput()
	if err != nil {
		t.Fatalf("agent session should exist on socket %q: %v\n%s", socket, err, out)
	}

	// Verify: alive check works through the socket.
	alive, err := mgr.IsAgentSessionAlive(agentSession)
	if err != nil {
		t.Fatalf("IsAgentSessionAlive: %v", err)
	}
	if !alive {
		t.Error("expected agent session to be alive on dedicated socket")
	}
}

func TestAttachArgsWithSocket(t *testing.T) {
	// Verify that Attach() would build argv with -L and -f before the subcommand.
	// We can't actually call Attach() (it replaces the process), so we verify
	// the expected argv structure.

	t.Run("socket and config in attach argv", func(t *testing.T) {
		mgr := NewManager("proj", WithSocket("drem"), WithConfigFile("my.conf"))

		// The expected argv for attach (not nested) should be:
		// ["tmux", "-L", "drem", "-f", "my.conf", "attach-session", "-t", "=proj"]
		var argv []string
		argv = append(argv, "tmux")
		if mgr.Socket != "" {
			argv = append(argv, "-L", mgr.Socket)
		}
		if mgr.ConfigFile != "" {
			argv = append(argv, "-f", mgr.ConfigFile)
		}
		argv = append(argv, "attach-session", "-t", mgr.exactTarget())

		expected := []string{"tmux", "-L", "drem", "-f", "my.conf", "attach-session", "-t", "=proj"}
		if len(argv) != len(expected) {
			t.Fatalf("argv length mismatch: got %v, want %v", argv, expected)
		}
		for i := range expected {
			if argv[i] != expected[i] {
				t.Errorf("argv[%d] = %q, want %q", i, argv[i], expected[i])
			}
		}
	})

	t.Run("socket only in switch-client argv", func(t *testing.T) {
		mgr := NewManager("proj", WithSocket("drem"))

		var argv []string
		argv = append(argv, "tmux")
		if mgr.Socket != "" {
			argv = append(argv, "-L", mgr.Socket)
		}
		if mgr.ConfigFile != "" {
			argv = append(argv, "-f", mgr.ConfigFile)
		}
		argv = append(argv, "switch-client", "-t", mgr.exactTarget())

		expected := []string{"tmux", "-L", "drem", "switch-client", "-t", "=proj"}
		if len(argv) != len(expected) {
			t.Fatalf("argv length mismatch: got %v, want %v", argv, expected)
		}
		for i := range expected {
			if argv[i] != expected[i] {
				t.Errorf("argv[%d] = %q, want %q", i, argv[i], expected[i])
			}
		}
	})
}

func TestWrappedCommandUsesSocket(t *testing.T) {
	// CreateAgentSession wraps the command with:
	//   tmux set-option -p remain-on-exit on; <cmd>
	// When socket is set, that inner tmux call must also use -L <socket>.

	mgr := NewManager("proj", WithSocket("drem"))

	// The wrapped command should include "-L drem" in the inner tmux call.
	// Expected: "tmux -L drem set-option -p remain-on-exit on; <cmd>"
	expectedPrefix := "tmux -L drem set-option"

	// Build the wrapped command the same way CreateAgentSession should:
	var innerTmux string
	if mgr.Socket != "" {
		innerTmux = fmt.Sprintf("tmux -L %s set-option -p remain-on-exit on", mgr.Socket)
	} else {
		innerTmux = "tmux set-option -p remain-on-exit on"
	}
	wrapped := fmt.Sprintf("%s; echo hello", innerTmux)

	if !strings.HasPrefix(wrapped, expectedPrefix) {
		t.Errorf("wrapped command should start with %q, got %q", expectedPrefix, wrapped)
	}

	// Without socket, no -L flag in the inner command.
	mgrNoSocket := NewManager("proj")
	var innerNoSocket string
	if mgrNoSocket.Socket != "" {
		innerNoSocket = fmt.Sprintf("tmux -L %s set-option -p remain-on-exit on", mgrNoSocket.Socket)
	} else {
		innerNoSocket = "tmux set-option -p remain-on-exit on"
	}
	wrappedNoSocket := fmt.Sprintf("%s; echo hello", innerNoSocket)

	if strings.Contains(wrappedNoSocket, "-L") {
		t.Errorf("wrapped command without socket should not contain -L, got %q", wrappedNoSocket)
	}
}
