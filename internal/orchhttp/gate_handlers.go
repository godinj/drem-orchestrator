package orchhttp

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/model"
)

// Gate mutation endpoints — POST /projects/{name}/tasks/{id}/{approve,reject,
// pass,fail,answer}. These are the single write surface the CLI and TUI will
// converge on post-containerization, replacing the short-lived host-side
// orchestrator that `drem cli approve` used to spin up. Every handler follows
// the same shape:
//
//   1. Parse project from URL, return 404 if it doesn't match the server's
//      single project.
//   2. Parse the task UUID from URL, return 400 on malformed input (public
//      read endpoints conflate this with 404 to avoid ID enumeration, but the
//      gate endpoints are operator-triggered so the distinction is useful).
//   3. Load the task, return 404 if missing.
//   4. Decode the optional JSON body for endpoints that take one.
//   5. Dispatch based on current task Status. Return 409 on any status
//      mismatch — callers (Kyle, future CLI, TUI) treat 409 as "you looked
//      at stale data, refresh and retry".
//   6. On orchestrator error return 500; log the full error server-side so
//      the response body can stay terse.
//   7. Re-fetch the task to build the response DTO. The re-fetch is what
//      proves to test #15 that POST /approve -> GET /tasks is read-after-
//      write consistent: both sides use the same *gorm.DB handle.

// errResponse is the shared JSON error envelope.
type errResponse struct {
	Error string `json:"error"`
}

// rejectRequest is the optional body of POST /reject. The CLI today accepts
// --reason=... for plan_review (ignored by HandlePlanRejected) and
// test_review (forwarded as feedback).
type rejectRequest struct {
	Reason string `json:"reason"`
}

// answerRequest is the required body of POST /answer.
type answerRequest struct {
	Body string `json:"body"`
}

// handleApproveTask dispatches POST /projects/{name}/tasks/{id}/approve to
// HandlePlanApproved or HandleTestReviewApproved based on current status.
func (s *Server) handleApproveTask(w http.ResponseWriter, r *http.Request) {
	if s.Orch == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "gate mutations not configured")
		return
	}
	if !s.requireProject(w, r) {
		return
	}
	task, ok := s.loadTaskForMutation(w, r)
	if !ok {
		return
	}

	var err error
	switch task.Status {
	case model.StatusPlanReview:
		err = s.Orch.HandlePlanApproved(task.ID)
	case model.StatusTestReview:
		err = s.Orch.HandleTestReviewApproved(task.ID)
	default:
		writeJSONError(w, http.StatusConflict,
			fmt.Sprintf("task in status %q, expected one of [plan_review, test_review]", task.Status))
		return
	}
	if err != nil {
		slog.Error("orchhttp: approve failed", "task_id", task.ID, "err", err)
		writeJSONError(w, http.StatusInternalServerError, "internal: "+err.Error())
		return
	}
	s.writeUpdatedTask(w, task.ID)
}

// handleRejectTask dispatches POST /projects/{name}/tasks/{id}/reject to
// HandlePlanRejected or HandleTestReviewRejected. The body is optional; an
// empty or missing body maps to reason="" (matching the CLI's default).
func (s *Server) handleRejectTask(w http.ResponseWriter, r *http.Request) {
	if s.Orch == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "gate mutations not configured")
		return
	}
	if !s.requireProject(w, r) {
		return
	}
	task, ok := s.loadTaskForMutation(w, r)
	if !ok {
		return
	}

	var req rejectRequest
	if err := decodeOptionalJSON(r.Body, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}

	var err error
	switch task.Status {
	case model.StatusPlanReview:
		err = s.Orch.HandlePlanRejected(task.ID)
	case model.StatusTestReview:
		err = s.Orch.HandleTestReviewRejected(task.ID, req.Reason)
	default:
		writeJSONError(w, http.StatusConflict,
			fmt.Sprintf("task in status %q, expected one of [plan_review, test_review]", task.Status))
		return
	}
	if err != nil {
		slog.Error("orchhttp: reject failed", "task_id", task.ID, "err", err)
		writeJSONError(w, http.StatusInternalServerError, "internal: "+err.Error())
		return
	}
	s.writeUpdatedTask(w, task.ID)
}

// handlePassTask dispatches POST /projects/{name}/tasks/{id}/pass. Only
// testing_ready is accepted.
func (s *Server) handlePassTask(w http.ResponseWriter, r *http.Request) {
	if s.Orch == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "gate mutations not configured")
		return
	}
	if !s.requireProject(w, r) {
		return
	}
	task, ok := s.loadTaskForMutation(w, r)
	if !ok {
		return
	}
	if task.Status != model.StatusTestingReady {
		writeJSONError(w, http.StatusConflict,
			fmt.Sprintf("task in status %q, expected one of [testing_ready]", task.Status))
		return
	}
	if err := s.Orch.HandleTestPassed(task.ID); err != nil {
		slog.Error("orchhttp: pass failed", "task_id", task.ID, "err", err)
		writeJSONError(w, http.StatusInternalServerError, "internal: "+err.Error())
		return
	}
	s.writeUpdatedTask(w, task.ID)
}

