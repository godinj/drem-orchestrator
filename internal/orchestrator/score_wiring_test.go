package orchestrator

import (
	"math"
	"testing"

	"github.com/godinj/drem-orchestrator/internal/constraints"
	"github.com/godinj/drem-orchestrator/internal/score"
)

// computePlanScores converts orchestrator plan types to score package inputs
// and computes quality scores for the plan review gate.
func computePlanScores(subtasks []planEntry, exceptions []tddException, validation PlanValidationResult) score.StepScore {
	entries := make([]score.PlanEntry, len(subtasks))
	for i, s := range subtasks {
		entries[i] = score.PlanEntry{
			Title:          s.Title,
			AgentType:      s.AgentType,
			Phase:          s.Phase,
			EstimatedFiles: s.EstimatedFiles,
			TestsFor:       s.TestsFor,
			Dependencies:   s.Dependencies,
		}
	}

	scoreExceptions := make([]score.TDDException, len(exceptions))
	for i, e := range exceptions {
		scoreExceptions[i] = score.TDDException{
			SubtaskIndex: e.SubtaskIndex,
			Reason:       e.Reason,
		}
	}

	return score.ScorePlan(score.PlanScoreInput{
		Entries:       entries,
		TDDExceptions: scoreExceptions,
		ValidationResult: score.PlanValidationResult{
			Valid:    validation.Valid,
			Warnings: validation.Warnings,
			Errors:   validation.Errors,
		},
	})
}

// computeImplementationScores converts implementation review inputs to score
// package types and computes quality scores for the implementation review gate.
func computeImplementationScores(report *constraints.Report, changedFiles []string, coverageOutput string) score.StepScore {
	passed, failed := 0, 0
	if report != nil {
		passed = report.Passed
		failed = report.Failed
	}
	return score.ScoreImplementation(score.ImplScoreInput{
		ConstraintsPassed: passed,
		ConstraintsFailed: failed,
		ChangedFiles:      changedFiles,
		CoverageOutput:    coverageOutput,
	})
}

const scoreTolerance = 0.001

func scoreApproxEqual(a, b float64) bool {
	return math.Abs(a-b) < scoreTolerance
}

// ---------------------------------------------------------------------------
// Test 1: Plan with full TDD coverage and doc subtask → all scores 1.0
// ---------------------------------------------------------------------------

func TestComputePlanScores_FullCoverage(t *testing.T) {
	subtasks := []planEntry{
		{Title: "test A", Phase: "test", TestsFor: []int{1}},
		{Title: "impl A", Phase: "implementation", EstimatedFiles: []string{"internal/foo/foo.go"}},
		{Title: "test B", Phase: "test", TestsFor: []int{3}},
		{Title: "impl B", Phase: "implementation", EstimatedFiles: []string{"internal/bar/bar.go"}},
		{Title: "update docs", Phase: "implementation", EstimatedFiles: []string{"README.md"}},
	}
	exceptions := []tddException(nil)
	validation := PlanValidationResult{Valid: true}

	scores := computePlanScores(subtasks, exceptions, validation)

	if !scoreApproxEqual(scores.TDD, 1.0) {
		t.Errorf("TDD = %v, want 1.0 (all impl subtasks covered by tests)", scores.TDD)
	}
	if !scoreApproxEqual(scores.Constitution, 1.0) {
		t.Errorf("Constitution = %v, want 1.0 (valid plan, no warnings)", scores.Constitution)
	}
	if !scoreApproxEqual(scores.Documentation, 1.0) {
		t.Errorf("Documentation = %v, want 1.0 (plan touches README.md)", scores.Documentation)
	}

	// Verify ScoresToMap produces the expected keys.
	m := score.ScoresToMap(scores)
	for _, key := range []string{"tdd", "constitution", "documentation", "depth", "formatted"} {
		if _, ok := m[key]; !ok {
			t.Errorf("ScoresToMap missing key %q", key)
		}
	}
}

// ---------------------------------------------------------------------------
// Test 2: Plan with missing test coverage → TDD < 1.0
// ---------------------------------------------------------------------------

func TestComputePlanScores_MissingTests(t *testing.T) {
	subtasks := []planEntry{
		{Title: "test A", Phase: "test", TestsFor: []int{1}},
		{Title: "impl A", Phase: "implementation"},
		{Title: "impl B", Phase: "implementation"}, // no test covers this
	}

	scores := computePlanScores(subtasks, nil, PlanValidationResult{Valid: true})

	if scores.TDD >= 1.0 {
		t.Errorf("TDD = %v, want < 1.0 (impl B has no covering test)", scores.TDD)
	}
	// Expect 0.5: 1 covered out of 2 impl subtasks.
	if !scoreApproxEqual(scores.TDD, 0.5) {
		t.Errorf("TDD = %v, want ~0.5", scores.TDD)
	}
}

