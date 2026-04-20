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
	// Skipped is true when the constraint could not be evaluated because a
	// required tool was unavailable (e.g. `go vet` running inside an image
	// that does not ship the Go toolchain). Skipped results do not count
	// toward Report.Failed or Report.Passed — they populate Report.Skipped
	// instead. A skipped result carries a SkipReason explaining why.
	Skipped    bool
	SkipReason string
}

// Report is the aggregate outcome of evaluating all constraints.
type Report struct {
	Results []Result
	Passed  int
	Failed  int
	Skipped int
}

// FormatReport renders a Report as human-readable text.
// Format matches check_constitution.sh output with one addition — SKIP rows
// surface constraints that could not be evaluated due to missing tools:
//
//	PASS: <name>
//	SKIP: <name> (<reason>)
//	FAIL: <name>
//	  <violation detail>
//	──────────────────────────
//	N checks passed, S skipped, M failed
//
// Skipped results are listed as "SKIP:" and are not counted toward the
// pass/fail totals. The summary always shows all three counters so
// operators can see at a glance when tool-availability has reduced
// coverage.
func FormatReport(report *Report) string {
	var b strings.Builder

	for _, r := range report.Results {
		switch {
		case r.Skipped:
			if r.SkipReason != "" {
				fmt.Fprintf(&b, "SKIP: %s (%s)\n", r.Name, r.SkipReason)
			} else {
				fmt.Fprintf(&b, "SKIP: %s\n", r.Name)
			}
		case r.Passed:
			fmt.Fprintf(&b, "PASS: %s\n", r.Name)
		default:
			fmt.Fprintf(&b, "FAIL: %s\n", r.Name)
			for _, msg := range r.Messages {
				fmt.Fprintf(&b, "  %s\n", msg)
			}
		}
	}

	b.WriteString("──────────────────────────\n")
	fmt.Fprintf(&b, "%d checks passed, %d skipped, %d failed\n",
		report.Passed, report.Skipped, report.Failed)

	return b.String()
}
