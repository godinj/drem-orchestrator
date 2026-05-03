package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/clarification"
	"github.com/godinj/drem-orchestrator/internal/gitexec"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/state"
	"github.com/godinj/drem-orchestrator/pkg/score"
)

const maxPlanRejections = 3

// ---------------------------------------------------------------------------
// Public methods for TUI interaction (task processing)
// ---------------------------------------------------------------------------

// materializeSubtasks parses the plan from a parent task and creates subtask
// records in the DB, including dependency and TestsFor mappings. It returns
// the parsed plan result and created subtask IDs, or an error.
// This is shared by HandlePlanApproved and the defensive check in
// processTestWriting (for plans approved via raw DB update that bypassed
// subtask creation).
func (o *Orchestrator) materializeSubtasks(task *model.Task) (*parsePlanResult, []uuid.UUID, error) {
	planResult, err := parsePlan(task.Plan)
	if err != nil {
		return nil, nil, fmt.Errorf("materialize subtasks: %w", err)
	}
	subtaskPlans := planResult.Subtasks

	var existingSubtasks []model.Task
	if err := o.db.Where("parent_task_id = ?", task.ID).
		Find(&existingSubtasks).Error; err != nil {
		return nil, nil, fmt.Errorf("materialize subtasks: load existing subtasks: %w", err)
	}
	usedExisting := make(map[uuid.UUID]bool, len(existingSubtasks))

	// Auto-generate TDD reverse dependencies from tests_for.
	merged := MergeTDDDependencies(subtaskPlans)

	// Create subtask records. We need to track created IDs for dependency mapping.
	createdIDs := make([]uuid.UUID, len(subtaskPlans))
	for i, sp := range subtaskPlans {
		var reused *model.Task
		for j := range existingSubtasks {
			existing := &existingSubtasks[j]
			if usedExisting[existing.ID] || existing.Status != model.StatusDone || existing.Title != sp.Title || existing.Phase != sp.Phase {
				continue
			}
			createdIDs[i] = existing.ID
			usedExisting[existing.ID] = true
			reused = existing
			break
		}
		if createdIDs[i] != uuid.Nil {
			for _, duplicate := range existingSubtasks {
				if duplicate.ID == reused.ID || duplicate.Status == model.StatusDone || duplicate.Title != sp.Title || duplicate.Phase != sp.Phase {
					continue
				}
				if err := o.DeleteSubtask(duplicate.ID); err != nil {
					return nil, nil, fmt.Errorf("materialize subtasks: delete stale duplicate subtask %s: %w", duplicate.ID, err)
				}
			}
			continue
		}

		subtaskID := uuid.New()
		createdIDs[i] = subtaskID

		ctx := model.JSONField{
			"agent_type":      sp.AgentType,
			"estimated_files": sp.EstimatedFiles,
		}
		if sp.Phase != "" {
			ctx["phase"] = sp.Phase
		}

		sub := model.Task{
			ID:           subtaskID,
			ProjectID:    task.ProjectID,
			ParentTaskID: &task.ID,
			Title:        sp.Title,
			Description:  sp.Description,
			Status:       model.StatusBacklog,
			Phase:        sp.Phase,
			Context:      ctx,
			Priority:     len(subtaskPlans) - i,
		}

		if err := o.db.Create(&sub).Error; err != nil {
			return nil, nil, fmt.Errorf("materialize subtasks: create subtask %d: %w", i, err)
		}
	}

	// Second pass: set dependency IDs (including auto-generated TDD deps).
	for i, sp := range merged {
		if len(sp.Dependencies) == 0 {
			continue
		}
		var depIDs model.JSONArray
		for _, depIdx := range sp.Dependencies {
			if depIdx >= 0 && depIdx < len(createdIDs) {
				depIDs = append(depIDs, createdIDs[depIdx].String())
			}
		}
		if len(depIDs) > 0 {
			if err := o.db.Model(&model.Task{}).Where("id = ?", createdIDs[i]).
				Update("dependency_ids", depIDs).Error; err != nil {
				return nil, nil, fmt.Errorf("materialize subtasks: update dependencies for subtask %d: %w", i, err)
			}
		}
	}

	// Third pass: set TestsFor on test-phase subtasks.
	for i, sp := range subtaskPlans {
		if len(sp.TestsFor) > 0 {
			var testsForIDs model.JSONArray
			for _, idx := range sp.TestsFor {
				if idx >= 0 && idx < len(createdIDs) {
					testsForIDs = append(testsForIDs, createdIDs[idx].String())
				}
			}
			if len(testsForIDs) > 0 {
				o.db.Model(&model.Task{}).Where("id = ?", createdIDs[i]).
					Update("tests_for", testsForIDs)
			}
		}
	}

	return planResult, createdIDs, nil
}

