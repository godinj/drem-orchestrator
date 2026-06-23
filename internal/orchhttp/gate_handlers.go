package orchhttp

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/pkg/orchdto"
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

type commentRequest struct {
	Author string `json:"author"`
	Body   string `json:"body"`
}

type archiveRequest struct {
	Actor  string `json:"actor"`
	Reason string `json:"reason"`
	Mode   string `json:"mode"`
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
//
// Parent re-animation cascade (Bug I #1 fix): when the target task is a
// subtask (ParentTaskID != nil) and its parent is also in StatusFailed, we
// first call RetryTask on the parent, then on the subtask. This re-animates
// the parent via the canonical failed→backlog edge so the tick loop's
// scheduleSubtasks(parent) path resumes and picks up the retried child on
// the next tick. Without the cascade, the subtask transitions to backlog
// but sits there forever because its parent stays at failed and the
// scheduler's parent-state gate never invokes scheduleSubtasks(parent).
//
// We reuse the RetryTask primitive (not a direct failed→in_progress edge)
// deliberately: the failed→backlog transition is where any subtask-detach
// / stale-agent-unlink logic lives, so cascading through the same entry
// point means no drift between child-level and parent-level retry. A DONE
// parent is left alone — the cascade is scoped to FAILED parents only.
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

	// Parent re-animation cascade: if the target is a subtask whose parent is
	// also in StatusFailed, retry the parent first so the scheduler's
	// parent-state gate opens back up. Any error on the parent load (other
	// than "not found") or retry surfaces as 500; a missing parent row just
	// skips the cascade and proceeds with the single-task retry.
	if task.ParentTaskID != nil {
		var parent model.Task
		err := s.DB.WithContext(r.Context()).
			Where("id = ?", *task.ParentTaskID).
			First(&parent).Error
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			// Parent row missing — treat as a standalone retry.
		case err != nil:
			slog.Error("orchhttp: retry parent load failed",
				"task_id", task.ID, "parent_id", *task.ParentTaskID, "err", err)
			writeJSONError(w, http.StatusInternalServerError, "internal: "+err.Error())
			return
		case parent.Status == model.StatusFailed:
			if err := s.Orch.RetryTask(parent.ID); err != nil {
				slog.Error("orchhttp: retry parent failed",
					"task_id", task.ID, "parent_id", parent.ID, "err", err)
				writeJSONError(w, http.StatusInternalServerError, "internal: "+err.Error())
				return
			}
		}
	}

	if err := s.Orch.RetryTask(task.ID); err != nil {
		slog.Error("orchhttp: retry failed", "task_id", task.ID, "err", err)
		writeJSONError(w, http.StatusInternalServerError, "internal: "+err.Error())
		return
	}
	s.writeUpdatedTask(w, task.ID)
}

// handleArchiveTask marks obsolete non-running work as cancelled without
// spawning, retrying, deleting, or otherwise moving it through the lifecycle.
func (s *Server) handleArchiveTask(w http.ResponseWriter, r *http.Request) {
	if !s.requireProject(w, r) {
		return
	}
	task, ok := s.loadTaskForMutation(w, r)
	if !ok {
		return
	}

	var req archiveRequest
	if err := decodeOptionalJSON(r.Body, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	req.Actor = strings.TrimSpace(req.Actor)
	if req.Actor == "" {
		req.Actor = "operator"
	}
	req.Reason = strings.TrimSpace(req.Reason)
	if req.Reason == "" {
		writeJSONError(w, http.StatusBadRequest, "reason is required")
		return
	}

	if task.Status == model.StatusCancelled {
		s.writeUpdatedTask(w, task.ID)
		return
	}
	archiveAssignmentOK, err := s.archiveAssignmentOK(r, task)
	if err != nil {
		slog.Error("orchhttp: archive assignment check failed", "task_id", task.ID, "err", err)
		writeJSONError(w, http.StatusInternalServerError, "internal: "+err.Error())
		return
	}
	if !archiveAllowedStatus(task.Status) || !archiveAssignmentOK {
		writeJSONError(w, http.StatusConflict,
			fmt.Sprintf("task in status %q or assigned to a live worker; archive requires non-running or stale assigned work", task.Status))
		return
	}

	previousStatus := task.Status
	previousWorker := ""
	if task.AssignedAgentID != nil {
		previousWorker = task.AssignedAgentID.String()
	}
	now := time.Now()
	err = s.DB.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&model.Task{}).
			Where("id = ? AND status IN ?", task.ID, archiveAllowedStatuses()).
			Updates(map[string]any{
				"status":            model.StatusCancelled,
				"assigned_agent_id": nil,
				"updated_at":        now,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return gorm.ErrInvalidTransaction
		}
		return tx.Create(&model.TaskEvent{
			ID:        uuid.New(),
			TaskID:    task.ID,
			EventType: "task_archived",
			OldValue:  string(previousStatus),
			NewValue:  string(model.StatusCancelled),
			Details: model.JSONField{
				"actor":              req.Actor,
				"reason":             req.Reason,
				"mode":               req.Mode,
				"obsolete":           true,
				"previous_worker_id": previousWorker,
				"archived_at":        now.UTC().Format(time.RFC3339Nano),
			},
			Actor:     req.Actor,
			CreatedAt: now,
		}).Error
	})
	if errors.Is(err, gorm.ErrInvalidTransaction) {
		writeJSONError(w, http.StatusConflict, "task changed while archiving; refresh and retry")
		return
	}
	if err != nil {
		slog.Error("orchhttp: archive failed", "task_id", task.ID, "err", err)
		writeJSONError(w, http.StatusInternalServerError, "internal: "+err.Error())
		return
	}
	s.writeUpdatedTask(w, task.ID)
}

