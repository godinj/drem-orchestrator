package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// stubQueueWatcher returns an httptest.Server that serves canned JSON
// at /v1/queue. Tests drive the CLI end-to-end against this.
func stubQueueWatcher(t *testing.T, token string, rows []map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/queue" {
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

// TestCsuiteAuditQueue_TableHeader verifies the default (table) format
// emits the documented column header for queue.
func TestCsuiteAuditQueue_TableHeader(t *testing.T) {
	rows := []map[string]any{{
		"agent":  "seth",
		"scope":  "inbox",
		"count":  float64(1),
		"oldest": "2026-04-22T00:05:00Z",
		"newest": "2026-04-22T00:05:00Z",
	}}
	srv := stubQueueWatcher(t, "tok", rows)
	defer srv.Close()
	tmp := t.TempDir()
	tokPath := writeTokenFile(t, tmp, "tok")

	var out bytes.Buffer
	code := runCsuiteAudit([]string{
		"queue",
		"--watcher-url", srv.URL,
		"--token", tokPath,
		"--format", "table",
	}, &out, io.Discard)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; output=%s", code, out.String())
	}
	got := out.String()
	want := "AGENT SCOPE COUNT OLDEST NEWEST"
	if !strings.Contains(got, want) {
		t.Errorf("table header missing %q; got:\n%s", want, got)
	}
}

// TestCsuiteAuditQueue_EmptyPrintsHeaderOnly verifies empty queue
// still prints the header row.
func TestCsuiteAuditQueue_EmptyPrintsHeaderOnly(t *testing.T) {
	srv := stubQueueWatcher(t, "tok", []map[string]any{})
	defer srv.Close()
	tmp := t.TempDir()
	tokPath := writeTokenFile(t, tmp, "tok")

	var out bytes.Buffer
	code := runCsuiteAudit([]string{
		"queue",
		"--watcher-url", srv.URL,
		"--token", tokPath,
		"--format", "table",
	}, &out, io.Discard)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "AGENT") {
		t.Errorf("empty queue should still print header; got %q", out.String())
	}
}

// TestCsuiteAuditQueue_JSONFormat verifies --format json passes the
// response through.
func TestCsuiteAuditQueue_JSONFormat(t *testing.T) {
	rows := []map[string]any{{
		"agent":  "seth",
		"scope":  "outbox",
		"count":  float64(42),
		"oldest": "2026-03-24T00:27:48Z",
		"newest": "2026-04-22T02:10:11Z",
	}}
	srv := stubQueueWatcher(t, "tok", rows)
	defer srv.Close()
	tmp := t.TempDir()
	tokPath := writeTokenFile(t, tmp, "tok")

	var out bytes.Buffer
	code := runCsuiteAudit([]string{
		"queue",
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
	if decoded[0]["agent"] != "seth" {
		t.Errorf("agent = %v, want seth", decoded[0]["agent"])
	}
}

// TestCsuiteAuditQueue_FlagsForwardedAsQuery verifies --agent/--scope/
// --stale translate to HTTP query params.
func TestCsuiteAuditQueue_FlagsForwardedAsQuery(t *testing.T) {
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
		"queue",
		"--watcher-url", srv.URL,
		"--token", tokPath,
		"--agent", "seth",
		"--scope", "inbox",
		"--stale", "30m",
		"--format", "json",
	}, &out, io.Discard)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	for _, k := range []string{"agent=seth", "scope=inbox", "stale=30m"} {
		if !strings.Contains(gotQuery, k) {
			t.Errorf("query %q missing %q", gotQuery, k)
		}
	}
}

// TestCsuiteAuditQueue_MissingTokenExits2 verifies the missing-token
// rule applies to the queue subcommand too.
func TestCsuiteAuditQueue_MissingTokenExits2(t *testing.T) {
	srv := stubQueueWatcher(t, "tok", nil)
	defer srv.Close()

	var out, errBuf bytes.Buffer
	code := runCsuiteAudit([]string{
		"queue",
		"--watcher-url", srv.URL,
		"--token", "/nonexistent/path",
	}, &out, &errBuf)
	if code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
	if !strings.Contains(errBuf.String(), "/nonexistent/path") {
		t.Errorf("stderr should mention token path; got %q", errBuf.String())
	}
}

// TestCsuiteAuditQueue_TableRowsRendered verifies the aggregated
// rows appear in the table output.
func TestCsuiteAuditQueue_TableRowsRendered(t *testing.T) {
	rows := []map[string]any{
		{
			"agent":  "seth",
			"scope":  "inbox",
			"count":  float64(1),
			"oldest": "2026-04-22T00:05:00Z",
			"newest": "2026-04-22T00:05:00Z",
		},
		{
			"agent":  "alex",
			"scope":  "quarantine",
			"count":  float64(1),
			"oldest": "2026-04-20T19:00:00Z",
			"newest": "2026-04-20T19:00:00Z",
		},
	}
	srv := stubQueueWatcher(t, "tok", rows)
	defer srv.Close()
	tmp := t.TempDir()
	tokPath := writeTokenFile(t, tmp, "tok")

	var out bytes.Buffer
	code := runCsuiteAudit([]string{
		"queue",
		"--watcher-url", srv.URL,
		"--token", tokPath,
		"--format", "table",
	}, &out, io.Discard)
	if code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	got := out.String()
	if !strings.Contains(got, "seth") || !strings.Contains(got, "inbox") {
		t.Errorf("expected seth/inbox row; got:\n%s", got)
	}
	if !strings.Contains(got, "alex") || !strings.Contains(got, "quarantine") {
		t.Errorf("expected alex/quarantine row; got:\n%s", got)
	}
}
