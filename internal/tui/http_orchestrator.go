package tui

// HTTPOrchestrator is the Phase-3 adapter that routes the TUI's gate
// mutation actions (approve / reject / pass / fail / answer) through
// pkg/orchclient over HTTP instead of calling an in-process
// *orchestrator.Orchestrator directly. It exists so the TUI can run as a
// pure HTTP client against a containerized orchestrator — the same
// invariant Kyle already honours — closing the double-writer escape hatch
// that `drem cli approve` exposed before the pivot (see
// plans/orch-api-gate-mutations.md).
//
// Scope:
//
//   - The seven Handle* gate methods (HandlePlanApproved,
//     HandlePlanRejected, HandleTestReviewApproved,
//     HandleTestReviewRejected, HandleTestPassed, HandleTestFailed,
//     HandleClarificationAnswer) go out over HTTP via the embedded
//     *orchclient.Client. Typed errors from orchclient (ErrWrongStatus,
//     ErrNotFound, ErrServer, transport errors) are surfaced verbatim so
//     the TUI's existing status-line renderer can pattern-match with
//     errors.As.
//
//   - All other methods on tui.TUIOrchestrator (PauseTask, ResumeTask,
//     RetryTask, CreateTask, DeleteTask, AddComment, DeleteComment,
//     DeletePlanStep, the Spawn*Session trio, GetAgentOutput,
//     ReapOrphanedSessions, IntegrationWorktreePath) fall through to an
//     optional Fallback TUIOrchestrator supplied at construction time via
//     WithFallback. When Fallback is nil the adapter returns a clear
//     "not supported via HTTP" error (or an empty string for
//     IntegrationWorktreePath) — enough signal for the TUI's status line
//     to render something useful without crashing.
//
// The constructor is NewHTTPOrchestrator(client, project); use
// WithFallback to attach the in-process orchestrator for non-gate
// methods. Phase 3 wires main.go to call both, so the production TUI
// retains full functionality while the gate path is now HTTP.

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/pkg/orchclient"
)

// errNotSupportedViaHTTP is returned by non-gate methods when no
// Fallback is wired. The message is deliberately terse because the TUI
// renders it verbatim in its status bar.
var errNotSupportedViaHTTP = errors.New("tui: method not supported via HTTP adapter; wire a fallback")

// HTTPOrchestrator implements tui.TUIOrchestrator by delegating gate
// mutations to an *orchclient.Client and non-gate methods to an optional
// Fallback.
//
// Zero values are not usable; construct with NewHTTPOrchestrator.
type HTTPOrchestrator struct {
	client   *orchclient.Client
	project  string
	fallback TUIOrchestrator
}

// NewHTTPOrchestrator constructs an adapter pointed at the orchestrator
// whose URL was baked into client, scoped to the given project name. The
// project name is threaded into every gate URL as the {name} path
// segment, matching the server's single-project-per-orchestrator model.
//
// The returned adapter has no fallback; non-gate methods will return
// errNotSupportedViaHTTP until WithFallback is called.
func NewHTTPOrchestrator(client *orchclient.Client, project string) *HTTPOrchestrator {
	return &HTTPOrchestrator{client: client, project: project}
}

// WithFallback attaches an in-process TUIOrchestrator that handles every
// method this adapter does not route over HTTP (everything except the
// seven Handle* gate methods). Returns the receiver so construction can
// chain. A nil fallback is valid and equivalent to not calling this
// method — non-gate calls will return errNotSupportedViaHTTP.
func (h *HTTPOrchestrator) WithFallback(fallback TUIOrchestrator) *HTTPOrchestrator {
	h.fallback = fallback
	return h
}

// gateContext returns a per-call context suitable for gate mutations.
// Using context.Background keeps the adapter compatible with the
// TUIOrchestrator interface, which does not take a ctx argument; the
// orchclient default HTTP timeout bounds the call.
func gateContext() context.Context {
	return context.Background()
}

// --- Gate mutations — routed over HTTP ----------------------------------

// HandlePlanApproved POSTs /approve; server resolves
// plan_review -> in_progress.
func (h *HTTPOrchestrator) HandlePlanApproved(taskID uuid.UUID) error {
	_, err := h.client.Approve(gateContext(), h.project, taskID)
	return err
}

// HandlePlanRejected POSTs /reject with an empty reason;
// plan_review -> rejected.
func (h *HTTPOrchestrator) HandlePlanRejected(taskID uuid.UUID) error {
	_, err := h.client.Reject(gateContext(), h.project, taskID, "")
	return err
}

// HandleTestReviewApproved POSTs /approve; server resolves
// test_review -> in_progress.
func (h *HTTPOrchestrator) HandleTestReviewApproved(taskID uuid.UUID) error {
	_, err := h.client.Approve(gateContext(), h.project, taskID)
	return err
}

// HandleTestReviewRejected POSTs /reject with the operator's feedback
// forwarded to the server as {"reason": "..."}. test_review ->
// test_writing, feedback persisted.
func (h *HTTPOrchestrator) HandleTestReviewRejected(taskID uuid.UUID, feedback string) error {
	_, err := h.client.Reject(gateContext(), h.project, taskID, feedback)
	return err
}

// HandleTestPassed POSTs /pass; testing_ready -> merging.
func (h *HTTPOrchestrator) HandleTestPassed(taskID uuid.UUID) error {
	_, err := h.client.Pass(gateContext(), h.project, taskID)
	return err
}

