package constraints

// ComparisonResult holds the outcome of comparing a baseline report against a
// current report on a per-constraint basis.
type ComparisonResult struct {
	// Dominated is true when any constraint worsened or introduced new violations.
	Dominated bool
	// NewViolations holds constraint names that transitioned PASS→FAIL.
	NewViolations []string
	// Worsened holds constraint names that transitioned FAIL→FAIL with more violations.
	Worsened []string
}

// Summary returns a human-readable description of the comparison outcome.
// When not dominated, it reports no regressions. When dominated, it names
// the new violations and worsened constraints.
func (r ComparisonResult) Summary() string {
	return ""
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
	return ComparisonResult{}
}
