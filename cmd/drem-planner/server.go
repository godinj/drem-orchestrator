// Package main hosts the drem-planner HTTP server. The binary entry point
// lives in main.go; this file carries the /plan handler + request/response
// shapes so unit tests can exercise the surface without bringing up signal
// handling or real claude-CLI invocations.
//
// Per plans/warm-planner-pivot.md, the planner is a warm single-replica
// container on drem-net:8090. Orch POSTs /plan with the full task + project
// context; the handler execs `claude` CLI as a subprocess per request and
// returns plan.json inline in the 200 body. See server_plan.go /
// server_claude.go (added in the next commit) for the subprocess wiring —
// this commit ships a stub that echoes a minimal plan back so orch wiring
// can be tested end-to-end before claude is on the container.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"expvar"

	"github.com/godinj/drem-orchestrator/internal/agent/httpserver"
)

// ---------------------------------------------------------------------------
// JSON shapes (plans/warm-planner-pivot.md §5)
// ---------------------------------------------------------------------------

// planRequest is the POST /plan body orch sends. The TaskContext /
// ProjectContext fields are intentionally typed as map[string]any so orch
// can evolve the shape without forcing a planner rebuild — the planner
// passes them opaquely into the claude prompt.
type planRequest struct {
	TaskID       string         `json:"task_id"`
	Task         map[string]any `json:"task"`
	Project      map[string]any `json:"project"`
	WorktreePath string         `json:"worktree_path"`
	Comments     []any          `json:"comments,omitempty"`
	TargetCoder  targetCoder    `json:"target_coder,omitempty"`
	Effort       string         `json:"effort,omitempty"`
}

type targetCoder struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
}

// planResponse is the 200 body returned to orch. The plan itself is
// returned inline (no shared-filesystem coupling, unlike the superseded
// spawn-on-demand design — see plans/warm-planner-pivot.md §0).
type planResponse struct {
	TaskID     string         `json:"task_id"`
	Plan       map[string]any `json:"plan"`
	TokensIn   int            `json:"tokens_in"`
	TokensOut  int            `json:"tokens_out"`
	DurationMS int            `json:"duration_ms"`
}

// ---------------------------------------------------------------------------
// Server
// ---------------------------------------------------------------------------

// Deps are the plug-in collaborators the planner needs. Keeping them on
// Deps lets tests stub the plan producer (commit 2 uses a trivial stub;
// commit 3 swaps in a real claude-CLI invocation).
type Deps struct {
	// GeneratePlan produces a plan for the given request. Normally
	// invokes the claude CLI; tests stub this to a deterministic plan.
	GeneratePlan func(ctx context.Context, req planRequest) (*planResult, error)

	// ProbeHealth is the /healthz precondition. Default implementation
	// checks the credentials file is readable + claude --version succeeds
	// in <2s; tests can stub to return nil unconditionally.
	ProbeHealth func(ctx context.Context) error
}

// planResult is the deps-layer result type. Kept separate from
// planResponse so telemetry fields the handler needs (duration_ms,
// claude exit code) don't leak into the public JSON surface.
type planResult struct {
	Plan      map[string]any
	TokensIn  int
	TokensOut int
	Duration  time.Duration
}

// Server wires the HTTP surface declared in plans/warm-planner-pivot.md §5.
type Server struct {
	Token  string
	Deps   Deps
	Logger *slog.Logger

	http          *httpserver.Server
	tokensIn      *expvar.Int
	tokensOut     *expvar.Int
	credentialsOK *expvar.Int
}

// plannerMetricsOnce dedupes planner-specific counters across repeated
// NewServer calls in tests.
var (
	plannerMetricsMu sync.Mutex
	plannerTokensIn  *expvar.Int
	plannerTokensOut *expvar.Int
	plannerCredsOK   *expvar.Int
)

func plannerMetrics() (*expvar.Int, *expvar.Int, *expvar.Int) {
	plannerMetricsMu.Lock()
	defer plannerMetricsMu.Unlock()
	if plannerTokensIn == nil {
		plannerTokensIn = expvar.NewInt("drem_planner_claude_tokens_in_total")
		plannerTokensOut = expvar.NewInt("drem_planner_claude_tokens_out_total")
		plannerCredsOK = expvar.NewInt("drem_planner_credentials_readable")
	}
	return plannerTokensIn, plannerTokensOut, plannerCredsOK
}

