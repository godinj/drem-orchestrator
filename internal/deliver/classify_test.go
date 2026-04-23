package deliver

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testLogger returns a *log.Logger that writes to buf with no prefix
// or flags — sufficient for string-matching in tests.
func testLogger(buf *bytes.Buffer) *log.Logger {
	return log.New(buf, "", 0)
}

// newCsuiteTree builds a /csuite-like directory tree rooted in
// t.TempDir, redirects the package-level csuiteRoot var for the
// duration of the test, and returns the root path so tests can
// stage outbox files under <root>/<persona>/outbox/.
func newCsuiteTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, persona := range []string{"mike", "alex", "seth", "kyle"} {
		if err := os.MkdirAll(filepath.Join(root, persona, "outbox"), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.MkdirAll(filepath.Join(root, persona, "inbox"), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	prev := csuiteRoot
	csuiteRoot = root
	t.Cleanup(func() { csuiteRoot = prev })
	return root
}

// stageOutbox writes body into <root>/<source>/outbox/<name> and
// returns (containerPath, sha256) for use in DeliverRequest.
//
// The "containerPath" returned here mirrors what a production
// persona would send: /csuite/<source>/outbox/<name>. The test's
// csuiteRoot redirection makes that path resolve to the temp dir.
func stageOutbox(t *testing.T, root, source, name string, body []byte) (string, string) {
	t.Helper()
	path := filepath.Join(root, source, "outbox", name)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write outbox: %v", err)
	}
	sum := sha256.Sum256(body)
	return path, hex.EncodeToString(sum[:])
}

// buildBody encodes a DeliverRequest to JSON for HTTP tests.
func buildBody(t *testing.T, source, outboxPath, sha string) []byte {
	t.Helper()
	b, err := json.Marshal(DeliverRequest{
		SourcePersona: source,
		OutboxPath:    outboxPath,
		SHA256:        sha,
		EmittedAt:     "2026-04-21T15:30:00Z",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// TestClassifyBytes_PersonaRecipient verifies scalar persona names
// route to ClassPersona with Dest set. Post-Phase-2, kyle is a
// regular persona alongside mike/alex/seth — all four share the
// ClassPersona code path.
func TestClassifyBytes_PersonaRecipient(t *testing.T) {
	cases := []string{"mike", "alex", "seth", "kyle"}
	for _, persona := range cases {
		persona := persona
		t.Run(persona, func(t *testing.T) {
			body := []byte(fmt.Sprintf("---\nfrom: someone\nto: %s\n---\n\nhello\n", persona))
			got, err := classifyBytes(body)
			if err != nil {
				t.Fatalf("classifyBytes: %v", err)
			}
			if got.Class != ClassPersona || got.Dest != persona {
				t.Errorf("got %+v, want class=persona dest=%s", got, persona)
			}
		})
	}
}

// TestClassifyBytes_MultiRecipientRejected verifies a YAML sequence
// "to:" produces ErrMultiRecipient.
func TestClassifyBytes_MultiRecipientRejected(t *testing.T) {
	body := []byte("---\nfrom: alex\nto:\n  - mike\n  - seth\n---\n\nmsg\n")
	_, err := classifyBytes(body)
	if !errors.Is(err, ErrMultiRecipient) {
		t.Errorf("expected ErrMultiRecipient, got %v", err)
	}
}

// TestClassifyBytes_UnknownRecipientQuarantines verifies an unknown
// scalar routes to quarantine (not rejected with 400).
func TestClassifyBytes_UnknownRecipientQuarantines(t *testing.T) {
	body := []byte("---\nfrom: alex\nto: dave\n---\n\nmsg\n")
	got, err := classifyBytes(body)
	if err != nil {
		t.Fatalf("classifyBytes: %v", err)
	}
	if got.Class != ClassQuarantine {
		t.Errorf("got class %q, want quarantine", got.Class)
	}
}

// TestClassifyBytes_MissingToQuarantines verifies frontmatter without
// a "to" field routes to quarantine.
func TestClassifyBytes_MissingToQuarantines(t *testing.T) {
	body := []byte("---\nfrom: alex\ntopic: chatter\n---\n\nmsg\n")
	got, err := classifyBytes(body)
	if err != nil {
		t.Fatalf("classifyBytes: %v", err)
	}
	if got.Class != ClassQuarantine {
		t.Errorf("got class %q, want quarantine", got.Class)
	}
}

// TestClassifyBytes_NoFrontmatterQuarantines verifies a body without
// any --- delimiters routes to quarantine (per plan: malformed-
// but-parse-failed is routed, not dropped).
func TestClassifyBytes_NoFrontmatterQuarantines(t *testing.T) {
	body := []byte("just a plain message with no frontmatter\n")
	got, err := classifyBytes(body)
	if err != nil {
		t.Fatalf("classifyBytes: %v", err)
	}
	if got.Class != ClassQuarantine {
		t.Errorf("got class %q, want quarantine", got.Class)
	}
}

// TestClassifyBytes_UnparseableFrontmatterQuarantines verifies invalid
// YAML inside the frontmatter block routes to quarantine, not 500.
func TestClassifyBytes_UnparseableFrontmatterQuarantines(t *testing.T) {
	body := []byte("---\nfrom: alex\nto: [malformed\n---\n\nmsg\n")
	got, err := classifyBytes(body)
	if err != nil {
		t.Fatalf("classifyBytes: %v", err)
	}
	if got.Class != ClassQuarantine {
		t.Errorf("got class %q, want quarantine; reason=%q", got.Class, got.Reason)
	}
}

// ---------------------------------------------------------------------------
// HTTP-level tests exercising ClassifyFile + quarantine write
// ---------------------------------------------------------------------------

// (persona / kyle happy-path tests live in deliver_inbox_test.go
// alongside the rest of commit-4's real-delivery coverage.)

// TestDeliver_QuarantineClass_WritesFileAndReturns202 verifies the
// quarantine path: file is copied to /csuite/quarantine/<source>/,
// ledger row is inserted with dest="quarantine", and the endpoint
// returns 202 Accepted with a delivery_id.
func TestDeliver_QuarantineClass_WritesFileAndReturns202(t *testing.T) {
	root := newCsuiteTree(t)
	body := []byte("---\nfrom: alex\nto: nobody-real\n---\n\nmsg\n")
	_, sha := stageOutbox(t, root, "alex", "q1.md", body)

	l := openTestLedger(t)
	h := Handler(Config{Token: "secret", Ledger: l})

	w := post(t, h, "secret", buildBody(t, "alex", "/csuite/alex/outbox/q1.md", sha))
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%q", w.Code, w.Body.String())
	}

	// Response includes delivery_id.
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["delivery_id"] == "" {
		t.Errorf("missing delivery_id in %v", resp)
	}

	// File landed under <root>/quarantine/alex/q1.md on disk.
	want := filepath.Join(root, "quarantine", "alex", "q1.md")
	got, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("read quarantine file: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("quarantine file contents differ")
	}

	// Ledger recorded dest="quarantine" and a protocol dest_path.
	d, found, err := l.Lookup(sha)
	if err != nil || !found {
		t.Fatalf("Lookup: found=%v err=%v", found, err)
	}
	if d.Dest != ClassQuarantine {
		t.Errorf("ledger dest = %q, want quarantine", d.Dest)
	}
	if d.DestPath != "/csuite/quarantine/alex/q1.md" {
		t.Errorf("ledger dest_path = %q, want /csuite/quarantine/alex/q1.md", d.DestPath)
	}
}

// TestDeliver_MultiRecipientRejected verifies a list "to:" is rejected
// with 400 and the documented body — before any ledger or FS state
// is touched.
func TestDeliver_MultiRecipientRejected(t *testing.T) {
	root := newCsuiteTree(t)
	body := []byte("---\nfrom: alex\nto:\n  - mike\n  - seth\n---\n\nbroadcast\n")
	_, sha := stageOutbox(t, root, "alex", "multi.md", body)

	l := openTestLedger(t)
	h := Handler(Config{Token: "secret", Ledger: l})

	w := post(t, h, "secret", buildBody(t, "alex", "/csuite/alex/outbox/multi.md", sha))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%q", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "multi-recipient") {
		t.Errorf("expected 'multi-recipient' in body, got %q", w.Body.String())
	}
	// No ledger row — reject happened before Insert.
	if _, found, _ := l.Lookup(sha); found {
		t.Error("unexpected ledger row after rejection")
	}
}

// TestDeliver_UnparseableFrontmatterQuarantines verifies the
// plan-mandated behaviour: malformed-but-parse-failed frontmatter is
// routed to quarantine with 202, NOT rejected with 400.
func TestDeliver_UnparseableFrontmatterQuarantines(t *testing.T) {
	root := newCsuiteTree(t)
	body := []byte("---\nto: [broken\n---\n\nbody\n")
	_, sha := stageOutbox(t, root, "alex", "bad.md", body)

	l := openTestLedger(t)
	h := Handler(Config{Token: "secret", Ledger: l})

	w := post(t, h, "secret", buildBody(t, "alex", "/csuite/alex/outbox/bad.md", sha))
	if w.Code != http.StatusAccepted {
		t.Errorf("status = %d, want 202; body=%q", w.Code, w.Body.String())
	}
	// File exists in quarantine.
	if _, err := os.Stat(filepath.Join(root, "quarantine", "alex", "bad.md")); err != nil {
		t.Errorf("quarantine file not created: %v", err)
	}
}

// TestDeliver_QuarantineLogLine verifies the "quarantine" log line is
// emitted with the reason for operator diagnosability.
func TestDeliver_QuarantineLogLine(t *testing.T) {
	root := newCsuiteTree(t)
	body := []byte("---\nfrom: alex\n# no to field\n---\n\nmsg\n")
	_, sha := stageOutbox(t, root, "alex", "ql.md", body)

	l := openTestLedger(t)
	var buf bytes.Buffer
	h := Handler(Config{Token: "secret", Ledger: l, Logger: testLogger(&buf)})

	w := post(t, h, "secret", buildBody(t, "alex", "/csuite/alex/outbox/ql.md", sha))
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", w.Code)
	}
	if !strings.Contains(buf.String(), "quarantine") {
		t.Errorf("expected 'quarantine' in log, got %q", buf.String())
	}
}