// HandlePlanApproved creates subtask records from the plan and transitions the
// task to IN_PROGRESS (or TEST_WRITING for TDD plans).
func (o *Orchestrator) HandlePlanApproved(taskID uuid.UUID) error {
	var task model.Task
	if err := o.db.First(&task, "id = ?", taskID).Error; err != nil {
		return fmt.Errorf("handle plan approved: load task: %w", err)
	}

	if task.Status != model.StatusPlanReview {
		return fmt.Errorf("handle plan approved: task %s is in %s, expected plan_review", taskID, task.Status)
	}

	planResult, _, err := o.materializeSubtasks(&task)
	if err != nil {
		return fmt.Errorf("handle plan approved: %w", err)
	}
	subtaskPlans := planResult.Subtasks

	// Store TDD exceptions on the parent task.
	if len(planResult.TDDExceptions) > 0 {
		exceptionsJSON, _ := json.Marshal(planResult.TDDExceptions)
		var exceptionsField any
		json.Unmarshal(exceptionsJSON, &exceptionsField)
		if task.TDDExceptions == nil {
			task.TDDExceptions = make(model.JSONField)
		}
		task.TDDExceptions["exceptions"] = exceptionsField
	}

	// Build wave schedule from the created subtasks.
	var createdSubtasks []model.Task
	if err := o.db.Where("parent_task_id = ?", task.ID).Find(&createdSubtasks).Error; err != nil {
		o.logger.Warn("handle plan approved: failed to load subtasks for scheduling", "error", err)
	} else if len(createdSubtasks) > 0 {
		schedule := BuildSchedule(createdSubtasks)
		scheduleJSON, marshalErr := json.Marshal(schedule)
		if marshalErr != nil {
			o.logger.Warn("handle plan approved: failed to marshal schedule", "error", marshalErr)
		} else {
			if task.Context == nil {
				task.Context = make(model.JSONField)
			}
			var scheduleField any
			if err := json.Unmarshal(scheduleJSON, &scheduleField); err != nil {
				o.logger.Warn("handle plan approved: failed to unmarshal schedule into context", "error", err)
			} else {
				task.Context["schedule"] = scheduleField
				o.logger.Info("wave schedule computed",
					"task_id", task.ID,
					"groups", len(schedule.Groups),
					"subtasks", len(createdSubtasks))
			}
		}
	}

	// Clear planner agent assignment now that review is complete.
	task.AssignedAgentID = nil

	// Write plan.json to the integration worktree as an untracked file.
	// Agents can read it from disk but it must not be committed — tracked
	// plan.json causes merge conflicts between feature branches.
	if task.WorktreeBranch != "" {
		featureName := strings.TrimPrefix(task.WorktreeBranch, "feature/")
		featureDir := o.worktree.FeatureWorktreePath(featureName)
		planJSON, marshalErr := json.MarshalIndent(task.Plan, "", "  ")
		if marshalErr != nil {
			o.logger.Warn("handle plan approved: failed to marshal plan for worktree", "error", marshalErr)
		} else {
			planPath := filepath.Join(featureDir, "plan.json")
			if writeErr := os.WriteFile(planPath, planJSON, 0o644); writeErr != nil {
				o.logger.Warn("handle plan approved: failed to write plan.json to worktree", "error", writeErr)
			}
			// If plan.json was previously tracked, untrack it.
			if removed, rmErr := gitexec.UntrackEphemeralFiles(context.Background(), featureDir); rmErr != nil {
				o.logger.Warn("handle plan approved: failed to untrack plan.json", "error", rmErr)
			} else if removed {
				o.logger.Info("handle plan approved: untracked plan.json in integration worktree",
					"task_id", task.ID)
			}
		}
	}

	// Determine transition target: TEST_WRITING if plan has test-phase subtasks.
	hasTestPhase := false
	for _, sp := range subtaskPlans {
		if sp.Phase == "test" {
			hasTestPhase = true
			break
		}
	}

	var targetStatus model.TaskStatus
	if hasTestPhase {
		targetStatus = model.StatusTestWriting
	} else {
		targetStatus = model.StatusInProgress
	}

	evt, err := state.TransitionTask(&task, targetStatus, "user", map[string]any{"action": "plan_approved"})
	if err != nil {
		return fmt.Errorf("handle plan approved: transition: %w", err)
	}
	if err := o.db.Save(&task).Error; err != nil {
		return fmt.Errorf("handle plan approved: save task: %w", err)
	}
	if err := o.db.Create(evt).Error; err != nil {
		return fmt.Errorf("handle plan approved: save event: %w", err)
	}

	o.emit("task_updated", &task)
	o.publishTaskTransition(task.ID.String(), evt.OldValue, evt.NewValue, "plan approved")
	o.logger.Info("plan approved", "task_id", task.ID, "subtask_count", len(subtaskPlans))
	return nil
}

