// Package main hosts the drem-classifier HTTP server. See cmd/drem-classifier
// for the binary entry point; server.go is split out so handlers can be
// exercised by unit tests without bringing up signal handling or gq probes.
package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"expvar"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/agent"
)

// ---------------------------------------------------------------------------
// JSON shapes (documented in plans/warm-direct-classifier.md §4)
// ---------------------------------------------------------------------------

// classifyRequest is the POST /classify body sent by orch.
type classifyRequest struct {
	TaskID      string         `json:"task_id"`
	Title       string         `json:"title"`
	Description string         `json:"description"`
	Context     map[string]any `json:"context,omitempty"`
}

// classifyResponse is the 200 body returned to orch. Fields mirror the
// agent.Classify telemetry plus the classifier decision fields so orch can
// persist without round-tripping through a file.
type classifyResponse struct {
	TaskID          string   `json:"task_id"`
	Category        string   `json:"category,omitempty"`
	ComplexityScore int      `json:"complexity_score,omitempty"`
	Title           string   `json:"title,omitempty"`
	Description     string   `json:"description,omitempty"`
	TargetFiles     []string `json:"target_files,omitempty"`
	Rationale       string   `json:"rationale,omitempty"`

	// Clarification pathway — mutually exclusive with the fields above.
	NeedsClarification bool     `json:"needs_clarification,omitempty"`
	Questions          []string `json:"questions,omitempty"`

	// Telemetry passed through from the LLM call.
	TokensIn   int `json:"tokens_in"`
	TokensOut  int `json:"tokens_out"`
	DurationMS int `json:"duration_ms"`
}

// errorBody is the JSON error shape returned for 4xx/5xx responses.
type errorBody struct {
	Error string `json:"error"`
}

// ---------------------------------------------------------------------------
// Server
// ---------------------------------------------------------------------------

// Deps are the plug-in collaborators the server needs. Keeping them on the
// Server struct lets tests stub the LLM and the upstream probe without a live
// sglang.
type Deps struct {
	// Classify is the classifier entry point — normally agent.Classify.
	Classify func(ctx context.Context, cfg agent.DirectClassifierConfig, in agent.ClassifyInput) (*agent.ClassifyResult, error)

	// ProbeUpstream returns nil when the upstream (gq/sglang) is reachable.
	// Called by /healthz. When nil, /healthz reports healthy as long as the
	// binary is running.
	ProbeUpstream func(ctx context.Context) error
}

// Server wires the HTTP surface declared in plans/warm-direct-classifier.md §4.
type Server struct {
	Cfg    agent.DirectClassifierConfig
	Token  string // Authorization: Bearer <token>; empty disables auth (tests/dev)
	Deps   Deps
	Logger *slog.Logger

	// Metrics exposed via /metrics. expvar-based so we don't pull a prometheus
	// dependency into a tiny binary; the shape matches plans §4 closely enough
	// that a scraper can re-label on ingest.
	metrics *serverMetrics

	// Cached upstream-probe result, refreshed by /healthz on a short TTL.
	probeMu    sync.Mutex
	probeErr   error
	probeAt    time.Time
	probeEvery time.Duration
}

type serverMetrics struct {
	requestsOK    *expvar.Int
	requests4xx   *expvar.Int
	requests5xx   *expvar.Int
	tokensIn      *expvar.Int
	tokensOut     *expvar.Int
	upstreamUp    *expvar.Int // 1 healthy, 0 not
	durationMSSum *expvar.Int
	durationCount *expvar.Int
}

// registerMetrics publishes the classifier metrics under unique expvar names.
// Tests create fresh Servers per-case; to keep expvar.Publish from double-
// registering we use a package-scoped singleton that atomically stores the
// first *serverMetrics and returns that on subsequent calls.
var (
	metricsOnce sync.Once
	metricsSet  atomic.Pointer[serverMetrics]
)

