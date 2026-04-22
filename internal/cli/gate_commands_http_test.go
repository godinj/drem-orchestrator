package cli

// Tests for the HTTP-backed gateHandlers — the five CLI verbs
// (approve, reject, pass, fail, answer) that POST against the
// containerized orchestrator's HTTP API instead of opening the
// SQLite DB directly. See plans/orch-api-gate-mutations.md.
//
// The tests inject a scripted fakeGateClient so the dispatcher can
// be exercised without a real server. A single end-to-end case wires
// a real *orchclient.Client against an httptest.Server to prove the
// wire-level shape still matches what the dispatcher produces.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/pkg/orchclient"
	"github.com/godinj/drem-orchestrator/pkg/orchdto"
)

// fakeGateClient is a scripted stand-in for *orchclient.Client that
// records every call and returns caller-configured responses/errors.
// It satisfies cli.GateClient so DispatchGate can take it directly.
type fakeGateClient struct {
	// ResolveTaskID response.
	resolvedID  string
	resolveErr  error
	resolveCall int32

	// Per-verb response.
	approveDTO orchdto.TaskDTO
	approveErr error
	rejectDTO  orchdto.TaskDTO
	rejectErr  error
	passDTO    orchdto.TaskDTO
	passErr    error
	failDTO    orchdto.TaskDTO
	failErr    error
	answerDTO  orchdto.TaskDTO
	answerErr  error

	// Call recording for assertions.
	lastProject string
	lastPrefix  string
	lastTaskID  uuid.UUID
	lastReason  string
	lastBody    string
	calls       []string
}

func (f *fakeGateClient) ResolveTaskID(ctx context.Context, project, prefix string) (string, error) {
	atomic.AddInt32(&f.resolveCall, 1)
	f.lastProject = project
	f.lastPrefix = prefix
	f.calls = append(f.calls, "resolve")
	if f.resolveErr != nil {
		return "", f.resolveErr
	}
	if f.resolvedID != "" {
		return f.resolvedID, nil
	}
	return prefix, nil
}

func (f *fakeGateClient) Approve(ctx context.Context, project string, taskID uuid.UUID) (orchdto.TaskDTO, error) {
	f.lastProject = project
	f.lastTaskID = taskID
	f.calls = append(f.calls, "approve")
	return f.approveDTO, f.approveErr
}

func (f *fakeGateClient) Reject(ctx context.Context, project string, taskID uuid.UUID, reason string) (orchdto.TaskDTO, error) {
	f.lastProject = project
	f.lastTaskID = taskID
	f.lastReason = reason
	f.calls = append(f.calls, "reject")
	return f.rejectDTO, f.rejectErr
}

func (f *fakeGateClient) Pass(ctx context.Context, project string, taskID uuid.UUID) (orchdto.TaskDTO, error) {
	f.lastProject = project
	f.lastTaskID = taskID
	f.calls = append(f.calls, "pass")
	return f.passDTO, f.passErr
}

func (f *fakeGateClient) Fail(ctx context.Context, project string, taskID uuid.UUID) (orchdto.TaskDTO, error) {
	f.lastProject = project
	f.lastTaskID = taskID
	f.calls = append(f.calls, "fail")
	return f.failDTO, f.failErr
}

func (f *fakeGateClient) Answer(ctx context.Context, project string, taskID uuid.UUID, body string) (orchdto.TaskDTO, error) {
	f.lastProject = project
	f.lastTaskID = taskID
	f.lastBody = body
	f.calls = append(f.calls, "answer")
	return f.answerDTO, f.answerErr
}

// Compile-time check: fakeGateClient satisfies the production interface.
var _ GateClient = (*fakeGateClient)(nil)

// runGate is a thin wrapper around DispatchGate so tests can name the
// call site consistently. handled=true is expected for every verb in
// this suite; we collapse it into the error return (non-handled = unknown
// verb = fail the test).
func runGate(client GateClient, project, subcommand string, args []string, w io.Writer, jsonMode bool) error {
	handled, err := DispatchGate(client, project, jsonMode, subcommand, args, w)
	if !handled {
		return errors.New("DispatchGate did not handle subcommand " + subcommand)
	}
	return err
}

