package constraints

import (
	"strings"
	"testing"
)

// TestCompareReports covers all four per-constraint state transitions and
// verifies that the gate decision (Dominated) and violation lists are correct.
func TestCompareReports(t *testing.T) {
	tests := []struct {
		name          string
		baseline      *Report
		current       *Report
		wantDominated bool
		wantNewViol   []string // nil means expect empty
		wantWorsened  []string // nil means expect empty
	}{
		{
			// PASS→PASS: constraint stays clean — no regression, gate must allow.
			name: "PASS to PASS: no regression, allowed",
			baseline: &Report{
				Results: []Result{{Name: "gofmt compliance", Passed: true}},
				Passed:  1,
			},
			current: &Report{
				Results: []Result{{Name: "gofmt compliance", Passed: true}},
				Passed:  1,
			},
			wantDominated: false,
		},
		{
			// PASS→FAIL: a previously-clean constraint now fails — new violation, gate blocks.
			name: "PASS to FAIL: new violation, blocked",
			baseline: &Report{
				Results: []Result{{Name: "line limit", Passed: true}},
				Passed:  1,
			},
			current: &Report{
				Results: []Result{
					{Name: "line limit", Passed: false, Messages: []string{"foo.go has 900 lines, exceeds limit of 800"}},
				},
				Failed: 1,
			},
			wantDominated: true,
			wantNewViol:   []string{"line limit"},
		},
		{
			// FAIL→PASS: a pre-existing violation was fixed — improvement, gate must allow.
			name: "FAIL to PASS: improvement, allowed",
			baseline: &Report{
				Results: []Result{
					{Name: "line limit", Passed: false, Messages: []string{"foo.go has 850 lines, exceeds limit of 800"}},
				},
				Failed: 1,
			},
			current: &Report{
				Results: []Result{{Name: "line limit", Passed: true}},
				Passed:  1,
			},
			wantDominated: false,
		},
		{
			// FAIL→FAIL with same message count: pre-existing violation unchanged — gate must allow.
			// Scenario: orchestrator/ internal import ceiling has been at 11 since before this task.
			name: "FAIL to FAIL (same magnitude): pre-existing, allowed",
			baseline: &Report{
				Results: []Result{
					{
						Name:   "Internal import ceiling",
						Passed: false,
						Messages: []string{
							"internal/orchestrator/ has 11 unique imports, exceeds limit of 6",
						},
					},
				},
				Failed: 1,
			},
			current: &Report{
				Results: []Result{
					{
						Name:   "Internal import ceiling",
						Passed: false,
						Messages: []string{
							"internal/orchestrator/ has 11 unique imports, exceeds limit of 6",
						},
					},
				},
				Failed: 1,
			},
			wantDominated: false,
		},
		{
			// FAIL→FAIL with fewer messages: violation partially recovered — still failing but
			// improved, gate must allow (not worse).
			name: "FAIL to FAIL (fewer messages): improved but still failing, allowed",
			baseline: &Report{
				Results: []Result{
					{
						Name:   "Exported function count",
						Passed: false,
						Messages: []string{
							"internal/tui/board.go has 25 matches, exceeds limit of 20",
							"internal/tui/render.go has 22 matches, exceeds limit of 20",
						},
					},
				},
				Failed: 1,
			},
			current: &Report{
				Results: []Result{
					{
						Name:   "Exported function count",
						Passed: false,
						Messages: []string{
							"internal/tui/board.go has 22 matches, exceeds limit of 20",
						},
					},
				},
				Failed: 1,
			},
			wantDominated: false,
		},
		{
			// FAIL→FAIL with more messages: violation grew — worsened, gate blocks.
			// Scenario: adding more imports to a package already over the ceiling.
			name: "FAIL to FAIL (more messages): worsened violation, blocked",
			baseline: &Report{
				Results: []Result{
					{
						Name:   "Internal import ceiling",
						Passed: false,
						Messages: []string{
							"internal/orchestrator/ has 11 unique imports, exceeds limit of 6",
						},
					},
				},
				Failed: 1,
			},
			current: &Report{
				Results: []Result{
					{
						Name:   "Internal import ceiling",
						Passed: false,
						Messages: []string{
							"internal/orchestrator/ has 11 unique imports, exceeds limit of 6",
							"internal/agent/ has 7 unique imports, exceeds limit of 6",
						},
					},
				},
				Failed: 1,
			},
			wantDominated: true,
			wantWorsened:  []string{"Internal import ceiling"},
		},
		{
			// Multi-constraint: one constraint improves (FAIL→PASS), another is pre-existing
			// (FAIL→FAIL same magnitude), and a third stays clean (PASS→PASS).
			// Verifies gate allows tasks that don't worsen any individual constraint even
			// when pre-existing failures remain — the fix for the pipeline standstill.
			name: "multi-constraint: improvement + pre-existing, no new violations, allowed",
			baseline: &Report{
				Results: []Result{
					{
						Name:     "Internal import ceiling",
						Passed:   false,
						Messages: []string{"internal/orchestrator/ has 11 unique imports, exceeds limit of 6"},
					},
					{
						Name:     "line limit",
						Passed:   false,
						Messages: []string{"internal/orchestrator/orchestrator.go has 2250 lines, exceeds limit of 800"},
					},
					{Name: "gofmt compliance", Passed: true},
				},
				Passed: 1,
				Failed: 2,
			},
			current: &Report{
				Results: []Result{
					{
						// Pre-existing: same count, allowed.
						Name:     "Internal import ceiling",
						Passed:   false,
						Messages: []string{"internal/orchestrator/ has 11 unique imports, exceeds limit of 6"},
					},
					{
						// Improvement: fixed line limit violation.
						Name:   "line limit",
						Passed: true,
					},
					{Name: "gofmt compliance", Passed: true},
				},
				Passed: 2,
				Failed: 1,
			},
			wantDominated: false,
		},
		{
			// Multi-constraint: improvement + pre-existing + new violation.
			// One new violation must dominate despite other improvements.
			name: "multi-constraint: new violation among improvements, blocked",
			baseline: &Report{
				Results: []Result{
					{
						Name:     "Internal import ceiling",
						Passed:   false,
						Messages: []string{"internal/orchestrator/ has 11 unique imports, exceeds limit of 6"},
					},
					{Name: "gofmt compliance", Passed: true},
					{Name: "go vet", Passed: true},
				},
				Passed: 2,
				Failed: 1,
			},
			current: &Report{
				Results: []Result{
					{
						// Pre-existing: unchanged.
						Name:     "Internal import ceiling",
						Passed:   false,
						Messages: []string{"internal/orchestrator/ has 11 unique imports, exceeds limit of 6"},
					},
					{Name: "gofmt compliance", Passed: true},
					{
						// New violation introduced by this task.
						Name:     "go vet",
						Passed:   false,
						Messages: []string{"internal/foo/bar.go:12:3: unused variable x"},
					},
				},
				Passed: 1,
				Failed: 2,
			},
			wantDominated: true,
			wantNewViol:   []string{"go vet"},
		},
		{
			// Multi-constraint: both worsened and new violation present.
			// Both lists should be populated.
			name: "multi-constraint: worsened and new violation together, blocked",
			baseline: &Report{
				Results: []Result{
					{
						Name:     "Internal import ceiling",
						Passed:   false,
						Messages: []string{"internal/orchestrator/ has 11 unique imports"},
					},
					{Name: "gofmt compliance", Passed: true},
				},
				Passed: 1,
				Failed: 1,
			},
			current: &Report{
				Results: []Result{
					{
						// Worsened: more packages now over ceiling.
						Name:   "Internal import ceiling",
						Passed: false,
						Messages: []string{
							"internal/orchestrator/ has 11 unique imports",
							"internal/agent/ has 7 unique imports",
						},
					},
					{
						// New violation.
						Name:     "gofmt compliance",
						Passed:   false,
						Messages: []string{"internal/foo/baz.go is not gofmt-clean"},
					},
				},
				Passed: 0,
				Failed: 2,
			},
			wantDominated: true,
			wantNewViol:   []string{"gofmt compliance"},
			wantWorsened:  []string{"Internal import ceiling"},
		},
		{
			// Edge case: empty reports (no constraints configured) must not be dominated.
			name:          "empty reports: not dominated",
			baseline:      &Report{},
			current:       &Report{},
			wantDominated: false,
		},
		{
			// Edge case: constraint appears only in current (not in baseline).
			// Treated as PASS→FAIL since baseline had no record of failure.
			name: "constraint only in current: treated as new violation, blocked",
			baseline: &Report{
				Results: []Result{},
			},
			current: &Report{
				Results: []Result{
					{
						Name:     "new depth constraint",
						Passed:   false,
						Messages: []string{"internal/foo/ export ratio 0.30 exceeds limit 0.15"},
					},
				},
				Failed: 1,
			},
			wantDominated: true,
			wantNewViol:   []string{"new depth constraint"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CompareReports(tt.baseline, tt.current)

			if got.Dominated != tt.wantDominated {
				t.Errorf("Dominated = %v, want %v", got.Dominated, tt.wantDominated)
			}

			if !containsExactly(got.NewViolations, tt.wantNewViol) {
				t.Errorf("NewViolations = %v, want %v", got.NewViolations, tt.wantNewViol)
			}

			if !containsExactly(got.Worsened, tt.wantWorsened) {
				t.Errorf("Worsened = %v, want %v", got.Worsened, tt.wantWorsened)
			}
		})
	}
}

