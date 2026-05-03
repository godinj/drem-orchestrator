package orchestrator

import (
	"math"
	"testing"

	"github.com/godinj/drem-orchestrator/pkg/score"
)

const scoreTolerance = 0.001

func scoreApproxEqual(a, b float64) bool {
	return math.Abs(a-b) < scoreTolerance
}

func TestScorePlanGate_ConvertsPlanInputs(t *testing.T) {
	subtasks := []planEntry{
		{Title: "test A", Phase: "test", TestsFor: []int{1}},
		{
			Title:          "impl A",
			Phase:          "implementation",
			EstimatedFiles: []string{"internal/foo/foo.go"},
			DepthMeta: &score.DepthMeta{
				ModuleBoundaries: []score.ModuleBoundary{
					{Package: "internal/foo", Description: "handles foo logic", Exports: 5},
				},
				InterfaceShapes: []score.InterfaceShape{
					{Package: "internal/foo", Functions: []string{"Run"}, Types: []string{"Runner"}},
				},
			},
		},
		{Title: "impl B", Phase: "implementation"},
	}
	exceptions := []tddException{{SubtaskIndex: 2, Reason: "config-only change"}}
	validation := PlanValidationResult{Valid: true, Warnings: []string{"warn"}}

	got := scorePlanGate(subtasks, exceptions, validation)

	checks := map[string]float64{
		"tdd":           0.75,
		"constitution":  0.9,
		"documentation": 0.0,
		"depth":         1.0,
	}
	for key, want := range checks {
		gotScore, ok := got[key].(float64)
		if !ok {
			t.Fatalf("scorePlanGate()[%q] = %T, want float64", key, got[key])
		}
		if !scoreApproxEqual(gotScore, want) {
			t.Errorf("scorePlanGate()[%q] = %v, want %v", key, gotScore, want)
		}
	}

	formatted, ok := got["formatted"].(string)
	if !ok {
		t.Fatalf("scorePlanGate()[formatted] = %T, want string", got["formatted"])
	}
	if formatted != "TDD: 75% | Constitution: 90% | Docs: 0% | Depth: 100%" {
		t.Errorf("formatted = %q", formatted)
	}
}

func TestScorePlanGate_DepthFallback(t *testing.T) {
	got := scorePlanGate([]planEntry{
		{Title: "impl A", Phase: "implementation", EstimatedFiles: []string{"internal/foo/foo.go"}},
		{Title: "impl B", Phase: "implementation"},
	}, nil, PlanValidationResult{Valid: true})

	depth, ok := got["depth"].(float64)
	if !ok {
		t.Fatalf("depth = %T, want float64", got["depth"])
	}
	if !scoreApproxEqual(depth, 0.5) {
		t.Errorf("depth = %v, want 0.5", depth)
	}
}

func TestScoreImplGate_UsesCanonicalImplementationScores(t *testing.T) {
	got := scoreImplGate(8, 1, []string{"internal/orchestrator/score_bridge.go", "README.md"}, "coverage: 90.0% of statements")

	checks := map[string]float64{
		"tdd":           0.9,
		"constitution":  8.0 / 9.0,
		"documentation": 1.0,
		"depth":         1.0,
	}
	for key, want := range checks {
		gotScore, ok := got[key].(float64)
		if !ok {
			t.Fatalf("scoreImplGate()[%q] = %T, want float64", key, got[key])
		}
		if !scoreApproxEqual(gotScore, want) {
			t.Errorf("scoreImplGate()[%q] = %v, want %v", key, gotScore, want)
		}
	}
}
