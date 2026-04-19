// Package orchhttp exposes the orchestrator's read-only public HTTP API
// (consumed by Kyle and the TUI) and the internal ingestion endpoint
// (consumed by agentmon). The orchestrator remains the sole writer to the
// project's SQLite database — this package is the only network surface
// through which external agents and reporting tools read state.
//
// Per docs/prd-containerization.md, the public endpoints are read-only;
// the single write endpoint is POST /internal/logs, authenticated via a
// per-project shared token. No WebSocket/SSE surface is exposed; log
// follow is implemented with chunked HTTP only.
package orchhttp

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"gorm.io/gorm"
)

// LogStreamer is the minimal interface Server needs to proxy
// `docker logs` for GET /logs. The real production implementation is
// satisfied by internal/container.Runtime; tests inject a fake that
// returns a deterministic in-memory reader so no Docker daemon is
// required.
type LogStreamer interface {
	StreamLogs(ctx context.Context, containerID string) (io.ReadCloser, error)
}

// ProjectInfo is the static description of the single project this
// orchestrator instance serves. It is supplied at Server construction
// time from drem.toml so the /projects endpoint can return Name,
// Language, and OrchURL without hitting the database.
type ProjectInfo struct {
	Name     string
	Language string
	OrchURL  string
}

// Server bundles everything the HTTP handlers need: a read-only GORM
// handle, the per-project shared token that guards /internal/*, a log
// streamer for GET /logs, and the project's static identity. Server is
// safe for concurrent use — handlers only perform reads (plus a single
// transactional write in the ingest handler) via the *gorm.DB which is
// already goroutine-safe.
type Server struct {
	DB          *gorm.DB
	SharedToken string
	DockerLogs  LogStreamer
	Project     ProjectInfo
}

// New constructs a Server. A nil DockerLogs is permitted when the caller
// does not plan to expose GET /logs (for example in unit tests that only
// exercise the database-backed endpoints). SharedToken may be empty only
// when /internal/* routes are not expected to be hit; the middleware
// always requires a non-empty header match, so an empty SharedToken
// effectively disables ingestion.
func New(db *gorm.DB, token string, logs LogStreamer, project ProjectInfo) *Server {
	return &Server{
		DB:          db,
		SharedToken: token,
		DockerLogs:  logs,
		Project:     project,
	}
}

// Routes returns an http.Handler that wires every endpoint this package
// exposes. A fresh ServeMux is returned per call, so callers may mount it
// standalone or behind their own router.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	// Public read-only endpoints.
	mux.HandleFunc("GET /projects", s.handleListProjects)
	mux.HandleFunc("GET /projects/{name}/tasks", s.handleListTasks)
	mux.HandleFunc("GET /projects/{name}/workers", s.handleListWorkers)
	mux.HandleFunc("GET /workers/{id}", s.handleGetWorker)
	mux.HandleFunc("GET /workers/{id}/history", s.handleWorkerHistory)
	mux.HandleFunc("GET /events", s.handleListEvents)
	mux.HandleFunc("GET /logs", s.handleLogs)

	// Internal ingestion endpoint — protected by header auth.
	mux.Handle("POST /internal/logs", s.requireAgentmonToken(http.HandlerFunc(s.handleIngest)))

	return recoverPanics(logRequests(mux))
}

// agentmonTokenHeader is the canonical HTTP header that agentmon sends on
// every POST /internal/logs request. The value is compared for exact
// byte-wise equality against Server.SharedToken.
const agentmonTokenHeader = "X-Drem-Agentmon-Token"

// requireAgentmonToken is a middleware that returns 401 Unauthorized for
// any request whose X-Drem-Agentmon-Token header does not match the
// configured SharedToken. An empty SharedToken rejects every request, so
// that an operator who forgets to configure ingestion cannot accidentally
// expose an unauthenticated write surface.
func (s *Server) requireAgentmonToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get(agentmonTokenHeader)
		if s.SharedToken == "" || got != s.SharedToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// logRequests wraps an http.Handler and logs method, path, status, and
// duration at info level. A statusRecorder is used so the status code is
// observable post-hoc; the default http.ResponseWriter does not expose it.
func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		slog.Info("orchhttp request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}

// recoverPanics is the outermost middleware: it converts a panic in any
// downstream handler into a 500 response and an error-level log entry,
// preventing one bad request from taking down the server goroutine.
func recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("orchhttp panic",
					"err", rec,
					"path", r.URL.Path,
					"stack", string(debug.Stack()),
				)
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// statusRecorder captures the response status so logRequests can report
// it. It otherwise passes through to the underlying ResponseWriter.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

// WriteHeader records the status code before delegating to the wrapped
// writer. Only the first call is honoured; subsequent calls overwrite but
// that matches the underlying writer's idempotent behaviour.
func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}