// handleFailTask dispatches POST /projects/{name}/tasks/{id}/fail. Only
// testing_ready is accepted.
func (s *Server) handleFailTask(w http.ResponseWriter, r *http.Request) {
	if s.Orch == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "gate mutations not configured")
		return
	}
	if !s.requireProject(w, r) {
		return
	}
	task, ok := s.loadTaskForMutation(w, r)
	if !ok {
		return
	}
	if task.Status != model.StatusTestingReady {
		writeJSONError(w, http.StatusConflict,
			fmt.Sprintf("task in status %q, expected one of [testing_ready]", task.Status))
		return
	}
	if err := s.Orch.HandleTestFailed(task.ID); err != nil {
		slog.Error("orchhttp: fail failed", "task_id", task.ID, "err", err)
		writeJSONError(w, http.StatusInternalServerError, "internal: "+err.Error())
		return
	}
	s.writeUpdatedTask(w, task.ID)
}

// handleAnswerTask dispatches POST /projects/{name}/tasks/{id}/answer. The
// body is required and must have a non-empty "body" field.
func (s *Server) handleAnswerTask(w http.ResponseWriter, r *http.Request) {
	if s.Orch == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "gate mutations not configured")
		return
	}
	if !s.requireProject(w, r) {
		return
	}
	task, ok := s.loadTaskForMutation(w, r)
	if !ok {
		return
	}

	var req answerRequest
	if err := decodeOptionalJSON(r.Body, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if req.Body == "" {
		writeJSONError(w, http.StatusBadRequest, "body is required")
		return
	}

	if task.Status != model.StatusNeedsClarification {
		writeJSONError(w, http.StatusConflict,
			fmt.Sprintf("task in status %q, expected one of [needs_clarification]", task.Status))
		return
	}
	if err := s.Orch.HandleClarificationAnswer(task.ID, req.Body); err != nil {
		slog.Error("orchhttp: answer failed", "task_id", task.ID, "err", err)
		writeJSONError(w, http.StatusInternalServerError, "internal: "+err.Error())
		return
	}
	s.writeUpdatedTask(w, task.ID)
}

// handleRetryTask dispatches POST /projects/{name}/tasks/{id}/retry. Only
// failed is accepted; any other status returns 409. Delegates to
// Orchestrator.RetryTask, which does the failed→backlog transition,
// clears retry_count/last_error/failure diagnostics, unlinks stale agents,
// and records a "user retried task" event. See
// internal/orchestrator/task_api.go RetryTask for the full semantics.
func (s *Server) handleRetryTask(w http.ResponseWriter, r *http.Request) {
	if s.Orch == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "gate mutations not configured")
		return
	}
	if !s.requireProject(w, r) {
		return
	}
	task, ok := s.loadTaskForMutation(w, r)
	if !ok {
		return
	}
	if task.Status != model.StatusFailed {
		writeJSONError(w, http.StatusConflict,
			fmt.Sprintf("task in status %q, expected one of [failed]", task.Status))
		return
	}
	if err := s.Orch.RetryTask(task.ID); err != nil {
		slog.Error("orchhttp: retry failed", "task_id", task.ID, "err", err)
		writeJSONError(w, http.StatusInternalServerError, "internal: "+err.Error())
		return
	}
	s.writeUpdatedTask(w, task.ID)
}

// requireProject verifies that the {name} path segment matches this server's
// single project. On mismatch it writes a 404 and returns false so callers
// can early-return.
func (s *Server) requireProject(w http.ResponseWriter, r *http.Request) bool {
	name := r.PathValue("name")
	if name != s.Project.Name {
		writeJSONError(w, http.StatusNotFound, "project not found")
		return false
	}
	return true
}

// loadTaskForMutation parses the {id} path segment, validates UUID syntax,
// and loads the task row. On any failure it writes the appropriate status
// code and JSON error, returning ok=false so the caller can early-return.
func (s *Server) loadTaskForMutation(w http.ResponseWriter, r *http.Request) (model.Task, bool) {
	idStr := r.PathValue("id")
	taskID, err := uuid.Parse(idStr)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid task id: "+idStr)
		return model.Task{}, false
	}
	var task model.Task
	err = s.DB.WithContext(r.Context()).Where("id = ?", taskID).First(&task).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		writeJSONError(w, http.StatusNotFound, "task not found")
		return model.Task{}, false
	}
	if err != nil {
		slog.Error("orchhttp: load task failed", "task_id", taskID, "err", err)
		writeJSONError(w, http.StatusInternalServerError, "internal: "+err.Error())
		return model.Task{}, false
	}
	return task, true
}

// writeUpdatedTask re-fetches the task row (capturing the orchestrator's
// state transition) and writes it as a TaskDTO at 200 OK. A post-mutation
// fetch error is rare — typically means the task row was deleted by a
// concurrent op — and surfaces as 500.
func (s *Server) writeUpdatedTask(w http.ResponseWriter, taskID uuid.UUID) {
	var updated model.Task
	if err := s.DB.Where("id = ?", taskID).First(&updated).Error; err != nil {
		slog.Error("orchhttp: re-fetch task failed", "task_id", taskID, "err", err)
		writeJSONError(w, http.StatusInternalServerError, "internal: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toTaskDTO(updated))
}

// decodeOptionalJSON decodes the body into v if non-empty. An empty body is
// treated as a no-op (leaves v at zero values) so callers can POST with no
// body to endpoints where every field is optional.
func decodeOptionalJSON(body io.ReadCloser, v any) error {
	defer body.Close()
	buf, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	if len(buf) == 0 {
		return nil
	}
	return json.Unmarshal(buf, v)
}

// writeJSONError writes status and a JSON error envelope. Using this helper
// (rather than http.Error) ensures every error response carries
// Content-Type: application/json — the gate endpoints are consumed by the
// TUI and (Phase 2) the CLI, both of which decode the body unconditionally.
func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errResponse{Error: msg})
}
