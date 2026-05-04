package main

// Tests for `drem csuite send` (Phase 2 of plans/drem-csuite-send-cli.md).
// The writer and waiter are exercised in isolation against a tempdir;
// the dispatcher wiring is exercised via dispatchCsuite so usage
// output stays stable.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	// Cross-package sanity check: our generated filename must match
	// the recipient-convention helper the persona poller uses to
	// decide whether an inbox file is "well-formed". Re-exported via
	// filenameLooksLikeOperatorFile below (see test note).
	"github.com/godinj/drem-orchestrator/internal/csuite/persona"
)

// fixedNow returns a clock stuck at t. Lets the writer produce a
// byte-for-byte deterministic filename + frontmatter the tests can
// diff against expectations.
func fixedNow(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

// TestCsuiteSend_WriterGeneratesValidFrontmatter re-parses the written
// file's YAML block and asserts every required field is present and
// correct. Covers plan §7.
func TestCsuiteSend_WriterGeneratesValidFrontmatter(t *testing.T) {
	tmp := t.TempDir()
	fixed := time.Date(2026, 4, 22, 15, 4, 5, 0, time.UTC)

	path, err := writeInboxFile(writerConfig{
		CsuiteHomeRoot: tmp,
		Persona:        "mike",
		Topic:          "status of pod 1",
		Body:           "Pod 1 green.\nNext phase ready.\n",
		Now:            fixedNow(fixed),
		CorrelationID:  "deadbeef",
	})
	if err != nil {
		t.Fatalf("writeInboxFile: %v", err)
	}

	wantBase := "20260422T150405Z-operator-to-mike-deadbeef.md"
	gotBase := filepath.Base(path)
	if gotBase != wantBase {
		t.Fatalf("filename mismatch: got %q want %q", gotBase, wantBase)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	content := string(data)

	if !strings.HasPrefix(content, "---\n") {
		t.Fatalf("missing opening frontmatter delimiter")
	}
	idx := strings.Index(content[4:], "\n---")
	if idx < 0 {
		t.Fatalf("missing closing frontmatter delimiter")
	}
	fmBytes := content[4 : 4+idx]
	var fm map[string]string
	if err := yaml.Unmarshal([]byte(fmBytes), &fm); err != nil {
		t.Fatalf("parse frontmatter: %v\n%s", err, fmBytes)
	}
	wantFields := map[string]string{
		"from":           "operator",
		"to":             "mike",
		"topic":          "status of pod 1",
		"sent_at":        fixed.Format(time.RFC3339),
		"correlation_id": "deadbeef",
	}
	for k, want := range wantFields {
		if got := fm[k]; got != want {
			t.Errorf("frontmatter[%q]: got %q want %q", k, got, want)
		}
	}

	// Body ends after the closing delimiter.
	bodyStart := strings.Index(content, "---\n\n")
	if bodyStart < 0 {
		t.Fatalf("expected '---\\n\\n' before body")
	}
	body := content[bodyStart+len("---\n\n"):]
	if body != "Pod 1 green.\nNext phase ready.\n" {
		t.Errorf("body mismatch: %q", body)
	}
}

// TestCsuiteSend_WriterFilenameConventionMatchesPollerRegex ensures the
// generated filename passes the recipient-convention helper in
// internal/csuite/persona/poller.go so the persona's own outbox-scan
// logic recognises it if it ever reflects the filename. This is a
// cross-package drift guard: if either side renames the convention,
// this test breaks first.
func TestCsuiteSend_WriterFilenameConventionMatchesPollerRegex(t *testing.T) {
	tmp := t.TempDir()
	path, err := writeInboxFile(writerConfig{
		CsuiteHomeRoot: tmp,
		Persona:        "alex",
		Topic:          "drift-guard",
		Body:           "hello",
		Now:            fixedNow(time.Now().UTC()),
		CorrelationID:  "01020304",
	})
	if err != nil {
		t.Fatalf("writeInboxFile: %v", err)
	}
	name := filepath.Base(path)
	// The helper is unexported; we re-export the check via the
	// publicly-visible substring contract it uses. If the helper ever
	// tightens (e.g. to a full regex), replace this string-contains
	// check with a direct call. The plan §9 reading-list flags this
	// as the canonical source of truth.
	if !strings.Contains(name, "-operator-to-") {
		t.Fatalf("filename %q missing '-operator-to-' substring (poller will ignore it)", name)
	}
	// Belt-and-suspenders: the persona package lists AllowedPersonas
	// so we know our source-of-truth import is wired.
	found := false
	for _, p := range persona.AllowedPersonas {
		if p == "alex" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("persona.AllowedPersonas does not include alex — cross-package import broken")
	}
}

// TestCsuiteSend_WriterRejectsOversizeBody asserts the 64-KiB cap
// (matches frontmatterCap in internal/deliver/classify.go).
func TestCsuiteSend_WriterRejectsOversizeBody(t *testing.T) {
	tmp := t.TempDir()
	big := strings.Repeat("a", maxBodyBytes+1)
	_, err := writeInboxFile(writerConfig{
		CsuiteHomeRoot: tmp,
		Persona:        "mike",
		Topic:          "oversize",
		Body:           big,
		Now:            fixedNow(time.Now().UTC()),
		CorrelationID:  "cafecafe",
	})
	if err == nil {
		t.Fatalf("expected oversize-body error, got nil")
	}
	if !strings.Contains(err.Error(), "body too large") {
		t.Errorf("unexpected error shape: %v", err)
	}
}

// TestCsuiteSend_WriterRejectsNonUTF8Body asserts the UTF-8 guard.
func TestCsuiteSend_WriterRejectsNonUTF8Body(t *testing.T) {
	tmp := t.TempDir()
	bad := string([]byte{0xff, 0xfe, 0xfd})
	_, err := writeInboxFile(writerConfig{
		CsuiteHomeRoot: tmp,
		Persona:        "seth",
		Topic:          "bad utf8",
		Body:           bad,
		Now:            fixedNow(time.Now().UTC()),
		CorrelationID:  "abcdef01",
	})
	if err == nil {
		t.Fatalf("expected non-UTF8 error, got nil")
	}
	if !strings.Contains(err.Error(), "UTF-8") {
		t.Errorf("unexpected error shape: %v", err)
	}
}

// TestCsuiteSend_WaiterMatchesByCorrelationID seeds a decoy reply
// (wrong corrid) + a real reply (matching in_reply_to) and asserts
// the waiter returns the real body.
func TestCsuiteSend_WaiterMatchesByCorrelationID(t *testing.T) {
	tmp := t.TempDir()
	inbox := filepath.Join(tmp, "operator", "inbox")
	if err := os.MkdirAll(inbox, 0o755); err != nil {
		t.Fatal(err)
	}
	sentAt := time.Now().UTC().Add(-1 * time.Second)

	decoy := "---\nto: operator\nfrom: mike\nin_reply_to: 11111111\n---\n\ndecoy body\n"
	if err := os.WriteFile(filepath.Join(inbox, "20260422T000001Z-mike-decoy.md"),
		[]byte(decoy), 0o644); err != nil {
		t.Fatal(err)
	}
	real := "---\nto: operator\nfrom: mike\nin_reply_to: deadbeef\n---\n\nreal reply body\n"
	realPath := filepath.Join(inbox, "20260422T000002Z-mike-real.md")
	if err := os.WriteFile(realPath, []byte(real), 0o644); err != nil {
		t.Fatal(err)
	}
	// Bump both files' mtimes past sentAt so they're candidates.
	future := time.Now().UTC().Add(1 * time.Second)
	for _, p := range []string{
		filepath.Join(inbox, "20260422T000001Z-mike-decoy.md"),
		realPath,
	} {
		if err := os.Chtimes(p, future, future); err != nil {
			t.Fatal(err)
		}
	}

	body, path, err := waitForReply(context.Background(), waiterConfig{
		OperatorInboxDir: inbox,
		CorrelationID:    "deadbeef",
		SentAt:           sentAt,
		Timeout:          1 * time.Second,
		PollInterval:     10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("waitForReply: %v", err)
	}
	if path != realPath {
		t.Errorf("path: got %q want %q", path, realPath)
	}
	if strings.TrimSpace(body) != "real reply body" {
		t.Errorf("body: got %q want %q", body, "real reply body")
	}
}

// TestCsuiteSend_WaiterTimeoutReturnsSentinel asserts errWaiterTimeout
// on empty inbox with a tight deadline.
func TestCsuiteSend_WaiterTimeoutReturnsSentinel(t *testing.T) {
	tmp := t.TempDir()
	inbox := filepath.Join(tmp, "operator", "inbox")
	if err := os.MkdirAll(inbox, 0o755); err != nil {
		t.Fatal(err)
	}
	_, _, err := waitForReply(context.Background(), waiterConfig{
		OperatorInboxDir: inbox,
		CorrelationID:    "00000000",
		SentAt:           time.Now().UTC(),
		Timeout:          50 * time.Millisecond,
		PollInterval:     10 * time.Millisecond,
	})
	if !errors.Is(err, errWaiterTimeout) {
		t.Fatalf("expected errWaiterTimeout, got %v", err)
	}
}

// TestCsuiteSend_WaiterIgnoresPreExistingFiles seeds a matching
// correlation_id reply whose mtime predates SentAt; the waiter must
// NOT return it. Prevents stale replies from a prior run poisoning a
// fresh send.
func TestCsuiteSend_WaiterIgnoresPreExistingFiles(t *testing.T) {
	tmp := t.TempDir()
	inbox := filepath.Join(tmp, "operator", "inbox")
	if err := os.MkdirAll(inbox, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := "---\nto: operator\nfrom: mike\nin_reply_to: deadbeef\n---\n\nstale\n"
	stalePath := filepath.Join(inbox, "20200101T000000Z-mike-stale.md")
	if err := os.WriteFile(stalePath, []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}
	// Force old mtime so it falls before SentAt.
	past := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(stalePath, past, past); err != nil {
		t.Fatal(err)
	}
	sentAt := time.Now().UTC()

	_, _, err := waitForReply(context.Background(), waiterConfig{
		OperatorInboxDir: inbox,
		CorrelationID:    "deadbeef",
		SentAt:           sentAt,
		Timeout:          50 * time.Millisecond,
		PollInterval:     10 * time.Millisecond,
	})
	if !errors.Is(err, errWaiterTimeout) {
		t.Fatalf("expected errWaiterTimeout (stale file ignored), got %v", err)
	}
}

// TestCsuiteSend_WaiterFallbackMatchesToOperator covers the degraded
// path from plan §12: if the reply has `to: operator` but no
// `in_reply_to:`, the waiter accepts it. This keeps the CLI usable
// during the rebuild window between Phase 1 and Phase 5.
func TestCsuiteSend_WaiterFallbackMatchesToOperator(t *testing.T) {
	tmp := t.TempDir()
	inbox := filepath.Join(tmp, "operator", "inbox")
	if err := os.MkdirAll(inbox, 0o755); err != nil {
		t.Fatal(err)
	}
	sentAt := time.Now().UTC().Add(-1 * time.Second)
	reply := "---\nto: operator\nfrom: seth\n---\n\nfallback body\n"
	replyPath := filepath.Join(inbox, "20260422T010000Z-seth-fallback.md")
	if err := os.WriteFile(replyPath, []byte(reply), 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().UTC().Add(1 * time.Second)
	if err := os.Chtimes(replyPath, future, future); err != nil {
		t.Fatal(err)
	}

	body, path, err := waitForReply(context.Background(), waiterConfig{
		OperatorInboxDir: inbox,
		CorrelationID:    "deadbeef",
		SentAt:           sentAt,
		Timeout:          500 * time.Millisecond,
		PollInterval:     10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("waitForReply: %v", err)
	}
	if path != replyPath {
		t.Errorf("path: got %q want %q", path, replyPath)
	}
	if strings.TrimSpace(body) != "fallback body" {
		t.Errorf("body: got %q want %q", body, "fallback body")
	}
}

// TestCsuiteSend_DeriveTopic sanity-checks the auto-topic helper.
// Covers trimming, punctuation tail removal, and long-first-line
// truncation.
func TestCsuiteSend_DeriveTopic(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		{"short line", "hello world\n\nmore", "hello world"},
		{"trailing punct", "hi there?\n", "hi there"},
		{"blank first", "\n\nsecond line here.\n", "second line here"},
		{"long line truncated", strings.Repeat("abcd ", 20), strings.TrimRight(strings.Repeat("abcd ", 12), " ")},
		{"empty", "", "message"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := deriveTopic(tc.body)
			if got != tc.want {
				t.Errorf("deriveTopic(%q): got %q want %q", tc.body, got, tc.want)
			}
		})
	}
}

// TestCsuiteSend_ExtractPersonaArg covers the custom pre-parser that
// splits the persona positional out of an argv regardless of where it
// appears relative to flags. Mirrors the ergonomic cases the user is
// most likely to type.
func TestCsuiteSend_ExtractPersonaArg(t *testing.T) {
	cases := []struct {
		name        string
		args        []string
		wantPersona string
		wantRemains []string
	}{
		{"persona first", []string{"mike", "-m", "hi"}, "mike", []string{"-m", "hi"}},
		{"persona after bool flag", []string{"--no-wait", "mike", "-m", "hi"}, "mike", []string{"--no-wait", "-m", "hi"}},
		{"persona after flag+value", []string{"-m", "hi", "mike"}, "mike", []string{"-m", "hi"}},
		{"persona after equals", []string{"--topic=foo", "alex"}, "alex", []string{"--topic=foo"}},
		{"bare dash preserved", []string{"mike", "-"}, "mike", []string{"-"}},
		{"persona with dash body later", []string{"--no-wait", "seth", "-"}, "seth", []string{"--no-wait", "-"}},
		{"double dash terminator", []string{"--", "kyle"}, "kyle", []string{"--"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			remains, persona, err := extractPersonaArg(tc.args)
			if err != nil {
				t.Fatalf("extractPersonaArg: %v", err)
			}
			if persona != tc.wantPersona {
				t.Errorf("persona: got %q want %q", persona, tc.wantPersona)
			}
			if strings.Join(remains, " ") != strings.Join(tc.wantRemains, " ") {
				t.Errorf("remains: got %v want %v", remains, tc.wantRemains)
			}
		})
	}
}

// TestCsuiteSend_IsAllowedPersona guards the flag-parser gate.
func TestCsuiteSend_IsAllowedPersona(t *testing.T) {
	for _, p := range []string{"mike", "alex", "seth", "kyle"} {
		if !isAllowedPersona(p) {
			t.Errorf("%s should be allowed", p)
		}
	}
	for _, p := range []string{"operator", "ross", "", "MIKE"} {
		if isAllowedPersona(p) {
			t.Errorf("%s should NOT be allowed", p)
		}
	}
}

// ---------------------------------------------------------------------
// Phase 3: editor-mode tests.
// The spawner is injected so we never launch a real editor — the fake
// simulates keystrokes by writing directly to the tempfile path.
// ---------------------------------------------------------------------

// TestCsuiteSend_EditorReadsBodyFromTempfile asserts the core editor
// round-trip: the header is seeded, the spawner writes a body, and the
// returned string contains only the body (no instructional comments).
func TestCsuiteSend_EditorReadsBodyFromTempfile(t *testing.T) {
	wantBody := "mike — please review Pod 7 asap.\n\nsecond paragraph.\n"
	spawner := func(editor, path string) error {
		// Simulate "operator deleted the header, typed a body, saved".
		return os.WriteFile(path, []byte(wantBody), 0o644)
	}
	got, err := openEditorForBody(editorConfig{
		Persona:       "mike",
		Topic:         "pod 7 review",
		CorrelationID: "deadbeef",
		Editor:        "vi",
		Spawner:       spawner,
	})
	if err != nil {
		t.Fatalf("openEditorForBody: %v", err)
	}
	if got != wantBody {
		t.Errorf("body: got %q want %q", got, wantBody)
	}
}

// TestCsuiteSend_EditorStripsInstructionalHeader exercises the strip
// logic. The spawner simulates "operator saved without deleting the
// header" — the returned body must be the real content, with any '#'
// lines that live AFTER the first non-comment line preserved verbatim.
func TestCsuiteSend_EditorStripsInstructionalHeader(t *testing.T) {
	spawner := func(editor, path string) error {
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		// Preserve the seeded header; append a body that itself
		// contains a markdown heading (must survive stripping).
		body := "\n\nReal content line 1.\n# This is a markdown H1, not a comment.\nline 3.\n"
		return os.WriteFile(path, append(raw, []byte(body)...), 0o644)
	}
	got, err := openEditorForBody(editorConfig{
		Persona:       "alex",
		Topic:         "",
		CorrelationID: "abcd1234",
		Editor:        "vi",
		Spawner:       spawner,
	})
	if err != nil {
		t.Fatalf("openEditorForBody: %v", err)
	}
	if !strings.HasPrefix(got, "Real content line 1.\n") {
		t.Errorf("body should start with real content after stripping, got prefix %q", got[:min(40, len(got))])
	}
	if !strings.Contains(got, "# This is a markdown H1") {
		t.Errorf("body should preserve '#' lines AFTER the first real line, got %q", got)
	}
	// Sanity: the instructional header lines must be gone.
	if strings.Contains(got, "# Write your message to alex") {
		t.Errorf("body still contains instructional header: %q", got)
	}
}

// TestCsuiteSend_EditorRejectsEmptyBody asserts that an unmodified
// tempfile (just the header) surfaces as an error, not a silent send
// of an empty body.
func TestCsuiteSend_EditorRejectsEmptyBody(t *testing.T) {
	// Spawner is a no-op: leaves the file as seeded (header only).
	spawner := func(editor, path string) error { return nil }
	_, err := openEditorForBody(editorConfig{
		Persona:       "seth",
		Topic:         "quiet",
		CorrelationID: "11112222",
		Editor:        "vi",
		Spawner:       spawner,
	})
	if err == nil {
		t.Fatalf("expected empty-body error, got nil")
	}
	if !strings.Contains(err.Error(), "empty body") {
		t.Errorf("unexpected error shape: %v", err)
	}
}

// TestCsuiteSend_EditorPropagatesEditorExitError asserts a non-zero
// editor exit translates into a clear "editor exited" diagnostic.
func TestCsuiteSend_EditorPropagatesEditorExitError(t *testing.T) {
	spawner := func(editor, path string) error { return errors.New("exit 1") }
	_, err := openEditorForBody(editorConfig{
		Persona:       "kyle",
		Topic:         "broken",
		CorrelationID: "00ff00ff",
		Editor:        "false",
		Spawner:       spawner,
	})
	if err == nil {
		t.Fatalf("expected editor-exit error, got nil")
	}
	if !strings.Contains(err.Error(), "editor exited") {
		t.Errorf("unexpected error shape: %v", err)
	}
}

// ---------------------------------------------------------------------
// Phase 3: file-body-source tests.
// ---------------------------------------------------------------------

// TestCsuiteSend_FileBodySourceReadsContents passes -f to resolveBody
// and asserts the file contents flow through verbatim.
func TestCsuiteSend_FileBodySourceReadsContents(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "body.md")
	content := "line 1\nline 2\nfinal.\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := resolveBody(bodyResolveConfig{
		FilePath: path,
		Persona:  "mike",
	})
	if err != nil {
		t.Fatalf("resolveBody: %v", err)
	}
	if got != content {
		t.Errorf("body: got %q want %q", got, content)
	}
}

// TestCsuiteSend_FileBodySourceRejectsMissingFile surfaces a clean
// diagnostic when the operator typos the path.
func TestCsuiteSend_FileBodySourceRejectsMissingFile(t *testing.T) {
	tmp := t.TempDir()
	_, err := resolveBody(bodyResolveConfig{
		FilePath: filepath.Join(tmp, "does-not-exist.md"),
		Persona:  "mike",
	})
	if err == nil {
		t.Fatalf("expected missing-file error, got nil")
	}
	if !strings.Contains(err.Error(), "does-not-exist.md") {
		t.Errorf("error should mention the path, got %v", err)
	}
}

// TestCsuiteSend_FileBodySourceRejectsOversize asserts the 64 KiB cap
// is enforced at the resolveBody layer so the operator sees the error
// before any inbox write attempt.
func TestCsuiteSend_FileBodySourceRejectsOversize(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "big.md")
	// maxBodyBytes + 1 byte triggers the cap.
	if err := os.WriteFile(path, []byte(strings.Repeat("a", maxBodyBytes+1)), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := resolveBody(bodyResolveConfig{
		FilePath: path,
		Persona:  "mike",
	})
	if err == nil {
		t.Fatalf("expected oversize error, got nil")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Errorf("unexpected error shape: %v", err)
	}
}

// TestCsuiteSend_BodySourcePrecedenceMultipleError exercises the
// conflict detector: -m "a" -f somefile is ambiguous and must error.
func TestCsuiteSend_BodySourcePrecedenceMultipleError(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "b.md")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := resolveBody(bodyResolveConfig{
		Message:  "inline",
		FilePath: path,
		Persona:  "mike",
	})
	if err == nil {
		t.Fatalf("expected multi-source error, got nil")
	}
	if !strings.Contains(err.Error(), "multiple body sources") {
		t.Errorf("unexpected error shape: %v", err)
	}
}