// NewServer returns a Server ready to Handler(). Deps.GeneratePlan defaults
// to stubGeneratePlan so early wiring tests can exercise the HTTP surface
// without claude installed.
func NewServer(token string, deps Deps, logger *slog.Logger) *Server {
	if deps.GeneratePlan == nil {
		deps.GeneratePlan = stubGeneratePlan
	}
	if logger == nil {
		logger = slog.Default()
	}
	tokensIn, tokensOut, credsOK := plannerMetrics()
	s := &Server{
		Token:         token,
		Deps:          deps,
		Logger:        logger,
		tokensIn:      tokensIn,
		tokensOut:     tokensOut,
		credentialsOK: credsOK,
	}
	s.http = httpserver.New(httpserver.Config{
		ListenAddr:  "", // managed by main.go
		BearerToken: token,
		HealthProbe: deps.ProbeHealth,
		MetricsNS:   "drem_planner",
		Logger:      logger,
	})
	s.http.Handle("POST", "/plan", s.handlePlan)
	return s
}

// Handler returns the composed http.Handler.
func (s *Server) Handler() http.Handler {
	return s.http.Handler()
}

// setHealthProbeTTL is the in-package shim for forcing /healthz to refresh
// on every call. Used by server_test.go.
func (s *Server) setHealthProbeTTL(d time.Duration) {
	s.http.SetHealthProbeTTL(d)
}

// ---------------------------------------------------------------------------
// POST /plan
// ---------------------------------------------------------------------------

// maxPlanRequestBytes caps the request body size so a pathological orch
// can't OOM the planner. The prompt + task context is usually <64 KiB;
// 1 MiB is a defensive ceiling.
const maxPlanRequestBytes = 1 << 20

func (s *Server) handlePlan(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	body, err := io.ReadAll(io.LimitReader(r.Body, maxPlanRequestBytes))
	if err != nil {
		s.http.WriteError(w, http.StatusBadRequest, fmt.Sprintf("read body: %v", err))
		return
	}

	var req planRequest
	if err := json.Unmarshal(body, &req); err != nil {
		s.http.WriteError(w, http.StatusBadRequest, fmt.Sprintf("decode body: %v", err))
		return
	}
	if req.TaskID == "" {
		s.http.WriteError(w, http.StatusBadRequest, "task_id is required")
		return
	}
	if req.WorktreePath == "" {
		s.http.WriteError(w, http.StatusBadRequest, "worktree_path is required")
		return
	}
	// Lightweight path-escape guard: reject relative paths and paths with
	// embedded `..` segments. Not a full containment check — the container
	// owns its own FS — but catches obvious misuse from orch.
	if !filepath.IsAbs(req.WorktreePath) || strings.Contains(req.WorktreePath, "..") {
		s.http.WriteError(w, http.StatusBadRequest, fmt.Sprintf("worktree_path must be absolute and free of '..': %q", req.WorktreePath))
		return
	}

	result, err := s.Deps.GeneratePlan(r.Context(), req)
	if err != nil {
		s.writePlanError(w, err)
		return
	}
	if result == nil || result.Plan == nil {
		s.http.WriteError(w, http.StatusInternalServerError, "planner returned nil plan")
		return
	}

	// Validate the plan shape before returning. Bad shape → 409 so orch
	// can count it against the retry budget without treating it as a
	// planner bug (500).
	if err := validatePlan(result.Plan); err != nil {
		s.http.WriteError(w, http.StatusConflict, fmt.Sprintf("plan validation failed: %v", err))
		return
	}

	resp := planResponse{
		TaskID:     req.TaskID,
		Plan:       result.Plan,
		TokensIn:   result.TokensIn,
		TokensOut:  result.TokensOut,
		DurationMS: int(result.Duration.Milliseconds()),
	}
	s.http.RecordOK(result.Duration)
	s.tokensIn.Add(int64(result.TokensIn))
	s.tokensOut.Add(int64(result.TokensOut))
	s.http.WriteJSON(w, http.StatusOK, resp)
}

