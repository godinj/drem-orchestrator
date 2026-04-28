package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// stubWatcher returns an httptest.Server that serves a canned JSON
// payload at /v1/deliveries. Tests use this to drive the CLI end-
// to-end without a real watcher.
func stubWatcher(t *testing.T, token string, rows []map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/deliveries" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if hdr := r.Header.Get("Authorization"); hdr != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"bad token"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(rows)
	}))
}

// writeTokenFile writes tok to a 0600 file in tmp and returns the
// path. Saves each test from duplicating the boilerplate.
func writeTokenFile(t *testing.T, tmp, tok string) string {
	t.Helper()
	p := filepath.Join(tmp, "csuite-watcher.token")
	if err := os.WriteFile(p, []byte(tok), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}
	return p
}

// TestCsuiteAuditList_TableHeader verifies the default (table) format
// emits the documented column header.
func TestCsuiteAuditList_TableHeader(t *testing.T) {
	rows := []map[string]any{{
		"id":           "aa11bb22cc33dd44",
		"from":         "kyle",
		"to":           "seth",
		"type":         "request",
		"priority":     "med",
		"subject":      "Test subject",
		"delivered_at": "2026-04-22T00:05:00Z",
		"status":       "delivered",
		"filename":     "msg.md",
	}}
	srv := stubWatcher(t, "tok", rows)
	defer srv.Close()
	tmp := t.TempDir()
	tokPath := writeTokenFile(t, tmp, "tok")

	var out bytes.Buffer
	code := runCsuiteAudit([]string{
		"list",
		"--watcher-url", srv.URL,
		"--token", tokPath,
		"--format", "table",
	}, &out, io.Discard)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; output=%s", code, out.String())
	}
	got := out.String()
	want := "TIME FROM TO TYPE PRIO SUBJECT STATUS ID"
	if !strings.Contains(got, want) {
		t.Errorf("table output missing header %q; got:\n%s", want, got)
	}
}

// TestCsuiteAuditList_EmptyResultPrintsHeaderOnly verifies an empty
// ledger still prints the header row — an empty list is not an error.
func TestCsuiteAuditList_EmptyResultPrintsHeaderOnly(t *testing.T) {
	srv := stubWatcher(t, "tok", []map[string]any{})
	defer srv.Close()
	tmp := t.TempDir()
	tokPath := writeTokenFile(t, tmp, "tok")

	var out bytes.Buffer
	code := runCsuiteAudit([]string{
		"list",
		"--watcher-url", srv.URL,
		"--token", tokPath,
		"--format", "table",
	}, &out, io.Discard)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	got := strings.TrimSpace(out.String())
	if !strings.Contains(got, "TIME") || !strings.Contains(got, "ID") {
		t.Errorf("empty result should still print header; got %q", got)
	}
}

// TestCsuiteAuditList_JSONFormat verifies --format json passes the
// watcher response through verbatim.
func TestCsuiteAuditList_JSONFormat(t *testing.T) {
	rows := []map[string]any{{
		"id":           "abc123",
		"from":         "kyle",
		"to":           "seth",
		"type":         "request",
		"priority":     "med",
		"subject":      "foo",
		"delivered_at": "2026-04-22T00:05:00Z",
		"status":       "delivered",
		"filename":     "msg.md",
	}}
	srv := stubWatcher(t, "tok", rows)
	defer srv.Close()
	tmp := t.TempDir()
	tokPath := writeTokenFile(t, tmp, "tok")

	var out bytes.Buffer
	code := runCsuiteAudit([]string{
		"list",
		"--watcher-url", srv.URL,
		"--token", tokPath,
		"--format", "json",
	}, &out, io.Discard)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	var decoded []map[string]any
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("decode: %v; output=%s", err, out.String())
	}
	if len(decoded) != 1 {
		t.Fatalf("len = %d, want 1", len(decoded))
	}
	if decoded[0]["id"] != "abc123" {
		t.Errorf("id = %v, want abc123", decoded[0]["id"])
	}
}

// TestCsuiteAuditList_FlagsForwardedAsQuery verifies that CLI flags
// get translated to HTTP query params on the watcher call.
func TestCsuiteAuditList_FlagsForwardedAsQuery(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("[]"))
	}))
	defer srv.Close()
	tmp := t.TempDir()
	tokPath := writeTokenFile(t, tmp, "tok")

	var out bytes.Buffer
	code := runCsuiteAudit([]string{
		"list",
		"--watcher-url", srv.URL,
		"--token", tokPath,
		"--from", "kyle",
		"--to", "seth",
		"--status", "delivered",
		"--type", "request",
		"--since", "1h",
		"--limit", "10",
		"--offset", "5",
		"--format", "json",
	}, &out, io.Discard)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	for _, k := range []string{"from=kyle", "to=seth", "status=delivered", "type=request", "since=1h", "limit=10", "offset=5"} {
		if !strings.Contains(gotQuery, k) {
			t.Errorf("query %q missing %q", gotQuery, k)
		}
	}
}

