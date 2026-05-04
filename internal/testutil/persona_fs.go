package testutil

import (
	"os"
	"path/filepath"
	"testing"
)

// PersonaFS is the fully-populated directory tree used by csuite persona tests.
// Each sub-path corresponds to the field of the same name on persona.Config.
type PersonaFS struct {
	Root       string
	InboxDir   string
	OutboxDir  string
	StateFile  string
	ArchiveDir string
	PromptFile string
}

// NewPersonaFS builds a tmpdir layout that mirrors what the compose bind-mounts
// create at runtime. Returning the paths rather than a persona.Config lets
// individual tests override just the piece they care about.
func NewPersonaFS(t *testing.T, promptBody string) PersonaFS {
	t.Helper()
	root := t.TempDir()
	inbox := filepath.Join(root, "inbox")
	outbox := filepath.Join(root, "outbox")
	archive := filepath.Join(inbox, ".archive")
	state := filepath.Join(root, "state.md")
	prompt := filepath.Join(root, "prompts", "seth.md")

	for _, d := range []string{inbox, outbox, archive, filepath.Dir(prompt)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	if err := os.WriteFile(prompt, []byte(promptBody), 0o644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}
	return PersonaFS{
		Root:       root,
		InboxDir:   inbox,
		OutboxDir:  outbox,
		StateFile:  state,
		ArchiveDir: archive,
		PromptFile: prompt,
	}
}