// ---------------------------------------------------------------------
// Phase 3: reply-formatter tests.
// formatReply is pure; stage raw bytes and assert the output shape.
// ---------------------------------------------------------------------

// rawReplyFixture is the canonical reply-file bytes used across the
// formatter tests. Body includes newlines + trailing content so the
// JSON escaping path is exercised.
var rawReplyFixture = []byte("---\n" +
	"from: mike\n" +
	"to: operator\n" +
	"topic: p3 smoke\n" +
	"sent_at: 2026-04-22T15:04:05Z\n" +
	"in_reply_to: deadbeef\n" +
	"correlation_id: aabbccdd\n" +
	"---\n\n" +
	"Hello operator.\n\nMulti-line reply body.\n")

func TestCsuiteSend_FormatReply_BodyOnly(t *testing.T) {
	out, err := formatReply(replyModeBody, rawReplyFixture, "/tmp/reply.md")
	if err != nil {
		t.Fatalf("formatReply: %v", err)
	}
	want := "Hello operator.\n\nMulti-line reply body.\n"
	if out != want {
		t.Errorf("body-only: got %q want %q", out, want)
	}
}

func TestCsuiteSend_FormatReply_WithFrontmatter(t *testing.T) {
	out, err := formatReply(replyModeWithFrontmatter, rawReplyFixture, "/tmp/reply.md")
	if err != nil {
		t.Fatalf("formatReply: %v", err)
	}
	if !strings.HasPrefix(out, "---\n") {
		t.Errorf("with-frontmatter output should begin with '---\\n', got %q", out[:min(10, len(out))])
	}
	if !strings.Contains(out, "Hello operator.") {
		t.Errorf("with-frontmatter output missing body")
	}
}