// TestCsuiteAuditList_MissingTokenFileExits2 verifies the CLI exits 2
// with a diagnostic pointing at the expected path when the token file
// does not exist. Do not auto-generate.
func TestCsuiteAuditList_MissingTokenFileExits2(t *testing.T) {
	srv := stubWatcher(t, "tok", nil)
	defer srv.Close()

	missing := filepath.Join(t.TempDir(), "nope")
	var out, errBuf bytes.Buffer
	code := runCsuiteAudit([]string{
		"list",
		"--watcher-url", srv.URL,
		"--token", missing,
	}, &out, &errBuf)
	if code != 2 {
		t.Errorf("exit code = %d, want 2; stderr=%s", code, errBuf.String())
	}
	if !strings.Contains(errBuf.String(), missing) {
		t.Errorf("stderr should mention the expected path %q, got %q", missing, errBuf.String())
	}
}

// TestCsuiteAuditList_WatcherReturns401ExitsNonZero verifies the CLI
// surfaces a 401 from the watcher as a non-zero exit.
func TestCsuiteAuditList_WatcherReturns401ExitsNonZero(t *testing.T) {
	srv := stubWatcher(t, "server-secret", nil)
	defer srv.Close()
	tmp := t.TempDir()
	tokPath := writeTokenFile(t, tmp, "wrong-secret")

	var out, errBuf bytes.Buffer
	code := runCsuiteAudit([]string{
		"list",
		"--watcher-url", srv.URL,
		"--token", tokPath,
	}, &out, &errBuf)
	if code == 0 {
		t.Errorf("exit code = %d, want non-zero on 401", code)
	}
}

// TestCsuiteAuditList_NonTTYDefaultsToJSON verifies the TTY rule: no
// TTY on stdout + no explicit --format → json.
func TestCsuiteAuditList_NonTTYDefaultsToJSON(t *testing.T) {
	rows := []map[string]any{{
		"id":           "abc",
		"from":         "kyle",
		"to":           "seth",
		"delivered_at": "2026-04-22T00:05:00Z",
		"status":       "delivered",
		"filename":     "x.md",
	}}
	srv := stubWatcher(t, "tok", rows)
	defer srv.Close()
	tmp := t.TempDir()
	tokPath := writeTokenFile(t, tmp, "tok")

	var out bytes.Buffer
	// bytes.Buffer is not a TTY, so the CLI should default to json.
	code := runCsuiteAudit([]string{
		"list",
		"--watcher-url", srv.URL,
		"--token", tokPath,
	}, &out, io.Discard)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	var decoded []map[string]any
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Errorf("non-TTY output should be JSON, got: %s (err=%v)", out.String(), err)
	}
	if len(decoded) != 1 {
		t.Errorf("len = %d, want 1", len(decoded))
	}
}

// Unused reference to silence lint if needed.
var _ = fmt.Sprintf