// HandlePlanRejected clears the plan and transitions back to PLANNING.
func (o *Orchestrator) HandlePlanRejected(taskID uuid.UUID) error {
	var task model.Task
	if err := o.db.First(&task, "id = ?", taskID).Error; err != nil {
		return fmt.Errorf("handle plan rejected: load task: %w", err)
	}

	if task.Status != model.StatusPlanReview {
		return fmt.Errorf("handle plan rejected: task %s is in %s, expected plan_review", taskID, task.Status)
	}

	task.Plan = nil
	// Record plan rejection metric before clearing assignment
	if o.metrics != nil && task.AssignedAgentID != nil {
		o.metrics.Record(*task.AssignedAgentID, "plan_rejected", 1.0, nil)
	}
	task.AssignedAgentID = nil

	evt, err := state.TransitionTask(&task, model.StatusPlanning, "user", map[string]any{"action": "plan_rejected"})
	if err != nil {
		return fmt.Errorf("handle plan rejected: transition: %w", err)
	}
	if err := o.db.Save(&task).Error; err != nil {
		return fmt.Errorf("handle plan rejected: save task: %w", err)
	}
	if err := o.db.Create(evt).Error; err != nil {
		return fmt.Errorf("handle plan rejected: save event: %w", err)
	}

	o.emit("task_updated", &task)
	o.publishTaskTransition(task.ID.String(), evt.OldValue, evt.NewValue, "plan rejected")
	o.logger.Info("plan rejected", "task_id", task.ID)
	return nil
}

// HandleTestPassed transitions from TESTING_READY to MERGING.
func (o *Orchestrator) HandleTestPassed(taskID uuid.UUID) error {
	var task model.Task
	if err := o.db.First(&task, "id = ?", taskID).Error; err != nil {
		return fmt.Errorf("handle test passed: load task: %w", err)
	}

	if task.Status != model.StatusTestingReady {
		return fmt.Errorf("handle test passed: task %s is in %s, expected testing_ready", taskID, task.Status)
	}

	evt, err := state.TransitionTask(&task, model.StatusMerging, "user", map[string]any{"action": "test_passed"})
	if err != nil {
		return fmt.Errorf("handle test passed: transition: %w", err)
	}
	if err := o.db.Save(&task).Error; err != nil {
		return fmt.Errorf("handle test passed: save task: %w", err)
	}
	if err := o.db.Create(evt).Error; err != nil {
		return fmt.Errorf("handle test passed: save event: %w", err)
	}

	o.emit("task_updated", &task)
	o.publishTaskTransition(task.ID.String(), evt.OldValue, evt.NewValue, "test passed")
	o.logger.Info("test passed, task merging", "task_id", task.ID)
	return nil
}