// ---------------------------------------------------------------------------
// Test 3: computeImplementationScores with constraint report and coverage
// ---------------------------------------------------------------------------

func TestComputeImplementationScores(t *testing.T) {
	report := &constraints.Report{
		Results: makeConstraintResults(8, 1),
		Passed:  8,
		Failed:  1,
	}
	changedFiles := []string{
		"internal/orchestrator/score_bridge.go",
		"README.md",
	}
	coverageOutput := "ok  \tgithub.com/godinj/drem-orchestrator/internal/score\tcoverage: 90.0% of statements"

	scores := computeImplementationScores(report, changedFiles, coverageOutput)

	// TDD: parsed from coverage output → 0.90
	if !scoreApproxEqual(scores.TDD, 0.90) {
		t.Errorf("TDD = %v, want 0.90", scores.TDD)
	}

	// Constitution: 8/(8+1) ≈ 0.889
	wantConstitution := 8.0 / 9.0
	if !scoreApproxEqual(scores.Constitution, wantConstitution) {
		t.Errorf("Constitution = %v, want %v", scores.Constitution, wantConstitution)
	}

	// Documentation: README.md in changed files → 1.0
	if !scoreApproxEqual(scores.Documentation, 1.0) {
		t.Errorf("Documentation = %v, want 1.0", scores.Documentation)
	}
}

// ---------------------------------------------------------------------------
// Test 4: computePlanScores type conversion integration
// ---------------------------------------------------------------------------

func TestComputePlanScores_Integration(t *testing.T) {
	tests := []struct {
		name       string
		subtasks   []planEntry
		exceptions []tddException
		validation PlanValidationResult
		wantTDD    float64
		wantConst  float64
		wantDoc    float64
	}{
		{
			name: "exception counts as 50%",
			subtasks: []planEntry{
				{Title: "test A", Phase: "test", TestsFor: []int{1}},
				{Title: "impl A", Phase: "implementation"},
				{Title: "impl B", Phase: "implementation"}, // exception
			},
			exceptions: []tddException{
				{SubtaskIndex: 2, Reason: "config-only change"},
			},
			validation: PlanValidationResult{Valid: true},
			wantTDD:    0.75, // (1.0 + 0.5) / 2
			wantConst:  1.0,
			wantDoc:    0.0,
		},
		{
			name: "warnings reduce constitution",
			subtasks: []planEntry{
				{Title: "impl", Phase: "implementation"},
			},
			validation: PlanValidationResult{
				Valid:    true,
				Warnings: []string{"warn1", "warn2"},
			},
			wantTDD:   0.0,
			wantConst: 0.8,
			wantDoc:   0.0,
		},
		{
			name: "errors make constitution 0",
			subtasks: []planEntry{
				{Title: "impl", Phase: "implementation"},
			},
			validation: PlanValidationResult{
				Valid:  false,
				Errors: []string{"bad"},
			},
			wantTDD:   0.0,
			wantConst: 0.0,
			wantDoc:   0.0,
		},
		{
			name: "docs/ file triggers documentation score",
			subtasks: []planEntry{
				{Title: "test A", Phase: "test", TestsFor: []int{1}},
				{Title: "impl A", Phase: "implementation", EstimatedFiles: []string{"docs/walkthrough.md"}},
			},
			validation: PlanValidationResult{Valid: true},
			wantTDD:    1.0,
			wantConst:  1.0,
			wantDoc:    1.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := computePlanScores(tt.subtasks, tt.exceptions, tt.validation)

			if !scoreApproxEqual(got.TDD, tt.wantTDD) {
				t.Errorf("TDD = %v, want %v", got.TDD, tt.wantTDD)
			}
			if !scoreApproxEqual(got.Constitution, tt.wantConst) {
				t.Errorf("Constitution = %v, want %v", got.Constitution, tt.wantConst)
			}
			if !scoreApproxEqual(got.Documentation, tt.wantDoc) {
				t.Errorf("Documentation = %v, want %v", got.Documentation, tt.wantDoc)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func makeConstraintResults(passed, failed int) []constraints.Result {
	results := make([]constraints.Result, 0, passed+failed)
	for i := 0; i < passed; i++ {
		results = append(results, constraints.Result{Name: "pass", Passed: true})
	}
	for i := 0; i < failed; i++ {
		results = append(results, constraints.Result{Name: "fail", Passed: false})
	}
	return results
}
