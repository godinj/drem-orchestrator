package constraints

import "strings"

// ComparisonResult holds the outcome of comparing a baseline report against a
// current report on a per-constraint basis.
type ComparisonResult struct {
	// Dominated is true when any constraint worsened or introduced new violations.
	Dominated bool
	// NewViolations holds constraint names that transitioned PASS→FAIL.
	NewViolations []string
	// Worsened holds constraint names that transitioned FAIL→FAIL with more violations.
	Worsened []string
	// Additions holds constraint names present in current but absent from baseline.
	// These are informational — they do not affect Dominated.
	Additions []string
}

// Magnitude returns the total number of blocking constraints
// (new violations + worsened constraints). Used by ShouldTerminateEarly
// to detect stagnation: if the magnitude does not decrease across retries,
// retrying is unlikely to help.
func (r ComparisonResult) Magnitude() int {
	return len(r.NewViolations) + len(r.Worsened)
}

// Summary returns a human-readable description of the comparison outcome.
// When not dominated, it reports no regressions. When dominated, it names
// the new violations and worsened constraints.
func (r ComparisonResult) Summary() string {
	if !r.Dominated {
		return "no regression: all constraints stable or improved"
	}
	var parts []string
	for _, name := range r.NewViolations {
		parts = append(parts, "new violation: "+name)
	}
	for _, name := range r.Worsened {
		parts = append(parts, "worsened: "+name)
	}
	for _, name := range r.Additions {
		parts = append(parts, "addition: "+name)
	}
	return strings.Join(parts, "; ")
}

// CompareReports compares baseline against current on a per-constraint basis.
// Each constraint is evaluated independently by its state transition:
//
//   - PASS→PASS: no regression, allowed
//   - PASS→FAIL: new violation, blocked (added to NewViolations)
//   - FAIL→PASS: improvement, allowed
//   - FAIL→FAIL (not worse): pre-existing, allowed
//   - FAIL→FAIL (worse): worsened violation, blocked (added to Worsened)
//
// Magnitude is measured by violation message count: more messages means worse.
// Dominated is true when any constraint is blocked.
func CompareReports(baseline, current *Report) ComparisonResult {
	// Build a lookup of baseline results by name.
	baselineByName := make(map[string]Result, len(baseline.Results))
	for _, r := range baseline.Results {
		baselineByName[r.Name] = r
	}

	var result ComparisonResult
	for _, cur := range current.Results {
		base, found := baselineByName[cur.Name]
		if !found {
			if !cur.Passed {
				result.Additions = append(result.Additions, cur.Name)
			}
			continue
		}

		switch {
		case base.Passed && cur.Passed:
			// PASS→PASS: no action.
		case base.Passed && !cur.Passed:
			// PASS→FAIL: new violation.
			result.NewViolations = append(result.NewViolations, cur.Name)
		case !base.Passed && cur.Passed:
			// FAIL→PASS: improvement, no action.
		default:
			// FAIL→FAIL: check magnitude via message count.
			if len(cur.Messages) > len(base.Messages) {
				result.Worsened = append(result.Worsened, cur.Name)
			}
		}
	}

	result.Dominated = len(result.NewViolations) > 0 || len(result.Worsened) > 0
	return result
}
