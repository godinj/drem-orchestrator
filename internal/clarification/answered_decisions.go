package clarification

// AnsweredDecisions extracts the Decision strings from a completed clarification
// session for all Q&A pairs where the user provided a real answer (not /done).
// The returned slice is used by FilterAnswered to deduplicate assumptions on
// subsequent clarification rounds.
//
// Returns an empty slice (not nil) when the session has no answered pairs.
func AnsweredDecisions(sessionData any) ([]string, error) {
	// stub — real implementation iterates session rounds and maps answered
	// Q&A pairs back to their original assumption Decision via re-prioritization.
	return []string{}, nil
}

// FilterAnswered removes assumptions that overlap with already-answered decisions,
// preventing the clarification loop from re-asking questions the user has already
// resolved. Overlap detection uses the same case-insensitive substring match as
// the internal deduplication in mergeAssumptions.
//
// Returns all assumptions unchanged when answered is empty.
// Returns an empty slice when every assumption is covered by an answered decision.
func FilterAnswered(assumptions []Assumption, answered []string) []Assumption {
	// stub — real implementation filters out any assumption whose Decision
	// overlaps (via decisionsOverlap) with any string in answered.
	return assumptions
}
