package deliver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// validBody returns a JSON body that passes schema validation for the
// given source persona. Tests override individual fields by decoding,
// mutating, and re-encoding.
func validBody(t *testing.T, source string) []byte {
	t.Helper()
	req := DeliverRequest{
		SourcePersona: source,
		OutboxPath:    "/csuite/" + source + "/outbox/2026-04-21T153000Z-" + source + "-reply-abc123.md",
		SHA256:        "9f1c9f1c9f1c9f1c9f1c9f1c9f1c9f1c9f1c9f1c9f1c9f1c9f1c9f1c9f1c9f1c",
		EmittedAt:     "2026-04-21T15:30:00Z",
	}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// newHandler builds a handler configured with the given token. Tests
// share this helper so test setup stays uniform.
func newHandler(token string) http.Handler {
	return Handler(Config{Token: token})
}

// post is a small helper that issues a POST /deliver against the
// supplied handler and returns the recorder. The token is attached
// iff non-empty.
func post(t *testing.T, h http.Handler, token string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/deliver", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("X-Csuite-Token", token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// TestHealthz_UnauthenticatedReturns200 verifies that /healthz is
// reachable without the X-Csuite-Token header. Liveness probes must
// never require the shared secret.
func TestHealthz_UnauthenticatedReturns200(t *testing.T) {
	h := newHandler("secret")
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("GET /healthz: status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("GET /healthz: Content-Type = %q, want application/json", ct)
	}
}

// TestHealthz_WrongMethod verifies that non-GET requests to /healthz
// are rejected. This is a small defensive guard against misuse.
func TestHealthz_WrongMethod(t *testing.T) {
	h := newHandler("secret")
	req := httptest.NewRequest(http.MethodPost, "/healthz", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /healthz: status = %d, want 405", w.Code)
	}
}

// TestDeliver_MissingToken verifies the endpoint rejects requests
// without the X-Csuite-Token header.
func TestDeliver_MissingToken(t *testing.T) {
	h := newHandler("secret")
	w := post(t, h, "", validBody(t, "alex"))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

// TestDeliver_WrongToken verifies the endpoint rejects requests with
// a mismatched X-Csuite-Token header.
func TestDeliver_WrongToken(t *testing.T) {
	h := newHandler("secret")
	w := post(t, h, "not-the-secret", validBody(t, "alex"))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

// TestDeliver_EmptyConfiguredTokenRejects verifies the safety rail:
// a watcher with no configured token must NOT accept anonymous
// requests, even when the request omits the header.
func TestDeliver_EmptyConfiguredTokenRejects(t *testing.T) {
	h := newHandler("")
	w := post(t, h, "", validBody(t, "alex"))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("empty-token config: status = %d, want 401", w.Code)
	}
	w2 := post(t, h, "anything", validBody(t, "alex"))
	if w2.Code != http.StatusUnauthorized {
		t.Errorf("empty-token config with client token: status = %d, want 401", w2.Code)
	}
}

// TestDeliver_MalformedJSON verifies the endpoint returns 400 on
// non-JSON bodies.
func TestDeliver_MalformedJSON(t *testing.T) {
	h := newHandler("secret")
	w := post(t, h, "secret", []byte("{not json"))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// TestDeliver_BadSourcePersona verifies the endpoint rejects source
// personas outside the approved set.
func TestDeliver_BadSourcePersona(t *testing.T) {
	h := newHandler("secret")
	body := []byte(`{"source_persona":"unknown","outbox_path":"/csuite/unknown/outbox/x.md","sha256":"9f1c9f1c9f1c9f1c9f1c9f1c9f1c9f1c9f1c9f1c9f1c9f1c9f1c9f1c9f1c9f1c","emitted_at":"2026-04-21T15:30:00Z"}`)
	w := post(t, h, "secret", body)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "source_persona") {
		t.Errorf("response should mention source_persona, got %q", w.Body.String())
	}
}

// TestDeliver_OutboxPathPrefixMismatch verifies the endpoint rejects
// outbox_path values that don't live under the source persona's
// outbox directory.
func TestDeliver_OutboxPathPrefixMismatch(t *testing.T) {
	h := newHandler("secret")
	body := []byte(`{"source_persona":"alex","outbox_path":"/csuite/mike/outbox/x.md","sha256":"9f1c9f1c9f1c9f1c9f1c9f1c9f1c9f1c9f1c9f1c9f1c9f1c9f1c9f1c9f1c9f1c","emitted_at":"2026-04-21T15:30:00Z"}`)
	w := post(t, h, "secret", body)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "/csuite/alex/outbox/") {
		t.Errorf("response should mention expected prefix, got %q", w.Body.String())
	}
}

// TestDeliver_BadSHA256 verifies the endpoint rejects sha256 values
// that aren't 64 hex characters.
func TestDeliver_BadSHA256(t *testing.T) {
	h := newHandler("secret")
	body := []byte(`{"source_persona":"alex","outbox_path":"/csuite/alex/outbox/x.md","sha256":"tooshort","emitted_at":"2026-04-21T15:30:00Z"}`)
	w := post(t, h, "secret", body)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// TestDeliver_BadEmittedAt verifies the endpoint rejects non-RFC3339
// emitted_at values.
func TestDeliver_BadEmittedAt(t *testing.T) {
	h := newHandler("secret")
	body := []byte(`{"source_persona":"alex","outbox_path":"/csuite/alex/outbox/x.md","sha256":"9f1c9f1c9f1c9f1c9f1c9f1c9f1c9f1c9f1c9f1c9f1c9f1c9f1c9f1c9f1c9f1c","emitted_at":"not-a-date"}`)
	w := post(t, h, "secret", body)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// TestDeliver_HappyPathReturns501 verifies that a fully valid request
// (good token + good schema) reaches the unimplemented delivery path
// and returns 501 Not Implemented. This pins the tracer-bullet
// behaviour for commit 1.
func TestDeliver_HappyPathReturns501(t *testing.T) {
	h := newHandler("secret")
	for _, persona := range []string{"mike", "alex", "ross", "seth"} {
		persona := persona
		t.Run(persona, func(t *testing.T) {
			w := post(t, h, "secret", validBody(t, persona))
			if w.Code != http.StatusNotImplemented {
				t.Errorf("persona=%s: status = %d, want 501; body=%q", persona, w.Code, w.Body.String())
			}
		})
	}
}

// TestDeliver_WrongMethod verifies non-POST requests are rejected
// with 405 (after auth — GET against /deliver still requires a token
// to avoid leaking the endpoint's existence, but method mismatch is
// what this test asserts).
func TestDeliver_WrongMethod(t *testing.T) {
	h := newHandler("secret")
	req := httptest.NewRequest(http.MethodGet, "/deliver", nil)
	req.Header.Set("X-Csuite-Token", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /deliver: status = %d, want 405", w.Code)
	}
}
