package watcher_test

// trigger_test.go defines the expected behaviour of InboxSignalTrigger BEFORE
// implementation (TDD). All tests must fail when run against the panic stubs
// in inbox_signal_trigger.go.
//
// Covered behaviours:
//
//  1. Signal detection  — a *.signal file in an agent inbox emits a TriggerEvent
//     with the correct AgentName.
//  2. Signal cleanup    — after detection the .signal file is removed so it cannot
//     re-trigger on the next poll.
//  3. Multiple agents   — signal files in different agent directories produce
//     separate TriggerEvents with the correct per-agent names.
//  4. Missing dirs      — if an agent inbox directory doesn't exist, Start and
//     Stop do not crash; no events are emitted.
//  5. New dir discovery — agent directories created after Start() are discovered
//     on the next poll cycle.
//  6. Stop behaviour    — after Stop(), the Events channel is closed and no
//     further events are emitted for signals written after Stop.
//  7. No spurious events — non-.signal files in inbox directories are ignored.

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/godinj/drem-orchestrator/internal/watcher"
)

const (
	// testPollInterval drives new-directory discovery in tests.
	// Short so tests complete quickly.
	testPollInterval = 10 * time.Millisecond

	// testEventTimeout is how long a test waits for an expected event before
	// declaring failure. Must be long enough for at least a few poll cycles.
	testEventTimeout = 300 * time.Millisecond

	// testQuietWindow is how long tests wait to confirm NO event arrives.
	testQuietWindow = 50 * time.Millisecond
)

// receiveEvent waits up to timeout for one TriggerEvent on ch.
// Returns the event and true if one arrived, or zero value and false on timeout
// or if the channel was closed before an event arrived.
func receiveEvent(ch <-chan watcher.TriggerEvent, timeout time.Duration) (watcher.TriggerEvent, bool) {
	select {
	case ev, ok := <-ch:
		if !ok {
			return watcher.TriggerEvent{}, false
		}
		return ev, true
	case <-time.After(timeout):
		return watcher.TriggerEvent{}, false
	}
}

// noEventReceived blocks for the given quiet window and returns true if no
// real event arrived on ch, false if one did. A closed-channel read (ok=false)
// is treated as "no event received" so callers can safely pass a closed channel.
func noEventReceived(ch <-chan watcher.TriggerEvent, window time.Duration) bool {
	select {
	case _, ok := <-ch:
		return !ok // closed channel means no real event; real event means false
	case <-time.After(window):
		return true
	}
}

// inboxDir returns <base>/<agent>/inbox.
func inboxDir(base, agent string) string {
	return filepath.Join(base, agent, "inbox")
}

// mkInbox creates <base>/<agent>/inbox/ and returns the inbox path.
func mkInbox(t *testing.T, base, agent string) string {
	t.Helper()
	dir := inboxDir(base, agent)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkInbox %s/%s: %v", base, agent, err)
	}
	return dir
}

// writeSignal creates <inbox>/wake.signal and returns its path.
func writeSignal(t *testing.T, inbox string) string {
	t.Helper()
	path := filepath.Join(inbox, "wake.signal")
	if err := os.WriteFile(path, nil, 0644); err != nil {
		t.Fatalf("writeSignal %s: %v", inbox, err)
	}
	return path
}

// startInboxTrigger creates a new InboxSignalTrigger, starts it, and registers a
// cleanup that calls Stop. Returns the started trigger.
func startInboxTrigger(t *testing.T, baseDir string) *watcher.InboxSignalTrigger {
	t.Helper()
	tr := watcher.NewInboxSignalTrigger(baseDir, testPollInterval)
	if err := tr.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		_ = tr.Stop()
	})
	return tr
}

// TestInboxSignalTrigger_SignalDetection verifies that creating a *.signal file
// in an agent inbox directory causes InboxSignalTrigger to emit a TriggerEvent
// with the correct AgentName.
func TestInboxSignalTrigger_SignalDetection(t *testing.T) {
	base := t.TempDir()
	inbox := mkInbox(t, base, "mike")
	tr := startInboxTrigger(t, base)

	writeSignal(t, inbox)

	ev, ok := receiveEvent(tr.Events(), testEventTimeout)
	if !ok {
		t.Fatal("expected TriggerEvent for mike, got none (timeout)")
	}
	if ev.AgentName != "mike" {
		t.Errorf("AgentName: got %q, want %q", ev.AgentName, "mike")
	}
}

// TestInboxSignalTrigger_SignalCleanup verifies that after a .signal file is
// detected and a TriggerEvent is emitted, the file is removed from disk so that
// it cannot trigger a second event on the next poll cycle.
func TestInboxSignalTrigger_SignalCleanup(t *testing.T) {
	base := t.TempDir()
	inbox := mkInbox(t, base, "alex")
	tr := startInboxTrigger(t, base)

	signalPath := writeSignal(t, inbox)

	// Wait for detection.
	if _, ok := receiveEvent(tr.Events(), testEventTimeout); !ok {
		t.Fatal("expected TriggerEvent for alex, got none (timeout)")
	}

	// The signal file must have been removed.
	if _, err := os.Stat(signalPath); !os.IsNotExist(err) {
		t.Errorf("signal file still exists at %s after cleanup; want removed", signalPath)
	}
}

