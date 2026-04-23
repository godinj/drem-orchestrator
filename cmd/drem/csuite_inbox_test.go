package main

// Tests for `drem csuite inbox` (Phase 4 of plans/drem-csuite-send-cli.md).
// Each subcommand is exercised end-to-end via its run* entry point with
// a tempdir pointed at via DREM_CSUITE_HOME. Covers:
//
//   - list default output, sort order, empty inbox.
//   - list --json payload shape + body_excerpt truncation.
//   - read <index>, read <path>, --with-frontmatter, --json, invalid index.
//   - archive by index + by path, auto-mkdir, destination-collision guard.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// setupInboxFixture creates a tempdir, points DREM_CSUITE_HOME at it,
// ensures <tmp>/operator/inbox/ exists, and returns the inbox dir path.
func setupInboxFixture(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("DREM_CSUITE_HOME", tmp)
	inbox := filepath.Join(tmp, "operator", "inbox")
	if err := os.MkdirAll(inbox, 0o755); err != nil {
		t.Fatalf("mkdir inbox: %v", err)
	}
	return inbox
}

// writeFakeReply drops a plausible persona→operator reply file into
// dir. sent_at is RFC3339-formatted so the sort-by-sent_at code path
// can parse it. correlation_id is cosmetic for these tests.
func writeFakeReply(t *testing.T, dir, name, from, topic, sentAt, body string) string {
	t.Helper()
	content := fmt.Sprintf("---\nfrom: %s\nto: operator\nsent_at: %s\ntopic: %s\nin_reply_to: abc123\n---\n\n%s\n",
		from, sentAt, topic, body)
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write fake reply %q: %v", path, err)
	}
	return path
}

// touchMtime forces a specific mtime on path — used to prove the
// sort-order code favours sent_at over mtime.
func touchMtime(t *testing.T, path string, ts time.Time) {
	t.Helper()
	if err := os.Chtimes(path, ts, ts); err != nil {
		t.Fatalf("chtimes %q: %v", path, err)
	}
}

// TestCsuiteInbox_ListEmptyInbox — empty inbox → no output, exit 0.
func TestCsuiteInbox_ListEmptyInbox(t *testing.T) {
	_ = setupInboxFixture(t)

	var stdout, stderr bytes.Buffer
	code := runCsuiteInboxList(nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	if out := stdout.String(); out != "" {
		t.Fatalf("expected empty stdout, got %q", out)
	}
}

// TestCsuiteInbox_ListOrdersBySentAt — three files with REVERSE-
// ordered mtimes (newest mtime first) but ASCENDING sent_at. List
// order must follow sent_at, proving the sort uses the frontmatter
// field, not mtime.
func TestCsuiteInbox_ListOrdersBySentAt(t *testing.T) {
	inbox := setupInboxFixture(t)

	// Write in mtime-descending order so ReadDir's lexical default
	// has no chance of accidentally producing the right answer.
	p3 := writeFakeReply(t, inbox, "c-mike.md", "mike", "third", "2026-04-22T12:00:03Z", "body 3")
	p2 := writeFakeReply(t, inbox, "b-mike.md", "mike", "second", "2026-04-22T12:00:02Z", "body 2")
	p1 := writeFakeReply(t, inbox, "a-mike.md", "mike", "first", "2026-04-22T12:00:01Z", "body 1")

	// Force mtimes newer→older so mtime-based sort would reverse it.
	touchMtime(t, p1, time.Date(2026, 4, 22, 20, 0, 0, 0, time.UTC))
	touchMtime(t, p2, time.Date(2026, 4, 22, 19, 0, 0, 0, time.UTC))
	touchMtime(t, p3, time.Date(2026, 4, 22, 18, 0, 0, 0, time.UTC))

	var stdout, stderr bytes.Buffer
	code := runCsuiteInboxList(nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	lines := strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d: %q", len(lines), stdout.String())
	}
	// Each line: <index>\t<ts>\t<from>\t<subject>\t<filename>
	wantTopics := []string{"first", "second", "third"}
	for i, want := range wantTopics {
		fields := strings.Split(lines[i], "\t")
		if len(fields) != 5 {
			t.Fatalf("line %d: expected 5 tab-fields, got %d: %q", i, len(fields), lines[i])
		}
		if fields[0] != fmt.Sprintf("%d", i+1) {
			t.Errorf("line %d index: got %q want %d", i, fields[0], i+1)
		}
		if fields[3] != want {
			t.Errorf("line %d topic: got %q want %q", i, fields[3], want)
		}
	}
}

// TestCsuiteInbox_ListJSON — --json emits a valid JSON array with
// the pinned field shape.
func TestCsuiteInbox_ListJSON(t *testing.T) {
	inbox := setupInboxFixture(t)
	writeFakeReply(t, inbox, "a.md", "mike", "topic-one", "2026-04-22T12:00:01Z", "line one")
	writeFakeReply(t, inbox, "b.md", "alex", "topic-two", "2026-04-22T12:00:02Z", "line two")

	var stdout, stderr bytes.Buffer
	code := runCsuiteInboxList([]string{"--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	var rows []listInboxJSONRow
	if err := json.Unmarshal(stdout.Bytes(), &rows); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, stdout.String())
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(rows))
	}
	if rows[0].Index != 1 || rows[1].Index != 2 {
		t.Errorf("indexes: got %d,%d want 1,2", rows[0].Index, rows[1].Index)
	}
	if rows[0].From != "mike" || rows[1].From != "alex" {
		t.Errorf("from: got %q,%q want mike,alex", rows[0].From, rows[1].From)
	}
	if rows[0].Topic != "topic-one" || rows[1].Topic != "topic-two" {
		t.Errorf("topic mismatch: %+v", rows)
	}
	if rows[0].BodyExcerpt != "line one" || rows[1].BodyExcerpt != "line two" {
		t.Errorf("body_excerpt mismatch: %+v", rows)
	}
	if rows[0].Path == "" || !strings.HasSuffix(rows[0].Path, "a.md") {
		t.Errorf("path[0] unexpected: %q", rows[0].Path)
	}
}

