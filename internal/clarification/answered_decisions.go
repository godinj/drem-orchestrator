package clarification

import (
	"fmt"
	"sort"
	"strings"
)

// AnsweredDecisions extracts the Decision strings for all Q&A pairs where the
// user provided a real answer (not /done). The returned slice is the exclusion
// list for FilterAnswered: the orchestrator calls AnsweredDecisions on a prior
// session then passes the result to FilterAnswered before running Evaluate on
// the replanned plan, preventing already-resolved questions from resurfacing.
//
// Returns an empty (non-nil) slice when the session has no answered pairs.
func AnsweredDecisions(sessionData any) ([]string, error) {
	s, err := unmarshalSession(sessionData)
	if err != nil {
		return nil, fmt.Errorf("answered decisions: %w", err)
	}

	if len(s.Rounds) == 0 || len(s.OriginalAssumptions) == 0 {
		return []string{}, nil
	}

	// Recreate the same sorted order that prioritizeQuestions applied when
	// building QAPairs so that QAPairs[i] maps back to sorted[i].Decision.
	sorted := make([]assumption, len(s.OriginalAssumptions))
	copy(sorted, s.OriginalAssumptions)
	sort.Slice(sorted, func(i, j int) bool {
		if len(sorted[i].Alternatives) != len(sorted[j].Alternatives) {
			return len(sorted[i].Alternatives) > len(sorted[j].Alternatives)
		}
		return strings.ToLower(sorted[i].Decision) < strings.ToLower(sorted[j].Decision)
	})

	var decisions []string
	for _, r := range s.Rounds {
		for i, qa := range r.QAPairs {
			if qa.Answer == "" || isDone(qa.Answer) {
				continue
			}
			if i < len(sorted) {
				decisions = append(decisions, sorted[i].Decision)
			}
		}
	}
	if decisions == nil {
		decisions = []string{}
	}
	return decisions, nil
}

// FilterAnswered removes assumptions whose Decision overlaps with a previously
// answered decision, preventing the clarification loop from re-asking questions
// the user has already resolved. priorData may be either:
//   - a []string of decision strings (e.g. from a prior AnsweredDecisions call)
//   - an opaque session value (from ProcessAnswer or Evaluate's SessionData),
//     in which case AnsweredDecisions is called internally to extract the list
//
// Returns all assumptions unchanged when priorData is nil, or when the prior
// session contains no answered questions. Returns an empty (non-nil) slice when
// every assumption is covered by a previously-answered decision.
func FilterAnswered(assumptions []Assumption, priorData any) []Assumption {
	if len(assumptions) == 0 || priorData == nil {
		return assumptions
	}

	var decided []string
	switch v := priorData.(type) {
	case []string:
		decided = v
	default:
		d, err := AnsweredDecisions(priorData)
		if err != nil || len(d) == 0 {
			return assumptions
		}
		decided = d
	}

	if len(decided) == 0 {
		return assumptions
	}

	filtered := make([]Assumption, 0, len(assumptions))
	for _, a := range assumptions {
		alreadyAnswered := false
		for _, d := range decided {
			if decisionsOverlap(a.Decision, d) {
				alreadyAnswered = true
				break
			}
		}
		if !alreadyAnswered {
			filtered = append(filtered, a)
		}
	}
	return filtered
}
