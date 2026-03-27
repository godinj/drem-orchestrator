package clarification

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// FilterAnswered tests
// ---------------------------------------------------------------------------

// TestFilterAnswered_NilSession_ReturnsAll verifies that when no prior session
// exists all assumptions pass through unchanged.
func TestFilterAnswered_NilSession_ReturnsAll(t *testing.T) {
	assumptions := []Assumption{
		{Decision: "Use PostgreSQL", Alternatives: []string{"MySQL"}},
		{Decision: "Use Redis", Alternatives: []string{"Memcached"}},
	}
	result := FilterAnswered(assumptions, nil)
	if len(result) != len(assumptions) {
		t.Errorf("expected %d assumptions with nil session, got %d", len(assumptions), len(result))
	}
}

// TestFilterAnswered_UnansweredSession_ReturnsAll verifies that a session with
// no answered questions (only asked) does not filter any assumptions.
func TestFilterAnswered_UnansweredSession_ReturnsAll(t *testing.T) {
	evalResult, err := Evaluate("{}", []Assumption{
		{Decision: "Use PostgreSQL", Alternatives: []string{"MySQL"}},
	}, "")
	if err != nil {
		t.Fatalf("setup evaluate: %v", err)
	}
	// Pass the unanswered session (no ProcessAnswer call yet).
	assumptions := []Assumption{
		{Decision: "Use PostgreSQL", Alternatives: []string{"MySQL"}},
		{Decision: "Use Redis"},
	}
	result := FilterAnswered(assumptions, evalResult.SessionData)
	if len(result) != len(assumptions) {
		t.Errorf("expected all %d assumptions with unanswered session, got %d", len(assumptions), len(result))
	}
}

// TestFilterAnswered_MatchingDecision_FiltersAssumption verifies that an
// assumption whose decision text overlaps with an already-answered question is
// removed from the returned slice.
func TestFilterAnswered_MatchingDecision_FiltersAssumption(t *testing.T) {
	evalResult, err := Evaluate("{}", []Assumption{
		{Decision: "Use PostgreSQL", Alternatives: []string{"MySQL"}, WhyChosen: "better JSON"},
	}, "")
	if err != nil || !evalResult.NeedsClarification {
		t.Fatalf("setup evaluate: err=%v needs=%v", err, evalResult != nil && evalResult.NeedsClarification)
	}
	answered, done, _, err := ProcessAnswer(evalResult.SessionData, "Yes, agreed")
	if err != nil {
		t.Fatalf("process answer: %v", err)
	}
	if !done {
		t.Fatal("expected session done after single answer")
	}

	assumptions := []Assumption{
		{Decision: "Use PostgreSQL", Alternatives: []string{"MySQL"}}, // answered — should be filtered
		{Decision: "Use Redis for caching"},                           // new — should remain
	}
	result := FilterAnswered(assumptions, answered)

	if len(result) != 1 {
		t.Fatalf("expected 1 remaining assumption after filtering, got %d: %v", len(result), result)
	}
	if result[0].Decision != "Use Redis for caching" {
		t.Errorf("wrong assumption survived filtering: %q", result[0].Decision)
	}
}

// TestFilterAnswered_AllMatchingDecisions_ReturnsEmpty verifies that when every
// assumption in the list matches an answered question the returned slice is empty.
func TestFilterAnswered_AllMatchingDecisions_ReturnsEmpty(t *testing.T) {
	assumptions := []Assumption{
		{Decision: "Use PostgreSQL", Alternatives: []string{"MySQL"}},
	}
	evalResult, err := Evaluate("{}", assumptions, "")
	if err != nil || !evalResult.NeedsClarification {
		t.Fatalf("setup: %v", err)
	}
	answered, _, _, err := ProcessAnswer(evalResult.SessionData, "Yes, PostgreSQL is fine")
	if err != nil {
		t.Fatalf("process answer: %v", err)
	}

	result := FilterAnswered(assumptions, answered)
	if len(result) != 0 {
		t.Errorf("expected empty result when all assumptions answered, got %d: %v", len(result), result)
	}
}

