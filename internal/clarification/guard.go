package clarification

// MaxClarificationRounds is the maximum number of planning → needs_clarification
// cycles before the orchestrator bypasses clarification and proceeds to plan_review.
// This prevents infinite loops when repeated replanning keeps generating assumptions.
const MaxClarificationRounds = 2

// ClarificationGuard encapsulates the circuit-breaker policy for the
// planning → needs_clarification loop. It unifies round tracking, assumption
// dedup, and the proceed/skip decision behind a single Check call so the
// orchestrator integration is a minimal one-liner.
type ClarificationGuard struct {
	rounds int
}

// NewGuard creates a ClarificationGuard initialised from the task context.
// It reads "clarification_rounds" from ctx to restore the current round count.
func NewGuard(ctx map[string]any) *ClarificationGuard {
	// stub: returns zero-value guard regardless of ctx
	return &ClarificationGuard{}
}

// Check runs the full circuit-breaker policy in a single call:
//  1. Returns proceed=true, capHit=true when rounds >= MaxClarificationRounds.
//  2. Filters assumptions against the prior session via FilterAnswered.
//  3. Returns proceed=true, capHit=false when no assumptions survive filtering.
//  4. Otherwise delegates to Evaluate and returns its result.
//
// priorSession is the value from task.Context["clarification_session"].
// When proceed=true the caller should transition to plan_review.
// When proceed=false the caller should transition to needs_clarification and
// must call RecordRound before persisting the task.
func (g *ClarificationGuard) Check(planJSON string, assumptions []Assumption, priorSession any, supervisorAnalysis string) (proceed bool, capHit bool, result *Result, err error) {
	// stub: always signals clarification needed without evaluating anything
	return false, false, nil, nil
}

// RecordRound increments the round counter in ctx["clarification_rounds"].
// Must be called after a Check that returned proceed=false (i.e. after the
// task is committed to a needs_clarification transition).
func (g *ClarificationGuard) RecordRound(ctx map[string]any) {
	// stub: no-op
}

// FilterAnswered returns the subset of assumptions whose decision text does not
// overlap with any question already answered in priorSessionData.
// Returns all assumptions unchanged when priorSessionData is nil or contains
// no answered questions.
func FilterAnswered(assumptions []Assumption, priorSessionData any) []Assumption {
	// stub: returns all assumptions unfiltered
	return assumptions
}
