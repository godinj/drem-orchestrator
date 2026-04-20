// Package httpserver provides the shared HTTP scaffolding used by the warm
// direct-agent binaries (drem-classifier and drem-planner today; drem-prep
// when it lands). Both roles expose the same pattern: a single authenticated
// POST endpoint, a /healthz that probes a role-specific precondition, and
// /metrics wired to expvar.
//
// Extracting this once keeps the auth middleware, healthz caching, and
// metric naming identical across agents so operators reading logs or
// scraping /metrics don't have to juggle per-binary shapes. See
// plans/warm-planner-pivot.md §3 for the design rationale.
package httpserver

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"expvar"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Config carries the knobs a role binary supplies when standing up its HTTP
// surface. The zero value is NOT valid — at minimum ListenAddr and MetricsNS
// must be set. Everything else is optional.
type Config struct {
	// ListenAddr is the HTTP bind address, e.g. ":8090".
	ListenAddr string
	// BearerToken enables Authorization: Bearer <token> enforcement on the
	// authenticated handlers. Empty disables auth (dev + unit tests).
	BearerToken string
	// HealthProbe is the role-specific precondition check. /healthz returns
	// 200 when this returns nil, 503 otherwise. May be nil; in that case
	// /healthz always reports healthy as long as the binary is up.
	HealthProbe func(context.Context) error
	// MetricsNS is the prefix applied to every exposed expvar counter, e.g.
	// "drem_classifier" or "drem_planner". Must be unique across binaries
	// running in the same process (tests reuse Server instances, so a
	// package-level sync.Once dedupes double-registration).
	MetricsNS string
	// Logger is the slog handler used for error paths. Defaults to
	// slog.Default() when nil.
	Logger *slog.Logger
	// HealthProbeTTL is the minimum interval between HealthProbe calls. Zero
	// defaults to 30s; tests override to force a fresh probe every call.
	HealthProbeTTL time.Duration
}

// Server is the shared framework. One per binary. Handle() wires
// role-specific endpoints; Handler() returns the composed http.Handler that
// the caller can drop into an http.Server.
type Server struct {
	cfg     Config
	mux     *http.ServeMux
	metrics *Metrics

	probeMu    sync.Mutex
	probeErr   error
	probeAt    time.Time
	probeEvery time.Duration
}

// Metrics is the expvar-backed counter set exposed at /metrics. Shared
// across binaries so operator dashboards can re-use the same ingest rules;
// see plans/warm-planner-pivot.md §3 for the full list.
type Metrics struct {
	RequestsOK    *expvar.Int
	Requests4xx   *expvar.Int
	Requests5xx   *expvar.Int
	UpstreamUp    *expvar.Int // 1 healthy, 0 not
	DurationMSSum *expvar.Int
	DurationCount *expvar.Int
}

// metricsRegistry dedupes per-namespace metrics so a test that constructs
// multiple Server instances with the same MetricsNS doesn't trip
// expvar.Publish's "reused var name" panic. The first call wins; the rest
// return the registered pointer.
var (
	metricsMu       sync.Mutex
	metricsRegistry = map[string]*Metrics{}
)

func getMetrics(ns string) *Metrics {
	metricsMu.Lock()
	defer metricsMu.Unlock()
	if m, ok := metricsRegistry[ns]; ok {
		return m
	}
	m := &Metrics{
		RequestsOK:    expvar.NewInt(ns + "_requests_ok"),
		Requests4xx:   expvar.NewInt(ns + "_requests_4xx"),
		Requests5xx:   expvar.NewInt(ns + "_requests_5xx"),
		UpstreamUp:    expvar.NewInt(ns + "_upstream_up"),
		DurationMSSum: expvar.NewInt(ns + "_duration_ms_sum"),
		DurationCount: expvar.NewInt(ns + "_duration_count"),
	}
	metricsRegistry[ns] = m
	return m
}

