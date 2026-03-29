package watcher_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/godinj/drem-orchestrator/internal/watcher"
)

// TestHasInboxMessages_WithMessages verifies that HasInboxMessages returns
// true when the agent's inbox contains at least one .md file.
func TestHasInboxMessages_WithMessages(t *testing.T) {
	base := t.TempDir()
	inbox := filepath.Join(base, "mike", "inbox")
	if err := os.MkdirAll(inbox, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inbox, "20260329-011834-kyle.md"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	if !watcher.HasInboxMessages(base, "mike") {
		t.Error("HasInboxMessages: got false, want true (inbox has a .md file)")
	}
}

// TestHasInboxMessages_EmptyInbox verifies that HasInboxMessages returns
// false when the agent's inbox contains no .md files.
func TestHasInboxMessages_EmptyInbox(t *testing.T) {
	base := t.TempDir()
	inbox := filepath.Join(base, "mike", "inbox")
	if err := os.MkdirAll(inbox, 0o755); err != nil {
		t.Fatal(err)
	}

	if watcher.HasInboxMessages(base, "mike") {
		t.Error("HasInboxMessages: got true, want false (inbox is empty)")
	}
}

// TestHasInboxMessages_NoInboxDir verifies that HasInboxMessages returns
// false when the agent's inbox directory does not exist.
func TestHasInboxMessages_NoInboxDir(t *testing.T) {
	base := t.TempDir()

	if watcher.HasInboxMessages(base, "mike") {
		t.Error("HasInboxMessages: got true, want false (inbox dir missing)")
	}
}

// TestHasInboxMessages_ArchiveIgnored verifies that .md files inside the
// archive/ subdirectory are not counted as unarchived inbox messages.
func TestHasInboxMessages_ArchiveIgnored(t *testing.T) {
	base := t.TempDir()
	inbox := filepath.Join(base, "mike", "inbox")
	archive := filepath.Join(inbox, "archive")
	if err := os.MkdirAll(archive, 0o755); err != nil {
		t.Fatal(err)
	}
	// Only an archived message exists.
	if err := os.WriteFile(filepath.Join(archive, "old-msg.md"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	if watcher.HasInboxMessages(base, "mike") {
		t.Error("HasInboxMessages: got true, want false (only archived messages)")
	}
}

// TestHasInboxMessages_NonMdFilesIgnored verifies that non-.md files in the
// inbox are not counted.
func TestHasInboxMessages_NonMdFilesIgnored(t *testing.T) {
	base := t.TempDir()
	inbox := filepath.Join(base, "mike", "inbox")
	if err := os.MkdirAll(inbox, 0o755); err != nil {
		t.Fatal(err)
	}
	// Write a signal file and a text file — neither is .md.
	if err := os.WriteFile(filepath.Join(inbox, "wake.signal"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inbox, "notes.txt"), []byte("notes"), 0o644); err != nil {
		t.Fatal(err)
	}

	if watcher.HasInboxMessages(base, "mike") {
		t.Error("HasInboxMessages: got true, want false (no .md files)")
	}
}

// TestHasInboxMessages_MultipleMessages verifies that HasInboxMessages returns
// true when multiple .md files are present.
func TestHasInboxMessages_MultipleMessages(t *testing.T) {
	base := t.TempDir()
	inbox := filepath.Join(base, "mike", "inbox")
	if err := os.MkdirAll(inbox, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"msg1.md", "msg2.md", "msg3.md"} {
		if err := os.WriteFile(filepath.Join(inbox, name), []byte("content"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if !watcher.HasInboxMessages(base, "mike") {
		t.Error("HasInboxMessages: got false, want true (multiple .md files)")
	}
}
