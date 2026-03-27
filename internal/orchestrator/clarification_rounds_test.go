package orchestrator

import (
	"strings"
	"testing"

	"github.com/godinj/drem-orchestrator/internal/clarification"
	"github.com/godinj/drem-orchestrator/internal/model"
)

// makePlanWithAssumptions builds a minimal valid plan JSONField containing
// subtasks and the specified assumptions. Each assumption is given two
// alternatives so clarification.Evaluate will return NeedsClarification=true.
func makePlanWithAssumptions(decisions ...string) model.JSONField {
	assumptions := make([]any, len(decisions))
	for i, d := range decisions {
		assumptions[i] = map[string]any{
			"decision":     d,
			"alternatives": []any{"Option A", "Option B"},
			"why_chosen":   "test reasoning for " + d,
		}
	}
	return model.JSONField{
		"subtasks": []any{
			map[string]any{
				"title":           "implement feature",
				"description":     "test subtask",
				"agent_type":      "coder",
				"estimated_files": []any{"main.go"},
			},
		},
		"assumptions": assumptions,
	}
}

// ---------------------------------------------------------------------------
// Round counter increment tests
// ---------------------------------------------------------------------------

// TestProcessPlanning_FirstClarificationRound_SetsCounter verifies that when
// processPlanning transitions a task to needs_clarification for the first time,
// task.Context["clarification_rounds"] is set to 1.
func TestProcessPlanning_FirstClarificationRound_SetsCounter(t *testing.T) {
	o, db, projectID := setupQuickFixTest(t)

	task := createStandardTask(t, db, projectID, "add feature with assumptions", model.StatusPlanning)
	task.Plan = makePlanWithAssumptions("Use PostgreSQL for storage")
	if err := db.Save(&task).Error; err != nil {
		t.Fatalf("save task with plan: %v", err)
	}

	if err := o.processPlanning(&task); err != nil {
		t.Fatalf("processPlanning: %v", err)
	}

	var updated model.Task
	if err := db.First(&updated, "id = ?", task.ID).Error; err != nil {
		t.Fatalf("reload task: %v", err)
	}

	if updated.Status != model.StatusNeedsClarification {
		t.Errorf("expected needs_clarification on first round, got %q", updated.Status)
	}
	if updated.Context == nil {
		t.Fatal("expected non-nil task context after clarification round")
	}
	rounds := updated.Context["clarification_rounds"]
	if rounds != float64(1) {
		t.Errorf("expected clarification_rounds=1 after first round, got %v (type %T)", rounds, rounds)
	}
}

// TestProcessPlanning_CounterIncrements_FromOneToTwo verifies that the counter
// persists across replan cycles: starting at 1 it becomes 2 when a second
// needs_clarification transition is triggered.
func TestProcessPlanning_CounterIncrements_FromOneToTwo(t *testing.T) {
	o, db, projectID := setupQuickFixTest(t)

	task := createStandardTask(t, db, projectID, "feature after first clarification", model.StatusPlanning)
	task.Plan = makePlanWithAssumptions("Use Redis for caching")
	task.Context = model.JSONField{
		"clarification_rounds": float64(1),
	}
	if err := db.Save(&task).Error; err != nil {
		t.Fatalf("save task: %v", err)
	}

	if err := o.processPlanning(&task); err != nil {
		t.Fatalf("processPlanning: %v", err)
	}

	var updated model.Task
	if err := db.First(&updated, "id = ?", task.ID).Error; err != nil {
		t.Fatalf("reload task: %v", err)
	}

	if updated.Status != model.StatusNeedsClarification {
		t.Errorf("expected needs_clarification (1 < MaxClarificationRounds), got %q", updated.Status)
	}
	if updated.Context == nil {
		t.Fatal("expected non-nil context")
	}
	rounds := updated.Context["clarification_rounds"]
	if rounds != float64(2) {
		t.Errorf("expected clarification_rounds=2 (incremented from 1), got %v", rounds)
	}
}

// ---------------------------------------------------------------------------
// Cap enforcement tests
// ---------------------------------------------------------------------------

// TestProcessPlanning_CapReached_SkipsToPlanReview verifies that after
// MaxClarificationRounds rounds the orchestrator bypasses Evaluate and
// transitions directly to plan_review without further clarification.
func TestProcessPlanning_CapReached_SkipsToPlanReview(t *testing.T) {
	o, db, projectID := setupQuickFixTest(t)

	task := createStandardTask(t, db, projectID, "feature at round cap", model.StatusPlanning)
	task.Plan = makePlanWithAssumptions("Use PostgreSQL for storage")
	task.Context = model.JSONField{
		"clarification_rounds": float64(clarification.MaxClarificationRounds),
	}
	if err := db.Save(&task).Error; err != nil {
		t.Fatalf("save task: %v", err)
	}

	if err := o.processPlanning(&task); err != nil {
		t.Fatalf("processPlanning: %v", err)
	}

	var updated model.Task
	if err := db.First(&updated, "id = ?", task.ID).Error; err != nil {
		t.Fatalf("reload task: %v", err)
	}

	if updated.Status != model.StatusPlanReview {
		t.Errorf("expected plan_review when round cap hit, got %q", updated.Status)
	}
}