// TestCsuiteInbox_ListBodyExcerptTruncates — body > 200 chars → JSON
// excerpt truncated to 200 with trailing ellipsis.
func TestCsuiteInbox_ListBodyExcerptTruncates(t *testing.T) {
	inbox := setupInboxFixture(t)
	longBody := strings.Repeat("x", 300)
	writeFakeReply(t, inbox, "a.md", "mike", "big", "2026-04-22T12:00:01Z", longBody)

	var stdout, stderr bytes.Buffer
	code := runCsuiteInboxList([]string{"--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	var rows []listInboxJSONRow
	if err := json.Unmarshal(stdout.Bytes(), &rows); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	excerpt := rows[0].BodyExcerpt
	// 200 runes + trailing "…"
	runes := []rune(excerpt)
	if len(runes) != 201 {
		t.Fatalf("excerpt rune-len: got %d want 201 — %q", len(runes), excerpt)
	}
	if runes[len(runes)-1] != '…' {
		t.Errorf("expected trailing ellipsis, got %q", string(runes[len(runes)-1]))
	}
}

// TestCsuiteInbox_ReadByIndex — resolve index 2 → prints body of the
// second file in sorted order.
func TestCsuiteInbox_ReadByIndex(t *testing.T) {
	inbox := setupInboxFixture(t)
	writeFakeReply(t, inbox, "a.md", "mike", "first", "2026-04-22T12:00:01Z", "body ONE")
	writeFakeReply(t, inbox, "b.md", "mike", "second", "2026-04-22T12:00:02Z", "body TWO")

	var stdout, stderr bytes.Buffer
	code := runCsuiteInboxRead([]string{"2"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "body TWO" {
		t.Errorf("body: got %q want %q", got, "body TWO")
	}
}

// TestCsuiteInbox_ReadByPath — pass a full path → prints that file's
// body regardless of list ordering.
func TestCsuiteInbox_ReadByPath(t *testing.T) {
	inbox := setupInboxFixture(t)
	writeFakeReply(t, inbox, "a.md", "mike", "first", "2026-04-22T12:00:01Z", "body ONE")
	pB := writeFakeReply(t, inbox, "b.md", "mike", "second", "2026-04-22T12:00:02Z", "body TWO")

	var stdout, stderr bytes.Buffer
	code := runCsuiteInboxRead([]string{pB}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "body TWO" {
		t.Errorf("body: got %q want %q", got, "body TWO")
	}
}

// TestCsuiteInbox_ReadInvalidIndex — index > list size → exit 2 with
// an error mentioning the range.
func TestCsuiteInbox_ReadInvalidIndex(t *testing.T) {
	inbox := setupInboxFixture(t)
	writeFakeReply(t, inbox, "a.md", "mike", "only", "2026-04-22T12:00:01Z", "body one")

	var stdout, stderr bytes.Buffer
	code := runCsuiteInboxRead([]string{"7"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit: got %d want 2 stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "out of range") {
		t.Errorf("stderr should mention out-of-range: %q", stderr.String())
	}
}

// TestCsuiteInbox_ReadWithFrontmatter — --with-frontmatter → prints
// the full file including the YAML block.
func TestCsuiteInbox_ReadWithFrontmatter(t *testing.T) {
	inbox := setupInboxFixture(t)
	writeFakeReply(t, inbox, "a.md", "mike", "fm test", "2026-04-22T12:00:01Z", "the body")

	var stdout, stderr bytes.Buffer
	code := runCsuiteInboxRead([]string{"1", "--with-frontmatter"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	out := stdout.String()
	if !strings.HasPrefix(out, "---\n") {
		t.Errorf("output should start with '---\\n': %q", out)
	}
	if !strings.Contains(out, "from: mike") {
		t.Errorf("output missing frontmatter from-field: %q", out)
	}
	if !strings.Contains(out, "the body") {
		t.Errorf("output missing body: %q", out)
	}
}

// TestCsuiteInbox_ReadJSON — --json → parses back as the Phase 3
// jsonReplyEnvelope shape.
func TestCsuiteInbox_ReadJSON(t *testing.T) {
	inbox := setupInboxFixture(t)
	writeFakeReply(t, inbox, "a.md", "mike", "json test", "2026-04-22T12:00:01Z", "the body")

	var stdout, stderr bytes.Buffer
	code := runCsuiteInboxRead([]string{"1", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	var env jsonReplyEnvelope
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &env); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, stdout.String())
	}
	if env.Frontmatter["from"] != "mike" {
		t.Errorf("fm.from: got %v want mike", env.Frontmatter["from"])
	}
	if env.Body != "the body\n" {
		t.Errorf("body: got %q want %q", env.Body, "the body\n")
	}
	if env.Path == "" {
		t.Errorf("path should be populated")
	}
}

// TestCsuiteInbox_ArchiveMovesFile — archive by index → file moves
// to .archive/, original gone.
func TestCsuiteInbox_ArchiveMovesFile(t *testing.T) {
	inbox := setupInboxFixture(t)
	src := writeFakeReply(t, inbox, "a.md", "mike", "arch", "2026-04-22T12:00:01Z", "body")

	var stdout, stderr bytes.Buffer
	code := runCsuiteInboxArchive([]string{"1"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Errorf("source should be gone, err=%v", err)
	}
	dst := filepath.Join(inbox, ".archive", "a.md")
	if _, err := os.Stat(dst); err != nil {
		t.Errorf("destination should exist: %v", err)
	}
	if got := strings.TrimSpace(stdout.String()); got != dst {
		t.Errorf("stdout: got %q want %q", got, dst)
	}
}

// TestCsuiteInbox_ArchiveCreatesArchiveDirIfMissing — .archive absent
// → MkdirAll'd then move succeeds.
func TestCsuiteInbox_ArchiveCreatesArchiveDirIfMissing(t *testing.T) {
	inbox := setupInboxFixture(t)
	// Confirm no .archive dir exists at the start of the test.
	if _, err := os.Stat(filepath.Join(inbox, ".archive")); !os.IsNotExist(err) {
		t.Fatalf("fixture invariant: .archive should not exist yet, err=%v", err)
	}
	writeFakeReply(t, inbox, "a.md", "mike", "arch", "2026-04-22T12:00:01Z", "body")

	var stdout, stderr bytes.Buffer
	code := runCsuiteInboxArchive([]string{"1"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	dst := filepath.Join(inbox, ".archive", "a.md")
	if _, err := os.Stat(dst); err != nil {
		t.Errorf("destination should exist: %v", err)
	}
}

// TestCsuiteInbox_ArchiveRejectsDestCollision — .archive/<same-name>
// already present → error, source untouched.
func TestCsuiteInbox_ArchiveRejectsDestCollision(t *testing.T) {
	inbox := setupInboxFixture(t)
	archDir := filepath.Join(inbox, ".archive")
	if err := os.MkdirAll(archDir, 0o755); err != nil {
		t.Fatalf("mkdir archive: %v", err)
	}
	src := writeFakeReply(t, inbox, "a.md", "mike", "arch", "2026-04-22T12:00:01Z", "body live")
	// Seed a same-named file in .archive/.
	dstPath := filepath.Join(archDir, "a.md")
	if err := os.WriteFile(dstPath, []byte("body preexisting"), 0o644); err != nil {
		t.Fatalf("seed .archive: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := runCsuiteInboxArchive([]string{"1"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit, stderr=%q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "destination already exists") {
		t.Errorf("stderr should cite destination-exists: %q", stderr.String())
	}
	// Source must remain.
	if _, err := os.Stat(src); err != nil {
		t.Errorf("source should survive: %v", err)
	}
	// Archive copy must be untouched (content check).
	data, err := os.ReadFile(dstPath)
	if err != nil {
		t.Fatalf("read archived: %v", err)
	}
	if string(data) != "body preexisting" {
		t.Errorf("archive copy was overwritten: %q", data)
	}
}

// TestCsuiteInbox_ArchiveByPath — arg by path → works identically.
func TestCsuiteInbox_ArchiveByPath(t *testing.T) {
	inbox := setupInboxFixture(t)
	src := writeFakeReply(t, inbox, "a.md", "mike", "arch", "2026-04-22T12:00:01Z", "body")

	var stdout, stderr bytes.Buffer
	code := runCsuiteInboxArchive([]string{src}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Errorf("source should be gone, err=%v", err)
	}
	dst := filepath.Join(inbox, ".archive", "a.md")
	if _, err := os.Stat(dst); err != nil {
		t.Errorf("destination should exist: %v", err)
	}
}

// TestCsuiteInbox_DispatchWiring — confirm `drem csuite inbox` routes
// through dispatchCsuite so the top-level help block stays in sync.
func TestCsuiteInbox_DispatchWiring(t *testing.T) {
	_ = setupInboxFixture(t)
	var stdout, stderr bytes.Buffer
	code := dispatchCsuite([]string{"inbox", "list"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
}
