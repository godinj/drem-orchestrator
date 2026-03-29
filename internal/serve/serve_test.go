package serve

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/godinj/drem-orchestrator/internal/testutil"
)

// ---------------------------------------------------------------------------
// Server lifecycle
// ---------------------------------------------------------------------------

// TestServer_StartsAndStops verifies that the server starts, binds a real
// listener (ListenAddr returns a non-empty address), responds to a health
// probe, and shuts down cleanly without error.
func TestServer_StartsAndStops(t *testing.T) {
	const token = "test-token"
	store := testutil.NewTestStore(t)
	s := New(Config{Token: token, Addr: "127.0.0.1:0", Store: store})

	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	addr := s.ListenAddr()
	if addr == "" {
		t.Fatal("ListenAddr returned empty string after Start")
	}

	// Verify the server actually handles requests.
	url := fmt.Sprintf("http://%s/api/health", addr)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/health: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("health probe status = %d, want 200", resp.StatusCode)
	}

	if err := s.Stop(); err != nil {
		t.Errorf("Stop: %v", err)
	}

	// After Stop, the server must no longer accept connections.
	_, err = http.Get(fmt.Sprintf("http://%s/api/health", addr))
	if err == nil {
		t.Error("expected connection refused after Stop, but request succeeded")
	}
}

// TestServer_ListenAddrDefault verifies that when Config.Addr is empty, New
// applies the default address (":8080") so Start knows where to bind.
func TestServer_ListenAddrDefault(t *testing.T) {
	store := testutil.NewTestStore(t)
	s := New(Config{Token: "tok", Store: store})

	// The internal cfg.Addr must be defaultAddr after New with empty Addr.
	if s.cfg.Addr != defaultAddr {
		t.Errorf("cfg.Addr = %q, want %q", s.cfg.Addr, defaultAddr)
	}
}

// TestServer_ListenAddrBeforeStart verifies ListenAddr returns empty string
// before Start is called.
func TestServer_ListenAddrBeforeStart(t *testing.T) {
	store := testutil.NewTestStore(t)
	s := New(Config{Token: "tok", Addr: "127.0.0.1:0", Store: store})

	if got := s.ListenAddr(); got != "" {
		t.Errorf("ListenAddr before Start = %q, want \"\"", got)
	}
}

// ---------------------------------------------------------------------------
// Route wiring through server
// ---------------------------------------------------------------------------

// TestServer_HealthRouteRequiresAuth verifies that the /api/health endpoint
// wired by buildMux requires a valid bearer token.
func TestServer_HealthRouteRequiresAuth(t *testing.T) {
	store := testutil.NewTestStore(t)
	s := New(Config{Token: "secret", Addr: "127.0.0.1:0", Store: store})
	mux := s.buildMux()

	cases := []struct {
		name   string
		header string
		want   int
	}{
		{"no header", "", http.StatusUnauthorized},
		{"wrong token", "Bearer wrong", http.StatusUnauthorized},
		{"correct token", "Bearer secret", http.StatusOK},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodGet, "/api/health", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			rw := newStatusRecorder()
			mux.ServeHTTP(rw, req)
			if rw.code != tc.want {
				t.Errorf("status = %d, want %d", rw.code, tc.want)
			}
		})
	}
}

// TestServer_AgentsRouteRequiresAuth verifies /api/agents also enforces auth.
func TestServer_AgentsRouteRequiresAuth(t *testing.T) {
	store := testutil.NewTestStore(t)
	s := New(Config{Token: "secret", Addr: "127.0.0.1:0", Store: store})
	mux := s.buildMux()

	req, _ := http.NewRequest(http.MethodGet, "/api/agents", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	rw := newStatusRecorder()
	mux.ServeHTTP(rw, req)

	if rw.code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rw.code)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// statusRecorder captures just the HTTP status code from a handler.
type statusRecorder struct {
	code    int
	headers http.Header
}

func newStatusRecorder() *statusRecorder {
	return &statusRecorder{code: http.StatusOK, headers: make(http.Header)}
}

func (r *statusRecorder) Header() http.Header         { return r.headers }
func (r *statusRecorder) WriteHeader(code int)        { r.code = code }
func (r *statusRecorder) Write(b []byte) (int, error) { return len(b), nil }

var _ http.ResponseWriter = (*statusRecorder)(nil)
