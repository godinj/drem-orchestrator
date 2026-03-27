package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/godinj/drem-orchestrator/internal/clarification"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/state"
	"github.com/godinj/drem-orchestrator/internal/supervisor"
)

// checkClarificationNeeds implements the clarification circuit-breaker for
// processPlanning. It applies two safeguards against infinite clarification
// loops:
//
//  1. Round cap (MaxClarificationRounds): after N rounds the orchestrator
//     skips clarification and proceeds to plan_review, recording
//     clarification_cap_reached in task.Context for observability.
//
//  2. Assumption dedup (FilterAnswered): assumptions whose decisions were
//     already addressed in a prior clarification session are removed before
//     Evaluate is called, preventing the same questions from resurfacing on
//     replan cycles.
//
// Returns true if the task was transitioned to needs_clarification (caller
// should return nil immediately). Returns false if no further clarification
// is required and the caller should proceed to plan_review.
func (o *Orchestrator) checkClarificationNeeds(task *model.Task) (bool, error) {
	// 1. Enforce round cap — skip to plan_review after MaxClarificationRounds.
	rounds := getClarificationRounds(task)
	if rounds >= clarification.MaxClarificationRounds {
		if task.Context == nil {
			task.Context = make(model.JSONField)
		}
		task.Context["clarification_cap_reached"] = true
		o.logger.Info("clarification round cap reached, skipping to plan_review",
			"task_id", task.ID, "rounds", rounds)
		return false, nil
	}

	// 2. Parse plan assumptions.
	planResult, err := parsePlan(task.Plan)
	if err != nil {
		o.logger.Warn("checkClarificationNeeds: parse plan failed", "task_id", task.ID, "error", err)
	}
	var assumptions []clarification.Assumption
	if planResult != nil {
		assumptions = planResult.Assumptions
	}

	// 3. Filter out assumptions already addressed in a prior session.
	if task.Context != nil && task.Context["clarification_session"] != nil {
		assumptions = clarification.FilterAnswered(assumptions, task.Context["clarification_session"])
	}

	// 4. If no assumptions remain after filtering, skip to plan_review.
	if len(assumptions) == 0 {
		return false, nil
	}

	// 5. Supervisor cross-check for any missed assumptions.
	var supervisorAnalysis string
	if o.supervisor != nil && planResult != nil {
		planJSON, _ := json.Marshal(task.Plan)
		assumptionsJSON, _ := json.Marshal(assumptions)
		crossCheckPrompt := supervisor.AssumptionCrossCheckPrompt(
			task.Title, task.Description, string(planJSON), string(assumptionsJSON),
		)
		var crossCheck supervisor.AssumptionCrossCheck
		if err := o.supervisor.EvaluateJSON(context.Background(), crossCheckPrompt, &crossCheck); err != nil {
			o.logger.Warn("supervisor assumption cross-check failed", "task_id", task.ID, "error", err)
		} else {
			analysisJSON, _ := json.Marshal(crossCheck.MissedAssumptions)
			supervisorAnalysis = string(analysisJSON)
		}
	}

	// 6. Evaluate clarification need.
	planJSON, _ := json.Marshal(task.Plan)
	evalResult, evalErr := clarification.Evaluate(string(planJSON), assumptions, supervisorAnalysis)
	if evalErr != nil {
		o.logger.Warn("clarification evaluate failed", "task_id", task.ID, "error", evalErr)
	}

	if evalResult == nil || !evalResult.NeedsClarification {
		return false, nil
	}

	// 7. Transition to needs_clarification and record the incremented round count.
	if task.Context == nil {
		task.Context = make(model.JSONField)
	}
	task.Context["clarification_session"] = evalResult.SessionData
	task.Context["clarification_questions"] = evalResult.Questions
	if len(evalResult.Questions) > 0 {
		task.Context["clarification_current_question"] = evalResult.Questions[0]
	}
	task.Context["clarification_rounds"] = float64(rounds + 1)

	event, transErr := state.TransitionTask(task, model.StatusNeedsClarification, "orchestrator", nil)
	if transErr != nil {
		return false, fmt.Errorf("checkClarificationNeeds: transition: %w", transErr)
	}
	if err := o.db.Save(task).Error; err != nil {
		return false, fmt.Errorf("checkClarificationNeeds: save task: %w", err)
	}
	if err := o.db.Create(event).Error; err != nil {
		return false, fmt.Errorf("checkClarificationNeeds: save event: %w", err)
	}
	o.emit("needs_clarification", map[string]any{"task_id": task.ID, "questions": evalResult.Questions})
	return true, nil
}

// getClarificationRounds reads the clarification round counter from
// task.Context. Returns 0 if the counter is absent or unparseable.
func getClarificationRounds(task *model.Task) int {
	if task.Context == nil {
		return 0
	}
	v, ok := task.Context["clarification_rounds"]
	if !ok {
		return 0
	}
	f, ok := v.(float64)
	if !ok {
		return 0
	}
	return int(f)
}
