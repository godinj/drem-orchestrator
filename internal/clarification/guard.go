package clarification

// MaxClarificationRounds is the maximum number of planning → needs_clarification
// cycles before the orchestrator bypasses clarification and proceeds to plan_review.
// This prevents infinite loops when repeated replanning keeps generating assumptions.
const MaxClarificationRounds = 2

// ClarificationGuard encapsulates the circuit-breaker policy for the
// planning → needs_clarification loop. It unifies round tracking, assumption
// dedup, and the proceed/skip decision behind a single Check call so the
// orchestrator integration is a minimal method call rather than scattered
// conditionals across processPlanning.
type ClarificationGuard struct {
	rounds int
}

// NewGuard creates a ClarificationGuard initialised from the task context.
// It reads "clarification_rounds" from ctx to restore the current round count.
// A nil ctx is safe and produces a guard with rounds=0.
func NewGuard(ctx map[string]any) *ClarificationGuard {
	g := &ClarificationGuard{}
	if ctx == nil {
		return g
	}
	if v, ok := ctx["clarification_rounds"]; ok {
		if f, ok := v.(float64); ok {
			g.rounds = int(f)
		}
	}
	return g
}

// Check runs the full circuit-breaker policy in a single call:
//  1. Returns proceed=true, capHit=true when rounds >= MaxClarificationRounds.
//  2. Filters assumptions against the prior session via FilterAnswered.
//  3. Returns proceed=true, capHit=false when no assumptions survive filtering.
//  4. Otherwise delegates to Evaluate and returns its result.
//
// priorSession is the value from task.Context["clarification_session"] (nil is safe).
// When proceed=true the caller should transition to plan_review.
// When proceed=false the caller should transition to needs_clarification and
// must call RecordRound before persisting the task.
func (g *ClarificationGuard) Check(planJSON string, assumptions []Assumption, priorSession any, supervisorAnalysis string) (proceed bool, capHit bool, result *Result, err error) {
	if g.rounds >= MaxClarificationRounds {
		return true, true, nil, nil
	}

	filtered := FilterAnswered(assumptions, priorSession)
	if len(filtered) == 0 {
		return true, false, nil, nil
	}

	r, err := Evaluate(planJSON, filtered, supervisorAnalysis)
	if err != nil {
		return false, false, nil, err
	}
	if r == nil || !r.NeedsClarification {
		return true, false, r, nil
	}
	return false, false, r, nil
}

// RecordRound increments the round counter in ctx["clarification_rounds"] by
// one relative to the round count at NewGuard time. Must be called after a
// Check that returned proceed=false (i.e. after the task is committed to a
// needs_clarification transition).
func (g *ClarificationGuard) RecordRound(ctx map[string]any) {
	ctx["clarification_rounds"] = float64(g.rounds + 1)
	g.rounds++
}