// TestProcessPlanning_CapReached_RecordsCapInContext verifies that when the
// clarification round cap is hit, task.Context["clarification_cap_reached"] is
// set to true so the log/observability layer can detect it.
func TestProcessPlanning_CapReached_RecordsCapInContext(t *testing.T) {
	o, db, projectID := setupQuickFixTest(t)

	task := createStandardTask(t, db, projectID, "feature at cap (observability)", model.StatusPlanning)
	task.Plan = makePlanWithAssumptions("Use PostgreSQL for storage")
	task.Context = model.JSONField{
		"clarification_rounds": float64(clarification.MaxClarificationRounds),
	}
	if err := db.Save(&task).Error; err != nil {
		t.Fatalf("save task: %v", err)
	}

	// Ignore the error — we are only checking context state.
	_ = o.processPlanning(&task)

	var updated model.Task
	if err := db.First(&updated, "id = ?", task.ID).Error; err != nil {
		t.Fatalf("reload task: %v", err)
	}

	if updated.Context == nil || updated.Context["clarification_cap_reached"] != true {
		t.Error("expected clarification_cap_reached=true in context when cap is hit (for log/observability)")
	}
}

// ---------------------------------------------------------------------------
// FilterAnswered integration tests
// ---------------------------------------------------------------------------

// TestProcessPlanning_AllPreviouslyAnswered_SkipsToPlanReview verifies that
// when every assumption in the new plan was already answered in a prior
// clarification session, processPlanning skips Evaluate and proceeds directly
// to plan_review without triggering another round.
func TestProcessPlanning_AllPreviouslyAnswered_SkipsToPlanReview(t *testing.T) {
	o, db, projectID := setupQuickFixTest(t)

	// Build an answered clarification session for "Use PostgreSQL".
	evalResult, err := clarification.Evaluate("{}", []clarification.Assumption{
		{Decision: "Use PostgreSQL", Alternatives: []string{"MySQL"}, WhyChosen: "JSON support"},
	}, "")
	if err != nil || !evalResult.NeedsClarification {
		t.Fatalf("setup evaluate: err=%v needs=%v", err, evalResult != nil && evalResult.NeedsClarification)
	}
	answeredSession, done, _, err := clarification.ProcessAnswer(evalResult.SessionData, "Yes, agreed")
	if err != nil {
		t.Fatalf("process answer: %v", err)
	}
	if !done {
		t.Fatal("expected session done after answering the only question")
	}

	// New plan contains the same assumption that was just answered.
	task := createStandardTask(t, db, projectID, "replanned with same assumption", model.StatusPlanning)
	task.Plan = makePlanWithAssumptions("Use PostgreSQL")
	// Store the answered session but NOT clarification_context, so the
	// existing coarse bypass is not triggered. The new code must use
	// FilterAnswered to reach plan_review.
	task.Context = model.JSONField{
		"clarification_session": answeredSession,
	}
	if err := db.Save(&task).Error; err != nil {
		t.Fatalf("save task: %v", err)
	}

	if err := o.processPlanning(&task); err != nil {
		t.Fatalf("processPlanning: %v", err)
	}

	var updated model.Task
	if err := db.First(&updated, "id = ?", task.ID).Error; err != nil {
		t.Fatalf("reload task: %v", err)
	}

	// FilterAnswered should have removed "Use PostgreSQL" → no assumptions
	// remain → skip Evaluate → plan_review.
	if updated.Status != model.StatusPlanReview {
		t.Errorf("expected plan_review when all assumptions previously answered, got %q", updated.Status)
	}
}

// TestProcessPlanning_PartiallyAnswered_EvaluatesRemaining verifies that when
// a plan contains both a previously-answered assumption and a new one, only
// the new assumption is evaluated. The task should enter needs_clarification
// with questions about the new assumption only, not the already-answered one.
func TestProcessPlanning_PartiallyAnswered_EvaluatesRemaining(t *testing.T) {
	o, db, projectID := setupQuickFixTest(t)

	// Answer only "Use PostgreSQL" in the prior session.
	evalResult, err := clarification.Evaluate("{}", []clarification.Assumption{
		{Decision: "Use PostgreSQL", Alternatives: []string{"MySQL"}},
	}, "")
	if err != nil || !evalResult.NeedsClarification {
		t.Fatalf("setup: %v", err)
	}
	answeredSession, done, _, err := clarification.ProcessAnswer(evalResult.SessionData, "Yes")
	if err != nil || !done {
		t.Fatalf("process answer: err=%v done=%v", err, done)
	}

	// Plan has both the answered assumption and a brand-new one.
	task := createStandardTask(t, db, projectID, "plan with old and new assumptions", model.StatusPlanning)
	task.Plan = makePlanWithAssumptions("Use PostgreSQL", "Use gRPC for API")
	task.Context = model.JSONField{
		"clarification_session": answeredSession,
	}
	if err := db.Save(&task).Error; err != nil {
		t.Fatalf("save task: %v", err)
	}

	if err := o.processPlanning(&task); err != nil {
		t.Fatalf("processPlanning: %v", err)
	}

	var updated model.Task
	if err := db.First(&updated, "id = ?", task.ID).Error; err != nil {
		t.Fatalf("reload task: %v", err)
	}

	// "Use gRPC for API" is new → should trigger clarification.
	if updated.Status != model.StatusNeedsClarification {
		t.Errorf("expected needs_clarification for remaining unanswered assumption, got %q", updated.Status)
	}

	// The clarification questions must not include the already-answered
	// "PostgreSQL" assumption — it should have been filtered out.
	if updated.Context != nil {
		if questions, ok := updated.Context["clarification_questions"].([]any); ok {
			for _, q := range questions {
				qStr, _ := q.(string)
				if strings.Contains(strings.ToLower(qStr), "postgresql") {
					t.Errorf("expected PostgreSQL assumption to be filtered from questions, but found it: %q", qStr)
				}
			}
		}
	}
}
