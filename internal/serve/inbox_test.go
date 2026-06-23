package serve

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/godinj/drem-orchestrator/internal/csuite/diskstore"
	"github.com/google/uuid"
)

func TestInboxQueueRouteRequiresAuth(t *testing.T) {
	store := diskstore.New(t.TempDir())
	mux := New(Config{Token: "secret", Store: store}).buildMux()

	req := httptest.NewRequest(http.MethodGet, "/api/inbox?agent=mike", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestInboxQueueGETListsLiveItems(t *testing.T) {
	root := t.TempDir()
	seedServeInboxFile(t, root, "mike", "inbox", "20260101T000000Z-operator-to-mike-aaaaaaaa.md", "live", "aaaaaaaa")
	seedServeInboxFile(t, root, "mike", filepath.Join("inbox", ".archive"), "20260101T000100Z-operator-to-mike-bbbbbbbb.md", "archived", "bbbbbbbb")
	mux := New(Config{Token: "secret", Store: diskstore.New(root)}).buildMux()

	req := httptest.NewRequest(http.MethodGet, "/api/inbox?agent=mike&limit=50&include_archived=false", nil)
	req.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var items []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &items); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len = %d, want 1", len(items))
	}
	if items[0]["subject"] != "live" || items[0]["filename"] == "" {
		t.Fatalf("unexpected item: %+v", items[0])
	}
}

func TestInboxQueueArchiveAndIgnoreEndpoints(t *testing.T) {
	root := t.TempDir()
	archiveName := "20260101T000000Z-operator-to-mike-aaaaaaaa.md"
	ignoreName := "20260101T000100Z-operator-to-mike-bbbbbbbb.md"
	seedServeInboxFile(t, root, "mike", "inbox", archiveName, "archive me", "aaaaaaaa")
	seedServeInboxFile(t, root, "mike", "inbox", ignoreName, "ignore me", "bbbbbbbb")
	store := diskstore.New(root)
	items, err := store.ListInboxQueue("mike", 0)
	if err != nil {
		t.Fatalf("ListInboxQueue: %v", err)
	}
	ids := map[string]string{}
	for _, item := range items {
		ids[item.Subject] = item.ID.String()
	}
	mux := New(Config{Token: "secret", Store: store}).buildMux()

	postInboxAction(t, mux, "/api/inbox/archive", ids["archive me"], http.StatusOK)
	postInboxAction(t, mux, "/api/inbox/ignore", ids["ignore me"], http.StatusOK)

	if _, err := os.Stat(filepath.Join(root, "mike", "inbox", ".archive", archiveName)); err != nil {
		t.Fatalf("archived file missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "mike", "inbox", ".ignored", ignoreName)); err != nil {
		t.Fatalf("ignored file missing: %v", err)
	}
}

func TestInboxQueueValidationFailures(t *testing.T) {
	mux := New(Config{Token: "secret", Store: diskstore.New(t.TempDir())}).buildMux()

	req := httptest.NewRequest(http.MethodGet, "/api/inbox", nil)
	req.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing agent status = %d, want 400", w.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/inbox?agent=ross", nil)
	req.Header.Set("Authorization", "Bearer secret")
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unknown persona status = %d, want 400", w.Code)
	}

	postInboxAction(t, mux, "/api/inbox/archive", uuid.NewString(), http.StatusNotFound)
}

func TestInboxQueueArchiveCollisionReturnsConflict(t *testing.T) {
	root := t.TempDir()
	name := "20260101T000000Z-operator-to-mike-aaaaaaaa.md"
	seedServeInboxFile(t, root, "mike", "inbox", name, "archive me", "aaaaaaaa")
	seedServeInboxFile(t, root, "mike", filepath.Join("inbox", ".archive"), name, "existing", "aaaaaaaa")
	store := diskstore.New(root)
	items, err := store.ListInboxQueue("mike", 0)
	if err != nil {
		t.Fatalf("ListInboxQueue: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items len = %d, want 1", len(items))
	}
	mux := New(Config{Token: "secret", Store: store}).buildMux()

	postInboxAction(t, mux, "/api/inbox/archive", items[0].ID.String(), http.StatusConflict)
}

func postInboxAction(t *testing.T, mux http.Handler, path, id string, want int) {
	t.Helper()
	body := []byte(`{"agent":"mike","id":"` + id + `","reason":"operator restart review"}`)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != want {
		t.Fatalf("POST %s status = %d, want %d; body: %s", path, w.Code, want, w.Body.String())
	}
}

func seedServeInboxFile(t *testing.T, root, persona, subdir, name, subject, corrid string) {
	t.Helper()
	dir := filepath.Join(root, persona, subdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	body := "---\n" +
		"from: operator\n" +
		"to: " + persona + "\n" +
		"topic: " + subject + "\n" +
		"sent_at: " + time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339) + "\n" +
		"correlation_id: " + corrid + "\n" +
		"---\n\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatalf("write inbox file: %v", err)
	}
}