func getMetrics() *serverMetrics {
	metricsOnce.Do(func() {
		m := &serverMetrics{
			requestsOK:    expvar.NewInt("drem_classifier_requests_ok"),
			requests4xx:   expvar.NewInt("drem_classifier_requests_4xx"),
			requests5xx:   expvar.NewInt("drem_classifier_requests_5xx"),
			tokensIn:      expvar.NewInt("drem_classifier_llm_tokens_in_total"),
			tokensOut:     expvar.NewInt("drem_classifier_llm_tokens_out_total"),
			upstreamUp:    expvar.NewInt("drem_classifier_upstream_up"),
			durationMSSum: expvar.NewInt("drem_classifier_duration_ms_sum"),
			durationCount: expvar.NewInt("drem_classifier_duration_count"),
		}
		metricsSet.Store(m)
	})
	return metricsSet.Load()
}

// NewServer returns a Server ready to Handler(). Deps.Classify defaults to
// agent.Classify when the caller leaves it nil.
func NewServer(cfg agent.DirectClassifierConfig, token string, deps Deps, logger *slog.Logger) *Server {
	if deps.Classify == nil {
		deps.Classify = agent.Classify
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{
		Cfg:        cfg,
		Token:      token,
		Deps:       deps,
		Logger:     logger,
		metrics:    getMetrics(),
		probeEvery: 30 * time.Second,
	}
}

// Handler returns an http.Handler that wires every route declared in
// plans/warm-direct-classifier.md §4.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("POST /classify", s.requireAuth(http.HandlerFunc(s.handleClassify)))
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	// /metrics is expvar so it's publicly scrape-able; no auth wrapper so
	// Prometheus doesn't need the shared token.
	mux.Handle("GET /metrics", expvar.Handler())
	return mux
}

// ---------------------------------------------------------------------------
// Auth
// ---------------------------------------------------------------------------

// requireAuth wraps next with Bearer token validation. Matches internal/serve
// so the operator only maintains one shared token shape.
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.Token == "" {
			// No token configured — allow all (dev + unit tests).
			next.ServeHTTP(w, r)
			return
		}
		auth := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(auth, prefix) {
			s.writeError(w, http.StatusUnauthorized, "missing or invalid authorization header")
			return
		}
		got := auth[len(prefix):]
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.Token)) != 1 {
			s.writeError(w, http.StatusUnauthorized, "invalid bearer token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------------------
// POST /classify
// ---------------------------------------------------------------------------

func (s *Server) handleClassify(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	body, err := io.ReadAll(io.LimitReader(r.Body, 256*1024))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, fmt.Sprintf("read body: %v", err))
		return
	}

	var req classifyRequest
	if err := json.Unmarshal(body, &req); err != nil {
		s.writeError(w, http.StatusBadRequest, fmt.Sprintf("decode body: %v", err))
		return
	}
	if req.TaskID == "" {
		s.writeError(w, http.StatusBadRequest, "task_id is required")
		return
	}
	if req.Title == "" {
		s.writeError(w, http.StatusBadRequest, "title is required")
		return
	}

	taskID, err := uuid.Parse(req.TaskID)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, fmt.Sprintf("task_id is not a valid uuid: %v", err))
		return
	}

	result, err := s.Deps.Classify(r.Context(), s.Cfg, agent.ClassifyInput{
		TaskID:      taskID,
		Title:       req.Title,
		Description: req.Description,
		Context:     req.Context,
	})
	if err != nil {
		s.writeClassifierError(w, err)
		return
	}

	// Parse the classifier JSON into the typed response so orch doesn't have
	// to re-parse on the other end. Unknown fields are preserved via the
	// structured classifyResponse shape.
	var parsed struct {
		Category           string   `json:"category"`
		ComplexityScore    int      `json:"complexity_score"`
		Title              string   `json:"title"`
		Description        string   `json:"description"`
		TargetFiles        []string `json:"target_files"`
		Rationale          string   `json:"rationale"`
		NeedsClarification bool     `json:"needs_clarification"`
		Questions          []string `json:"questions"`
	}
	if err := json.Unmarshal(result.JSON, &parsed); err != nil {
		// Classify itself validates JSON, so a failure here is a bug, not
		// upstream error; return 500 so the orch retry loop surfaces it.
		s.writeError(w, http.StatusInternalServerError, fmt.Sprintf("parse classifier output: %v", err))
		return
	}

	resp := classifyResponse{
		TaskID:             req.TaskID,
		Category:           parsed.Category,
		ComplexityScore:    parsed.ComplexityScore,
		Title:              parsed.Title,
		Description:        parsed.Description,
		TargetFiles:        parsed.TargetFiles,
		Rationale:          parsed.Rationale,
		NeedsClarification: parsed.NeedsClarification,
		Questions:          parsed.Questions,
		TokensIn:           result.TokensIn,
		TokensOut:          result.TokensOut,
		DurationMS:         int(result.Duration.Milliseconds()),
	}

	s.metrics.requestsOK.Add(1)
	s.metrics.tokensIn.Add(int64(result.TokensIn))
	s.metrics.tokensOut.Add(int64(result.TokensOut))
	s.metrics.durationMSSum.Add(result.Duration.Milliseconds())
	s.metrics.durationCount.Add(1)

	s.writeJSON(w, http.StatusOK, resp)
}