// New constructs a Server ready to take Handle() registrations. Panics on
// MetricsNS empty because an anonymous expvar collision would be much
// harder to debug downstream.
func New(cfg Config) *Server {
	if cfg.MetricsNS == "" {
		panic("httpserver.New: Config.MetricsNS is required")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	ttl := cfg.HealthProbeTTL
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	s := &Server{
		cfg:        cfg,
		mux:        http.NewServeMux(),
		metrics:    getMetrics(cfg.MetricsNS),
		probeEvery: ttl,
	}
	// Wire the standard endpoints. Role-specific endpoints are attached by
	// the caller via Handle().
	s.mux.HandleFunc("GET /healthz", s.handleHealthz)
	s.mux.Handle("GET /metrics", expvar.Handler())
	return s
}

// Metrics returns the shared counter set so the role binary can bump its
// own role-specific fields (tokens, successful plans, etc.) without
// re-publishing them.
func (s *Server) Metrics() *Metrics {
	return s.metrics
}

// Logger returns the configured slog handler for consistent logging from
// role handlers.
func (s *Server) Logger() *slog.Logger {
	return s.cfg.Logger
}

// SetHealthProbeTTL is used by tests to force /healthz to re-run the probe
// on every request. Not part of the public contract.
func (s *Server) SetHealthProbeTTL(d time.Duration) {
	s.probeEvery = d
}

// Handle registers a handler for the given method+path. The handler is
// wrapped with bearer-token auth when Config.BearerToken is non-empty;
// otherwise it's attached verbatim (dev + unit-test mode).
func (s *Server) Handle(method, path string, h http.HandlerFunc) {
	pattern := method + " " + path
	s.mux.Handle(pattern, s.requireAuth(h))
}

// HandleNoAuth registers a handler without wrapping it in auth middleware.
// Used for health + metrics endpoints that remain public even when a token
// is configured.
func (s *Server) HandleNoAuth(method, path string, h http.HandlerFunc) {
	pattern := method + " " + path
	s.mux.HandleFunc(pattern, h)
}

// Handler returns the composed http.Handler for use with http.Server.
func (s *Server) Handler() http.Handler {
	return s.mux
}

// ---------------------------------------------------------------------------
// Auth
// ---------------------------------------------------------------------------

// requireAuth wraps next with Bearer token validation. Matches internal/serve
// so operators only maintain one shared token shape.
func (s *Server) requireAuth(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.BearerToken == "" {
			next.ServeHTTP(w, r)
			return
		}
		auth := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(auth, prefix) {
			s.WriteError(w, http.StatusUnauthorized, "missing or invalid authorization header")
			return
		}
		got := auth[len(prefix):]
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.cfg.BearerToken)) != 1 {
			s.WriteError(w, http.StatusUnauthorized, "invalid bearer token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------------------
// /healthz
// ---------------------------------------------------------------------------

type healthBody struct {
	OK       bool   `json:"ok"`
	Upstream string `json:"upstream"`
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	ok := s.cachedProbe(r.Context())
	body := healthBody{OK: ok}
	if ok {
		body.Upstream = "reachable"
		s.metrics.UpstreamUp.Set(1)
	} else {
		body.Upstream = "unreachable"
		s.metrics.UpstreamUp.Set(0)
	}
	status := http.StatusOK
	if !ok {
		status = http.StatusServiceUnavailable
	}
	s.WriteJSON(w, status, body)
}

// cachedProbe runs the configured HealthProbe at most once per probeEvery
// interval. Without a probe, reports healthy unconditionally so /healthz
// doesn't falsely red-flag.
func (s *Server) cachedProbe(ctx context.Context) bool {
	if s.cfg.HealthProbe == nil {
		return true
	}
	s.probeMu.Lock()
	defer s.probeMu.Unlock()
	if time.Since(s.probeAt) > s.probeEvery {
		s.probeErr = s.cfg.HealthProbe(ctx)
		s.probeAt = time.Now()
	}
	return s.probeErr == nil
}

// ---------------------------------------------------------------------------
// helpers — shared by callers
// ---------------------------------------------------------------------------

// errorBody is the JSON shape for 4xx/5xx responses.
type errorBody struct {
	Error string `json:"error"`
}

// WriteJSON serialises body at the given HTTP status. Errors are logged
// via the configured logger; nothing surfaces to the client once headers
// have been written.
func (s *Server) WriteJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		s.cfg.Logger.Error("write json body", "error", err)
	}
}

// WriteError writes a JSON error envelope and bumps the appropriate
// requests_{4xx,5xx} counter.
func (s *Server) WriteError(w http.ResponseWriter, status int, msg string) {
	switch {
	case status >= 500:
		s.metrics.Requests5xx.Add(1)
	case status >= 400:
		s.metrics.Requests4xx.Add(1)
	}
	s.WriteJSON(w, status, errorBody{Error: msg})
}

// RecordOK bumps the requests_ok counter plus the duration histogram. Call
// this from role handlers on the happy path.
func (s *Server) RecordOK(duration time.Duration) {
	s.metrics.RequestsOK.Add(1)
	s.metrics.DurationMSSum.Add(duration.Milliseconds())
	s.metrics.DurationCount.Add(1)
}
