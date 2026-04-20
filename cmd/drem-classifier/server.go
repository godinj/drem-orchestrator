// Package main hosts the drem-classifier HTTP server. See cmd/drem-classifier
// for the binary entry point; server.go is split out so handlers can be
// exercised by unit tests without bringing up signal handling or gq probes.
//
// The HTTP scaffolding (auth middleware, /healthz, /metrics) lives in
// internal/agent/httpserver so the classifier, planner, and future prep
// binary share one auth/metrics shape. This file focuses on the role-
// specific /classify handler.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"expvar"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/agent"
	"github.com/godinj/drem-orchestrator/internal/agent/httpserver"
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

// ---------------------------------------------------------------------------
// Server
// ---------------------------------------------------------------------------

// Deps are the plug-in collaborators the classifier needs. Keeping them on
// Deps lets tests stub the LLM and the upstream probe without a live sglang.
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

	http         *httpserver.Server
	tokensIn     *expvar.Int
	tokensOut    *expvar.Int
}

// classifierTokenMetricsOnce dedupes the classifier-specific token counters
// across repeated NewServer calls in tests (Go's test binary keeps expvar's
// global registry, so double-registering panics).
var (
	classifierTokenMu   sync.Mutex
	classifierTokensIn  *expvar.Int
	classifierTokensOut *expvar.Int
)

func classifierTokenMetrics() (*expvar.Int, *expvar.Int) {
	classifierTokenMu.Lock()
	defer classifierTokenMu.Unlock()
	if classifierTokensIn == nil {
		classifierTokensIn = expvar.NewInt("drem_classifier_llm_tokens_in_total")
		classifierTokensOut = expvar.NewInt("drem_classifier_llm_tokens_out_total")
	}
	return classifierTokensIn, classifierTokensOut
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
	tokensIn, tokensOut := classifierTokenMetrics()
	s := &Server{
		Cfg:       cfg,
		Token:     token,
		Deps:      deps,
		Logger:    logger,
		tokensIn:  tokensIn,
		tokensOut: tokensOut,
	}
	s.http = httpserver.New(httpserver.Config{
		ListenAddr:  "", // managed by main.go
		BearerToken: token,
		HealthProbe: deps.ProbeUpstream,
		MetricsNS:   "drem_classifier",
		Logger:      logger,
	})
	s.http.Handle("POST", "/classify", s.handleClassify)
	return s
}

// Handler returns an http.Handler with every route wired per
// plans/warm-direct-classifier.md §4.
func (s *Server) Handler() http.Handler {
	return s.http.Handler()
}

// ---------------------------------------------------------------------------
// POST /classify
// ---------------------------------------------------------------------------

func (s *Server) handleClassify(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	body, err := io.ReadAll(io.LimitReader(r.Body, 256*1024))
	if err != nil {
		s.http.WriteError(w, http.StatusBadRequest, fmt.Sprintf("read body: %v", err))
		return
	}

	var req classifyRequest
	if err := json.Unmarshal(body, &req); err != nil {
		s.http.WriteError(w, http.StatusBadRequest, fmt.Sprintf("decode body: %v", err))
		return
	}
	if req.TaskID == "" {
		s.http.WriteError(w, http.StatusBadRequest, "task_id is required")
		return
	}
	if req.Title == "" {
		s.http.WriteError(w, http.StatusBadRequest, "title is required")
		return
	}

	taskID, err := uuid.Parse(req.TaskID)
	if err != nil {
		s.http.WriteError(w, http.StatusBadRequest, fmt.Sprintf("task_id is not a valid uuid: %v", err))
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
		s.http.WriteError(w, http.StatusInternalServerError, fmt.Sprintf("parse classifier output: %v", err))
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

	s.http.RecordOK(result.Duration)
	s.tokensIn.Add(int64(result.TokensIn))
	s.tokensOut.Add(int64(result.TokensOut))
	s.http.WriteJSON(w, http.StatusOK, resp)
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
	s.http.WriteError(w, status, msg)
}

// setHealthProbeTTL is used by tests to force /healthz to refresh its probe
// cache on every call. Deliberately unexported — tests in this package can
// reach it, nobody outside can.
func (s *Server) setHealthProbeTTL(d time.Duration) {
	s.http.SetHealthProbeTTL(d)
}