// HandleTestFailed transitions from TESTING_READY back to IN_PROGRESS.
func (o *Orchestrator) HandleTestFailed(taskID uuid.UUID) error {
	var task model.Task
	if err := o.db.First(&task, "id = ?", taskID).Error; err != nil {
		return fmt.Errorf("handle test failed: load task: %w", err)
	}

	if task.Status != model.StatusTestingReady {
		return fmt.Errorf("handle test failed: task %s is in %s, expected testing_ready", taskID, task.Status)
	}

	task.AssignedAgentID = nil

	evt, err := state.TransitionTask(&task, model.StatusInProgress, "user", map[string]any{"action": "test_failed"})
	if err != nil {
		return fmt.Errorf("handle test failed: transition: %w", err)
	}
	if err := o.db.Save(&task).Error; err != nil {
		return fmt.Errorf("handle test failed: save task: %w", err)
	}
	if err := o.db.Create(evt).Error; err != nil {
		return fmt.Errorf("handle test failed: save event: %w", err)
	}

	o.emit("task_updated", &task)
	o.publishTaskTransition(task.ID.String(), evt.OldValue, evt.NewValue, "test failed")
	o.logger.Info("test failed, task back to in_progress", "task_id", task.ID)
	return nil
}

// HandleTestReviewApproved transitions a task from TEST_REVIEW to IN_PROGRESS.
func (o *Orchestrator) HandleTestReviewApproved(taskID uuid.UUID) error {
	var task model.Task
	if err := o.db.First(&task, "id = ?", taskID).Error; err != nil {
		return fmt.Errorf("handle test review approved: load task: %w", err)
	}

	if task.Status != model.StatusTestReview {
		return fmt.Errorf("handle test review approved: task %s is in %s, expected test_review", taskID, task.Status)
	}

	evt, err := state.TransitionTask(&task, model.StatusInProgress, "user", map[string]any{"action": "test_review_approved"})
	if err != nil {
		return fmt.Errorf("handle test review approved: transition: %w", err)
	}
	if err := o.db.Save(&task).Error; err != nil {
		return fmt.Errorf("handle test review approved: save task: %w", err)
	}
	if err := o.db.Create(evt).Error; err != nil {
		return fmt.Errorf("handle test review approved: save event: %w", err)
	}

	o.emit("task_updated", &task)
	o.publishTaskTransition(task.ID.String(), evt.OldValue, evt.NewValue, "test review approved")
	o.logger.Info("test review approved, scheduling implementation", "task_id", task.ID)
	return nil
}

