package orchhttp

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/state"
	"github.com/godinj/drem-orchestrator/pkg/orchdto"
)

// handleReviseTaskPlan lets an attributed adapter repair a reviewer-rejected
// plan without invoking the planner again. Scope remains immutable: every
// revised subtask is validated against the original TaskSpecification.
func (s *Server) handleReviseTaskPlan(w http.ResponseWriter, r *http.Request) {
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
	if task.Status != model.StatusPlanReview {
		writeJSONError(w, http.StatusConflict,
			fmt.Sprintf("task in status %q, expected one of [plan_review]", task.Status))
		return
	}
	actor, ok := requireMutationActor(w, r)
	if !ok {
		return
	}

	var req orchdto.ReviseTaskPlanRequest
	if err := decodeRequiredJSON(r.Body, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if strings.TrimSpace(req.Reason) == "" {
		writeJSONError(w, http.StatusBadRequest, "reason is required")
		return
	}

	var specification model.TaskSpecification
	if err := s.DB.WithContext(r.Context()).Where("task_id = ?", task.ID).First(&specification).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			writeJSONError(w, http.StatusConflict, "task has no immutable specification; its plan cannot be revised through the adapter")
			return
		}
		writeDBError(w, err)
		return
	}
	var spec orchdto.TaskSpecDTO
	if err := json.Unmarshal([]byte(specification.SpecJSON), &spec); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "stored task specification is invalid")
		return
	}
	if err := validateExecutionPlan(req.ExecutionPlan, spec.ProposedScope); err != nil {
		writeJSONError(w, http.StatusBadRequest, "execution_plan: "+err.Error())
		return
	}
	spec.ExecutionPlan = &req.ExecutionPlan
	if err := validateIntegrationSeams(spec); err != nil {
		writeJSONError(w, http.StatusBadRequest, "integration_seams: "+err.Error())
		return
	}
	plan, err := executionPlanField(&req.ExecutionPlan)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.Orch.RevisePlan(task.ID, task.StateVersion, plan, actor, strings.TrimSpace(req.Reason)); err != nil {
		if errors.Is(err, state.ErrStaleTransition) {
			writeJSONError(w, http.StatusConflict, err.Error())
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal: "+err.Error())
		return
	}
	s.writeUpdatedTask(w, task.ID)
}

func decodeRequiredJSON(body io.ReadCloser, target any) error {
	defer body.Close()
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("expected one JSON object")
	}
	return nil
}
