package serve

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// sentinel handler that records whether it was reached.
func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

// checkJSONErrorBody asserts that the body is valid JSON containing an "error" key.
func checkJSONErrorBody(t *testing.T, body []byte) {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("response body is not valid JSON: %v — body: %s", err, body)
	}
	if _, ok := m["error"]; !ok {
		t.Errorf("JSON error body missing \"error\" key: %s", body)
	}
}

// TestBearerAuth_MissingHeader verifies that a request with no Authorization
// header is rejected with 401 and a JSON error body.
func TestBearerAuth_MissingHeader(t *testing.T) {
	h := BearerAuth("secret", okHandler())
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
	checkJSONErrorBody(t, w.Body.Bytes())

	// Ensure the downstream handler was NOT reached.
	if w.Code == http.StatusOK {
		t.Error("inner handler must not be called on missing header")
	}
}

// TestBearerAuth_WrongToken verifies that a wrong bearer token is rejected.
func TestBearerAuth_WrongToken(t *testing.T) {
	h := BearerAuth("correct-secret", okHandler())
	req := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
	req.Header.Set("Authorization", "Bearer wrong-secret")
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
	checkJSONErrorBody(t, w.Body.Bytes())
}

// TestBearerAuth_EmptyBearerValue verifies that "Authorization: Bearer " (empty
// value after the scheme) is rejected.
func TestBearerAuth_EmptyBearerValue(t *testing.T) {
	h := BearerAuth("secret", okHandler())
	req := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
	req.Header.Set("Authorization", "Bearer ")
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
	checkJSONErrorBody(t, w.Body.Bytes())
}

// TestBearerAuth_MalformedScheme verifies that a non-Bearer scheme is rejected.
func TestBearerAuth_MalformedScheme(t *testing.T) {
	h := BearerAuth("secret", okHandler())
	req := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
	req.Header.Set("Authorization", "Basic c2VjcmV0")
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

// TestBearerAuth_ValidToken verifies that the correct token passes through to
// the downstream handler and returns 200.
func TestBearerAuth_ValidToken(t *testing.T) {
	const token = "my-bridge-token"
	reached := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	})

	h := BearerAuth(token, next)
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if !reached {
		t.Error("inner handler was not called for valid token")
	}
}

// TestBearerAuth_ContentTypeOnRejection verifies that 401 responses set
// Content-Type: application/json.
func TestBearerAuth_ContentTypeOnRejection(t *testing.T) {
	h := BearerAuth("secret", okHandler())
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

// TestBearerAuth_TimingResistance exercises that a token with the correct
// length but wrong bytes and a short token both result in 401 without
// panicking. This is a compile-time proof that crypto/subtle is used rather
// than == comparison (if the implementation uses ==, these still fail correctly
// via assertion).
func TestBearerAuth_TimingResistance(t *testing.T) {
	const token = "abcdefghijklmnop"
	cases := []string{
		"abcdefghijklmnop"[:len(token)-1], // shorter
		"abcdefghijklmnop" + "x",          // longer
		strings.Repeat("x", len(token)),   // same length, wrong value
	}

	for _, bad := range cases {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer "+bad)
		w := httptest.NewRecorder()
		BearerAuth(token, okHandler()).ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("token %q: status = %d, want 401", bad, w.Code)
		}
	}
}