// containsExactly returns true if got and want contain the same elements,
// regardless of order, treating nil and empty slice as equivalent.
func containsExactly(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	counts := make(map[string]int, len(want))
	for _, s := range want {
		counts[s]++
	}
	for _, s := range got {
		counts[s]--
		if counts[s] < 0 {
			return false
		}
	}
	return true
}

// TestComparisonResult_Summary verifies that Summary produces human-readable
// output describing the outcome in each dominated / non-dominated case.
func TestComparisonResult_Summary(t *testing.T) {
	tests := []struct {
		name        string
		result      ComparisonResult
		wantContain []string // substrings that must appear in the summary
		wantAbsent  []string // substrings that must NOT appear
	}{
		{
			name:        "not dominated: summary indicates no regressions",
			result:      ComparisonResult{Dominated: false},
			wantContain: []string{"no regression"},
		},
		{
			name: "dominated with new violation: summary names the constraint",
			result: ComparisonResult{
				Dominated:     true,
				NewViolations: []string{"line limit"},
			},
			wantContain: []string{"line limit"},
		},
		{
			name: "dominated with worsened: summary names the worsened constraint",
			result: ComparisonResult{
				Dominated: true,
				Worsened:  []string{"Internal import ceiling"},
			},
			wantContain: []string{"Internal import ceiling"},
		},
		{
			name: "dominated with both new and worsened: summary names all",
			result: ComparisonResult{
				Dominated:     true,
				NewViolations: []string{"gofmt compliance"},
				Worsened:      []string{"Internal import ceiling"},
			},
			wantContain: []string{"gofmt compliance", "Internal import ceiling"},
		},
		{
			name:       "not dominated: summary must not claim violations",
			result:     ComparisonResult{Dominated: false},
			wantAbsent: []string{"violation", "blocked", "worsened"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.result.Summary()

			for _, want := range tt.wantContain {
				if !strings.Contains(got, want) {
					t.Errorf("Summary() = %q; expected it to contain %q", got, want)
				}
			}

			for _, absent := range tt.wantAbsent {
				if strings.Contains(got, absent) {
					t.Errorf("Summary() = %q; expected it NOT to contain %q", got, absent)
				}
			}
		})
	}
}
