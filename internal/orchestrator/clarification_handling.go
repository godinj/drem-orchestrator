package orchestrator

import (
	"encoding/json"

	"github.com/godinj/drem-orchestrator/internal/clarification"
	"github.com/godinj/drem-orchestrator/internal/model"
)

// clarificationCheck holds the outcome of decideClarification. The opaque
// SessionData and Questions fields let task_processing.go act on the result
// without importing the clarification package, keeping internal import counts
// within the orchestrator's shrink-only ceiling.
type clarificationCheck struct {
	Proceed     bool // true = skip to plan_review; false = needs clarification
	CapReached  bool // true = round cap hit; Proceed will also be true
	SessionData any
	Questions   []string
	Rounds      int // current round count before any increment
}

// decideClarification runs the full clarification circuit-breaker policy and
// returns a decision struct. The caller (processPlanning in task_processing.go)
// is responsible for all state transitions and DB persistence. This split keeps
// the heavy clarification logic out of task_processing.go while staying within
// the orchestrator package's internal import ceiling.
//
// Two safeguards are applied:
//  1. Round cap: returns Proceed=true, CapReached=true after MaxClarificationRounds.
//  2. Assumption dedup: FilterAnswered removes previously-answered assumptions;
//     if none remain, returns Proceed=true, CapReached=false.
func (o *Orchestrator) decideClarification(task *model.Task) clarificationCheck {
	rounds := getClarificationRounds(task)
	if rounds >= clarification.MaxClarificationRounds {
		return clarificationCheck{Proceed: true, CapReached: true, Rounds: rounds}
	}

	planResult, err := parsePlan(task.Plan)
	if err != nil {
		o.logger.Warn("decideClarification: parse plan failed", "task_id", task.ID, "error", err)
	}
	var assumptions []clarification.Assumption
	if planResult != nil {
		assumptions = planResult.Assumptions
	}

	var priorSession any
	if task.Context != nil {
		priorSession = task.Context["clarification_session"]
	}

	g := clarification.NewGuard(task.Context)
	planJSONBytes, _ := json.Marshal(task.Plan)
	proceed, capHit, result, evalErr := g.Check(string(planJSONBytes), assumptions, priorSession, "")
	if evalErr != nil {
		o.logger.Warn("decideClarification: evaluate failed", "task_id", task.ID, "error", evalErr)
		return clarificationCheck{Proceed: true, Rounds: rounds}
	}

	if proceed {
		return clarificationCheck{Proceed: true, CapReached: capHit, Rounds: rounds}
	}

	var questions []string
	var sessionData any
	if result != nil {
		questions = result.Questions
		sessionData = result.SessionData
	}
	return clarificationCheck{
		Proceed:     false,
		SessionData: sessionData,
		Questions:   questions,
		Rounds:      rounds,
	}
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
