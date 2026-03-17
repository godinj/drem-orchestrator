package score

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

const tolerance = 0.001

func approxEqual(a, b float64) bool {
	return math.Abs(a-b) < tolerance
}

// ---------------------------------------------------------------------------
// ScorePlan tests
// ---------------------------------------------------------------------------

func TestScorePlan_TDD(t *testing.T) {
	tests := []struct {
		name    string
		input   PlanScoreInput
		wantTDD float64
	}{
		{
			name: "all impl subtasks covered by test subtasks",
			input: PlanScoreInput{
				Entries: []PlanEntry{
					{Title: "test A", Phase: "test", TestsFor: []int{1}},
					{Title: "impl A", Phase: "implementation"},
					{Title: "test B", Phase: "test", TestsFor: []int{3}},
					{Title: "impl B", Phase: "implementation"},
					{Title: "test C", Phase: "test", TestsFor: []int{5}},
					{Title: "impl C", Phase: "implementation"},
				},
			},
			wantTDD: 1.0,
		},
		{
			name: "2 impl subtasks, 1 covered, 1 missing test",
			input: PlanScoreInput{
				Entries: []PlanEntry{
					{Title: "test A", Phase: "test", TestsFor: []int{1}},
					{Title: "impl A", Phase: "implementation"},
					{Title: "impl B", Phase: "implementation"},
				},
			},
			wantTDD: 0.5,
		},
		{
			name: "3 impl, 1 covered, 1 exception, 1 missing — exception counts as 50%",
			input: PlanScoreInput{
				Entries: []PlanEntry{
					{Title: "test A", Phase: "test", TestsFor: []int{1}},
					{Title: "impl A", Phase: "implementation"},
					{Title: "impl B", Phase: "implementation"}, // exception
					{Title: "impl C", Phase: "implementation"}, // missing
				},
				TDDExceptions: []TDDException{
					{SubtaskIndex: 2, Reason: "config-only change"},
				},
			},
			wantTDD: 0.5, // (1.0 + 0.5 + 0.0) / 3
		},
		{
			name: "no impl subtasks (all research) — TDD = 1.0",
			input: PlanScoreInput{
				Entries: []PlanEntry{
					{Title: "research A", Phase: "research"},
					{Title: "research B", Phase: "research"},
				},
			},
			wantTDD: 1.0,
		},
		{
			name: "phase ordering violation (test depends on impl) reduces TDD by 0.25",
			input: PlanScoreInput{
				Entries: []PlanEntry{
					{Title: "impl A", Phase: "implementation"},
					{Title: "test A", Phase: "test", TestsFor: []int{0}, Dependencies: []int{0}},
				},
			},
			wantTDD: 0.75, // 1.0 (covered) - 0.25 (ordering violation)
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ScorePlan(tt.input)
			if !approxEqual(got.TDD, tt.wantTDD) {
				t.Errorf("TDD = %v, want %v", got.TDD, tt.wantTDD)
			}
		})
	}
}

func TestScorePlan_Constitution(t *testing.T) {
	tests := []struct {
		name             string
		validation       PlanValidationResult
		wantConstitution float64
	}{
		{
			name:             "0 errors, 0 warnings",
			validation:       PlanValidationResult{Valid: true},
			wantConstitution: 1.0,
		},
		{
			name: "0 errors, 2 warnings — each warning reduces by 0.1",
			validation: PlanValidationResult{
				Valid:    true,
				Warnings: []string{"warn1", "warn2"},
			},
			wantConstitution: 0.8,
		},
		{
			name: "1 error — constitution = 0",
			validation: PlanValidationResult{
				Valid:  false,
				Errors: []string{"something wrong"},
			},
			wantConstitution: 0.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := PlanScoreInput{ValidationResult: tt.validation}
			got := ScorePlan(input)
			if !approxEqual(got.Constitution, tt.wantConstitution) {
				t.Errorf("Constitution = %v, want %v", got.Constitution, tt.wantConstitution)
			}
		})
	}
}

