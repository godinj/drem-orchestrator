package agent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/tmux"
)

// requireTmux skips the test if tmux is not available in PATH.
func requireTmux(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("requires tmux")
	}
}

// testSessionName returns a unique session name for a test run.
func testSessionName(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("drem-agent-test-%d", time.Now().UnixNano())
}

// newTestRunner creates a Runner with no DB or worktree manager, suitable for
// testing methods that only use tmux and the running map.
func newTestRunner(t *testing.T, tm *tmux.Manager) *Runner {
	t.Helper()
	return &Runner{
		tmux:          tm,
		maxConcurrent: 4,
		running:       make(map[uuid.UUID]*RunningAgent),
		completions:   make(chan Completion, 4),
		semaphore:     make(chan struct{}, 4),
	}
}

func TestVerifySpawn_SessionAlive(t *testing.T) {
	requireTmux(t)

	session := testSessionName(t)
	mgr := tmux.NewManager(session)

	agentSession := session + "/test-alive-agent"
	t.Cleanup(func() { _ = mgr.KillAgentSession(agentSession) })

	// Create a long-running agent session.
	if err := mgr.CreateAgentSession(agentSession, "sleep 300", "/tmp"); err != nil {
		t.Fatalf("CreateAgentSession: %v", err)
	}

	r := newTestRunner(t, mgr)
	agentID := uuid.New()

	// Add agent to running map.
	r.mu.Lock()
	r.running[agentID] = &RunningAgent{
		AgentID:     agentID,
		TmuxSession: agentSession,
	}
	r.mu.Unlock()

	// Run verifySpawn with a short delay.
	r.verifySpawn(agentID, agentSession, 100*time.Millisecond)

	// No completion should have been sent since the session is alive.
	select {
	case c := <-r.completions:
		t.Fatalf("unexpected completion sent for alive session: %+v", c)
	default:
		// Good — no completion sent.
	}
}

func TestVerifySpawn_SessionDead(t *testing.T) {
	requireTmux(t)

	session := testSessionName(t)
	mgr := tmux.NewManager(session)

	// Use a nonexistent session name — IsAgentSessionAlive returns false, nil.
	deadSession := session + "/dead-agent-session"

	r := newTestRunner(t, mgr)
	agentID := uuid.New()

	// Add agent to running map.
	r.mu.Lock()
	r.running[agentID] = &RunningAgent{
		AgentID:     agentID,
		TmuxSession: deadSession,
	}
	r.mu.Unlock()

	// Run verifySpawn with a short delay.
	r.verifySpawn(agentID, deadSession, 100*time.Millisecond)

	// A failure completion should have been sent.
	select {
	case c := <-r.completions:
		if c.AgentID != agentID {
			t.Errorf("completion agent ID = %s, want %s", c.AgentID, agentID)
		}
		if c.ReturnCode != 1 {
			t.Errorf("completion ReturnCode = %d, want 1", c.ReturnCode)
		}
	default:
		t.Fatal("expected completion for dead session, but none sent")
	}
}

func TestVerifySpawn_AlreadyCompleted(t *testing.T) {
	requireTmux(t)

	session := testSessionName(t)
	mgr := tmux.NewManager(session)

	// Use a nonexistent session — but the agent won't be in the running map.
	deadSession := session + "/already-done-agent"

	r := newTestRunner(t, mgr)
	agentID := uuid.New()

	// Do NOT add agent to running map — simulates already completed.

	// Run verifySpawn with a short delay.
	r.verifySpawn(agentID, deadSession, 100*time.Millisecond)

	// No completion should be sent because the agent is not in the running map.
	select {
	case c := <-r.completions:
		t.Fatalf("unexpected completion for already-completed agent: %+v", c)
	default:
		// Good — no double-complete.
	}
}

func TestPromptWriteVerification_Success(t *testing.T) {
	dir := t.TempDir()
	promptPath := filepath.Join(dir, "agent-prompt.md")
	prompt := "This is a test prompt with special chars: $HOME $(echo hello) `backticks` 'quotes'"

	if err := os.WriteFile(promptPath, []byte(prompt), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Verify readback matches.
	written, err := os.ReadFile(promptPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(written) != len(prompt) {
		t.Errorf("prompt length mismatch: wrote %d, read %d", len(prompt), len(written))
	}
	if string(written) != prompt {
		t.Errorf("prompt content mismatch: wrote %q, read %q", prompt, string(written))
	}
}

func TestPromptWriteVerification_ReadOnlyPath(t *testing.T) {
	dir := t.TempDir()

	// Create a read-only directory.
	roDir := filepath.Join(dir, "readonly")
	if err := os.MkdirAll(roDir, 0o555); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	t.Cleanup(func() {
		// Restore write permissions so t.TempDir cleanup works.
		_ = os.Chmod(roDir, 0o755)
	})

	promptPath := filepath.Join(roDir, "agent-prompt.md")
	prompt := "test prompt"

	err := os.WriteFile(promptPath, []byte(prompt), 0o644)
	if err == nil {
		t.Fatal("expected WriteFile to fail on read-only directory, but it succeeded")
	}
}
