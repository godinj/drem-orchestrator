package persona_test

// reaper_test.go covers the startup-scan reaper that reconciles
// orphan `.failures` sidecars against their `.md` anchors.
// Scoreboard item 3 / attack plan §3 Group A.

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/godinj/drem-orchestrator/internal/csuite/persona"
)

// newReaperFS builds the persona filesystem layout without going
// through persona.New — the reaper runs before the poller is
// constructed.
func newReaperFS(t *testing.T) (persona.Config, string, string) {
	t.Helper()
	root := t.TempDir()
	inbox := filepath.Join(root, "inbox")
	archive := filepath.Join(inbox, ".archive")
	for _, d := range []string{inbox, archive} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	cfg := persona.Config{
		Persona:    "seth",
		InboxDir:   inbox,
		ArchiveDir: archive,
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return cfg, inbox, archive
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestReaper_ReapsOrphanSidecarWhenAnchorInArchive is the primary
// regression proof. The exact scenario from Seth's inbox 2026-04-22:
// a `.failures` sidecar sits next to no .md, but the .md itself
// lives in .archive/ (Claude's retry succeeded, sidecar counter was
// never cleaned up).
func TestReaper_ReapsOrphanSidecarWhenAnchorInArchive(t *testing.T) {
	cfg, inbox, archive := newReaperFS(t)

	// Anchor is in archive; sidecar is in inbox — the orphan shape.
	writeFile(t, filepath.Join(archive, "msg.md"), "anchor body")
	writeFile(t, filepath.Join(inbox, "msg.md.failures"), "2\n")

	res := persona.ReapOnceOnStartup(cfg)
	if res.Reaped != 1 {
		t.Errorf("Reaped = %d, want 1", res.Reaped)
	}
	if res.Active != 0 || res.TrueFailures != 0 {
		t.Errorf("unexpected counts: active=%d true_failures=%d", res.Active, res.TrueFailures)
	}

	// Sidecar must be gone.
	if _, err := os.Stat(filepath.Join(inbox, "msg.md.failures")); !os.IsNotExist(err) {
		t.Errorf("sidecar should be reaped, stat err=%v", err)
	}
	// Anchor must still be in archive.
	if _, err := os.Stat(filepath.Join(archive, "msg.md")); err != nil {
		t.Errorf("archived anchor should still exist: %v", err)
	}
}

// TestReaper_LeavesActiveSidecar verifies the in-flight retry case:
// anchor .md is in the inbox AND sidecar is in the inbox — the
// retry loop is actively attempting the message, so we leave the
// counter alone.
func TestReaper_LeavesActiveSidecar(t *testing.T) {
	cfg, inbox, _ := newReaperFS(t)

	writeFile(t, filepath.Join(inbox, "active.md"), "active body")
	writeFile(t, filepath.Join(inbox, "active.md.failures"), "1\n")

	res := persona.ReapOnceOnStartup(cfg)
	if res.Reaped != 0 {
		t.Errorf("Reaped = %d, want 0 (active retry)", res.Reaped)
	}
	if res.Active != 1 {
		t.Errorf("Active = %d, want 1", res.Active)
	}

	// Both files untouched.
	if _, err := os.Stat(filepath.Join(inbox, "active.md.failures")); err != nil {
		t.Errorf("active sidecar should remain: %v", err)
	}
	if _, err := os.Stat(filepath.Join(inbox, "active.md")); err != nil {
		t.Errorf("active anchor should remain: %v", err)
	}
}

// TestReaper_ReapsAnchorWithFailedSuffix covers (b'): when the
// failure-retention path renamed the anchor to <name>.failed in the
// archive, the sidecar is still an orphan and should be reaped.
func TestReaper_ReapsAnchorWithFailedSuffix(t *testing.T) {
	cfg, inbox, archive := newReaperFS(t)

	writeFile(t, filepath.Join(archive, "broken.md.failed"), "failed body")
	writeFile(t, filepath.Join(inbox, "broken.md.failures"), "3\n")

	res := persona.ReapOnceOnStartup(cfg)
	if res.Reaped != 1 {
		t.Errorf("Reaped = %d, want 1", res.Reaped)
	}
	if _, err := os.Stat(filepath.Join(inbox, "broken.md.failures")); !os.IsNotExist(err) {
		t.Errorf("sidecar should be reaped after .failed anchor: %v", err)
	}
}

// TestReaper_TrueFailureKeepsSidecarAndLogs verifies the (c)
// path: no anchor anywhere. The sidecar is left in place and
// TrueFailures is incremented so the operator notices.
func TestReaper_TrueFailureKeepsSidecarAndLogs(t *testing.T) {
	cfg, inbox, _ := newReaperFS(t)

	writeFile(t, filepath.Join(inbox, "gone.md.failures"), "5\n")

	res := persona.ReapOnceOnStartup(cfg)
	if res.TrueFailures != 1 {
		t.Errorf("TrueFailures = %d, want 1", res.TrueFailures)
	}
	if res.Reaped != 0 {
		t.Errorf("Reaped = %d, want 0 (no anchor to reconcile)", res.Reaped)
	}
	// Sidecar must remain for operator review.
	if _, err := os.Stat(filepath.Join(inbox, "gone.md.failures")); err != nil {
		t.Errorf("true-failure sidecar should remain: %v", err)
	}
}

// TestReaper_MixedCase exercises all three paths in one run so the
// counts add up.
func TestReaper_MixedCase(t *testing.T) {
	cfg, inbox, archive := newReaperFS(t)

	// Orphan (anchor in archive).
	writeFile(t, filepath.Join(archive, "a.md"), "a")
	writeFile(t, filepath.Join(inbox, "a.md.failures"), "2\n")
	// Active (anchor + sidecar in inbox).
	writeFile(t, filepath.Join(inbox, "b.md"), "b")
	writeFile(t, filepath.Join(inbox, "b.md.failures"), "1\n")
	// True failure (no anchor).
	writeFile(t, filepath.Join(inbox, "c.md.failures"), "3\n")
	// Non-sidecar file to confirm skip.
	writeFile(t, filepath.Join(inbox, "d.md"), "d")

	res := persona.ReapOnceOnStartup(cfg)
	if res.Scanned != 3 {
		t.Errorf("Scanned = %d, want 3 (non-.failures files ignored)", res.Scanned)
	}
	if res.Reaped != 1 {
		t.Errorf("Reaped = %d, want 1", res.Reaped)
	}
	if res.Active != 1 {
		t.Errorf("Active = %d, want 1", res.Active)
	}
	if res.TrueFailures != 1 {
		t.Errorf("TrueFailures = %d, want 1", res.TrueFailures)
	}
}

// TestReaper_EmptyInboxNoop confirms the reaper handles a clean
// inbox without surprise counters.
func TestReaper_EmptyInboxNoop(t *testing.T) {
	cfg, _, _ := newReaperFS(t)
	res := persona.ReapOnceOnStartup(cfg)
	if res.Scanned != 0 || res.Reaped != 0 || res.Active != 0 || res.TrueFailures != 0 {
		t.Errorf("non-zero counts on empty inbox: %+v", res)
	}
}