// sampleTaskDTO builds a minimal TaskDTO the fake returns from each
// verb so the CLI-side rendering has something to print.
func sampleTaskDTO(id uuid.UUID, status string) orchdto.TaskDTO {
	return orchdto.TaskDTO{
		ID:     id.String(),
		Title:  "example",
		Status: status,
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Approve
// ────────────────────────────────────────────────────────────────────────────

func TestHTTPApproveHappyPath(t *testing.T) {
	id := uuid.New()
	f := &fakeGateClient{
		resolvedID: id.String(),
		approveDTO: sampleTaskDTO(id, "in_progress"),
	}

	var buf bytes.Buffer
	err := runGate(f, "canvas", "approve", []string{id.String()[:8]}, &buf, false)
	require.NoError(t, err)
	require.Equal(t, []string{"resolve", "approve"}, f.calls)
	require.Equal(t, "canvas", f.lastProject)
	require.Equal(t, id, f.lastTaskID)
	require.Contains(t, buf.String(), id.String()[:8])
	require.Contains(t, buf.String(), "in_progress")
}

func TestHTTPApproveFullUUIDSkipsResolve(t *testing.T) {
	id := uuid.New()
	f := &fakeGateClient{
		resolvedID: id.String(),
		approveDTO: sampleTaskDTO(id, "in_progress"),
	}

	var buf bytes.Buffer
	err := runGate(f, "canvas", "approve", []string{id.String()}, &buf, false)
	require.NoError(t, err)
	require.Equal(t, id, f.lastTaskID)
}

func TestHTTPApproveWrongStatusSurfacesTyped(t *testing.T) {
	id := uuid.New()
	f := &fakeGateClient{
		resolvedID: id.String(),
		approveErr: &orchclient.ErrWrongStatus{Message: `task in status "in_progress", expected one of [plan_review, test_review]`},
	}
	var buf bytes.Buffer
	err := runGate(f, "canvas", "approve", []string{id.String()[:8]}, &buf, false)
	require.Error(t, err)
	require.Contains(t, err.Error(), "wrong status")
	require.Contains(t, err.Error(), "plan_review")
}

func TestHTTPApproveAmbiguousPrefix(t *testing.T) {
	f := &fakeGateClient{
		resolveErr: &orchclient.ErrAmbiguousPrefix{
			Project: "canvas",
			Prefix:  "abc",
			Matches: []string{"abc12345-...", "abcdef00-..."},
		},
	}
	var buf bytes.Buffer
	err := runGate(f, "canvas", "approve", []string{"abc12345"}, &buf, false)
	require.Error(t, err)
	require.Contains(t, err.Error(), "multiple tasks")
	require.Equal(t, int32(1), atomic.LoadInt32(&f.resolveCall), "exactly one resolve call")
	require.NotContains(t, f.calls, "approve")
}

func TestHTTPApproveNoMatch(t *testing.T) {
	f := &fakeGateClient{
		resolveErr: &orchclient.ErrNoMatch{Project: "canvas", Prefix: "deadbeef"},
	}
	var buf bytes.Buffer
	err := runGate(f, "canvas", "approve", []string{"deadbeef"}, &buf, false)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no task")
	require.NotContains(t, f.calls, "approve")
}

func TestHTTPApproveMissingArg(t *testing.T) {
	f := &fakeGateClient{}
	var buf bytes.Buffer
	err := runGate(f, "canvas", "approve", nil, &buf, false)
	require.Error(t, err)
	require.Contains(t, err.Error(), "usage")
	require.Empty(t, f.calls)
}

func TestHTTPApproveTransportError(t *testing.T) {
	id := uuid.New()
	f := &fakeGateClient{
		resolvedID: id.String(),
		approveErr: fmt.Errorf("connection refused"),
	}
	var buf bytes.Buffer
	err := runGate(f, "canvas", "approve", []string{id.String()[:8]}, &buf, false)
	require.Error(t, err)
	require.Contains(t, err.Error(), "connection refused")
}

func TestHTTPApproveJSONMode(t *testing.T) {
	id := uuid.New()
	f := &fakeGateClient{
		resolvedID: id.String(),
		approveDTO: sampleTaskDTO(id, "in_progress"),
	}
	var buf bytes.Buffer
	err := runGate(f, "canvas", "approve", []string{id.String()[:8]}, &buf, true)
	require.NoError(t, err)

	var out orchdto.TaskDTO
	require.NoError(t, json.Unmarshal(buf.Bytes(), &out))
	require.Equal(t, id.String(), out.ID)
	require.Equal(t, "in_progress", out.Status)
}

// ────────────────────────────────────────────────────────────────────────────
// Reject
// ────────────────────────────────────────────────────────────────────────────

func TestHTTPRejectHappyPath(t *testing.T) {
	id := uuid.New()
	f := &fakeGateClient{
		resolvedID: id.String(),
		rejectDTO:  sampleTaskDTO(id, "rejected"),
	}
	var buf bytes.Buffer
	err := runGate(f, "canvas", "reject", []string{id.String()[:8], "--reason=plan too vague"}, &buf, false)
	require.NoError(t, err)
	require.Equal(t, "plan too vague", f.lastReason)
	require.Contains(t, buf.String(), "rejected")
}

func TestHTTPRejectWithoutReasonSendsEmpty(t *testing.T) {
	id := uuid.New()
	f := &fakeGateClient{
		resolvedID: id.String(),
		rejectDTO:  sampleTaskDTO(id, "rejected"),
	}
	var buf bytes.Buffer
	err := runGate(f, "canvas", "reject", []string{id.String()[:8]}, &buf, false)
	require.NoError(t, err)
	require.Equal(t, "", f.lastReason)
}

func TestHTTPRejectWrongStatus(t *testing.T) {
	id := uuid.New()
	f := &fakeGateClient{
		resolvedID: id.String(),
		rejectErr:  &orchclient.ErrWrongStatus{Message: `task in status "merging"`},
	}
	var buf bytes.Buffer
	err := runGate(f, "canvas", "reject", []string{id.String()[:8], "--reason=x"}, &buf, false)
	require.Error(t, err)
	require.Contains(t, err.Error(), "wrong status")
}

// ────────────────────────────────────────────────────────────────────────────
// Pass / Fail
// ────────────────────────────────────────────────────────────────────────────

func TestHTTPPassHappy(t *testing.T) {
	id := uuid.New()
	f := &fakeGateClient{
		resolvedID: id.String(),
		passDTO:    sampleTaskDTO(id, "merging"),
	}
	var buf bytes.Buffer
	err := runGate(f, "canvas", "pass", []string{id.String()[:8]}, &buf, false)
	require.NoError(t, err)
	require.Contains(t, buf.String(), "merging")
}

func TestHTTPPassWrongStatus(t *testing.T) {
	id := uuid.New()
	f := &fakeGateClient{
		resolvedID: id.String(),
		passErr:    &orchclient.ErrWrongStatus{Message: `task in status "planning", expected one of [testing_ready]`},
	}
	var buf bytes.Buffer
	err := runGate(f, "canvas", "pass", []string{id.String()[:8]}, &buf, false)
	require.Error(t, err)
	require.Contains(t, err.Error(), "testing_ready")
}

func TestHTTPFailHappy(t *testing.T) {
	id := uuid.New()
	f := &fakeGateClient{
		resolvedID: id.String(),
		failDTO:    sampleTaskDTO(id, "in_progress"),
	}
	var buf bytes.Buffer
	err := runGate(f, "canvas", "fail", []string{id.String()[:8]}, &buf, false)
	require.NoError(t, err)
	require.Contains(t, buf.String(), "in_progress")
}

// ────────────────────────────────────────────────────────────────────────────
// Answer
// ────────────────────────────────────────────────────────────────────────────

func TestHTTPAnswerHappy(t *testing.T) {
	id := uuid.New()
	f := &fakeGateClient{
		resolvedID: id.String(),
		answerDTO:  sampleTaskDTO(id, "planning"),
	}
	var buf bytes.Buffer
	err := runGate(f, "canvas", "answer", []string{id.String()[:8], "--body=yes use 9090"}, &buf, false)
	require.NoError(t, err)
	require.Equal(t, "yes use 9090", f.lastBody)
	require.Contains(t, buf.String(), "planning")
}

func TestHTTPAnswerMissingBody(t *testing.T) {
	id := uuid.New()
	f := &fakeGateClient{resolvedID: id.String()}
	var buf bytes.Buffer
	err := runGate(f, "canvas", "answer", []string{id.String()[:8]}, &buf, false)
	require.Error(t, err)
	require.Contains(t, strings.ToLower(err.Error()), "body")
	require.NotContains(t, f.calls, "answer")
}

func TestHTTPAnswerWrongStatus(t *testing.T) {
	id := uuid.New()
	f := &fakeGateClient{
		resolvedID: id.String(),
		answerErr:  &orchclient.ErrWrongStatus{Message: `task in status "planning", expected one of [needs_clarification]`},
	}
	var buf bytes.Buffer
	err := runGate(f, "canvas", "answer", []string{id.String()[:8], "--body=ok"}, &buf, false)
	require.Error(t, err)
	require.Contains(t, err.Error(), "needs_clarification")
}

// ────────────────────────────────────────────────────────────────────────────
// End-to-end via httptest — real *orchclient.Client against a server
// that speaks the gate API spec. Proves the dispatcher's wire-level
// expectations match the real client.
// ────────────────────────────────────────────────────────────────────────────

func TestHTTPApproveAgainstRealClient(t *testing.T) {
	id := uuid.New()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /projects/canvas/tasks/"+id.String()+"/approve", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		dto := sampleTaskDTO(id, "in_progress")
		_ = json.NewEncoder(w).Encode(dto)
	})
	// ListTasks for prefix resolution (canvas has exactly one task).
	mux.HandleFunc("GET /projects/canvas/tasks", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]orchdto.TaskDTO{sampleTaskDTO(id, "plan_review")})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	client := orchclient.New(ts.URL)
	var buf bytes.Buffer
	err := runGate(client, "canvas", "approve", []string{id.String()[:8]}, &buf, false)
	require.NoError(t, err)
	require.Contains(t, buf.String(), "in_progress")
}