func (s *Server) archiveAssignmentOK(r *http.Request, task model.Task) (bool, error) {
	if task.AssignedAgentID == nil {
		return true, nil
	}

	var ag model.Agent
	err := s.DB.WithContext(r.Context()).Where("id = ?", *task.AssignedAgentID).First(&ag).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if ag.Status == model.AgentWorking {
		return false, nil
	}
	return true, nil
}

func archiveAllowedStatus(status model.TaskStatus) bool {
	for _, allowed := range archiveAllowedStatuses() {
		if status == allowed {
			return true
		}
	}
	return false
}

func archiveAllowedStatuses() []model.TaskStatus {
	return []model.TaskStatus{
		model.StatusBacklog,
		model.StatusPlanning,
		model.StatusNeedsClarification,
		model.StatusPlanReview,
		model.StatusTestReview,
		model.StatusPaused,
		model.StatusFailed,
		model.StatusRejected,
	}
}

// handleCommentTask appends an advisory comment to a task. Comments are not a
// lifecycle transition, so the endpoint accepts any existing task status and
// records the author/body as a TaskComment for the next agent spawn prompt.
func (s *Server) handleCommentTask(w http.ResponseWriter, r *http.Request) {
	if !s.requireProject(w, r) {
		return
	}
	task, ok := s.loadTaskForMutation(w, r)
	if !ok {
		return
	}

	var req commentRequest
	if err := decodeOptionalJSON(r.Body, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	req.Body = strings.TrimSpace(req.Body)
	if req.Body == "" {
		writeJSONError(w, http.StatusBadRequest, "body is required")
		return
	}
	req.Author = strings.TrimSpace(req.Author)
	if req.Author == "" {
		req.Author = "csuite"
	}

	comment := model.TaskComment{
		ID:     uuid.New(),
		TaskID: task.ID,
		Author: req.Author,
		Body:   req.Body,
	}
	if err := s.DB.WithContext(r.Context()).Create(&comment).Error; err != nil {
		slog.Error("orchhttp: comment failed", "task_id", task.ID, "err", err)
		writeJSONError(w, http.StatusInternalServerError, "internal: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toTaskCommentDTO(comment))
}

// handleRecoveryAuditTask records Kyle's structured recovery audit event. It is
// scoped to the recovery payload so clients cannot write arbitrary task events.
func (s *Server) handleRecoveryAuditTask(w http.ResponseWriter, r *http.Request) {
	if !s.requireProject(w, r) {
		return
	}
	task, ok := s.loadTaskForMutation(w, r)
	if !ok {
		return
	}

	var req orchdto.RecoveryAuditRequest
	if err := decodeOptionalJSON(r.Body, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	req.Actor = strings.TrimSpace(req.Actor)
	req.PolicyRule = strings.TrimSpace(req.PolicyRule)
	req.Evidence = strings.TrimSpace(req.Evidence)
	req.Surface = strings.TrimSpace(req.Surface)
	req.Action = strings.TrimSpace(req.Action)
	req.Result = strings.TrimSpace(req.Result)
	req.NextFollowUp = strings.TrimSpace(req.NextFollowUp)
	if req.Actor == "" || req.PolicyRule == "" || req.Action == "" || req.Result == "" {
		writeJSONError(w, http.StatusBadRequest, "actor, policy_rule, action, and result are required")
		return
	}

	now := time.Now()
	event := model.TaskEvent{
		ID:        uuid.New(),
		TaskID:    task.ID,
		EventType: "kyle_recovery_audit",
		NewValue:  req.Action,
		Details: model.JSONField{
			"actor":             req.Actor,
			"policy_rule":       req.PolicyRule,
			"observed_evidence": req.Evidence,
			"surface":           req.Surface,
			"action":            req.Action,
			"result":            req.Result,
			"next_follow_up":    req.NextFollowUp,
			"supported_path":    req.SupportedPath,
			"break_glass_path":  req.BreakGlassPath,
		},
		Actor:     req.Actor,
		CreatedAt: now,
	}
	if err := s.DB.WithContext(r.Context()).Create(&event).Error; err != nil {
		slog.Error("orchhttp: recovery audit failed", "task_id", task.ID, "err", err)
		writeJSONError(w, http.StatusInternalServerError, "internal: "+err.Error())
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"task_id":   event.TaskID.String(),
		"old_value": event.OldValue,
		"new_value": event.NewValue,
		"actor":     event.Actor,
		"details":   event.Details,
	})
	writeJSON(w, http.StatusOK, orchdto.EventDTO{Timestamp: event.CreatedAt, Type: event.EventType, Payload: payload})
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
