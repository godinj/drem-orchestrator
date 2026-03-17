package constraints

import (
	"fmt"
	"strings"
)

// Result is the outcome of evaluating a single constraint.
type Result struct {
	Name     string
	Type     string // "command", "max_lines", "max_matches", "no_match"
	Passed   bool
	Messages []string // violation details
}

// Report is the aggregate outcome of evaluating all constraints.
type Report struct {
	Results []Result
	Passed  int
	Failed  int
}

// FormatReport renders a Report as human-readable text.
// Format matches check_constitution.sh output:
//
//	PASS: <name>
//	FAIL: <name>
//	  <violation detail>
//	──────────────────────────
//	N checks passed, M failed
func FormatReport(report *Report) string {
	var b strings.Builder

	for _, r := range report.Results {
		if r.Passed {
			fmt.Fprintf(&b, "PASS: %s\n", r.Name)
		} else {
			fmt.Fprintf(&b, "FAIL: %s\n", r.Name)
			for _, msg := range r.Messages {
				fmt.Fprintf(&b, "  %s\n", msg)
			}
		}
	}

	b.WriteString("──────────────────────────\n")
	fmt.Fprintf(&b, "%d checks passed, %d failed\n", report.Passed, report.Failed)

	return b.String()
}
