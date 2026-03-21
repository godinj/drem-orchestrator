// Package clarification encapsulates the plan clarification loop logic behind
// a narrow public interface. It is a pure logic module with no external
// dependencies beyond the standard library.
//
// The public surface consists of two types (Assumption, Result) and three
// functions (Evaluate, ProcessAnswer, ReplanContext). All internal state is
// hidden behind the opaque SessionData field.
package clarification

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Assumption represents a single planner-reported assumption.
type Assumption struct {
	Decision     string   `json:"decision"`
	Alternatives []string `json:"alternatives"`
	WhyChosen    string   `json:"why_chosen"`
}

// Result is returned by Evaluate to indicate whether clarification is needed.
type Result struct {
	NeedsClarification bool
	Questions          []string // consolidated, deduplicated, ordered
	SessionData        any      // opaque — store in task.Context["clarification_session"]
}

// Evaluate merges planner-reported assumptions with supervisor-detected ones,
// deduplicates, prioritizes, and determines whether clarification is needed.
// supervisorAnalysis is the raw string from the supervisor's cross-check.
// Returns a Result with NeedsClarification=false if assumptions is empty and
// supervisorAnalysis finds nothing.
func Evaluate(planJSON string, assumptions []Assumption, supervisorAnalysis string) (*Result, error) {
	plannerAssumptions := make([]assumption, 0, len(assumptions))
	for _, a := range assumptions {
		plannerAssumptions = append(plannerAssumptions, assumption{
			Decision:     a.Decision,
			Alternatives: a.Alternatives,
			WhyChosen:    a.WhyChosen,
		})
	}

	merged := mergeAssumptions(plannerAssumptions, supervisorAnalysis)

	if len(merged) == 0 {
		s := &session{
			Rounds:              []round{},
			OriginalAssumptions: merged,
			PreviousPlanJSON:    planJSON,
			Done:                true,
		}
		data, err := marshalSession(s)
		if err != nil {
			return nil, fmt.Errorf("marshal session: %w", err)
		}
		return &Result{
			NeedsClarification: false,
			SessionData:        data,
		}, nil
	}

	questions := prioritizeQuestions(merged)

	qaPairs := make([]qaPair, len(questions))
	for i, q := range questions {
		qaPairs[i] = qaPair{Question: q}
	}

	s := &session{
		Rounds: []round{{
			QAPairs:      qaPairs,
			CurrentIndex: 0,
		}},
		OriginalAssumptions: merged,
		PreviousPlanJSON:    planJSON,
		Done:                false,
	}

	data, err := marshalSession(s)
	if err != nil {
		return nil, fmt.Errorf("marshal session: %w", err)
	}

	return &Result{
		NeedsClarification: true,
		Questions:          questions,
		SessionData:        data,
	}, nil
}

// ProcessAnswer records a user answer for the current question, detects the
// /done command, and determines whether another round of questions is needed.
// sessionData is the opaque value from Result.SessionData or a previous
// ProcessAnswer call. Returns updated session data, whether the loop is done,
// and the next question (empty if done).
func ProcessAnswer(sessionData any, answer string) (updatedSession any, done bool, nextQuestion string, err error) {
	s, err := unmarshalSession(sessionData)
	if err != nil {
		return sessionData, false, "", fmt.Errorf("unmarshal session: %w", err)
	}

	if s.Done {
		data, marshalErr := marshalSession(s)
		if marshalErr != nil {
			return sessionData, true, "", marshalErr
		}
		return data, true, "", nil
	}

	if len(s.Rounds) == 0 {
		s.Done = true
		data, marshalErr := marshalSession(s)
		if marshalErr != nil {
			return sessionData, true, "", marshalErr
		}
		return data, true, "", nil
	}

	currentRound := &s.Rounds[len(s.Rounds)-1]

	// Record the answer for the current question.
	if currentRound.CurrentIndex < len(currentRound.QAPairs) {
		currentRound.QAPairs[currentRound.CurrentIndex].Answer = answer
	}

	// Check for /done command.
	if isDone(answer) {
		s.Done = true
		data, marshalErr := marshalSession(s)
		if marshalErr != nil {
			return sessionData, true, "", marshalErr
		}
		return data, true, "", nil
	}

	// Advance to next question.
	currentRound.CurrentIndex++

	if currentRound.CurrentIndex >= len(currentRound.QAPairs) {
		// All questions answered in this round.
		s.Done = true
		data, marshalErr := marshalSession(s)
		if marshalErr != nil {
			return sessionData, true, "", marshalErr
		}
		return data, true, "", nil
	}

	next := currentRound.QAPairs[currentRound.CurrentIndex].Question

	data, marshalErr := marshalSession(s)
	if marshalErr != nil {
		return sessionData, false, "", marshalErr
	}
	return data, false, next, nil
}

