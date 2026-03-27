package clarification

import (
	"testing"
)

// --- AnsweredDecisions tests ---

// TestAnsweredDecisions_AfterAllAnswered verifies that after answering every
// question in a session the returned slice contains the Decision string for
// each assumption.
func TestAnsweredDecisions_AfterAllAnswered(t *testing.T) {
	assumptions := []Assumption{
		{Decision: "Use SQLite", Alternatives: []string{"Postgres"}, WhyChosen: "simplicity"},
		{Decision: "Use REST", Alternatives: []string{"gRPC"}, WhyChosen: "familiarity"},
	}

	result, err := Evaluate("{}", assumptions, "")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	session := result.SessionData
	session, _, _, err = ProcessAnswer(session, "Yes, SQLite is fine")
	if err != nil {
		t.Fatalf("ProcessAnswer 1: %v", err)
	}
	session, _, _, err = ProcessAnswer(session, "Yes, REST is fine")
	if err != nil {
		t.Fatalf("ProcessAnswer 2: %v", err)
	}

	decided, err := AnsweredDecisions(session)
	if err != nil {
		t.Fatalf("AnsweredDecisions: %v", err)
	}
	if len(decided) != 2 {
		t.Fatalf("expected 2 answered decisions, got %d: %v", len(decided), decided)
	}
	// Both decisions must appear (order may vary).
	hasSQLite := false
	hasREST := false
	for _, d := range decided {
		if decisionsOverlap(d, "Use SQLite") {
			hasSQLite = true
		}
		if decisionsOverlap(d, "Use REST") {
			hasREST = true
		}
	}
	if !hasSQLite {
		t.Errorf("expected 'Use SQLite' in answered decisions, got: %v", decided)
	}
	if !hasREST {
		t.Errorf("expected 'Use REST' in answered decisions, got: %v", decided)
	}
}

// TestAnsweredDecisions_NoAnswers verifies that a session where the user
// typed /done immediately (no real answers) returns an empty slice.
func TestAnsweredDecisions_NoAnswers(t *testing.T) {
	assumptions := []Assumption{
		{Decision: "Use Kafka", Alternatives: []string{"RabbitMQ"}, WhyChosen: "throughput"},
	}

	result, err := Evaluate("{}", assumptions, "")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	session, _, _, err := ProcessAnswer(result.SessionData, "/done")
	if err != nil {
		t.Fatalf("ProcessAnswer: %v", err)
	}

	decided, err := AnsweredDecisions(session)
	if err != nil {
		t.Fatalf("AnsweredDecisions: %v", err)
	}
	if len(decided) != 0 {
		t.Errorf("expected empty slice when only /done was given, got: %v", decided)
	}
}

// TestAnsweredDecisions_EmptySession verifies that a session produced from an
// Evaluate call with no assumptions returns an empty slice.
func TestAnsweredDecisions_EmptySession(t *testing.T) {
	result, err := Evaluate("{}", nil, "")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	// No ProcessAnswer calls — session is complete with no rounds.
	decided, err := AnsweredDecisions(result.SessionData)
	if err != nil {
		t.Fatalf("AnsweredDecisions on empty session: %v", err)
	}
	if len(decided) != 0 {
		t.Errorf("expected empty slice for session with no questions, got: %v", decided)
	}
}

