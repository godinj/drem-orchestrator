package deliver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// auditHandler builds a handler with a ledger and an audit token so
// tests can exercise the /v1/deliveries endpoint end-to-end.
func auditHandler(t *testing.T, token string) (http.Handler, *Ledger) {
	t.Helper()
	l := openTestLedger(t)
	h := Handler(Config{Token: "deliver-secret", Ledger: l, AuditToken: token})
	return h, l
}

// seedDelivery inserts a Delivery row with the named fields so tests
// can filter deliveries back out of /v1/deliveries.
func seedDelivery(t *testing.T, l *Ledger, sha, from, to string, at time.Time) {
	t.Helper()
	if err := l.Insert(Delivery{
		SHA256:        sha,
		SourcePersona: from,
		Dest:          to,
		SourcePath:    fmt.Sprintf("/csuite/%s/outbox/%s.md", from, sha[:8]),
		DestPath:      fmt.Sprintf("/csuite/%s/inbox/%s-%s.md", to, from, sha[:8]),
		DeliveredAt:   at,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

// TestDeliveries_MissingBearerReturns401 verifies the endpoint rejects
// anonymous requests with no Authorization header.
func TestDeliveries_MissingBearerReturns401(t *testing.T) {
	h, _ := auditHandler(t, "audit-secret")
	req := httptest.NewRequest(http.MethodGet, "/v1/deliveries", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

// TestDeliveries_WrongBearerReturns401 verifies the endpoint rejects
// mismatched bearer tokens.
func TestDeliveries_WrongBearerReturns401(t *testing.T) {
	h, _ := auditHandler(t, "audit-secret")
	req := httptest.NewRequest(http.MethodGet, "/v1/deliveries", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

// TestDeliveries_EmptyConfiguredTokenRejects verifies the safety rail:
// a watcher with no configured audit token must NOT accept any
// request, including ones with an empty bearer.
func TestDeliveries_EmptyConfiguredTokenRejects(t *testing.T) {
	h, _ := auditHandler(t, "")
	req := httptest.NewRequest(http.MethodGet, "/v1/deliveries", nil)
	req.Header.Set("Authorization", "Bearer anything")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("empty-token config: status = %d, want 401", w.Code)
	}
}

// TestDeliveries_WrongMethod verifies non-GET requests are rejected.
func TestDeliveries_WrongMethod(t *testing.T) {
	h, _ := auditHandler(t, "audit-secret")
	req := httptest.NewRequest(http.MethodPost, "/v1/deliveries", nil)
	req.Header.Set("Authorization", "Bearer audit-secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /v1/deliveries: status = %d, want 405", w.Code)
	}
}

// TestDeliveries_EmptyLedgerReturnsEmptyArray verifies the endpoint
// returns a JSON `[]` (not null) when the ledger is empty.
func TestDeliveries_EmptyLedgerReturnsEmptyArray(t *testing.T) {
	h, _ := auditHandler(t, "audit-secret")
	req := httptest.NewRequest(http.MethodGet, "/v1/deliveries", nil)
	req.Header.Set("Authorization", "Bearer audit-secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	got := strings.TrimSpace(w.Body.String())
	if got != "[]" {
		t.Errorf("empty ledger body = %q, want []", got)
	}
}

// TestDeliveries_ReturnsSeededRows verifies that inserted ledger rows
// appear in the response with the documented field shape.
func TestDeliveries_ReturnsSeededRows(t *testing.T) {
	h, l := auditHandler(t, "audit-secret")
	seedDelivery(t, l, "aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11",
		"kyle", "seth", time.Date(2026, 4, 22, 0, 5, 0, 0, time.UTC))
	seedDelivery(t, l, "bb22bb22bb22bb22bb22bb22bb22bb22bb22bb22bb22bb22bb22bb22bb22bb22",
		"seth", "kyle", time.Date(2026, 4, 21, 23, 57, 0, 0, time.UTC))

	req := httptest.NewRequest(http.MethodGet, "/v1/deliveries", nil)
	req.Header.Set("Authorization", "Bearer audit-secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}

	var out []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("len(out) = %d, want 2", len(out))
	}

	// Rows should be ordered newest-first by delivered_at.
	first := out[0]
	if first["id"] != "aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11" {
		t.Errorf("first id = %v, want sha of newer row", first["id"])
	}
	if first["from"] != "kyle" {
		t.Errorf("first from = %v, want kyle", first["from"])
	}
	if first["to"] != "seth" {
		t.Errorf("first to = %v, want seth", first["to"])
	}
	if first["status"] != "delivered" {
		t.Errorf("first status = %v, want delivered", first["status"])
	}
	if _, ok := first["delivered_at"]; !ok {
		t.Errorf("first row missing delivered_at field")
	}
	if _, ok := first["filename"]; !ok {
		t.Errorf("first row missing filename field")
	}
}

// TestDeliveries_QuarantineStatus verifies rows with Dest ==
// ClassQuarantine are reported with status "quarantined".
func TestDeliveries_QuarantineStatus(t *testing.T) {
	h, l := auditHandler(t, "audit-secret")
	if err := l.Insert(Delivery{
		SHA256:        "cc33cc33cc33cc33cc33cc33cc33cc33cc33cc33cc33cc33cc33cc33cc33cc33",
		SourcePersona: "alex",
		Dest:          ClassQuarantine,
		SourcePath:    "/csuite/alex/outbox/bad.md",
		DestPath:      "/csuite/quarantine/alex/bad.md",
		DeliveredAt:   time.Now().UTC(),
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/deliveries", nil)
	req.Header.Set("Authorization", "Bearer audit-secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var out []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("len = %d, want 1", len(out))
	}
	if out[0]["status"] != "quarantined" {
		t.Errorf("status = %v, want quarantined", out[0]["status"])
	}
}

// TestDeliveries_FilterFrom verifies the `from` query param filters to
// rows whose SourcePersona matches.
func TestDeliveries_FilterFrom(t *testing.T) {
	h, l := auditHandler(t, "audit-secret")
	now := time.Now().UTC()
	seedDelivery(t, l, "dd44dd44dd44dd44dd44dd44dd44dd44dd44dd44dd44dd44dd44dd44dd44dd44",
		"kyle", "seth", now)
	seedDelivery(t, l, "ee55ee55ee55ee55ee55ee55ee55ee55ee55ee55ee55ee55ee55ee55ee55ee55",
		"seth", "kyle", now.Add(-time.Hour))

	req := httptest.NewRequest(http.MethodGet, "/v1/deliveries?from=kyle", nil)
	req.Header.Set("Authorization", "Bearer audit-secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var out []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("len = %d, want 1", len(out))
	}
	if out[0]["from"] != "kyle" {
		t.Errorf("from = %v, want kyle", out[0]["from"])
	}
}

// TestDeliveries_FilterTo verifies the `to` query param filters by
// destination.
func TestDeliveries_FilterTo(t *testing.T) {
	h, l := auditHandler(t, "audit-secret")
	now := time.Now().UTC()
	seedDelivery(t, l, "ff66ff66ff66ff66ff66ff66ff66ff66ff66ff66ff66ff66ff66ff66ff66ff66",
		"kyle", "seth", now)
	seedDelivery(t, l, "aa77aa77aa77aa77aa77aa77aa77aa77aa77aa77aa77aa77aa77aa77aa77aa77",
		"seth", "kyle", now.Add(-time.Hour))

	req := httptest.NewRequest(http.MethodGet, "/v1/deliveries?to=kyle", nil)
	req.Header.Set("Authorization", "Bearer audit-secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var out []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("len = %d, want 1", len(out))
	}
	if out[0]["to"] != "kyle" {
		t.Errorf("to = %v, want kyle", out[0]["to"])
	}
}

// TestDeliveries_FilterStatus verifies the `status` query param
// filters to delivered or quarantined rows.
func TestDeliveries_FilterStatus(t *testing.T) {
	h, l := auditHandler(t, "audit-secret")
	now := time.Now().UTC()
	seedDelivery(t, l, "1111111111111111111111111111111111111111111111111111111111111111",
		"kyle", "seth", now)
	if err := l.Insert(Delivery{
		SHA256:        "2222222222222222222222222222222222222222222222222222222222222222",
		SourcePersona: "alex",
		Dest:          ClassQuarantine,
		SourcePath:    "/csuite/alex/outbox/bad.md",
		DestPath:      "/csuite/quarantine/alex/bad.md",
		DeliveredAt:   now.Add(-time.Hour),
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/deliveries?status=quarantined", nil)
	req.Header.Set("Authorization", "Bearer audit-secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var out []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("len = %d, want 1", len(out))
	}
	if out[0]["status"] != "quarantined" {
		t.Errorf("status = %v, want quarantined", out[0]["status"])
	}
}

// TestDeliveries_FilterSinceDuration verifies the `since` query param
// accepts a duration like "1h" and filters to rows newer than (now -
// duration).
func TestDeliveries_FilterSinceDuration(t *testing.T) {
	h, l := auditHandler(t, "audit-secret")
	now := time.Now().UTC()
	seedDelivery(t, l, "3333333333333333333333333333333333333333333333333333333333333333",
		"kyle", "seth", now.Add(-10*time.Minute))
	seedDelivery(t, l, "4444444444444444444444444444444444444444444444444444444444444444",
		"kyle", "seth", now.Add(-5*time.Hour))

	req := httptest.NewRequest(http.MethodGet, "/v1/deliveries?since=1h", nil)
	req.Header.Set("Authorization", "Bearer audit-secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var out []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("len = %d, want 1 (only the recent row within 1h)", len(out))
	}
}

// TestDeliveries_LimitDefaultAndCap verifies the limit defaults to 50
// and is capped at 500.
func TestDeliveries_LimitDefaultAndCap(t *testing.T) {
	h, l := auditHandler(t, "audit-secret")
	// Seed 60 rows so the default-50 limit can be observed.
	base := time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 60; i++ {
		sha := fmt.Sprintf("%064d", i)
		seedDelivery(t, l, sha, "kyle", "seth", base.Add(time.Duration(i)*time.Minute))
	}

	// Default limit == 50.
	req := httptest.NewRequest(http.MethodGet, "/v1/deliveries", nil)
	req.Header.Set("Authorization", "Bearer audit-secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var out []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 50 {
		t.Errorf("default limit: got %d, want 50", len(out))
	}

	// Explicit limit of 10.
	req = httptest.NewRequest(http.MethodGet, "/v1/deliveries?limit=10", nil)
	req.Header.Set("Authorization", "Bearer audit-secret")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	var out2 []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out2); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out2) != 10 {
		t.Errorf("limit=10: got %d, want 10", len(out2))
	}

	// Over-cap clamps to 500 (we only have 60 rows so expect 60).
	req = httptest.NewRequest(http.MethodGet, "/v1/deliveries?limit=9999", nil)
	req.Header.Set("Authorization", "Bearer audit-secret")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	var out3 []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out3); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out3) != 60 {
		t.Errorf("limit=9999 (60 rows exist): got %d, want 60", len(out3))
	}
}

// TestDeliveries_Offset verifies that offset skips rows.
func TestDeliveries_Offset(t *testing.T) {
	h, l := auditHandler(t, "audit-secret")
	base := time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		sha := fmt.Sprintf("%064d", i)
		seedDelivery(t, l, sha, "kyle", "seth", base.Add(time.Duration(i)*time.Minute))
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/deliveries?offset=2&limit=2", nil)
	req.Header.Set("Authorization", "Bearer audit-secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var out []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 2 {
		t.Errorf("offset=2&limit=2: got %d, want 2", len(out))
	}
}

// TestDeliveries_FrontmatterFieldsPopulated verifies that type,
// priority, subject, and tldr are extracted from the delivered file's
// frontmatter when available.
func TestDeliveries_FrontmatterFieldsPopulated(t *testing.T) {
	// Redirect csuiteRoot to a tempdir so the delivered file path resolves.
	tmp := t.TempDir()
	oldRoot := csuiteRoot
	csuiteRoot = tmp
	t.Cleanup(func() { csuiteRoot = oldRoot })

	// Write a delivered file with frontmatter.
	destDir := filepath.Join(tmp, "seth", "inbox")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	fm := `---
from: kyle
to: seth
type: request
priority: high
subject: Full spec for csuite audit CLI
tldr: Please draft the v1 + v2 spec.
---

Body text here.
`
	destPath := filepath.Join(destDir, "msg.md")
	if err := os.WriteFile(destPath, []byte(fm), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	h, l := auditHandler(t, "audit-secret")
	if err := l.Insert(Delivery{
		SHA256:        "5555555555555555555555555555555555555555555555555555555555555555",
		SourcePersona: "kyle",
		Dest:          "seth",
		SourcePath:    "/csuite/kyle/outbox/msg.md",
		DestPath:      "/csuite/seth/inbox/msg.md",
		DeliveredAt:   time.Now().UTC(),
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/deliveries", nil)
	req.Header.Set("Authorization", "Bearer audit-secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var out []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("len = %d, want 1", len(out))
	}
	if out[0]["type"] != "request" {
		t.Errorf("type = %v, want request", out[0]["type"])
	}
	if out[0]["priority"] != "high" {
		t.Errorf("priority = %v, want high", out[0]["priority"])
	}
	if out[0]["subject"] != "Full spec for csuite audit CLI" {
		t.Errorf("subject = %v", out[0]["subject"])
	}
	if out[0]["filename"] != "msg.md" {
		t.Errorf("filename = %v, want msg.md", out[0]["filename"])
	}
}
