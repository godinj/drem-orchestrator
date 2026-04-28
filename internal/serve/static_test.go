package serve

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/godinj/drem-orchestrator/internal/testutil"
)

// ---------------------------------------------------------------------------
// PWA index.html
// ---------------------------------------------------------------------------

// TestPWA_IndexHTML verifies that GET / returns the app shell HTML with a
// 200 status and text/html content type.
func TestPWA_IndexHTML(t *testing.T) {
	store := testutil.NewTestStore(t)
	s := New(Config{Token: "tok", Addr: "127.0.0.1:0", Store: store})
	mux := s.buildMux()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("GET / status = %d, want 200", w.Code)
	}

	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/html") {
		t.Errorf("GET / Content-Type = %q, want text/html", ct)
	}

	body := w.Body.String()
	if !strings.Contains(body, "C-Suite") {
		t.Error("GET / body does not contain 'C-Suite'")
	}
	if !strings.Contains(body, "manifest.json") {
		t.Error("GET / body does not contain manifest.json link")
	}
	if !strings.Contains(body, "app.js") {
		t.Error("GET / body does not reference app.js")
	}
}

// TestPWA_IndexNoAuthRequired verifies that PWA static assets are served
// without requiring bearer token authentication.
func TestPWA_IndexNoAuthRequired(t *testing.T) {
	store := testutil.NewTestStore(t)
	s := New(Config{Token: "secret", Addr: "127.0.0.1:0", Store: store})
	mux := s.buildMux()

	// No Authorization header — should still get 200 for static assets.
	paths := []string{"/", "/style.css", "/app.js", "/voice.js", "/manifest.json", "/sw.js"}
	for _, path := range paths {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("GET %s without auth: status = %d, want 200", path, w.Code)
		}
	}
}

// ---------------------------------------------------------------------------
// manifest.json
// ---------------------------------------------------------------------------

// TestPWA_ManifestJSON verifies that /manifest.json returns valid JSON with
// required PWA fields.
func TestPWA_ManifestJSON(t *testing.T) {
	store := testutil.NewTestStore(t)
	s := New(Config{Token: "tok", Addr: "127.0.0.1:0", Store: store})
	mux := s.buildMux()

	req := httptest.NewRequest(http.MethodGet, "/manifest.json", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /manifest.json status = %d, want 200", w.Code)
	}

	var manifest struct {
		Name      string `json:"name"`
		ShortName string `json:"short_name"`
		StartURL  string `json:"start_url"`
		Display   string `json:"display"`
		Icons     []struct {
			Src   string `json:"src"`
			Sizes string `json:"sizes"`
		} `json:"icons"`
	}
	if err := json.NewDecoder(w.Body).Decode(&manifest); err != nil {
		t.Fatalf("manifest.json parse error: %v", err)
	}

	if manifest.Name == "" {
		t.Error("manifest.json missing name")
	}
	if manifest.ShortName == "" {
		t.Error("manifest.json missing short_name")
	}
	if manifest.StartURL != "/" {
		t.Errorf("manifest.json start_url = %q, want '/'", manifest.StartURL)
	}
	if manifest.Display != "standalone" {
		t.Errorf("manifest.json display = %q, want 'standalone'", manifest.Display)
	}
	if len(manifest.Icons) == 0 {
		t.Error("manifest.json has no icons")
	}
}

// ---------------------------------------------------------------------------
// Service worker
// ---------------------------------------------------------------------------

// TestPWA_ServiceWorker verifies that /sw.js returns JavaScript content.
func TestPWA_ServiceWorker(t *testing.T) {
	store := testutil.NewTestStore(t)
	s := New(Config{Token: "tok", Addr: "127.0.0.1:0", Store: store})
	mux := s.buildMux()

	req := httptest.NewRequest(http.MethodGet, "/sw.js", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /sw.js status = %d, want 200", w.Code)
	}

	body := w.Body.String()
	if !strings.Contains(body, "addEventListener") {
		t.Error("GET /sw.js does not look like a service worker (missing addEventListener)")
	}
	if !strings.Contains(body, "install") {
		t.Error("GET /sw.js missing install event handler")
	}
	if !strings.Contains(body, "fetch") {
		t.Error("GET /sw.js missing fetch event handler")
	}
}

// ---------------------------------------------------------------------------
// CSS
// ---------------------------------------------------------------------------

// TestPWA_StyleCSS verifies /style.css returns CSS content.
func TestPWA_StyleCSS(t *testing.T) {
	store := testutil.NewTestStore(t)
	s := New(Config{Token: "tok", Addr: "127.0.0.1:0", Store: store})
	mux := s.buildMux()

	req := httptest.NewRequest(http.MethodGet, "/style.css", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /style.css status = %d, want 200", w.Code)
	}

	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "css") {
		t.Errorf("GET /style.css Content-Type = %q, want text/css", ct)
	}
}