// TestFilterAnswered_PartialMatch_ReturnsOnlyUnmatched verifies that only
// unmatched assumptions are returned when the session covers a proper subset.
func TestFilterAnswered_PartialMatch_ReturnsOnlyUnmatched(t *testing.T) {
	// Build a session that answers only "Use PostgreSQL".
	evalResult, err := Evaluate("{}", []Assumption{
		{Decision: "Use PostgreSQL", Alternatives: []string{"MySQL"}, WhyChosen: "JSON support"},
	}, "")
	if err != nil || !evalResult.NeedsClarification {
		t.Fatalf("setup: %v", err)
	}
	answered, _, _, err := ProcessAnswer(evalResult.SessionData, "Yes, agreed")
	if err != nil {
		t.Fatalf("process answer: %v", err)
	}

	assumptions := []Assumption{
		{Decision: "Use PostgreSQL", Alternatives: []string{"MySQL"}},            // answered — filtered
		{Decision: "Use Redis for caching", Alternatives: []string{"Memcached"}}, // new — remains
	}
	result := FilterAnswered(assumptions, answered)

	if len(result) != 1 {
		t.Fatalf("expected 1 unfiltered assumption, got %d", len(result))
	}
	if result[0].Decision != "Use Redis for caching" {
		t.Errorf("wrong assumption survived filtering: %q", result[0].Decision)
	}
}

// TestFilterAnswered_CaseInsensitive_Filters verifies that the match is
// case-insensitive so a decision in ALL-CAPS matches a lowercase answered
// question.
func TestFilterAnswered_CaseInsensitive_Filters(t *testing.T) {
	evalResult, err := Evaluate("{}", []Assumption{
		{Decision: "use postgresql for storage", Alternatives: []string{"MySQL"}},
	}, "")
	if err != nil || !evalResult.NeedsClarification {
		t.Fatalf("setup: %v", err)
	}
	answered, _, _, err := ProcessAnswer(evalResult.SessionData, "Yes")
	if err != nil {
		t.Fatalf("process answer: %v", err)
	}

	// Assumption with different casing — should still be filtered out.
	result := FilterAnswered([]Assumption{
		{Decision: "USE POSTGRESQL FOR STORAGE"},
	}, answered)

	if len(result) != 0 {
		t.Errorf("expected case-insensitive filter to remove assumption, got %d remaining: %v", len(result), result)
	}
}

// ---------------------------------------------------------------------------
// ClarificationGuard.NewGuard tests
// ---------------------------------------------------------------------------

// TestGuard_NewGuard_ReadsRoundsFromContext verifies that NewGuard initialises
// the guard's round count from the "clarification_rounds" context key.
func TestGuard_NewGuard_ReadsRoundsFromContext(t *testing.T) {
	ctx := map[string]any{
		"clarification_rounds": float64(1),
	}
	g := NewGuard(ctx)
	if g == nil {
		t.Fatal("expected non-nil guard")
	}
	if g.rounds != 1 {
		t.Errorf("expected guard rounds=1 from context, got %d", g.rounds)
	}
}

// TestGuard_NewGuard_ZeroWhenContextEmpty verifies that rounds defaults to 0
// when the context map has no "clarification_rounds" key.
func TestGuard_NewGuard_ZeroWhenContextEmpty(t *testing.T) {
	g := NewGuard(map[string]any{})
	if g.rounds != 0 {
		t.Errorf("expected rounds=0 with empty context, got %d", g.rounds)
	}
}

// TestGuard_NewGuard_ZeroWhenContextNil verifies that a nil context is safe.
func TestGuard_NewGuard_ZeroWhenContextNil(t *testing.T) {
	g := NewGuard(nil)
	if g == nil || g.rounds != 0 {
		t.Errorf("expected rounds=0 with nil context, got guard=%v", g)
	}
}

// ---------------------------------------------------------------------------
// ClarificationGuard.Check tests
// ---------------------------------------------------------------------------

// TestGuard_Check_CapReached_ReturnsProceedAndCapHit verifies that when the
// guard's round count equals MaxClarificationRounds, Check returns
// proceed=true and capHit=true without calling Evaluate.
func TestGuard_Check_CapReached_ReturnsProceedAndCapHit(t *testing.T) {
	ctx := map[string]any{
		"clarification_rounds": float64(MaxClarificationRounds),
	}
	g := NewGuard(ctx)

	assumptions := []Assumption{
		{Decision: "Use PostgreSQL", Alternatives: []string{"MySQL"}},
	}
	proceed, capHit, result, err := g.Check("{}", assumptions, nil, "")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !proceed {
		t.Error("expected proceed=true when cap is reached")
	}
	if !capHit {
		t.Error("expected capHit=true when cap is reached")
	}
	if result != nil && result.NeedsClarification {
		t.Error("expected no clarification when cap is reached")
	}
}

