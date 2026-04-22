package deliver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// seedQueueFile writes a .md file with the given mtime into the
// tempdir-backed /csuite tree so /v1/queue reads can find it.
func seedQueueFile(t *testing.T, agent, scope, name string, mtime time.Time) {
	t.Helper()
	var dir string
	switch scope {
	case "quarantine":
		dir = filepath.Join(csuiteRoot, "quarantine", agent)
	default:
		dir = filepath.Join(csuiteRoot, agent, scope)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("---\nto: "+agent+"\n---\n\nbody\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
}

// useTempCsuiteRoot redirects csuiteRoot to a fresh tempdir for the
// duration of the test. All file ops in these tests go through
// resolveCsuitePath so redirection is cheap.
func useTempCsuiteRoot(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	old := csuiteRoot
	csuiteRoot = tmp
	t.Cleanup(func() { csuiteRoot = old })
	return tmp
}

// TestQueue_MissingBearerReturns401 verifies anonymous /v1/queue
// requests are rejected.
func TestQueue_MissingBearerReturns401(t *testing.T) {
	useTempCsuiteRoot(t)
	h, _ := auditHandler(t, "audit-secret")
	req := httptest.NewRequest(http.MethodGet, "/v1/queue", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

// TestQueue_WrongBearerReturns401 verifies mismatched bearer tokens
// are rejected.
func TestQueue_WrongBearerReturns401(t *testing.T) {
	useTempCsuiteRoot(t)
	h, _ := auditHandler(t, "audit-secret")
	req := httptest.NewRequest(http.MethodGet, "/v1/queue", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

// TestQueue_EmptyConfiguredTokenRejects verifies the fail-closed
// posture: a watcher with no audit token must reject all /v1/queue
// traffic.
func TestQueue_EmptyConfiguredTokenRejects(t *testing.T) {
	useTempCsuiteRoot(t)
	h, _ := auditHandler(t, "")
	req := httptest.NewRequest(http.MethodGet, "/v1/queue", nil)
	req.Header.Set("Authorization", "Bearer anything")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

// TestQueue_WrongMethod verifies non-GET requests are rejected.
func TestQueue_WrongMethod(t *testing.T) {
	useTempCsuiteRoot(t)
	h, _ := auditHandler(t, "audit-secret")
	req := httptest.NewRequest(http.MethodPost, "/v1/queue", nil)
	req.Header.Set("Authorization", "Bearer audit-secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /v1/queue: status = %d, want 405", w.Code)
	}
}

// TestQueue_EmptyTreeReturnsEmptyArray verifies that a fresh /csuite
// tree with no files returns JSON "[]" rather than null.
func TestQueue_EmptyTreeReturnsEmptyArray(t *testing.T) {
	useTempCsuiteRoot(t)
	h, _ := auditHandler(t, "audit-secret")
	req := httptest.NewRequest(http.MethodGet, "/v1/queue", nil)
	req.Header.Set("Authorization", "Bearer audit-secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	got := strings.TrimSpace(w.Body.String())
	if got != "[]" {
		t.Errorf("body = %q, want []", got)
	}
}

// TestQueue_ReturnsSeededRows verifies that inbox/outbox/quarantine
// directories with files become queueRow entries with correct
// counts and timestamp bounds.
func TestQueue_ReturnsSeededRows(t *testing.T) {
	useTempCsuiteRoot(t)

	oldest := time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC)
	middle := time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC)
	newest := time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC)

	seedQueueFile(t, "seth", "inbox", "a.md", oldest)
	seedQueueFile(t, "seth", "inbox", "b.md", newest)
	seedQueueFile(t, "alex", "outbox", "c.md", middle)
	seedQueueFile(t, "alex", "quarantine", "d.md", oldest)

	h, _ := auditHandler(t, "audit-secret")
	req := httptest.NewRequest(http.MethodGet, "/v1/queue", nil)
	req.Header.Set("Authorization", "Bearer audit-secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var rows []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("len = %d, want 3 (seth/inbox, alex/outbox, alex/quarantine)", len(rows))
	}

	// Build a keyed map for stable assertions.
	byKey := map[string]map[string]any{}
	for _, r := range rows {
		byKey[r["agent"].(string)+"/"+r["scope"].(string)] = r
	}
	sethInbox, ok := byKey["seth/inbox"]
	if !ok {
		t.Fatalf("no seth/inbox row in %+v", rows)
	}
	if int(sethInbox["count"].(float64)) != 2 {
		t.Errorf("seth/inbox count = %v, want 2", sethInbox["count"])
	}
	if sethInbox["oldest"] != oldest.Format(time.RFC3339) {
		t.Errorf("seth/inbox oldest = %v, want %s", sethInbox["oldest"], oldest.Format(time.RFC3339))
	}
	if sethInbox["newest"] != newest.Format(time.RFC3339) {
		t.Errorf("seth/inbox newest = %v, want %s", sethInbox["newest"], newest.Format(time.RFC3339))
	}
	alexQuar, ok := byKey["alex/quarantine"]
	if !ok {
		t.Fatalf("no alex/quarantine row in %+v", rows)
	}
	if int(alexQuar["count"].(float64)) != 1 {
		t.Errorf("alex/quarantine count = %v, want 1", alexQuar["count"])
	}
}

// TestQueue_AgentFilter verifies the `agent` query param filters to
// rows for that agent only.
func TestQueue_AgentFilter(t *testing.T) {
	useTempCsuiteRoot(t)
	seedQueueFile(t, "seth", "inbox", "a.md", time.Now())
	seedQueueFile(t, "alex", "inbox", "b.md", time.Now())

	h, _ := auditHandler(t, "audit-secret")
	req := httptest.NewRequest(http.MethodGet, "/v1/queue?agent=seth", nil)
	req.Header.Set("Authorization", "Bearer audit-secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var rows []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("len = %d, want 1", len(rows))
	}
	if rows[0]["agent"] != "seth" {
		t.Errorf("agent = %v, want seth", rows[0]["agent"])
	}
}

// TestQueue_ScopeFilter verifies the `scope` query param filters to
// a single scope (e.g. inbox or quarantine).
func TestQueue_ScopeFilter(t *testing.T) {
	useTempCsuiteRoot(t)
	seedQueueFile(t, "seth", "inbox", "a.md", time.Now())
	seedQueueFile(t, "seth", "outbox", "b.md", time.Now())

	h, _ := auditHandler(t, "audit-secret")
	req := httptest.NewRequest(http.MethodGet, "/v1/queue?scope=outbox", nil)
	req.Header.Set("Authorization", "Bearer audit-secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var rows []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Collect scopes — all should be "outbox".
	scopes := []string{}
	for _, r := range rows {
		scopes = append(scopes, r["scope"].(string))
	}
	sort.Strings(scopes)
	if len(scopes) != 1 || scopes[0] != "outbox" {
		t.Errorf("scopes = %v, want [outbox]", scopes)
	}
}

// TestQueue_StaleFilter verifies the `stale` param filters to files
// older than now - stale (i.e. excludes recent files).
func TestQueue_StaleFilter(t *testing.T) {
	useTempCsuiteRoot(t)
	old := time.Now().Add(-2 * time.Hour)
	recent := time.Now().Add(-5 * time.Minute)
	seedQueueFile(t, "seth", "inbox", "old.md", old)
	seedQueueFile(t, "seth", "inbox", "recent.md", recent)

	h, _ := auditHandler(t, "audit-secret")
	req := httptest.NewRequest(http.MethodGet, "/v1/queue?stale=1h", nil)
	req.Header.Set("Authorization", "Bearer audit-secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var rows []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("len = %d, want 1 (stale 1h = only the old file)", len(rows))
	}
	if int(rows[0]["count"].(float64)) != 1 {
		t.Errorf("count = %v, want 1", rows[0]["count"])
	}
}

// TestQueue_SkipsNonMdFiles verifies non-.md files in the scope dir
// are ignored (the watcher writes only .md).
func TestQueue_SkipsNonMdFiles(t *testing.T) {
	useTempCsuiteRoot(t)
	seedQueueFile(t, "seth", "inbox", "a.md", time.Now())
	// Sneak a .tmp file into the same dir.
	dir := filepath.Join(csuiteRoot, "seth", "inbox")
	if err := os.WriteFile(filepath.Join(dir, "atomic-write.tmp"), []byte("junk"), 0o644); err != nil {
		t.Fatalf("write tmp: %v", err)
	}

	h, _ := auditHandler(t, "audit-secret")
	req := httptest.NewRequest(http.MethodGet, "/v1/queue?agent=seth&scope=inbox", nil)
	req.Header.Set("Authorization", "Bearer audit-secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var rows []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("len = %d, want 1", len(rows))
	}
	if int(rows[0]["count"].(float64)) != 1 {
		t.Errorf("count = %v, want 1 (tmp file must be ignored)", rows[0]["count"])
	}
}

// TestQueue_SkipsSubdirs verifies that outbox/delivered/ (and other
// subdirs) do not contribute to the outbox count. The watcher moves
// delivered files into that subdir; they should not re-appear as
// pending outbox entries.
func TestQueue_SkipsSubdirs(t *testing.T) {
	useTempCsuiteRoot(t)
	seedQueueFile(t, "seth", "outbox", "pending.md", time.Now())
	// Create a delivered/ subdir with a file in it.
	sub := filepath.Join(csuiteRoot, "seth", "outbox", "delivered")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir delivered: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sub, "done.md"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write delivered: %v", err)
	}

	h, _ := auditHandler(t, "audit-secret")
	req := httptest.NewRequest(http.MethodGet, "/v1/queue?agent=seth&scope=outbox", nil)
	req.Header.Set("Authorization", "Bearer audit-secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var rows []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("len = %d, want 1", len(rows))
	}
	if int(rows[0]["count"].(float64)) != 1 {
		t.Errorf("count = %v, want 1 (delivered/ subdir ignored)", rows[0]["count"])
	}
}

// TestQueue_MissingScopeDirNoRow verifies that a scope dir that
// doesn't exist on disk simply contributes no row (not an error).
func TestQueue_MissingScopeDirNoRow(t *testing.T) {
	useTempCsuiteRoot(t)
	// Do not create any dirs.
	h, _ := auditHandler(t, "audit-secret")
	req := httptest.NewRequest(http.MethodGet, "/v1/queue?agent=seth", nil)
	req.Header.Set("Authorization", "Bearer audit-secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	got := strings.TrimSpace(w.Body.String())
	if got != "[]" {
		t.Errorf("body = %q, want []", got)
	}
}