// HandleTestReviewRejected marks rejected test subtasks as REJECTED, clones
// them with feedback, and transitions the parent back to TEST_WRITING.
// After 3 rejection rounds, pauses the task and spawns a diagnostic agent.
func (o *Orchestrator) HandleTestReviewRejected(taskID uuid.UUID, feedback string) error {
	var task model.Task
	if err := o.db.First(&task, "id = ?", taskID).Error; err != nil {
		return fmt.Errorf("handle test review rejected: load task: %w", err)
	}

	if task.Status != model.StatusTestReview {
		return fmt.Errorf("handle test review rejected: task %s is in %s, expected test_review", taskID, task.Status)
	}

	if task.Context == nil {
		task.Context = make(model.JSONField)
	}

	rejectionCount := 0
	if v, ok := task.Context["test_rejection_count"].(float64); ok {
		rejectionCount = int(v)
	}
	rejectionCount++
	task.Context["test_rejection_count"] = float64(rejectionCount)

	feedbackKey := fmt.Sprintf("test_rejection_feedback_%d", rejectionCount)
	task.Context[feedbackKey] = feedback

	if rejectionCount >= maxPlanRejections {
		evt1, err := state.TransitionTask(&task, model.StatusTestWriting, "user", map[string]any{
			"action": "test_review_rejected_diagnostic",
			"reason": "3 rejection rounds exceeded",
		})
		if err != nil {
			return fmt.Errorf("handle test review rejected: transition to test_writing: %w", err)
		}
		if err := o.db.Create(evt1).Error; err != nil {
			return fmt.Errorf("handle test review rejected: save event1: %w", err)
		}

		evt2, err := state.TransitionTask(&task, model.StatusPaused, "user", map[string]any{
			"action": "test_review_rejected_paused",
			"reason": "3 test rejection rounds exceeded, diagnostic required",
		})
		if err != nil {
			return fmt.Errorf("handle test review rejected: transition to paused: %w", err)
		}
		task.Context["paused_from"] = string(model.StatusTestWriting)
		task.Context["diagnostic_required"] = true

		if err := o.db.Save(&task).Error; err != nil {
			return fmt.Errorf("handle test review rejected: save task: %w", err)
		}
		if err := o.db.Create(evt2).Error; err != nil {
			return fmt.Errorf("handle test review rejected: save event2: %w", err)
		}

		o.emit("task_updated", &task)
		o.publishTaskTransition(task.ID.String(), evt1.OldValue, string(model.StatusPaused), "test review rejected 3 times, paused for diagnostic")
		o.logger.Warn("test review rejected 3 times, task paused for diagnostic",
			"task_id", task.ID, "rejection_count", rejectionCount)

		if err := o.spawnDiagnosticAgent(&task); err != nil {
			o.logger.Warn("failed to spawn diagnostic agent", "task_id", task.ID, "error", err)
		}
		return nil
	}

	var doneTestSubtasks []model.Task
	if err := o.db.Where(
		"parent_task_id = ? AND phase = ? AND status = ?",
		task.ID, "test", model.StatusDone,
	).Find(&doneTestSubtasks).Error; err != nil {
		return fmt.Errorf("handle test review rejected: query test subtasks: %w", err)
	}

	for i := range doneTestSubtasks {
		sub := &doneTestSubtasks[i]

		sub.Status = model.StatusRejected
		sub.UpdatedAt = time.Now()
		if err := o.db.Save(sub).Error; err != nil {
			return fmt.Errorf("handle test review rejected: reject subtask %s: %w", sub.ID, err)
		}

		rejectEvt := &model.TaskEvent{
			ID:        uuid.New(),
			TaskID:    sub.ID,
			EventType: "status_change",
			OldValue:  string(model.StatusDone),
			NewValue:  string(model.StatusRejected),
			Details:   model.JSONField{"action": "test_review_rejected", "feedback": feedback},
			Actor:     "user",
			CreatedAt: time.Now(),
		}
		if err := o.db.Create(rejectEvt).Error; err != nil {
			return fmt.Errorf("handle test review rejected: save reject event for %s: %w", sub.ID, err)
		}

		revisionSuffix := fmt.Sprintf(" (revision %d)", rejectionCount)
		newDescription := sub.Description + "\n\n## Rejection Feedback\n\n" + feedback

		var newCtx model.JSONField
		if sub.Context != nil {
			newCtx = make(model.JSONField, len(sub.Context))
			for k, v := range sub.Context {
				newCtx[k] = v
			}
		} else {
			newCtx = make(model.JSONField)
		}

		replacementID := uuid.New()
		replacement := model.Task{
			ID:            replacementID,
			ProjectID:     sub.ProjectID,
			ParentTaskID:  sub.ParentTaskID,
			Title:         sub.Title + revisionSuffix,
			Description:   newDescription,
			Status:        model.StatusBacklog,
			Priority:      sub.Priority,
			DependencyIDs: sub.DependencyIDs,
			Phase:         sub.Phase,
			TestsFor:      sub.TestsFor,
			Context:       newCtx,
		}

		if err := o.db.Create(&replacement).Error; err != nil {
			return fmt.Errorf("handle test review rejected: create replacement for %s: %w", sub.ID, err)
		}

		o.logger.Info("test subtask rejected and replaced",
			"original_id", sub.ID,
			"replacement_id", replacementID,
			"revision", rejectionCount)
	}

	evt, err := state.TransitionTask(&task, model.StatusTestWriting, "user", map[string]any{
		"action":          "test_review_rejected",
		"rejection_count": rejectionCount,
		"feedback":        feedback,
		"subtasks_cloned": len(doneTestSubtasks),
	})
	if err != nil {
		return fmt.Errorf("handle test review rejected: transition to test_writing: %w", err)
	}
	if err := o.db.Save(&task).Error; err != nil {
		return fmt.Errorf("handle test review rejected: save task: %w", err)
	}
	if err := o.db.Create(evt).Error; err != nil {
		return fmt.Errorf("handle test review rejected: save event: %w", err)
	}

	o.emit("task_updated", &task)
	o.publishTaskTransition(task.ID.String(), evt.OldValue, evt.NewValue, "test review rejected")
	o.logger.Info("test review rejected, back to test writing",
		"task_id", task.ID,
		"rejection_count", rejectionCount,
		"subtasks_cloned", len(doneTestSubtasks))
	return nil
}