// TestGuard_Check_CapExceeded_ReturnsProceedAndCapHit verifies the same
// circuit-breaker behaviour when rounds strictly exceeds the cap.
func TestGuard_Check_CapExceeded_ReturnsProceedAndCapHit(t *testing.T) {
	ctx := map[string]any{
		"clarification_rounds": float64(MaxClarificationRounds + 1),
	}
	g := NewGuard(ctx)

	proceed, capHit, _, err := g.Check("{}", []Assumption{{Decision: "Use PostgreSQL"}}, nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !proceed {
		t.Error("expected proceed=true when rounds exceed cap")
	}
	if !capHit {
		t.Error("expected capHit=true when rounds exceed cap")
	}
}

// TestGuard_Check_AllFiltered_ReturnsProceedNoCap verifies that when all
// assumptions are filtered by a prior session, Check returns proceed=true and
// capHit=false (distinguishable from the cap-reached case).
func TestGuard_Check_AllFiltered_ReturnsProceedNoCap(t *testing.T) {
	// Build an answered session for "Use PostgreSQL".
	evalResult, err := Evaluate("{}", []Assumption{
		{Decision: "Use PostgreSQL", Alternatives: []string{"MySQL"}},
	}, "")
	if err != nil || !evalResult.NeedsClarification {
		t.Fatalf("setup: %v", err)
	}
	answered, _, _, err := ProcessAnswer(evalResult.SessionData, "Yes")
	if err != nil {
		t.Fatalf("process answer: %v", err)
	}

	g := NewGuard(nil) // rounds = 0, cap not reached
	assumptions := []Assumption{
		{Decision: "Use PostgreSQL", Alternatives: []string{"MySQL"}},
	}
	proceed, capHit, result, err := g.Check("{}", assumptions, answered, "")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !proceed {
		t.Error("expected proceed=true when all assumptions are filtered")
	}
	if capHit {
		t.Error("expected capHit=false (filter bypass, not cap bypass)")
	}
	if result != nil && result.NeedsClarification {
		t.Error("expected no clarification when all assumptions filtered")
	}
}

// TestGuard_Check_AssumptionsPresent_ReturnsClarification verifies that when
// the cap is not reached and assumptions survive filtering, Check delegates to
// Evaluate and returns proceed=false with a non-nil Result.
func TestGuard_Check_AssumptionsPresent_ReturnsClarification(t *testing.T) {
	g := NewGuard(nil) // rounds = 0

	assumptions := []Assumption{
		{Decision: "Use PostgreSQL", Alternatives: []string{"MySQL"}},
	}
	proceed, capHit, result, err := g.Check("{}", assumptions, nil, "")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if proceed {
		t.Error("expected proceed=false when assumptions present and cap not reached")
	}
	if capHit {
		t.Error("expected capHit=false when cap not reached")
	}
	if result == nil {
		t.Fatal("expected non-nil result when clarification needed")
	}
	if !result.NeedsClarification {
		t.Error("expected NeedsClarification=true")
	}
	if len(result.Questions) == 0 {
		t.Error("expected at least one question in result")
	}
	if !strings.Contains(result.Questions[0], "PostgreSQL") {
		t.Errorf("expected question to mention PostgreSQL, got: %q", result.Questions[0])
	}
}

// ---------------------------------------------------------------------------
// ClarificationGuard.RecordRound tests
// ---------------------------------------------------------------------------

// TestGuard_RecordRound_SetsCounterFromZero verifies that RecordRound sets
// "clarification_rounds" to 1 in an empty context map.
func TestGuard_RecordRound_SetsCounterFromZero(t *testing.T) {
	ctx := map[string]any{}
	g := NewGuard(ctx)

	g.RecordRound(ctx)

	rounds, ok := ctx["clarification_rounds"]
	if !ok {
		t.Fatal("expected clarification_rounds to be set after RecordRound")
	}
	if rounds != float64(1) {
		t.Errorf("expected rounds=1 after first RecordRound, got %v", rounds)
	}
}

// TestGuard_RecordRound_IncrementsExistingCounter verifies that RecordRound
// increments an existing counter rather than resetting it.
func TestGuard_RecordRound_IncrementsExistingCounter(t *testing.T) {
	ctx := map[string]any{
		"clarification_rounds": float64(1),
	}
	g := NewGuard(ctx)

	g.RecordRound(ctx)

	rounds := ctx["clarification_rounds"]
	if rounds != float64(2) {
		t.Errorf("expected rounds=2 after RecordRound from 1, got %v", rounds)
	}
}