func TestCsuiteAuditBacklog_CountsFilesystemAndDB(t *testing.T) {
	root := t.TempDir()
	writeBacklogFile(t, root, "kyle", "inbox", "one.md")
	writeBacklogFile(t, root, "kyle", "inbox", "two.md")
	writeBacklogFile(t, root, "kyle", "inbox", "ignored.txt")
	writeBacklogFile(t, root, "kyle", "acks", "ack.md")
	writeBacklogFile(t, root, "kyle", "outbox", "reply.md")
	writeBacklogFile(t, root, "mike", "outbox", "mike.md")

	dbPath := filepath.Join(root, "csuite.db")
	db := openBacklogTestDB(t, dbPath)
	mustExecBacklogTestDB(t, db, `CREATE TABLE csuite_inbox_messages (persona TEXT, read_at TEXT)`)
	mustExecBacklogTestDB(t, db, `INSERT INTO csuite_inbox_messages (persona, read_at) VALUES ('kyle', NULL), ('kyle', '2026-04-27T00:00:00Z'), ('mike', NULL)`)
	mustExecBacklogTestDB(t, db, `CREATE TABLE event_deliveries (event_id TEXT, agent TEXT, acked_at TEXT)`)
	mustExecBacklogTestDB(t, db, `INSERT INTO event_deliveries (event_id, agent, acked_at) VALUES ('e1', 'kyle', NULL), ('e2', 'kyle', '2026-04-27T00:00:00Z'), ('e3', 'seth', NULL)`)
	db.Close()

	var out bytes.Buffer
	code := runCsuiteAudit([]string{"backlog", "--csuite-dir", root, "--format", "json"}, &out, &bytes.Buffer{})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}

	rows := decodeBacklogRows(t, out.Bytes())
	kyle := backlogRowByPersona(t, rows, "kyle")
	if kyle.FilesystemInboxMarkdownCount != 2 || kyle.FilesystemAcksMarkdownCount != 1 || kyle.FilesystemOutboxMarkdownCount != 1 {
		t.Fatalf("kyle filesystem counts = inbox %d acks %d outbox %d, want 2/1/1", kyle.FilesystemInboxMarkdownCount, kyle.FilesystemAcksMarkdownCount, kyle.FilesystemOutboxMarkdownCount)
	}
	if kyle.DBCsuiteInboxUnreadCount == nil || *kyle.DBCsuiteInboxUnreadCount != 1 {
		t.Fatalf("kyle DB inbox unread = %v, want 1", kyle.DBCsuiteInboxUnreadCount)
	}
	if kyle.DBEventDeliveriesUnackedCount == nil || *kyle.DBEventDeliveriesUnackedCount != 1 {
		t.Fatalf("kyle DB event unacked = %v, want 1", kyle.DBEventDeliveriesUnackedCount)
	}

	mike := backlogRowByPersona(t, rows, "mike")
	if mike.FilesystemOutboxMarkdownCount != 1 {
		t.Fatalf("mike filesystem outbox = %d, want 1", mike.FilesystemOutboxMarkdownCount)
	}
	if mike.DBCsuiteInboxUnreadCount == nil || *mike.DBCsuiteInboxUnreadCount != 1 {
		t.Fatalf("mike DB inbox unread = %v, want 1", mike.DBCsuiteInboxUnreadCount)
	}
	if mike.DBEventDeliveriesUnackedCount != nil {
		t.Fatalf("mike DB event unacked = %v, want nil/unavailable", *mike.DBEventDeliveriesUnackedCount)
	}
}

func TestCsuiteAuditBacklog_MissingDBAndTablesAreUnavailable(t *testing.T) {
	root := t.TempDir()
	writeBacklogFile(t, root, "alex", "inbox", "note.md")

	var out bytes.Buffer
	code := runCsuiteAudit([]string{"backlog", "--csuite-dir", root, "--format", "table"}, &out, &bytes.Buffer{})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	got := out.String()
	for _, label := range []string{
		"FILESYSTEM_INBOX_MD_COUNT",
		"FILESYSTEM_ACKS_MD_COUNT",
		"FILESYSTEM_OUTBOX_MD_COUNT",
		"DB_CSUITE_INBOX_MESSAGES_UNREAD_COUNT",
		"DB_EVENT_DELIVERIES_UNACKED_COUNT",
	} {
		if !strings.Contains(got, label) {
			t.Fatalf("table output missing explicit label %q:\n%s", label, got)
		}
	}
	if !strings.Contains(got, "alex 1 0 0 unavailable unavailable") {
		t.Fatalf("alex row should include filesystem count and unavailable DB counts:\n%s", got)
	}
}

func writeBacklogFile(t *testing.T, root, persona, dir, name string) {
	t.Helper()
	fullDir := filepath.Join(root, persona, dir)
	if err := os.MkdirAll(fullDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", fullDir, err)
	}
	if err := os.WriteFile(filepath.Join(fullDir, name), []byte("test"), 0o644); err != nil {
		t.Fatalf("write backlog file: %v", err)
	}
}

func openBacklogTestDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	return db
}

func mustExecBacklogTestDB(t *testing.T, db *sql.DB, query string) {
	t.Helper()
	if _, err := db.Exec(query); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

func decodeBacklogRows(t *testing.T, data []byte) []csuiteBacklogRow {
	t.Helper()
	var rows []csuiteBacklogRow
	if err := json.Unmarshal(data, &rows); err != nil {
		t.Fatalf("decode backlog json: %v; output=%s", err, string(data))
	}
	return rows
}

func backlogRowByPersona(t *testing.T, rows []csuiteBacklogRow, persona string) csuiteBacklogRow {
	t.Helper()
	for _, row := range rows {
		if row.Persona == persona {
			return row
		}
	}
	t.Fatalf("missing row for persona %s", persona)
	return csuiteBacklogRow{}
}