// ---------------------------------------------------------------------------
// App JS
// ---------------------------------------------------------------------------

// TestPWA_AppJS verifies /app.js returns JavaScript.
func TestPWA_AppJS(t *testing.T) {
	store := testutil.NewTestStore(t)
	s := New(Config{Token: "tok", Addr: "127.0.0.1:0", Store: store})
	mux := s.buildMux()

	req := httptest.NewRequest(http.MethodGet, "/app.js", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /app.js status = %d, want 200", w.Code)
	}

	body := w.Body.String()
	if !strings.Contains(body, "serviceWorker") {
		t.Error("GET /app.js does not reference serviceWorker registration")
	}
	if !strings.Contains(body, "csuite_last_seen") {
		t.Error("GET /app.js does not persist local last-seen state")
	}
	if !strings.Contains(body, "refreshAllAgentMessages") {
		t.Error("GET /app.js does not refresh missed messages")
	}
	if !strings.Contains(body, "detectNoAuthMode") {
		t.Error("GET /app.js does not detect disabled auth mode")
	}
}

// ---------------------------------------------------------------------------
// Voice JS
// ---------------------------------------------------------------------------

// TestPWA_VoiceJS verifies /voice.js returns JavaScript with speech controls.
func TestPWA_VoiceJS(t *testing.T) {
	store := testutil.NewTestStore(t)
	s := New(Config{Token: "tok", Addr: "127.0.0.1:0", Store: store})
	mux := s.buildMux()

	req := httptest.NewRequest(http.MethodGet, "/voice.js", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /voice.js status = %d, want 200", w.Code)
	}

	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "javascript") {
		t.Errorf("GET /voice.js Content-Type = %q, want application/javascript", ct)
	}

	body := w.Body.String()
	if !strings.Contains(body, "SpeechRecognition") {
		t.Error("GET /voice.js does not contain SpeechRecognition")
	}
	if !strings.Contains(body, "SpeechSynthesis") {
		t.Error("GET /voice.js does not contain SpeechSynthesis")
	}
}

// ---------------------------------------------------------------------------
// Icons
// ---------------------------------------------------------------------------

// TestPWA_IconsExist verifies that the icon files referenced by manifest.json
// are served correctly.
func TestPWA_IconsExist(t *testing.T) {
	store := testutil.NewTestStore(t)
	s := New(Config{Token: "tok", Addr: "127.0.0.1:0", Store: store})
	mux := s.buildMux()

	paths := []string{"/icons/icon-192.svg", "/icons/icon-512.svg"}
	for _, path := range paths {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("GET %s status = %d, want 200", path, w.Code)
			continue
		}

		body := w.Body.String()
		if !strings.Contains(body, "<svg") {
			t.Errorf("GET %s does not contain SVG content", path)
		}
	}
}

// ---------------------------------------------------------------------------
// API routes still require auth
// ---------------------------------------------------------------------------

// TestPWA_APIRoutesStillRequireAuth verifies that even with the PWA handler
// serving static files at "/", API endpoints still require bearer auth.
func TestPWA_APIRoutesStillRequireAuth(t *testing.T) {
	store := testutil.NewTestStore(t)
	s := New(Config{Token: "secret", Addr: "127.0.0.1:0", Store: store})
	mux := s.buildMux()

	apiPaths := []string{"/api/health", "/api/agents", "/api/messages"}
	for _, path := range apiPaths {
		// Without auth header — should get 401.
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)

		if w.Code != http.StatusUnauthorized {
			t.Errorf("GET %s without auth: status = %d, want 401", path, w.Code)
		}
	}
}

// ---------------------------------------------------------------------------
// Full-stack: serve over real HTTP
// ---------------------------------------------------------------------------

// TestPWA_ServedOverHTTP starts the full server and verifies PWA assets are
// served over real HTTP connections alongside the authenticated API.
func TestPWA_ServedOverHTTP(t *testing.T) {
	store := testutil.NewTestStore(t)
	const token = "test-pwa-token"
	s := New(Config{Token: token, Addr: "127.0.0.1:0", Store: store})

	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Stop()

	base := "http://" + s.ListenAddr()

	// PWA index — no auth.
	resp, err := http.Get(base + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET / status = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(string(body), "C-Suite") {
		t.Error("GET / body missing C-Suite")
	}

	// API health — with auth.
	req, _ := http.NewRequest(http.MethodGet, base+"/api/health", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/health: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("GET /api/health with auth: status = %d, want 200", resp2.StatusCode)
	}

	// API health — without auth.
	resp3, err := http.Get(base + "/api/health")
	if err != nil {
		t.Fatalf("GET /api/health (no auth): %v", err)
	}
	resp3.Body.Close()
	if resp3.StatusCode != http.StatusUnauthorized {
		t.Errorf("GET /api/health without auth: status = %d, want 401", resp3.StatusCode)
	}
}
