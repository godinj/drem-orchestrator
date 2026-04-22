package tui_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/pkg/orchclient"
	"github.com/godinj/drem-orchestrator/pkg/orchdto"

	"github.com/godinj/drem-orchestrator/internal/tui"
)

// gateHandler is a minimal scripted HTTP handler for the orchestrator's
// POST /projects/.../tasks/.../{verb} endpoints. It mirrors the pattern in
// pkg/orchclient/gate_test.go but lives in the tui package so adapter
// behaviour can be verified end-to-end without pulling in orchhttp.
type gateHandler struct {
	// last request, observed by the handler
	method  string
	path    string
	rawBody []byte

	// configurable response
	status   int
	respBody string

	// call counter
	calls int32
}

func (g *gateHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	atomic.AddInt32(&g.calls, 1)
	g.method = r.Method
	g.path = r.URL.Path
	if r.Body != nil {
		b, _ := io.ReadAll(r.Body)
		g.rawBody = b
	}
	w.Header().Set("Content-Type", "application/json")
	if g.status == 0 {
		g.status = http.StatusOK
	}
	w.WriteHeader(g.status)
	_, _ = io.WriteString(w, g.respBody)
}

func dtoJSON(t *testing.T, id uuid.UUID, status string) string {
	t.Helper()
	b, err := json.Marshal(orchdto.TaskDTO{
		ID:        id.String(),
		Title:     "sample",
		Status:    status,
		CreatedAt: time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 4, 21, 11, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	return string(b)
}

func newAdapterWithHandler(t *testing.T, h http.Handler, project string) *tui.HTTPOrchestrator {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return tui.NewHTTPOrchestrator(orchclient.New(ts.URL), project)
}

// --- interface conformance ----------------------------------------------

// TestHTTPOrchestratorSatisfiesTUIOrchestrator is a compile-time assertion
// that *tui.HTTPOrchestrator implements the tui.TUIOrchestrator interface.
// Phase 3 wires this adapter in place of the in-process orchestrator for
// the TUI's five gate-mutation actions; the interface bind-check keeps the
// two from drifting. Mirrors the assertion in orchestrator_test.go for
// *orchestrator.Orchestrator.
func TestHTTPOrchestratorSatisfiesTUIOrchestrator(t *testing.T) {
	var _ tui.TUIOrchestrator = (*tui.HTTPOrchestrator)(nil)
}

// --- HandlePlanApproved -------------------------------------------------

func TestHTTPOrchestrator_HandlePlanApproved_HappyPath(t *testing.T) {
	id := uuid.New()
	h := &gateHandler{status: http.StatusOK, respBody: dtoJSON(t, id, "in_progress")}
	adapter := newAdapterWithHandler(t, h, "canvas")

	err := adapter.HandlePlanApproved(id)
	require.NoError(t, err)
	require.Equal(t, http.MethodPost, h.method)
	require.Equal(t, "/projects/canvas/tasks/"+id.String()+"/approve", h.path)
}

func TestHTTPOrchestrator_HandlePlanApproved_WrongStatus(t *testing.T) {
	h := &gateHandler{status: http.StatusConflict, respBody: `{"error":"task in status \"backlog\", expected one of [plan_review, test_review]"}`}
	adapter := newAdapterWithHandler(t, h, "canvas")

	err := adapter.HandlePlanApproved(uuid.New())
	require.Error(t, err)
	var ws *orchclient.ErrWrongStatus
	require.True(t, errors.As(err, &ws), "want *ErrWrongStatus, got %T: %v", err, err)
}

// --- HandlePlanRejected -------------------------------------------------

func TestHTTPOrchestrator_HandlePlanRejected_HappyPath(t *testing.T) {
	id := uuid.New()
	h := &gateHandler{status: http.StatusOK, respBody: dtoJSON(t, id, "rejected")}
	adapter := newAdapterWithHandler(t, h, "canvas")

	err := adapter.HandlePlanRejected(id)
	require.NoError(t, err)
	require.Equal(t, http.MethodPost, h.method)
	require.Equal(t, "/projects/canvas/tasks/"+id.String()+"/reject", h.path)

	// Plan rejection carries an empty reason; the wire body still
	// round-trips {"reason":""} so the server can distinguish
	// "field omitted" from "explicit empty reason".
	var payload struct {
		Reason string `json:"reason"`
	}
	require.NoError(t, json.Unmarshal(h.rawBody, &payload))
	require.Equal(t, "", payload.Reason)
}

func TestHTTPOrchestrator_HandlePlanRejected_WrongStatus(t *testing.T) {
	h := &gateHandler{status: http.StatusConflict, respBody: `{"error":"task in status \"merging\", expected one of [plan_review, test_review]"}`}
	adapter := newAdapterWithHandler(t, h, "canvas")

	err := adapter.HandlePlanRejected(uuid.New())
	require.Error(t, err)
	var ws *orchclient.ErrWrongStatus
	require.True(t, errors.As(err, &ws), "want *ErrWrongStatus, got %T: %v", err, err)
}

// --- HandleTestReviewApproved -------------------------------------------

func TestHTTPOrchestrator_HandleTestReviewApproved_HappyPath(t *testing.T) {
	id := uuid.New()
	h := &gateHandler{status: http.StatusOK, respBody: dtoJSON(t, id, "in_progress")}
	adapter := newAdapterWithHandler(t, h, "canvas")

	err := adapter.HandleTestReviewApproved(id)
	require.NoError(t, err)
	// Both HandlePlanApproved and HandleTestReviewApproved target the
	// same server endpoint; the server picks which transition based on
	// the task's current status. This is the contract documented in
	// plans/orch-api-gate-mutations.md.
	require.Equal(t, "/projects/canvas/tasks/"+id.String()+"/approve", h.path)
}

// --- HandleTestReviewRejected -------------------------------------------

func TestHTTPOrchestrator_HandleTestReviewRejected_HappyPath(t *testing.T) {
	id := uuid.New()
	h := &gateHandler{status: http.StatusOK, respBody: dtoJSON(t, id, "test_writing")}
	adapter := newAdapterWithHandler(t, h, "canvas")

	err := adapter.HandleTestReviewRejected(id, "tests miss the edge case")
	require.NoError(t, err)
	require.Equal(t, "/projects/canvas/tasks/"+id.String()+"/reject", h.path)

	var payload struct {
		Reason string `json:"reason"`
	}
	require.NoError(t, json.Unmarshal(h.rawBody, &payload))
	require.Equal(t, "tests miss the edge case", payload.Reason)
}

func TestHTTPOrchestrator_HandleTestReviewRejected_WrongStatus(t *testing.T) {
	h := &gateHandler{status: http.StatusConflict, respBody: `{"error":"task in status \"planning\", expected one of [plan_review, test_review]"}`}
	adapter := newAdapterWithHandler(t, h, "canvas")

	err := adapter.HandleTestReviewRejected(uuid.New(), "feedback")
	require.Error(t, err)
	var ws *orchclient.ErrWrongStatus
	require.True(t, errors.As(err, &ws), "want *ErrWrongStatus, got %T: %v", err, err)
}

// --- HandleTestPassed ---------------------------------------------------

func TestHTTPOrchestrator_HandleTestPassed_HappyPath(t *testing.T) {
	id := uuid.New()
	h := &gateHandler{status: http.StatusOK, respBody: dtoJSON(t, id, "merging")}
	adapter := newAdapterWithHandler(t, h, "canvas")

	err := adapter.HandleTestPassed(id)
	require.NoError(t, err)
	require.Equal(t, "/projects/canvas/tasks/"+id.String()+"/pass", h.path)
}

func TestHTTPOrchestrator_HandleTestPassed_WrongStatus(t *testing.T) {
	h := &gateHandler{status: http.StatusConflict, respBody: `{"error":"task in status \"in_progress\", expected one of [testing_ready]"}`}
	adapter := newAdapterWithHandler(t, h, "canvas")

	err := adapter.HandleTestPassed(uuid.New())
	require.Error(t, err)
	var ws *orchclient.ErrWrongStatus
	require.True(t, errors.As(err, &ws), "want *ErrWrongStatus, got %T: %v", err, err)
}

// --- HandleTestFailed ---------------------------------------------------

func TestHTTPOrchestrator_HandleTestFailed_HappyPath(t *testing.T) {
	id := uuid.New()
	h := &gateHandler{status: http.StatusOK, respBody: dtoJSON(t, id, "in_progress")}
	adapter := newAdapterWithHandler(t, h, "canvas")

	err := adapter.HandleTestFailed(id)
	require.NoError(t, err)
	require.Equal(t, "/projects/canvas/tasks/"+id.String()+"/fail", h.path)
}

func TestHTTPOrchestrator_HandleTestFailed_WrongStatus(t *testing.T) {
	h := &gateHandler{status: http.StatusConflict, respBody: `{"error":"task in status \"backlog\", expected one of [testing_ready]"}`}
	adapter := newAdapterWithHandler(t, h, "canvas")

	err := adapter.HandleTestFailed(uuid.New())
	require.Error(t, err)
	var ws *orchclient.ErrWrongStatus
	require.True(t, errors.As(err, &ws), "want *ErrWrongStatus, got %T: %v", err, err)
}

// --- HandleClarificationAnswer ------------------------------------------

func TestHTTPOrchestrator_HandleClarificationAnswer_HappyPath(t *testing.T) {
	id := uuid.New()
	h := &gateHandler{status: http.StatusOK, respBody: dtoJSON(t, id, "planning")}
	adapter := newAdapterWithHandler(t, h, "canvas")

	err := adapter.HandleClarificationAnswer(id, "use port 9090")
	require.NoError(t, err)
	require.Equal(t, "/projects/canvas/tasks/"+id.String()+"/answer", h.path)

	var payload struct {
		Body string `json:"body"`
	}
	require.NoError(t, json.Unmarshal(h.rawBody, &payload))
	require.Equal(t, "use port 9090", payload.Body)
}

func TestHTTPOrchestrator_HandleClarificationAnswer_WrongStatus(t *testing.T) {
	h := &gateHandler{status: http.StatusConflict, respBody: `{"error":"task in status \"planning\", expected one of [needs_clarification]"}`}
	adapter := newAdapterWithHandler(t, h, "canvas")

	err := adapter.HandleClarificationAnswer(uuid.New(), "answer")
	require.Error(t, err)
	var ws *orchclient.ErrWrongStatus
	require.True(t, errors.As(err, &ws), "want *ErrWrongStatus, got %T: %v", err, err)
}

// --- Error typing: server errors are surfaced verbatim -------------------

// TestHTTPOrchestrator_ServerError_SurfacedAsTypedError exercises the
// 500 path — the adapter must not swallow or re-wrap the typed error so
// the caller can pattern-match via errors.As.
func TestHTTPOrchestrator_ServerError_SurfacedAsTypedError(t *testing.T) {
	h := &gateHandler{status: http.StatusInternalServerError, respBody: `{"error":"db exploded"}`}
	adapter := newAdapterWithHandler(t, h, "canvas")

	err := adapter.HandlePlanApproved(uuid.New())
	require.Error(t, err)
	var se *orchclient.ErrServer
	require.True(t, errors.As(err, &se), "want *ErrServer, got %T: %v", err, err)
	require.Contains(t, se.Error(), "db exploded")
}

// --- Project name propagation -------------------------------------------

// TestHTTPOrchestrator_ProjectNameThreadedIntoURL asserts the adapter
// uses the project it was constructed with, not some hard-coded value.
// This matters because the TUI is constructed once per `drem` invocation
// for a specific project name; swapping projects should require a fresh
// adapter.
func TestHTTPOrchestrator_ProjectNameThreadedIntoURL(t *testing.T) {
	id := uuid.New()
	h := &gateHandler{status: http.StatusOK, respBody: dtoJSON(t, id, "in_progress")}
	adapter := newAdapterWithHandler(t, h, "my-project-x")

	require.NoError(t, adapter.HandlePlanApproved(id))
	require.True(t, strings.HasPrefix(h.path, "/projects/my-project-x/tasks/"),
		"expected path to begin with /projects/my-project-x/tasks/, got %q", h.path)
}