// TestAnsweredDecisions_OnlyRealAnswers verifies that only the decisions where
// the user provided a real answer (not /done) are included. In a three-question
// session where the first two are answered and /done is used on the third, only
// two decisions should be returned.
func TestAnsweredDecisions_OnlyRealAnswers(t *testing.T) {
	assumptions := []Assumption{
		{Decision: "Use Redis", Alternatives: []string{"Memcached"}, WhyChosen: "features"},
		{Decision: "Use Docker", Alternatives: []string{"Podman"}, WhyChosen: "ecosystem"},
		{Decision: "Use Nginx", Alternatives: []string{"Caddy"}, WhyChosen: "stability"},
	}

	result, err := Evaluate("{}", assumptions, "")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	session := result.SessionData
	session, _, _, err = ProcessAnswer(session, "Yes, Redis works")
	if err != nil {
		t.Fatalf("ProcessAnswer 1: %v", err)
	}
	session, _, _, err = ProcessAnswer(session, "Docker is fine")
	if err != nil {
		t.Fatalf("ProcessAnswer 2: %v", err)
	}
	session, _, _, err = ProcessAnswer(session, "/done")
	if err != nil {
		t.Fatalf("ProcessAnswer /done: %v", err)
	}

	decided, err := AnsweredDecisions(session)
	if err != nil {
		t.Fatalf("AnsweredDecisions: %v", err)
	}
	if len(decided) != 2 {
		t.Fatalf("expected 2 real answers (not /done), got %d: %v", len(decided), decided)
	}
}

// --- FilterAnswered tests ---

// TestFilterAnswered_RemovesOverlap verifies that assumptions whose Decision
// overlaps with an answered decision are removed from the result.
func TestFilterAnswered_RemovesOverlap(t *testing.T) {
	assumptions := []Assumption{
		{Decision: "Use SQLite for storage", Alternatives: []string{"Postgres"}, WhyChosen: "simplicity"},
		{Decision: "Use Redis for caching", Alternatives: []string{"Memcached"}, WhyChosen: "features"},
	}
	// "SQLite for storage" is a substring of the first assumption's Decision.
	answered := []string{"SQLite for storage"}

	filtered := FilterAnswered(assumptions, answered)
	if len(filtered) != 1 {
		t.Fatalf("expected 1 assumption after filtering, got %d: %v", len(filtered), filtered)
	}
	if decisionsOverlap(filtered[0].Decision, "SQLite") {
		t.Errorf("expected SQLite assumption to be filtered out, got: %s", filtered[0].Decision)
	}
	if !decisionsOverlap(filtered[0].Decision, "Redis") {
		t.Errorf("expected Redis assumption to remain, got: %s", filtered[0].Decision)
	}
}

// TestFilterAnswered_KeepsNonOverlap verifies that assumptions with no overlap
// to any answered decision are preserved.
func TestFilterAnswered_KeepsNonOverlap(t *testing.T) {
	assumptions := []Assumption{
		{Decision: "Use PostgreSQL", Alternatives: []string{"MySQL"}, WhyChosen: "features"},
		{Decision: "Deploy on Kubernetes", Alternatives: []string{"ECS"}, WhyChosen: "expertise"},
	}
	// Answered decision does not overlap with either assumption.
	answered := []string{"Use Redis"}

	filtered := FilterAnswered(assumptions, answered)
	if len(filtered) != 2 {
		t.Fatalf("expected both assumptions kept when no overlap, got %d: %v", len(filtered), filtered)
	}
}

// TestFilterAnswered_EmptyAnswered verifies that passing an empty answered list
// returns all assumptions unchanged.
func TestFilterAnswered_EmptyAnswered(t *testing.T) {
	assumptions := []Assumption{
		{Decision: "Use gRPC", Alternatives: []string{"REST"}, WhyChosen: "performance"},
		{Decision: "Use TypeScript", Alternatives: []string{"JavaScript"}, WhyChosen: "types"},
	}

	filtered := FilterAnswered(assumptions, nil)
	if len(filtered) != 2 {
		t.Fatalf("expected all assumptions returned for nil answered, got %d", len(filtered))
	}

	filtered = FilterAnswered(assumptions, []string{})
	if len(filtered) != 2 {
		t.Fatalf("expected all assumptions returned for empty answered, got %d", len(filtered))
	}
}