// planEntry is an intermediate struct for parsing plans from JSON that may
// include dependency indices and TDD phase information.
type planEntry struct {
	Title          string           `json:"title"`
	Description    string           `json:"description"`
	AgentType      string           `json:"agent_type"`
	EstimatedFiles []string         `json:"estimated_files"`
	Files          []string         `json:"files"`
	Dependencies   []int            `json:"dependencies"`
	Priority       int              `json:"priority"`
	IsTest         bool             `json:"is_test,omitempty"`
	Phase          string           `json:"phase,omitempty"`
	TestsFor       []int            `json:"tests_for,omitempty"`
	DepthMeta      *score.DepthMeta `json:"depth_meta,omitempty"`
}

// tddException represents a planner-declared exception to TDD enforcement
// for a specific subtask.
type tddException struct {
	SubtaskIndex int    `json:"subtask_index"`
	Reason       string `json:"reason"`
}

// parsePlanResult holds the full parsed plan output.
type parsePlanResult struct {
	Subtasks      []planEntry
	TDDExceptions []tddException
	Assumptions   []clarification.Assumption // extracted from plan.json "assumptions" field
}

// parsePlan extracts subtask plans and TDD exceptions from a task's Plan JSONField.
func parsePlan(planField model.JSONField) (*parsePlanResult, error) {
	if planField == nil {
		return nil, fmt.Errorf("parse plan: plan is nil")
	}

	// The plan is stored as {"subtasks": [...]}.
	subtasksRaw, ok := planField["subtasks"]
	if !ok {
		return nil, fmt.Errorf("parse plan: no subtasks key in plan")
	}

	// Marshal back to JSON and unmarshal into planEntry slice.
	b, err := json.Marshal(subtasksRaw)
	if err != nil {
		return nil, fmt.Errorf("parse plan: marshal subtasks: %w", err)
	}

	var entries []planEntry
	if err := json.Unmarshal(b, &entries); err != nil {
		return nil, fmt.Errorf("parse plan: unmarshal subtasks: %w", err)
	}

	// Normalize: use "files" as fallback for "estimated_files".
	for i := range entries {
		if len(entries[i].EstimatedFiles) == 0 && len(entries[i].Files) > 0 {
			entries[i].EstimatedFiles = entries[i].Files
		}
		if entries[i].AgentType == "" {
			entries[i].AgentType = string(model.AgentCoder)
		}
	}

	// Extract TDD exceptions if present (backward compatible — missing key is not an error).
	var exceptions []tddException
	if exceptionsRaw, hasExceptions := planField["tdd_exceptions"]; hasExceptions {
		eb, err := json.Marshal(exceptionsRaw)
		if err != nil {
			return nil, fmt.Errorf("parse plan: marshal tdd_exceptions: %w", err)
		}
		if err := json.Unmarshal(eb, &exceptions); err != nil {
			return nil, fmt.Errorf("parse plan: unmarshal tdd_exceptions: %w", err)
		}
	}

	// Extract assumptions if present (backward compatible — missing key means empty slice).
	var assumptions []clarification.Assumption
	if assumptionsRaw, hasAssumptions := planField["assumptions"]; hasAssumptions {
		ab, err := json.Marshal(assumptionsRaw)
		if err != nil {
			return nil, fmt.Errorf("parse plan: marshal assumptions: %w", err)
		}
		if err := json.Unmarshal(ab, &assumptions); err != nil {
			return nil, fmt.Errorf("parse plan: unmarshal assumptions: %w", err)
		}
	}

	return &parsePlanResult{
		Subtasks:      entries,
		TDDExceptions: exceptions,
		Assumptions:   assumptions,
	}, nil
}