// TestInboxSignalTrigger_MultipleAgents verifies that signal files placed in
// different agent inbox directories each produce a separate TriggerEvent with
// the correct per-agent AgentName.
func TestInboxSignalTrigger_MultipleAgents(t *testing.T) {
	base := t.TempDir()
	inboxAlice := mkInbox(t, base, "alice")
	inboxBob := mkInbox(t, base, "bob")
	tr := startInboxTrigger(t, base)

	writeSignal(t, inboxAlice)
	writeSignal(t, inboxBob)

	// Collect two events (order is not guaranteed).
	received := make(map[string]bool)
	for i := 0; i < 2; i++ {
		ev, ok := receiveEvent(tr.Events(), testEventTimeout)
		if !ok {
			t.Fatalf("expected 2 TriggerEvents, got %d (timeout on event %d)", i, i+1)
		}
		received[ev.AgentName] = true
	}

	want := []string{"alice", "bob"}
	got := make([]string, 0, len(received))
	for k := range received {
		got = append(got, k)
	}
	sort.Strings(got)

	if len(got) != len(want) {
		t.Errorf("agent names: got %v, want %v", got, want)
	} else {
		for i, name := range want {
			if got[i] != name {
				t.Errorf("agent names[%d]: got %q, want %q", i, got[i], name)
			}
		}
	}
}

// TestInboxSignalTrigger_MissingDirectories verifies that if the agent inbox
// directories do not exist at Start time, the trigger does not crash and emits
// no events.
func TestInboxSignalTrigger_MissingDirectories(t *testing.T) {
	base := t.TempDir()
	// No agent subdirectories created — base exists but has no children.
	tr := startInboxTrigger(t, base)

	if !noEventReceived(tr.Events(), testQuietWindow) {
		t.Error("unexpected TriggerEvent for empty base directory")
	}

	// Stop must also succeed without crash.
	if err := tr.Stop(); err != nil {
		t.Errorf("Stop on empty base dir: %v", err)
	}
}

// TestInboxSignalTrigger_NewDirectoryDiscovery verifies that agent directories
// created after Start() is called are discovered on the next poll cycle. A
// signal written into a newly created inbox must produce a TriggerEvent.
func TestInboxSignalTrigger_NewDirectoryDiscovery(t *testing.T) {
	base := t.TempDir()
	// Start with an empty base — no agent directories yet.
	tr := startInboxTrigger(t, base)

	// Allow at least one poll cycle to complete before creating the new dir.
	time.Sleep(3 * testPollInterval)

	// Now create a new agent directory (simulates dynamic agent registration).
	inbox := mkInbox(t, base, "newagent")

	// Wait for a poll cycle to discover the new directory.
	time.Sleep(3 * testPollInterval)

	// Write a signal into the newly discovered inbox.
	writeSignal(t, inbox)

	ev, ok := receiveEvent(tr.Events(), testEventTimeout)
	if !ok {
		t.Fatal("expected TriggerEvent for newagent after directory discovery, got none (timeout)")
	}
	if ev.AgentName != "newagent" {
		t.Errorf("AgentName: got %q, want %q", ev.AgentName, "newagent")
	}
}

// TestInboxSignalTrigger_StopBehaviour verifies that after Stop() returns:
//  1. The Events channel is closed (receive returns ok=false).
//  2. Signals written after Stop produce no further events.
func TestInboxSignalTrigger_StopBehaviour(t *testing.T) {
	base := t.TempDir()
	inbox := mkInbox(t, base, "mike")

	tr := watcher.NewInboxSignalTrigger(base, testPollInterval)
	if err := tr.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if err := tr.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// After Stop, the Events channel must be closed.
	ch := tr.Events()
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("Events channel is still open after Stop; want closed")
		}
		// ok == false: channel was closed — correct
	case <-time.After(testEventTimeout):
		t.Error("Events channel not closed after Stop (timeout)")
	}

	// Signals written after Stop must not produce events (channel is closed,
	// so any read will return the zero value with ok=false, not a real event).
	writeSignal(t, inbox)
	if !noEventReceived(ch, testQuietWindow) {
		t.Error("received unexpected TriggerEvent after Stop")
	}
}

// TestInboxSignalTrigger_NoSpuriousEvents verifies that files with extensions
// other than .signal in agent inbox directories do not produce TriggerEvents.
func TestInboxSignalTrigger_NoSpuriousEvents(t *testing.T) {
	base := t.TempDir()
	inbox := mkInbox(t, base, "seth")
	tr := startInboxTrigger(t, base)

	// Write files that must NOT trigger events.
	nonSignalFiles := []string{
		filepath.Join(inbox, "notes.txt"),
		filepath.Join(inbox, "signal"),      // no extension
		filepath.Join(inbox, "signal.bak"),  // wrong extension
		filepath.Join(inbox, ".signalfile"), // dot-prefixed, wrong extension
	}
	for _, path := range nonSignalFiles {
		if err := os.WriteFile(path, nil, 0644); err != nil {
			t.Fatalf("write non-signal file %s: %v", path, err)
		}
	}

	if !noEventReceived(tr.Events(), testQuietWindow) {
		t.Error("TriggerEvent received for non-.signal file; want no events")
	}
}