// TestFilterAnswered_AllAnswered verifies that when every assumption has an
// overlapping answered decision, the result is an empty slice.
func TestFilterAnswered_AllAnswered(t *testing.T) {
	assumptions := []Assumption{
		{Decision: "Use WebSocket", Alternatives: []string{"SSE"}, WhyChosen: "bidirectional"},
		{Decision: "Use React", Alternatives: []string{"Vue"}, WhyChosen: "ecosystem"},
	}
	answered := []string{"WebSocket", "React"}

	filtered := FilterAnswered(assumptions, answered)
	if len(filtered) != 0 {
		t.Fatalf("expected empty slice when all assumptions answered, got %d: %v", len(filtered), filtered)
	}
}

// TestFilterAnswered_CaseInsensitiveOverlap verifies that overlap detection is
// case-insensitive, consistent with decisionsOverlap.
func TestFilterAnswered_CaseInsensitiveOverlap(t *testing.T) {
	assumptions := []Assumption{
		{Decision: "Use SQLite for storage", Alternatives: []string{"Postgres"}, WhyChosen: "simplicity"},
	}
	// Upper-case variant should still match.
	answered := []string{"SQLITE FOR STORAGE"}

	filtered := FilterAnswered(assumptions, answered)
	if len(filtered) != 0 {
		t.Fatalf("expected SQLite assumption removed by case-insensitive match, got %d: %v", len(filtered), filtered)
	}
}

// TestFilterAnswered_NilAssumptions verifies that passing a nil assumptions
// slice does not panic and returns an empty slice.
func TestFilterAnswered_NilAssumptions(t *testing.T) {
	filtered := FilterAnswered(nil, []string{"anything"})
	if filtered == nil {
		// Nil is acceptable, but must not panic.
		return
	}
	if len(filtered) != 0 {
		t.Errorf("expected empty result for nil assumptions, got %d", len(filtered))
	}
}

// TestAnsweredDecisions_IntegrationWithFilterAnswered is an end-to-end test
// verifying that AnsweredDecisions and FilterAnswered work together: after a
// completed clarification session, new assumptions that overlap with answered
// ones are removed, and only genuinely new assumptions survive.
func TestAnsweredDecisions_IntegrationWithFilterAnswered(t *testing.T) {
	// First clarification round: two assumptions, both answered.
	firstRoundAssumptions := []Assumption{
		{Decision: "Use SQLite for storage", Alternatives: []string{"Postgres"}, WhyChosen: "simplicity"},
		{Decision: "Deploy on AWS", Alternatives: []string{"GCP"}, WhyChosen: "existing infra"},
	}

	result, err := Evaluate("{}", firstRoundAssumptions, "")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	session := result.SessionData
	session, _, _, err = ProcessAnswer(session, "Yes, SQLite is correct")
	if err != nil {
		t.Fatalf("ProcessAnswer 1: %v", err)
	}
	session, _, _, err = ProcessAnswer(session, "AWS is fine")
	if err != nil {
		t.Fatalf("ProcessAnswer 2: %v", err)
	}

	answered, err := AnsweredDecisions(session)
	if err != nil {
		t.Fatalf("AnsweredDecisions: %v", err)
	}
	if len(answered) != 2 {
		t.Fatalf("expected 2 answered decisions, got %d", len(answered))
	}

	// Second clarification round: three assumptions — two old (overlap) + one new.
	secondRoundAssumptions := []Assumption{
		{Decision: "Use SQLite for storage", Alternatives: []string{"Postgres"}, WhyChosen: "simplicity"},
		{Decision: "Deploy on AWS", Alternatives: []string{"GCP"}, WhyChosen: "existing infra"},
		{Decision: "Use gRPC for API", Alternatives: []string{"REST"}, WhyChosen: "performance"},
	}

	filtered := FilterAnswered(secondRoundAssumptions, answered)
	if len(filtered) != 1 {
		t.Fatalf("expected 1 new assumption after filtering, got %d: %v", len(filtered), filtered)
	}
	if !decisionsOverlap(filtered[0].Decision, "gRPC") {
		t.Errorf("expected the gRPC assumption to survive filtering, got: %s", filtered[0].Decision)
	}
}