// HandleClarificationAnswer processes a user's answer to a clarification question.
// Called by the TUI when the user submits a comment on a NEEDS_CLARIFICATION task.
func (o *Orchestrator) HandleClarificationAnswer(taskID uuid.UUID, answer string) error {
	var task model.Task
	if err := o.db.First(&task, "id = ?", taskID).Error; err != nil {
		return fmt.Errorf("handle clarification answer: load task: %w", err)
	}

	if task.Status != model.StatusNeedsClarification {
		return fmt.Errorf("handle clarification answer: task %s is in %s, expected needs_clarification", taskID, task.Status)
	}

	if task.Context == nil {
		return fmt.Errorf("handle clarification answer: task %s has no context", taskID)
	}

	sessionData, ok := task.Context["clarification_session"]
	if !ok {
		return fmt.Errorf("handle clarification answer: no clarification_session in context")
	}

	updatedSession, done, nextQuestion, err := clarification.ProcessAnswer(sessionData, answer)
	if err != nil {
		return fmt.Errorf("handle clarification answer: process answer: %w", err)
	}
	task.Context["clarification_session"] = updatedSession

	if done {
		// All questions answered — build replan context and transition back to planning.
		replanCtx, err := clarification.ReplanContext(updatedSession)
		if err != nil {
			return fmt.Errorf("handle clarification answer: replan context: %w", err)
		}
		task.Context["clarification_context"] = replanCtx

		// Clear the plan so the planner re-plans with clarification context.
		task.Plan = nil

		event, err := state.TransitionTask(&task, model.StatusPlanning, "user", map[string]any{
			"action": "clarification_complete",
		})
		if err != nil {
			return fmt.Errorf("handle clarification answer: transition to planning: %w", err)
		}
		if err := o.db.Save(&task).Error; err != nil {
			return fmt.Errorf("handle clarification answer: save task: %w", err)
		}
		if err := o.db.Create(event).Error; err != nil {
			return fmt.Errorf("handle clarification answer: save event: %w", err)
		}
		o.emit("task_updated", &task)
		o.publishTaskTransition(task.ID.String(), event.OldValue, event.NewValue, "clarification complete, replanning")
		o.logger.Info("clarification complete, replanning", "task_id", task.ID)
		return nil
	}

	// More questions remain — store next question and save.
	task.Context["clarification_current_question"] = nextQuestion
	if err := o.db.Save(&task).Error; err != nil {
		return fmt.Errorf("handle clarification answer: save task: %w", err)
	}
	o.emit("task_updated", &task)
	o.logger.Info("clarification answer received, next question",
		"task_id", task.ID, "next_question", nextQuestion)
	return nil
}