// HandleTestFailed POSTs /fail; testing_ready -> in_progress.
func (h *HTTPOrchestrator) HandleTestFailed(taskID uuid.UUID) error {
	_, err := h.client.Fail(gateContext(), h.project, taskID)
	return err
}

// HandleClarificationAnswer POSTs /answer with the operator's reply;
// needs_clarification -> planning (when all questions are answered).
func (h *HTTPOrchestrator) HandleClarificationAnswer(taskID uuid.UUID, answer string) error {
	_, err := h.client.Answer(gateContext(), h.project, taskID, answer)
	return err
}

// --- Non-gate methods — delegated to Fallback ---------------------------
//
// These are deliberately thin: the adapter does not touch the network on
// these paths. Phase 3's scope is the five gate actions; promoting any of
// the below to a real HTTP call is a separate design question (needs an
// endpoint, tests, and a spec) tracked in docs/prd-containerization.md.

// PauseTask delegates to the fallback orchestrator.
func (h *HTTPOrchestrator) PauseTask(taskID uuid.UUID) error {
	if h.fallback == nil {
		return errNotSupportedViaHTTP
	}
	return h.fallback.PauseTask(taskID)
}

// ResumeTask delegates to the fallback orchestrator.
func (h *HTTPOrchestrator) ResumeTask(taskID uuid.UUID) error {
	if h.fallback == nil {
		return errNotSupportedViaHTTP
	}
	return h.fallback.ResumeTask(taskID)
}

// RetryTask delegates to the fallback orchestrator.
func (h *HTTPOrchestrator) RetryTask(taskID uuid.UUID) error {
	if h.fallback == nil {
		return errNotSupportedViaHTTP
	}
	return h.fallback.RetryTask(taskID)
}

// CreateTask delegates to the fallback orchestrator.
func (h *HTTPOrchestrator) CreateTask(title, description string, priority int) (*model.Task, error) {
	if h.fallback == nil {
		return nil, errNotSupportedViaHTTP
	}
	return h.fallback.CreateTask(title, description, priority)
}

// DeleteTask delegates to the fallback orchestrator.
func (h *HTTPOrchestrator) DeleteTask(taskID uuid.UUID) error {
	if h.fallback == nil {
		return errNotSupportedViaHTTP
	}
	return h.fallback.DeleteTask(taskID)
}

// AddComment delegates to the fallback orchestrator.
func (h *HTTPOrchestrator) AddComment(taskID uuid.UUID, author, body string) error {
	if h.fallback == nil {
		return errNotSupportedViaHTTP
	}
	return h.fallback.AddComment(taskID, author, body)
}

// DeleteComment delegates to the fallback orchestrator.
func (h *HTTPOrchestrator) DeleteComment(commentID uuid.UUID) error {
	if h.fallback == nil {
		return errNotSupportedViaHTTP
	}
	return h.fallback.DeleteComment(commentID)
}

// DeletePlanStep delegates to the fallback orchestrator.
func (h *HTTPOrchestrator) DeletePlanStep(taskID uuid.UUID, stepIndex int) error {
	if h.fallback == nil {
		return errNotSupportedViaHTTP
	}
	return h.fallback.DeletePlanStep(taskID, stepIndex)
}

// SpawnSupervisorSession delegates to the fallback orchestrator.
func (h *HTTPOrchestrator) SpawnSupervisorSession(taskID uuid.UUID) (string, error) {
	if h.fallback == nil {
		return "", errNotSupportedViaHTTP
	}
	return h.fallback.SpawnSupervisorSession(taskID)
}

// SpawnReviewerSession delegates to the fallback orchestrator.
func (h *HTTPOrchestrator) SpawnReviewerSession(taskID uuid.UUID) (string, error) {
	if h.fallback == nil {
		return "", errNotSupportedViaHTTP
	}
	return h.fallback.SpawnReviewerSession(taskID)
}

// SpawnFixerSession delegates to the fallback orchestrator.
func (h *HTTPOrchestrator) SpawnFixerSession(taskID uuid.UUID) (string, error) {
	if h.fallback == nil {
		return "", errNotSupportedViaHTTP
	}
	return h.fallback.SpawnFixerSession(taskID)
}

// GetAgentOutput delegates to the fallback orchestrator.
func (h *HTTPOrchestrator) GetAgentOutput(agentID uuid.UUID) (string, error) {
	if h.fallback == nil {
		return "", errNotSupportedViaHTTP
	}
	return h.fallback.GetAgentOutput(agentID)
}

// ReapOrphanedSessions delegates to the fallback orchestrator.
func (h *HTTPOrchestrator) ReapOrphanedSessions() (int, error) {
	if h.fallback == nil {
		return 0, errNotSupportedViaHTTP
	}
	return h.fallback.ReapOrphanedSessions()
}

// IntegrationWorktreePath delegates to the fallback orchestrator. With no
// fallback the returned empty string causes the TUI's "open in editor"
// action to render a clear error rather than crash.
func (h *HTTPOrchestrator) IntegrationWorktreePath(taskID uuid.UUID) string {
	if h.fallback == nil {
		return ""
	}
	return h.fallback.IntegrationWorktreePath(taskID)
}

// Compile-time check that *HTTPOrchestrator satisfies TUIOrchestrator.
// This mirrors the assertion on *orchestrator.Orchestrator in
// internal/tui/orchestrator_test.go.
var _ TUIOrchestrator = (*HTTPOrchestrator)(nil)