// writePlanError maps a planner-side error to an appropriate HTTP status.
// Shape mirrors the classifier's writeClassifierError: timeouts → 504,
// upstream Anthropic failures → 502, everything else → 500.
func (s *Server) writePlanError(w http.ResponseWriter, err error) {
	msg := err.Error()
	var status int
	switch {
	case errors.Is(err, context.DeadlineExceeded) || strings.Contains(msg, "deadline exceeded") || strings.Contains(msg, "Client.Timeout"):
		status = http.StatusGatewayTimeout
	case strings.Contains(msg, "upstream") || strings.Contains(msg, "anthropic"):
		status = http.StatusBadGateway
	default:
		status = http.StatusInternalServerError
	}
	s.http.WriteError(w, status, msg)
}

// ---------------------------------------------------------------------------
// plan validation
// ---------------------------------------------------------------------------

// validatePlan applies the plan-shape rules from
// plans/warm-planner-pivot.md §6. The orch-side validator is a thin
// wrapper; the planner server-side guarantees these same invariants so
// orch can trust the 200 body without re-parsing everything.
//
//   - subtasks must be a non-empty array of objects.
//   - tests_for / dependencies indices must be within the subtasks slice.
func validatePlan(plan map[string]any) error {
	raw, ok := plan["subtasks"]
	if !ok {
		return errors.New("missing subtasks")
	}
	list, ok := raw.([]any)
	if !ok {
		return errors.New("subtasks is not an array")
	}
	if len(list) == 0 {
		return errors.New("subtasks is empty")
	}
	n := len(list)
	for i, entry := range list {
		m, ok := entry.(map[string]any)
		if !ok {
			return fmt.Errorf("subtask %d is not an object", i)
		}
		if err := validatePlanIndexList(m, "tests_for", n, i); err != nil {
			return err
		}
		if err := validatePlanIndexList(m, "dependencies", n, i); err != nil {
			return err
		}
	}
	return nil
}

func validatePlanIndexList(m map[string]any, field string, n, subtaskIdx int) error {
	raw, ok := m[field]
	if !ok {
		return nil
	}
	list, ok := raw.([]any)
	if !ok {
		return fmt.Errorf("subtask %d field %q is not an array", subtaskIdx, field)
	}
	for _, v := range list {
		var idx int
		switch val := v.(type) {
		case float64:
			idx = int(val)
		case int:
			idx = val
		default:
			return fmt.Errorf("subtask %d field %q has non-integer element", subtaskIdx, field)
		}
		if idx < 0 || idx >= n {
			return fmt.Errorf("subtask %d field %q index %d out of range [0,%d)", subtaskIdx, field, idx, n)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Stub plan generator (commit 2)
// ---------------------------------------------------------------------------

// stubGeneratePlan returns a deterministic minimal plan so orch wiring can
// be exercised without claude installed. Commit 3 replaces the default
// Deps.GeneratePlan with a real subprocess implementation; this stub
// stays in place as the test default.
func stubGeneratePlan(ctx context.Context, req planRequest) (*planResult, error) {
	plan := map[string]any{
		"subtasks": []any{
			map[string]any{
				"title":       "test stub for " + req.TaskID,
				"description": "placeholder test subtask emitted by the planner stub",
				"agent_type":  "coder",
				"phase":       "test",
				"tests_for":   []any{float64(1)},
				"files":       []any{"stub_test.go"},
			},
			map[string]any{
				"title":       "impl stub for " + req.TaskID,
				"description": "placeholder implementation subtask emitted by the planner stub",
				"agent_type":  "coder",
				"phase":       "implementation",
				"files":       []any{"stub.go"},
			},
		},
	}
	return &planResult{
		Plan:      plan,
		TokensIn:  0,
		TokensOut: 0,
		Duration:  0,
	}, nil
}

// ---------------------------------------------------------------------------
// Credentials probe
// ---------------------------------------------------------------------------

// defaultCredentialsPath returns the standard Codex auth file path inside the
// container ($HOME/.codex/auth.json). Keeping
// it as a function (not a constant) lets tests override HOME.
func defaultCredentialsPath() string {
	base := os.Getenv("CODEX_HOME")
	if base == "" {
		home := os.Getenv("HOME")
		if home == "" {
			home = "/home/drem"
		}
		base = filepath.Join(home, ".codex")
	}
	return filepath.Join(base, "auth.json")
}

// credentialsProbe returns nil when the credentials file is readable,
// signalling that the container has usable Codex auth. /healthz gates on
// this — a failed probe means the operator hasn't run `codex login` on the
// host or the bind-mount broke.
func credentialsProbe(ctx context.Context, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("credentials unreadable at %s: %w", path, err)
	}
	_ = f.Close()
	return nil
}
