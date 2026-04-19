package agent

import (
	"testing"

	"github.com/godinj/drem-orchestrator/internal/testutil"
)

// fakeTmuxSessionManager is an in-memory stand-in for *tmux.Manager used by
// tests that exercise ReapOrphanedSessions and NewRunner's session-name
// fallback paths, keeping the test off the host-mode tmux package.
type fakeTmuxSessionManager struct {
	sessions         []string
	alive            map[string]bool
	killed           []string
	createCalls      []string
	dashboardSession string
	listErr          error
}

func (f *fakeTmuxSessionManager) ListAgentSessions() ([]string, error) {
	return f.sessions, f.listErr
}

func (f *fakeTmuxSessionManager) IsAgentSessionAlive(sessionName string) (bool, error) {
	return f.alive[sessionName], nil
}

func (f *fakeTmuxSessionManager) KillAgentSession(sessionName string) error {
	f.killed = append(f.killed, sessionName)
	return nil
}

func (f *fakeTmuxSessionManager) CreateAgentSession(sessionName, _, _ string) error {
	f.createCalls = append(f.createCalls, sessionName)
	return nil
}

// Implements the optional dashboardSessionNamer extension so NewRunner
// can seed tmuxSessionName without a concrete tmux.Manager.
func (f *fakeTmuxSessionManager) SessionName() string {
	return f.dashboardSession
}

func TestNewRunner_WithFakeTmuxManager_SeedsSessionName(t *testing.T) {
	db := testutil.NewTestDB(t)
	fake := &fakeTmuxSessionManager{dashboardSession: "drem-proj"}
	r := NewRunner(db, fake, nil, "/usr/bin/claude", "", 2, nil)
	if r == nil {
		t.Fatal("NewRunner returned nil")
	}
	if r.tmuxSessionName != "drem-proj" {
		t.Errorf("tmuxSessionName = %q, want drem-proj", r.tmuxSessionName)
	}
	if r.TmuxManager() == nil {
		t.Error("TmuxManager should return the fake, not nil")
	}
}

func TestNewRunner_NilTmuxManager_EmptySessionName(t *testing.T) {
	db := testutil.NewTestDB(t)
	r := NewRunner(db, nil, nil, "/usr/bin/claude", "", 2, nil)
	if r.tmuxSessionName != "" {
		t.Errorf("tmuxSessionName = %q, want empty when manager is nil", r.tmuxSessionName)
	}
	if r.TmuxManager() != nil {
		t.Error("TmuxManager should be nil when constructor passed nil")
	}
}

func TestReapOrphanedSessions_WithFakeManager(t *testing.T) {
	fake := &fakeTmuxSessionManager{
		sessions: []string{"drem/agent-a", "drem/agent-b"},
		alive:    map[string]bool{"drem/agent-a": false, "drem/agent-b": true},
	}
	r := &Runner{tmuxMgr: fake}
	reaped, err := r.ReapOrphanedSessions()
	if err != nil {
		t.Fatalf("ReapOrphanedSessions: %v", err)
	}
	if reaped != 1 {
		t.Errorf("reaped = %d, want 1", reaped)
	}
	if len(fake.killed) != 1 || fake.killed[0] != "drem/agent-a" {
		t.Errorf("killed sessions = %v, want [drem/agent-a]", fake.killed)
	}
}