// ReplanContext builds the compacted Q&A transcript and previous plan summary
// for injection into the planner prompt on retry. The returned string is
// markdown-formatted for direct inclusion in the prompt.
func ReplanContext(sessionData any) (string, error) {
	s, err := unmarshalSession(sessionData)
	if err != nil {
		return "", fmt.Errorf("unmarshal session: %w", err)
	}

	// Collect all answered Q&A pairs across all rounds.
	var answered []qaPair
	for _, r := range s.Rounds {
		for _, qa := range r.QAPairs {
			if qa.Answer != "" && !isDone(qa.Answer) {
				answered = append(answered, qa)
			}
		}
	}

	if len(answered) == 0 {
		return "## Clarification Context\n\nNo clarification questions were answered.\n", nil
	}

	// If more than 5 Q&A pairs, compact by grouping.
	if len(answered) > 5 {
		return compactTranscript(answered), nil
	}

	var b strings.Builder
	b.WriteString("## Clarification Context\n\n")
	b.WriteString("The user answered the following questions about your previous plan:\n\n")

	for _, qa := range answered {
		fmt.Fprintf(&b, "**Q:** %s\n**A:** %s\n\n", qa.Question, qa.Answer)
	}

	b.WriteString("Incorporate these answers into your revised plan. Do not re-ask resolved questions.\n")

	return b.String(), nil
}

// --- unexported types ---

// assumption is the internal representation of a planner or supervisor
// assumption after merging.
type assumption struct {
	Decision     string   `json:"decision"`
	Alternatives []string `json:"alternatives"`
	WhyChosen    string   `json:"why_chosen"`
}

// qaPair stores a question and its user-provided answer.
type qaPair struct {
	Question string `json:"question"`
	Answer   string `json:"answer,omitempty"`
}

// round tracks a single round of questions and answers.
type round struct {
	QAPairs      []qaPair `json:"qa_pairs"`
	CurrentIndex int      `json:"current_index"`
}

// session tracks the full clarification loop state. It is JSON-serializable
// so the orchestrator can store it in task.Context["clarification_session"].
type session struct {
	Rounds              []round      `json:"rounds"`
	OriginalAssumptions []assumption `json:"original_assumptions"`
	PreviousPlanJSON    string       `json:"previous_plan_json"`
	Done                bool         `json:"done"`
}

// supervisorFinding represents one item from the supervisor's JSON output.
type supervisorFinding struct {
	Decision  string `json:"decision"`
	Reasoning string `json:"reasoning"`
}

// --- unexported functions ---

// marshalSession serializes the session to a JSON-compatible any value.
func marshalSession(s *session) (any, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("marshal session: %w", err)
	}
	var result any
	if err := json.Unmarshal(b, &result); err != nil {
		return nil, fmt.Errorf("unmarshal session to any: %w", err)
	}
	return result, nil
}

// unmarshalSession deserializes sessionData (map[string]any or raw JSON bytes)
// back into an internal session struct.
func unmarshalSession(data any) (*session, error) {
	if data == nil {
		return nil, fmt.Errorf("session data is nil")
	}

	var b []byte
	var err error

	switch v := data.(type) {
	case []byte:
		b = v
	case string:
		b = []byte(v)
	default:
		// Assume it's a map[string]any or similar — re-marshal then unmarshal.
		b, err = json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("re-marshal session data: %w", err)
		}
	}

	var s session
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("unmarshal session: %w", err)
	}
	return &s, nil
}

// mergeAssumptions parses supervisor output, merges with planner assumptions,
// and deduplicates by semantic similarity (case-insensitive substring match
// on the decision field).
func mergeAssumptions(planner []assumption, supervisorRaw string) []assumption {
	var supervisorFindings []supervisorFinding
	if strings.TrimSpace(supervisorRaw) != "" {
		// Try to extract JSON array from the supervisor output.
		trimmed := strings.TrimSpace(supervisorRaw)
		// Find the first '[' for array start.
		if idx := strings.Index(trimmed, "["); idx >= 0 {
			// Find matching ']'.
			depth := 0
			for i := idx; i < len(trimmed); i++ {
				switch trimmed[i] {
				case '[':
					depth++
				case ']':
					depth--
					if depth == 0 {
						jsonStr := trimmed[idx : i+1]
						// Best-effort parse; skip on error.
						_ = json.Unmarshal([]byte(jsonStr), &supervisorFindings)
						goto parsed
					}
				}
			}
		}
	}
parsed:

	// Convert supervisor findings to assumptions.
	supervisorAssumptions := make([]assumption, 0, len(supervisorFindings))
	for _, f := range supervisorFindings {
		if f.Decision == "" {
			continue
		}
		supervisorAssumptions = append(supervisorAssumptions, assumption{
			Decision:  f.Decision,
			WhyChosen: f.Reasoning,
		})
	}

	// Start with planner assumptions.
	merged := make([]assumption, len(planner))
	copy(merged, planner)

	// Add supervisor assumptions, deduplicating.
	for _, sa := range supervisorAssumptions {
		duplicate := false
		for i, existing := range merged {
			if decisionsOverlap(existing.Decision, sa.Decision) {
				// Keep the one with more detail (longer alternatives list).
				if len(sa.Alternatives) > len(existing.Alternatives) {
					merged[i] = sa
				}
				duplicate = true
				break
			}
		}
		if !duplicate {
			merged = append(merged, sa)
		}
	}

	return merged
}