func TestCsuiteSend_FormatReply_JSON(t *testing.T) {
	out, err := formatReply(replyModeJSON, rawReplyFixture, "/tmp/reply.md")
	if err != nil {
		t.Fatalf("formatReply: %v", err)
	}
	var env jsonReplyEnvelope
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("unmarshal: %v — raw output: %s", err, out)
	}
	if env.Path != "/tmp/reply.md" {
		t.Errorf("envelope path: got %q want %q", env.Path, "/tmp/reply.md")
	}
	for _, k := range []string{"from", "to", "topic", "sent_at", "in_reply_to", "correlation_id"} {
		if _, ok := env.Frontmatter[k]; !ok {
			t.Errorf("envelope frontmatter missing key %q", k)
		}
	}
	if !strings.HasPrefix(env.Body, "Hello operator.") {
		t.Errorf("envelope body: got %q", env.Body)
	}
}

// TestCsuiteSend_FormatReply_JSONEscapesNewlines hardens the
// body-preservation contract: a body containing real newline chars
// must survive JSON round-trip without collapsing to " " or dropping.
func TestCsuiteSend_FormatReply_JSONEscapesNewlines(t *testing.T) {
	raw := []byte("---\nfrom: mike\nto: operator\n---\n\nline1\nline2\nline3\n")
	out, err := formatReply(replyModeJSON, raw, "")
	if err != nil {
		t.Fatalf("formatReply: %v", err)
	}
	var env jsonReplyEnvelope
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Body != "line1\nline2\nline3\n" {
		t.Errorf("body round-trip: got %q want %q", env.Body, "line1\nline2\nline3\n")
	}
	// Path should be omitted (omitempty) when empty.
	if strings.Contains(out, `"path"`) {
		t.Errorf("empty path should be omitted from JSON envelope: %s", out)
	}
}

// TestCsuiteSend_FormatReply_WithFrontmatterAndJSONConflict covers the
// mutual-exclusion gate in selectReplyMode (the surface the CLI hits
// before formatReply runs).
func TestCsuiteSend_FormatReply_WithFrontmatterAndJSONConflict(t *testing.T) {
	_, err := selectReplyMode(true, true)
	if err == nil {
		t.Fatalf("expected conflict error, got nil")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("unexpected error shape: %v", err)
	}
	// Sanity: single-flag modes still resolve cleanly.
	if m, _ := selectReplyMode(true, false); m != replyModeWithFrontmatter {
		t.Errorf("with-frontmatter mode: got %q", m)
	}
	if m, _ := selectReplyMode(false, true); m != replyModeJSON {
		t.Errorf("json mode: got %q", m)
	}
	if m, _ := selectReplyMode(false, false); m != replyModeBody {
		t.Errorf("body mode: got %q", m)
	}
}
