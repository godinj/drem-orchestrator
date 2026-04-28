// Package orchhttp exposes the orchestrator's public HTTP API
// (consumed by Kyle, the TUI, and dremctl) and the internal ingestion endpoint
// (consumed by agentmon). The orchestrator remains the sole writer to the
// project's SQLite database — this package is the only network surface
// through which external agents and reporting tools read state.
//
// Mutating public endpoints are intentionally narrow lifecycle/comment
// operations handled in-process by the orchestrator. No WebSocket/SSE surface
// is exposed; log follow is implemented with chunked HTTP only.
package orchhttp

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/logging"
)

// LogStreamer is the minimal interface Server needs to proxy
// `docker logs` for GET /logs. The real production implementation is
// satisfied by internal/container.Runtime; tests inject a fake that
// returns a deterministic in-memory reader so no Docker daemon is
// required.
type LogStreamer interface {
	StreamLogs(ctx context.Context, containerID string) (io.ReadCloser, error)
}

// GateOrchestrator is the minimum orchestrator surface the gate mutation
// endpoints (POST /approve, /reject, /pass, /fail, /answer) need. It is
// defined here — at the consumption site — per the "interfaces at the
// consumption site" constitution rule, so this package does not import
// internal/orchestrator or internal/tui just to name a type.
//
// *orchestrator.Orchestrator satisfies this interface in production; tests
// inject a fake that records method calls and optionally returns a scripted
// error so every branch of the handler can be exercised without a real
// orchestrator.
type GateOrchestrator interface {
	HandlePlanApproved(taskID uuid.UUID) error
	HandlePlanRejected(taskID uuid.UUID) error
	HandleTestReviewApproved(taskID uuid.UUID) error
	HandleTestReviewRejected(taskID uuid.UUID, feedback string) error
	HandleTestPassed(taskID uuid.UUID) error
	HandleTestFailed(taskID uuid.UUID) error
	HandleClarificationAnswer(taskID uuid.UUID, answer string) error
	// RetryTask transitions a task in StatusFailed back to StatusBacklog so
	// the scheduler can redispatch it. Used by POST /tasks/{id}/retry and
	// the TUI's retry action. Returns an error if the task is not in
	// StatusFailed or is missing.
	RetryTask(taskID uuid.UUID) error
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
//
// Orch is the optional gate-mutation hook. When set, POST /approve,
// /reject, /pass, /fail, /answer delegate to it; when nil those endpoints
// return 503 so a read-only dev setup does not have to wire one. The
// containerized production server sets Orch at construction time so the
// single in-process orchestrator is the sole writer to the project DB —
// closing the pre-containerization escape hatch where `drem cli approve`
// spawned a second orchestrator inside a host process.
type Server struct {
	DB          *gorm.DB
	SharedToken string
	DockerLogs  LogStreamer
	Project     ProjectInfo
	Orch        GateOrchestrator
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

	// Public project endpoints.
	mux.HandleFunc("GET /projects", s.handleListProjects)
	mux.HandleFunc("GET /projects/{name}/tasks", s.handleListTasks)
	mux.HandleFunc("POST /projects/{name}/tasks", s.handleCreateTask)
	mux.HandleFunc("GET /projects/{name}/workers", s.handleListWorkers)
	mux.HandleFunc("GET /workers/{id}", s.handleGetWorker)
	mux.HandleFunc("GET /workers/{id}/history", s.handleWorkerHistory)
	mux.HandleFunc("GET /events", s.handleListEvents)
	mux.HandleFunc("GET /logs", s.handleLogs)

	// Gate mutation endpoints — delegate to the in-process orchestrator so
	// the container remains the sole writer to the project DB.
	mux.HandleFunc("POST /projects/{name}/tasks/{id}/approve", s.handleApproveTask)
	mux.HandleFunc("POST /projects/{name}/tasks/{id}/reject", s.handleRejectTask)
	mux.HandleFunc("POST /projects/{name}/tasks/{id}/pass", s.handlePassTask)
	mux.HandleFunc("POST /projects/{name}/tasks/{id}/fail", s.handleFailTask)
	mux.HandleFunc("POST /projects/{name}/tasks/{id}/answer", s.handleAnswerTask)
	mux.HandleFunc("POST /projects/{name}/tasks/{id}/retry", s.handleRetryTask)
	mux.HandleFunc("POST /projects/{name}/tasks/{id}/archive", s.handleArchiveTask)
	mux.HandleFunc("POST /projects/{name}/tasks/{id}/comments", s.handleCommentTask)

	// Internal ingestion endpoint — protected by header auth.
	mux.Handle("POST /internal/logs", s.requireAgentmonToken(http.HandlerFunc(s.handleIngest)))

	// Middleware stack (outermost first):
	//   recoverPanics -> logRequests -> WithLoadShedding -> mux
	// WithLoadShedding sits inside the logger so shed 503s still get
	// logged with path + duration, but outside the mux so an overflow
	// never reaches a handler goroutine. See internal/orchhttp/middleware.go
	// for the per-endpoint /tasks cap and the global cap (Bug E W1).
	return recoverPanics(logRequests(WithLoadShedding(mux)))
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

// requestLogSampler gates the per-request "orchhttp request" log line
// so a retry storm cannot reproduce the 2026-04-21 drem.log blow-up
// (1406 lines per 200 KB tail at 495 req/s). EveryN(32) keeps enough
// signal for routine triage without letting a bad client turn the log
// file into a DoS vector. Bug E W4.1. See internal/logging/sampler.go.
//
// The sampler is package-level so every Server shares admission state —
// tests that spin up multiple servers do not reset the counter, which
// matches the production shape (one orch process, one log file).
var requestLogSampler = logging.NewSampler(logging.EveryN(32))

// logRequests wraps an http.Handler and logs method, path, status, and
// duration at info level. A statusRecorder is used so the status code is
// observable post-hoc; the default http.ResponseWriter does not expose it.
// Emissions are sampled per site-tag via requestLogSampler; suppressed
// requests still execute their handler but skip the log line.
func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		if !requestLogSampler.Allow(requestLogSite(r)) {
			return
		}
		slog.Info("orchhttp request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}

// LogRequestsForTest exposes logRequests to external test packages so
// the W4.1 apply-site regression can exercise the real middleware
// wiring without standing up a full Server. Production code must use
// Server.Routes instead.
func LogRequestsForTest(next http.Handler) http.Handler { return logRequests(next) }

// requestLogSite maps a request to the sampler site-tag. Method + path
// means /tasks floods share one counter (the site we most want to
// sample), while /workers and /events each get their own budget. Query
// strings are excluded so ?status=backlog and ?status=done roll up to
// the same site — we do not want ten thousand "statuses" in the
// sync.Map.
func requestLogSite(r *http.Request) string {
	return r.Method + " " + r.URL.Path
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