func TestScorePlan_Documentation(t *testing.T) {
	tests := []struct {
		name    string
		entries []PlanEntry
		wantDoc float64
	}{
		{
			name: "subtask touches README",
			entries: []PlanEntry{
				{Title: "update docs", Phase: "implementation", EstimatedFiles: []string{"README.md", "internal/foo/foo.go"}},
			},
			wantDoc: 1.0,
		},
		{
			name: "subtask touches docs/ dir",
			entries: []PlanEntry{
				{Title: "add walkthrough", Phase: "implementation", EstimatedFiles: []string{"docs/walkthrough.md"}},
			},
			wantDoc: 1.0,
		},
		{
			name: "no subtask touches doc files",
			entries: []PlanEntry{
				{Title: "impl", Phase: "implementation", EstimatedFiles: []string{"internal/foo/foo.go"}},
			},
			wantDoc: 0.0,
		},
		{
			name: "only agent prompts, no README or docs/",
			entries: []PlanEntry{
				{Title: "prompts", Phase: "implementation", EstimatedFiles: []string{".drem/prompts/agent.md"}},
			},
			wantDoc: 0.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := PlanScoreInput{Entries: tt.entries}
			got := ScorePlan(input)
			if !approxEqual(got.Documentation, tt.wantDoc) {
				t.Errorf("Documentation = %v, want %v", got.Documentation, tt.wantDoc)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ScoreImplementation tests
// ---------------------------------------------------------------------------

func TestScoreImplementation_TDD(t *testing.T) {
	tests := []struct {
		name           string
		coverageOutput string
		wantTDD        float64
	}{
		{
			name:           "single package 85% coverage",
			coverageOutput: "ok  \tgithub.com/godinj/drem-orchestrator/internal/score\tcoverage: 85.0% of statements",
			wantTDD:        0.85,
		},
		{
			name:           "100% coverage",
			coverageOutput: "ok  \tgithub.com/godinj/drem-orchestrator/internal/score\tcoverage: 100.0% of statements",
			wantTDD:        1.0,
		},
		{
			name:           "empty coverage output",
			coverageOutput: "",
			wantTDD:        0.0,
		},
		{
			name:           "unparseable coverage output",
			coverageOutput: "FAIL some garbage",
			wantTDD:        0.0,
		},
		{
			name: "multiple package coverage lines — average",
			coverageOutput: "ok  \tpkg/a\tcoverage: 80.0% of statements\n" +
				"ok  \tpkg/b\tcoverage: 60.0% of statements",
			wantTDD: 0.70,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := ImplScoreInput{CoverageOutput: tt.coverageOutput}
			got := ScoreImplementation(input)
			if !approxEqual(got.TDD, tt.wantTDD) {
				t.Errorf("TDD = %v, want %v", got.TDD, tt.wantTDD)
			}
		})
	}
}

func TestScoreImplementation_Constitution(t *testing.T) {
	tests := []struct {
		name             string
		passed           int
		failed           int
		wantConstitution float64
	}{
		{
			name:             "9 passed, 0 failed",
			passed:           9,
			failed:           0,
			wantConstitution: 1.0,
		},
		{
			name:             "7 passed, 2 failed",
			passed:           7,
			failed:           2,
			wantConstitution: 7.0 / 9.0,
		},
		{
			name:             "0 passed, 0 failed — treated as perfect",
			passed:           0,
			failed:           0,
			wantConstitution: 1.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := ImplScoreInput{ConstraintsPassed: tt.passed, ConstraintsFailed: tt.failed}
			got := ScoreImplementation(input)
			if !approxEqual(got.Constitution, tt.wantConstitution) {
				t.Errorf("Constitution = %v, want %v", got.Constitution, tt.wantConstitution)
			}
		})
	}
}

func TestScoreImplementation_Documentation(t *testing.T) {
	tests := []struct {
		name         string
		changedFiles []string
		wantDoc      float64
	}{
		{
			name:         "README.md in changed files",
			changedFiles: []string{"internal/score/score.go", "README.md"},
			wantDoc:      1.0,
		},
		{
			name:         "docs/ file in changed files",
			changedFiles: []string{"docs/walkthrough.md"},
			wantDoc:      1.0,
		},
		{
			name:         "only .go files, no docs",
			changedFiles: []string{"internal/score/score.go", "internal/score/score_test.go"},
			wantDoc:      0.0,
		},
		{
			name:         "both .go and docs/ files",
			changedFiles: []string{"internal/foo/foo.go", "docs/api.md"},
			wantDoc:      1.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := ImplScoreInput{ChangedFiles: tt.changedFiles}
			got := ScoreImplementation(input)
			if !approxEqual(got.Documentation, tt.wantDoc) {
				t.Errorf("Documentation = %v, want %v", got.Documentation, tt.wantDoc)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// FormatScores tests
// ---------------------------------------------------------------------------

func TestFormatScores(t *testing.T) {
	tests := []struct {
		name       string
		score      StepScore
		wantSubstr []string
	}{
		{
			name:  "all dimensions present with percentages",
			score: StepScore{TDD: 0.85, Constitution: 1.0, Documentation: 0.0},
			wantSubstr: []string{
				"TDD: 85%",
				"Constitution: 100%",
				"Docs: 0%",
			},
		},
		{
			name:  "exact format: pipe-separated",
			score: StepScore{TDD: 0.85, Constitution: 1.0, Documentation: 0.0},
			wantSubstr: []string{
				"TDD: 85% | Constitution: 100% | Docs: 0%",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatScores(tt.score)
			for _, want := range tt.wantSubstr {
				if !strings.Contains(got, want) {
					t.Errorf("FormatScores() = %q, want substring %q", got, want)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ScoresToMap tests
// ---------------------------------------------------------------------------

func TestScoresToMap(t *testing.T) {
	score := StepScore{TDD: 0.85, Constitution: 1.0, Documentation: 0.5}
	m := ScoresToMap(score)

	// Round-trip through JSON
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}

	checks := map[string]float64{
		"tdd":           0.85,
		"constitution":  1.0,
		"documentation": 0.5,
	}
	for key, want := range checks {
		got, ok := decoded[key].(float64)
		if !ok {
			t.Errorf("key %q missing or not float64 in decoded map", key)
			continue
		}
		if !approxEqual(got, want) {
			t.Errorf("decoded[%q] = %v, want %v", key, got, want)
		}
	}
}
