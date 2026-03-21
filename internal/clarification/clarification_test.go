package clarification

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// --- Evaluate tests ---

func TestEvaluate_EmptyInputs(t *testing.T) {
	result, err := Evaluate("{}", nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.NeedsClarification {
		t.Error("expected NeedsClarification=false for empty inputs")
	}
	if len(result.Questions) != 0 {
		t.Errorf("expected no questions, got %d", len(result.Questions))
	}
}

func TestEvaluate_PlannerAssumptionsOnly(t *testing.T) {
	assumptions := []Assumption{
		{
			Decision:     "Use SQLite for storage",
			Alternatives: []string{"PostgreSQL", "MySQL"},
			WhyChosen:    "Simpler deployment",
		},
		{
			Decision:     "Use REST API",
			Alternatives: []string{"GraphQL"},
			WhyChosen:    "Team familiarity",
		},
	}

	result, err := Evaluate("{}", assumptions, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.NeedsClarification {
		t.Error("expected NeedsClarification=true")
	}
	if len(result.Questions) != 2 {
		t.Errorf("expected 2 questions, got %d", len(result.Questions))
	}
	// First question should be about SQLite (2 alternatives > 1 alternative).
	if !strings.Contains(result.Questions[0], "SQLite") {
		t.Errorf("expected first question about SQLite (more alternatives), got: %s", result.Questions[0])
	}
	if result.SessionData == nil {
		t.Error("expected non-nil SessionData")
	}
}

func TestEvaluate_SupervisorAnalysisOnly(t *testing.T) {
	supervisorJSON := `[{"decision": "Chosen monorepo structure", "reasoning": "May cause build issues"}]`

	result, err := Evaluate("{}", nil, supervisorJSON)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.NeedsClarification {
		t.Error("expected NeedsClarification=true")
	}
	if len(result.Questions) != 1 {
		t.Errorf("expected 1 question, got %d", len(result.Questions))
	}
	if !strings.Contains(result.Questions[0], "monorepo") {
		t.Errorf("expected question about monorepo, got: %s", result.Questions[0])
	}
}

func TestEvaluate_MergedAndDeduplicated(t *testing.T) {
	assumptions := []Assumption{
		{
			Decision:     "Use SQLite for storage",
			Alternatives: []string{"PostgreSQL", "MySQL"},
			WhyChosen:    "Simpler deployment",
		},
	}

	// Supervisor also mentions SQLite — should be deduplicated.
	supervisorJSON := `[{"decision": "SQLite for storage", "reasoning": "Potential scaling issues"}]`

	result, err := Evaluate("{}", assumptions, supervisorJSON)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.NeedsClarification {
		t.Error("expected NeedsClarification=true")
	}
	// Should be deduplicated to 1 question.
	if len(result.Questions) != 1 {
		t.Errorf("expected 1 question after deduplication, got %d: %v", len(result.Questions), result.Questions)
	}
}

func TestEvaluate_MalformedSupervisorJSON(t *testing.T) {
	assumptions := []Assumption{
		{
			Decision:     "Use Go modules",
			Alternatives: []string{"dep", "glide"},
			WhyChosen:    "Standard tooling",
		},
	}

	// Malformed JSON — should be handled gracefully.
	result, err := Evaluate("{}", assumptions, "this is not json at all {broken")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.NeedsClarification {
		t.Error("expected NeedsClarification=true (planner assumptions still present)")
	}
	if len(result.Questions) != 1 {
		t.Errorf("expected 1 question from planner, got %d", len(result.Questions))
	}
}

func TestEvaluate_SingleAssumption(t *testing.T) {
	assumptions := []Assumption{
		{
			Decision:     "Deploy to AWS",
			Alternatives: []string{"GCP", "Azure"},
			WhyChosen:    "Existing infrastructure",
		},
	}

	result, err := Evaluate("{}", assumptions, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Questions) != 1 {
		t.Errorf("expected exactly 1 question, got %d", len(result.Questions))
	}
	if !strings.Contains(result.Questions[0], "Deploy to AWS") {
		t.Errorf("question should mention the decision, got: %s", result.Questions[0])
	}
	if !strings.Contains(result.Questions[0], "GCP") {
		t.Errorf("question should mention alternatives, got: %s", result.Questions[0])
	}
}

func TestEvaluate_SupervisorWithPrefixText(t *testing.T) {
	// Supervisor output may have text before the JSON array.
	supervisorOutput := `Here are my findings:
[{"decision": "Using gRPC", "reasoning": "REST might be simpler for this use case"}]
That's all I found.`

	result, err := Evaluate("{}", nil, supervisorOutput)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.NeedsClarification {
		t.Error("expected NeedsClarification=true")
	}
	if len(result.Questions) != 1 {
		t.Errorf("expected 1 question, got %d", len(result.Questions))
	}
}

// --- ProcessAnswer tests ---

func TestProcessAnswer_AnswerFirstQuestion(t *testing.T) {
	result, err := Evaluate("{}", []Assumption{
		{Decision: "Use Redis", Alternatives: []string{"Memcached"}, WhyChosen: "Feature rich"},
		{Decision: "Use Docker", Alternatives: []string{"Podman"}, WhyChosen: "Ecosystem"},
	}, "")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	updated, done, next, err := ProcessAnswer(result.SessionData, "Yes, Redis is fine")
	if err != nil {
		t.Fatalf("ProcessAnswer: %v", err)
	}
	if done {
		t.Error("expected not done after first answer")
	}
	if next == "" {
		t.Error("expected a next question")
	}
	if updated == nil {
		t.Error("expected non-nil updated session")
	}
}

func TestProcessAnswer_AnswerAllQuestions(t *testing.T) {
	result, err := Evaluate("{}", []Assumption{
		{Decision: "Choice A", Alternatives: []string{"B"}, WhyChosen: "Reason A"},
		{Decision: "Choice C", Alternatives: []string{"D"}, WhyChosen: "Reason C"},
	}, "")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	session := result.SessionData

	// Answer first question.
	session, done, _, err := ProcessAnswer(session, "Agree with first")
	if err != nil {
		t.Fatalf("ProcessAnswer 1: %v", err)
	}
	if done {
		t.Error("should not be done after first answer")
	}

	// Answer second (last) question.
	session, done, next, err := ProcessAnswer(session, "Agree with second")
	if err != nil {
		t.Fatalf("ProcessAnswer 2: %v", err)
	}
	if !done {
		t.Error("expected done after answering all questions")
	}
	if next != "" {
		t.Errorf("expected empty next question when done, got: %s", next)
	}
	_ = session
}

func TestProcessAnswer_DoneOnFirstQuestion(t *testing.T) {
	result, err := Evaluate("{}", []Assumption{
		{Decision: "Choice A", Alternatives: []string{"B"}, WhyChosen: "Reason"},
		{Decision: "Choice C", Alternatives: []string{"D"}, WhyChosen: "Reason"},
	}, "")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	_, done, next, err := ProcessAnswer(result.SessionData, "/done")
	if err != nil {
		t.Fatalf("ProcessAnswer: %v", err)
	}
	if !done {
		t.Error("expected done when /done is entered")
	}
	if next != "" {
		t.Errorf("expected empty next question, got: %s", next)
	}
}

func TestProcessAnswer_DoneCaseInsensitive(t *testing.T) {
	result, err := Evaluate("{}", []Assumption{
		{Decision: "Choice", Alternatives: []string{"Alt"}, WhyChosen: "Why"},
	}, "")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	cases := []string{"/Done", "/DONE", "/dOnE"}
	for _, cmd := range cases {
		_, done, _, err := ProcessAnswer(result.SessionData, cmd)
		if err != nil {
			t.Fatalf("ProcessAnswer(%q): %v", cmd, err)
		}
		if !done {
			t.Errorf("expected done for %q", cmd)
		}
	}
}

func TestProcessAnswer_DoneWithTrailingWhitespace(t *testing.T) {
	result, err := Evaluate("{}", []Assumption{
		{Decision: "Choice", Alternatives: []string{"Alt"}, WhyChosen: "Why"},
	}, "")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	_, done, _, err := ProcessAnswer(result.SessionData, "/done   \t  ")
	if err != nil {
		t.Fatalf("ProcessAnswer: %v", err)
	}
	if !done {
		t.Error("expected done with trailing whitespace")
	}
}

func TestProcessAnswer_AnswerThenDone(t *testing.T) {
	result, err := Evaluate("{}", []Assumption{
		{Decision: "Choice A", Alternatives: []string{"B"}, WhyChosen: "Reason"},
		{Decision: "Choice C", Alternatives: []string{"D"}, WhyChosen: "Reason"},
		{Decision: "Choice E", Alternatives: []string{"F"}, WhyChosen: "Reason"},
	}, "")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	session := result.SessionData

	// Answer first question.
	session, done, _, err := ProcessAnswer(session, "Yes, I agree")
	if err != nil {
		t.Fatalf("ProcessAnswer 1: %v", err)
	}
	if done {
		t.Error("should not be done yet")
	}

	// /done on second question.
	session, done, _, err = ProcessAnswer(session, "/done")
	if err != nil {
		t.Fatalf("ProcessAnswer 2: %v", err)
	}
	if !done {
		t.Error("expected done after /done")
	}

	// Verify the first answer was preserved in the session.
	ctx, err := ReplanContext(session)
	if err != nil {
		t.Fatalf("ReplanContext: %v", err)
	}
	if !strings.Contains(ctx, "Yes, I agree") {
		t.Error("expected first answer to be preserved in replan context")
	}
}

func TestProcessAnswer_AfterDone(t *testing.T) {
	result, err := Evaluate("{}", []Assumption{
		{Decision: "Choice", Alternatives: []string{"Alt"}, WhyChosen: "Why"},
	}, "")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	session, done, _, err := ProcessAnswer(result.SessionData, "/done")
	if err != nil {
		t.Fatalf("ProcessAnswer: %v", err)
	}
	if !done {
		t.Fatal("expected done")
	}

	// Call again after done — should be a no-op returning done.
	session, done, next, err := ProcessAnswer(session, "extra answer")
	if err != nil {
		t.Fatalf("ProcessAnswer after done: %v", err)
	}
	if !done {
		t.Error("expected still done")
	}
	if next != "" {
		t.Errorf("expected empty next question, got: %s", next)
	}
	_ = session
}

// --- ReplanContext tests ---

func TestReplanContext_SingleRound(t *testing.T) {
	result, err := Evaluate("{}", []Assumption{
		{Decision: "Use Kafka", Alternatives: []string{"RabbitMQ"}, WhyChosen: "Throughput"},
	}, "")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	session, _, _, err := ProcessAnswer(result.SessionData, "Yes, Kafka is correct")
	if err != nil {
		t.Fatalf("ProcessAnswer: %v", err)
	}

	ctx, err := ReplanContext(session)
	if err != nil {
		t.Fatalf("ReplanContext: %v", err)
	}
	if !strings.Contains(ctx, "## Clarification Context") {
		t.Error("expected clarification context header")
	}
	if !strings.Contains(ctx, "Kafka") {
		t.Error("expected question content in output")
	}
	if !strings.Contains(ctx, "Yes, Kafka is correct") {
		t.Error("expected answer in output")
	}
	if !strings.Contains(ctx, "Incorporate these answers") {
		t.Error("expected instruction to incorporate answers")
	}
}

func TestReplanContext_MultiRound(t *testing.T) {
	result, err := Evaluate("{}", []Assumption{
		{Decision: "Use TypeScript", Alternatives: []string{"JavaScript"}, WhyChosen: "Type safety"},
		{Decision: "Use React", Alternatives: []string{"Vue"}, WhyChosen: "Ecosystem"},
	}, "")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	session := result.SessionData
	session, _, _, err = ProcessAnswer(session, "TypeScript is fine")
	if err != nil {
		t.Fatalf("ProcessAnswer 1: %v", err)
	}
	session, _, _, err = ProcessAnswer(session, "React works for us")
	if err != nil {
		t.Fatalf("ProcessAnswer 2: %v", err)
	}

	ctx, err := ReplanContext(session)
	if err != nil {
		t.Fatalf("ReplanContext: %v", err)
	}
	if !strings.Contains(ctx, "TypeScript is fine") {
		t.Error("expected first answer in output")
	}
	if !strings.Contains(ctx, "React works for us") {
		t.Error("expected second answer in output")
	}
}

func TestReplanContext_DoneBeforeAllAnswered(t *testing.T) {
	result, err := Evaluate("{}", []Assumption{
		{Decision: "Choice A", Alternatives: []string{"B"}, WhyChosen: "Reason"},
		{Decision: "Choice C", Alternatives: []string{"D"}, WhyChosen: "Reason"},
		{Decision: "Choice E", Alternatives: []string{"F"}, WhyChosen: "Reason"},
	}, "")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	session := result.SessionData
	session, _, _, err = ProcessAnswer(session, "Answer to first")
	if err != nil {
		t.Fatalf("ProcessAnswer 1: %v", err)
	}
	session, _, _, err = ProcessAnswer(session, "/done")
	if err != nil {
		t.Fatalf("ProcessAnswer 2: %v", err)
	}

	ctx, err := ReplanContext(session)
	if err != nil {
		t.Fatalf("ReplanContext: %v", err)
	}

	// Only the answered question should appear, not the /done or unanswered ones.
	if !strings.Contains(ctx, "Answer to first") {
		t.Error("expected answered question in output")
	}
	if strings.Contains(ctx, "/done") {
		t.Error("should not contain /done in output")
	}
}

func TestReplanContext_ContainsHeader(t *testing.T) {
	result, err := Evaluate("{}", []Assumption{
		{Decision: "X", Alternatives: []string{"Y"}, WhyChosen: "Z"},
	}, "")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	session, _, _, err := ProcessAnswer(result.SessionData, "OK")
	if err != nil {
		t.Fatalf("ProcessAnswer: %v", err)
	}

	ctx, err := ReplanContext(session)
	if err != nil {
		t.Fatalf("ReplanContext: %v", err)
	}
	if !strings.HasPrefix(ctx, "## Clarification Context") {
		t.Error("expected output to start with ## Clarification Context")
	}
}

func TestReplanContext_ContainsIncorporateInstruction(t *testing.T) {
	result, err := Evaluate("{}", []Assumption{
		{Decision: "X", Alternatives: []string{"Y"}, WhyChosen: "Z"},
	}, "")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	session, _, _, err := ProcessAnswer(result.SessionData, "Sounds good")
	if err != nil {
		t.Fatalf("ProcessAnswer: %v", err)
	}

	ctx, err := ReplanContext(session)
	if err != nil {
		t.Fatalf("ReplanContext: %v", err)
	}
	if !strings.Contains(ctx, "Incorporate these answers into your revised plan") {
		t.Error("expected instruction to incorporate answers")
	}
}

// --- Integration flow test ---

func TestIntegrationFlow_EvaluateAnswerReplan(t *testing.T) {
	// Step 1: Evaluate with assumptions.
	planJSON := `{"tasks": [{"title": "Build API"}]}`
	assumptions := []Assumption{
		{
			Decision:     "Use gRPC for inter-service communication",
			Alternatives: []string{"REST", "GraphQL"},
			WhyChosen:    "Performance requirements",
		},
		{
			Decision:     "Deploy on Kubernetes",
			Alternatives: []string{"ECS", "bare metal"},
			WhyChosen:    "Team expertise",
		},
	}

	result, err := Evaluate(planJSON, assumptions, "")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !result.NeedsClarification {
		t.Fatal("expected NeedsClarification=true")
	}
	if len(result.Questions) != 2 {
		t.Fatalf("expected 2 questions, got %d", len(result.Questions))
	}

	// Step 2: Answer questions via ProcessAnswer loop.
	session := result.SessionData

	// Answer first question.
	session, done, next, err := ProcessAnswer(session, "Yes, gRPC is the right call")
	if err != nil {
		t.Fatalf("ProcessAnswer 1: %v", err)
	}
	if done {
		t.Fatal("should not be done after first answer")
	}
	if next == "" {
		t.Fatal("expected next question")
	}

	// Answer second question.
	session, done, next, err = ProcessAnswer(session, "Actually, let's use ECS instead")
	if err != nil {
		t.Fatalf("ProcessAnswer 2: %v", err)
	}
	if !done {
		t.Fatal("expected done after all questions answered")
	}
	if next != "" {
		t.Fatalf("expected empty next question, got: %s", next)
	}

	// Step 3: Build replan context.
	ctx, err := ReplanContext(session)
	if err != nil {
		t.Fatalf("ReplanContext: %v", err)
	}

	if !strings.Contains(ctx, "## Clarification Context") {
		t.Error("missing header")
	}
	if !strings.Contains(ctx, "gRPC") {
		t.Error("missing gRPC question content")
	}
	if !strings.Contains(ctx, "Yes, gRPC is the right call") {
		t.Error("missing first answer")
	}
	if !strings.Contains(ctx, "Actually, let's use ECS instead") {
		t.Error("missing second answer")
	}
	if !strings.Contains(ctx, "Incorporate these answers") {
		t.Error("missing incorporation instruction")
	}
}

func TestIntegrationFlow_WithSupervisorAndDone(t *testing.T) {
	assumptions := []Assumption{
		{
			Decision:     "Use WebSocket for real-time updates",
			Alternatives: []string{"SSE", "polling"},
			WhyChosen:    "Bidirectional communication needed",
		},
	}
	// Supervisor decision is a substring of the planner decision — should deduplicate.
	supervisorJSON := `[{"decision": "WebSocket for real-time updates", "reasoning": "Consider SSE for simpler one-way flows"}]`

	result, err := Evaluate("{}", assumptions, supervisorJSON)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	// Supervisor finding overlaps with planner assumption — should deduplicate.
	if len(result.Questions) != 1 {
		t.Fatalf("expected 1 question after dedup, got %d: %v", len(result.Questions), result.Questions)
	}

	// User sends /done immediately.
	session, done, _, err := ProcessAnswer(result.SessionData, "/done")
	if err != nil {
		t.Fatalf("ProcessAnswer: %v", err)
	}
	if !done {
		t.Fatal("expected done")
	}

	// ReplanContext should have no answered questions.
	ctx, err := ReplanContext(session)
	if err != nil {
		t.Fatalf("ReplanContext: %v", err)
	}
	if !strings.Contains(ctx, "No clarification questions were answered") {
		t.Errorf("expected no-answers message, got: %s", ctx)
	}
}

// --- Internal function tests ---

func TestIsDone(t *testing.T) {
	cases := []struct {
		input string
		want  bool
	}{
		{"/done", true},
		{"/Done", true},
		{"/DONE", true},
		{"/done ", true},
		{"/done   \t", true},
		{" /done", true},
		{"done", false},
		{"/don", false},
		{"/done extra text", false},
		{"", false},
	}

	for _, tc := range cases {
		got := isDone(tc.input)
		if got != tc.want {
			t.Errorf("isDone(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

func TestDecisionsOverlap(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"Use SQLite for storage", "SQLite for storage", true},
		{"Use Redis", "Use Memcached", false},
		{"Deploy on AWS", "deploy on aws", true},
		{"", "anything", true}, // empty string is contained in everything
	}

	for _, tc := range cases {
		got := decisionsOverlap(tc.a, tc.b)
		if got != tc.want {
			t.Errorf("decisionsOverlap(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestSessionSerializationRoundTrip(t *testing.T) {
	s := &session{
		Rounds: []round{
			{
				QAPairs: []qaPair{
					{Question: "Q1?", Answer: "A1"},
					{Question: "Q2?", Answer: "A2"},
				},
				CurrentIndex: 2,
			},
		},
		OriginalAssumptions: []assumption{
			{Decision: "D1", Alternatives: []string{"alt1"}, WhyChosen: "reason"},
		},
		PreviousPlanJSON: `{"test": true}`,
		Done:             true,
	}

	data, err := marshalSession(s)
	if err != nil {
		t.Fatalf("marshalSession: %v", err)
	}

	restored, err := unmarshalSession(data)
	if err != nil {
		t.Fatalf("unmarshalSession: %v", err)
	}

	if restored.Done != s.Done {
		t.Errorf("Done: got %v, want %v", restored.Done, s.Done)
	}
	if len(restored.Rounds) != len(s.Rounds) {
		t.Fatalf("Rounds: got %d, want %d", len(restored.Rounds), len(s.Rounds))
	}
	if len(restored.Rounds[0].QAPairs) != 2 {
		t.Errorf("QAPairs: got %d, want 2", len(restored.Rounds[0].QAPairs))
	}
	if restored.Rounds[0].QAPairs[0].Answer != "A1" {
		t.Errorf("first answer: got %q, want %q", restored.Rounds[0].QAPairs[0].Answer, "A1")
	}
}

func TestSessionSerializationFromJSONBytes(t *testing.T) {
	s := &session{
		Rounds: []round{{
			QAPairs:      []qaPair{{Question: "Q?", Answer: "A"}},
			CurrentIndex: 1,
		}},
		Done: true,
	}

	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	restored, err := unmarshalSession(b)
	if err != nil {
		t.Fatalf("unmarshalSession from bytes: %v", err)
	}
	if !restored.Done {
		t.Error("expected Done=true")
	}
}

func TestMergeAssumptions_EmptySupervisor(t *testing.T) {
	planner := []assumption{
		{Decision: "Use X", Alternatives: []string{"Y"}, WhyChosen: "because"},
	}

	merged := mergeAssumptions(planner, "")
	if len(merged) != 1 {
		t.Errorf("expected 1, got %d", len(merged))
	}
}

func TestMergeAssumptions_EmptyPlanner(t *testing.T) {
	supervisorJSON := `[{"decision": "Use gRPC", "reasoning": "performance"}]`
	merged := mergeAssumptions(nil, supervisorJSON)
	if len(merged) != 1 {
		t.Errorf("expected 1, got %d", len(merged))
	}
}

func TestMergeAssumptions_DeduplicatesOverlapping(t *testing.T) {
	planner := []assumption{
		{Decision: "Use SQLite for storage", Alternatives: []string{"Postgres", "MySQL"}, WhyChosen: "simplicity"},
	}

	supervisorJSON := `[{"decision": "SQLite for storage", "reasoning": "scaling concerns"}]`
	merged := mergeAssumptions(planner, supervisorJSON)
	if len(merged) != 1 {
		t.Errorf("expected 1 after dedup, got %d", len(merged))
	}
	// Planner version has more alternatives, should be kept.
	if len(merged[0].Alternatives) != 2 {
		t.Errorf("expected 2 alternatives (planner version kept), got %d", len(merged[0].Alternatives))
	}
}

func TestPrioritizeQuestions_OrderByAlternativesCount(t *testing.T) {
	assumptions := []assumption{
		{Decision: "Choice B", Alternatives: []string{"x"}, WhyChosen: "r"},
		{Decision: "Choice A", Alternatives: []string{"x", "y", "z"}, WhyChosen: "r"},
	}

	questions := prioritizeQuestions(assumptions)
	if len(questions) != 2 {
		t.Fatalf("expected 2, got %d", len(questions))
	}
	// Choice A has 3 alternatives, should come first.
	if !strings.Contains(questions[0], "Choice A") {
		t.Errorf("expected Choice A first (more alternatives), got: %s", questions[0])
	}
}

func TestReplanContext_CompactTranscriptForManyPairs(t *testing.T) {
	// Create a session with >5 Q&A pairs to trigger compaction.
	assumptions := make([]Assumption, 7)
	for i := range assumptions {
		assumptions[i] = Assumption{
			Decision:     fmt.Sprintf("Decision %d", i),
			Alternatives: []string{"alt"},
			WhyChosen:    "reason",
		}
	}

	result, err := Evaluate("{}", assumptions, "")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	session := result.SessionData
	for i := 0; i < 7; i++ {
		var d bool
		session, d, _, err = ProcessAnswer(session, fmt.Sprintf("Answer %d", i))
		if err != nil {
			t.Fatalf("ProcessAnswer %d: %v", i, err)
		}
		if i < 6 && d {
			t.Fatalf("expected not done at step %d", i)
		}
	}

	ctx, err := ReplanContext(session)
	if err != nil {
		t.Fatalf("ReplanContext: %v", err)
	}
	if !strings.Contains(ctx, "## Clarification Context") {
		t.Error("missing header in compact output")
	}
	if !strings.Contains(ctx, "Incorporate these answers") {
		t.Error("missing instruction in compact output")
	}
	// All answers should still be present even in compact form.
	for i := 0; i < 7; i++ {
		if !strings.Contains(ctx, fmt.Sprintf("Answer %d", i)) {
			t.Errorf("missing Answer %d in compact output", i)
		}
	}
}