// writeClassifierError maps an agent.Classify error to an appropriate HTTP
// status per plans §4. Timeouts become 504, upstream failures 502, everything
// else 500 (classifier bug; orch retries on the next tick).
func (s *Server) writeClassifierError(w http.ResponseWriter, err error) {
	msg := err.Error()
	var status int
	switch {
	case errors.Is(err, context.DeadlineExceeded) || strings.Contains(msg, "Client.Timeout") || strings.Contains(msg, "deadline exceeded"):
		status = http.StatusGatewayTimeout
	case strings.Contains(msg, "API returned status 5") || strings.Contains(msg, "API call failed"):
		status = http.StatusBadGateway
	default:
		status = http.StatusInternalServerError
	}
	s.writeError(w, status, msg)
}

// ---------------------------------------------------------------------------
// GET /healthz
// ---------------------------------------------------------------------------

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	ok := s.cachedProbe(r.Context())
	type health struct {
		OK       bool   `json:"ok"`
		Upstream string `json:"upstream"`
	}
	body := health{OK: ok}
	if ok {
		body.Upstream = "reachable"
		s.metrics.upstreamUp.Set(1)
	} else {
		body.Upstream = "unreachable"
		s.metrics.upstreamUp.Set(0)
	}
	status := http.StatusOK
	if !ok {
		status = http.StatusServiceUnavailable
	}
	s.writeJSON(w, status, body)
}

// cachedProbe returns the most recent upstream-probe outcome, refreshing it
// when the TTL expires. Without a ProbeUpstream wired in (unit tests, or
// very early startup) we assume healthy so /healthz doesn't falsely gate on
// the optional collaborator.
func (s *Server) cachedProbe(ctx context.Context) bool {
	if s.Deps.ProbeUpstream == nil {
		return true
	}
	s.probeMu.Lock()
	defer s.probeMu.Unlock()
	if time.Since(s.probeAt) > s.probeEvery {
		s.probeErr = s.Deps.ProbeUpstream(ctx)
		s.probeAt = time.Now()
	}
	return s.probeErr == nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func (s *Server) writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		s.Logger.Error("write json body", "error", err)
	}
}

func (s *Server) writeError(w http.ResponseWriter, status int, msg string) {
	switch {
	case status >= 500:
		s.metrics.requests5xx.Add(1)
	case status >= 400:
		s.metrics.requests4xx.Add(1)
	}
	s.writeJSON(w, status, errorBody{Error: msg})
}