// decisionsOverlap checks if two decision strings refer to the same decision
// using case-insensitive substring matching.
func decisionsOverlap(a, b string) bool {
	aLower := strings.ToLower(a)
	bLower := strings.ToLower(b)
	return strings.Contains(aLower, bLower) || strings.Contains(bLower, aLower)
}

// prioritizeQuestions converts assumptions to user-facing questions, ordered
// by impact (decisions with more alternatives first, then alphabetical).
func prioritizeQuestions(merged []assumption) []string {
	// Sort: more alternatives first, then alphabetical by decision.
	sorted := make([]assumption, len(merged))
	copy(sorted, merged)

	sort.Slice(sorted, func(i, j int) bool {
		if len(sorted[i].Alternatives) != len(sorted[j].Alternatives) {
			return len(sorted[i].Alternatives) > len(sorted[j].Alternatives)
		}
		return strings.ToLower(sorted[i].Decision) < strings.ToLower(sorted[j].Decision)
	})

	questions := make([]string, 0, len(sorted))
	for _, a := range sorted {
		q := formatQuestion(a)
		questions = append(questions, q)
	}
	return questions
}

// formatQuestion converts an assumption to a natural user-facing question.
func formatQuestion(a assumption) string {
	var b strings.Builder

	fmt.Fprintf(&b, "The plan chose: %s.", a.Decision)

	if len(a.Alternatives) > 0 {
		fmt.Fprintf(&b, " Alternatives considered: %s.", strings.Join(a.Alternatives, ", "))
	}

	if a.WhyChosen != "" {
		fmt.Fprintf(&b, " Reasoning: %s.", a.WhyChosen)
	}

	b.WriteString(" Do you agree with this choice?")
	return b.String()
}

// isDone detects the /done command (case-insensitive, with optional trailing
// whitespace).
func isDone(answer string) bool {
	return strings.ToLower(strings.TrimSpace(answer)) == "/done"
}

// compactTranscript summarizes Q&A pairs into a concise markdown block when
// there are more than 5 pairs, grouping by theme.
func compactTranscript(pairs []qaPair) string {
	var b strings.Builder
	b.WriteString("## Clarification Context\n\n")
	b.WriteString("The user answered the following questions about your previous plan:\n\n")

	// Group by rough theme (first significant word of the question).
	type group struct {
		theme string
		pairs []qaPair
	}

	groups := make(map[string]*group)
	var order []string

	for _, qa := range pairs {
		theme := extractTheme(qa.Question)
		if g, ok := groups[theme]; ok {
			g.pairs = append(g.pairs, qa)
		} else {
			groups[theme] = &group{theme: theme, pairs: []qaPair{qa}}
			order = append(order, theme)
		}
	}

	for _, theme := range order {
		g := groups[theme]
		if len(g.pairs) > 1 {
			fmt.Fprintf(&b, "### %s\n\n", theme)
			for _, qa := range g.pairs {
				fmt.Fprintf(&b, "- **Q:** %s\n  **A:** %s\n", qa.Question, qa.Answer)
			}
			b.WriteString("\n")
		} else {
			qa := g.pairs[0]
			fmt.Fprintf(&b, "**Q:** %s\n**A:** %s\n\n", qa.Question, qa.Answer)
		}
	}

	b.WriteString("Incorporate these answers into your revised plan. Do not re-ask resolved questions.\n")

	return b.String()
}

// extractTheme extracts a rough theme from a question for grouping purposes.
// It looks for the subject after "The plan chose:" prefix.
func extractTheme(question string) string {
	lower := strings.ToLower(question)
	if idx := strings.Index(lower, "the plan chose:"); idx >= 0 {
		rest := question[idx+len("The plan chose:"):]
		rest = strings.TrimSpace(rest)
		// Take up to the first period.
		if dotIdx := strings.Index(rest, "."); dotIdx > 0 {
			rest = rest[:dotIdx]
		}
		// Trim to a reasonable length for a theme name.
		if len(rest) > 60 {
			rest = rest[:60] + "..."
		}
		return strings.TrimSpace(rest)
	}
	// Fallback: first 40 chars.
	if len(question) > 40 {
		return question[:40] + "..."
	}
	return question
}
