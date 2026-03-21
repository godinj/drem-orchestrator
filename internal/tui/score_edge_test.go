package tui

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// renderScoreBadge edge cases
// ---------------------------------------------------------------------------

func TestRenderScoreBadge_NoFormattedField(t *testing.T) {
	scores := map[string]any{
		"tdd":          0.9,
		"constitution": 1.0,
	}
	got := renderScoreBadge(scores)
	if got != "" {
		t.Errorf("expected empty string without 'formatted' field, got %q", got)
	}
}

func TestRenderScoreBadge_ZeroScores(t *testing.T) {
	scores := map[string]any{
		"tdd":           0.0,
		"constitution":  0.0,
		"documentation": 0.0,
		"formatted":     "TDD: 0% | Constitution: 0% | Docs: 0%",
	}
	got := renderScoreBadge(scores)
	if got == "" {
		t.Fatal("expected non-empty badge for zero scores")
	}
	if !strings.Contains(got, "TDD: 0%") {
		t.Errorf("badge should contain TDD: 0%%, got %q", got)
	}
}

func TestRenderScoreBadge_PartialScores(t *testing.T) {
	scores := map[string]any{
		"tdd":           0.5,
		"constitution":  0.5,
		"documentation": 0.5,
		"formatted":     "TDD: 50% | Constitution: 50% | Docs: 50%",
	}
	got := renderScoreBadge(scores)
	if got == "" {
		t.Fatal("expected non-empty badge for partial scores")
	}
}

func TestRenderScoreBadge_HighTDDLowConstitution(t *testing.T) {
	// This should trigger the danger color (con < 0.5)
	scores := map[string]any{
		"tdd":           0.9,
		"constitution":  0.3,
		"documentation": 1.0,
		"formatted":     "TDD: 90% | Constitution: 30% | Docs: 100%",
	}
	got := renderScoreBadge(scores)
	if got == "" {
		t.Fatal("expected non-empty badge")
	}
	if !strings.Contains(got, "Constitution: 30%") {
		t.Errorf("badge should contain Constitution: 30%%, got %q", got)
	}
}

func TestRenderScoreBadge_MediumTDDHighConstitution(t *testing.T) {
	// tdd >= 0.5 but < 0.8, con >= 0.5 => warning color
	scores := map[string]any{
		"tdd":           0.6,
		"constitution":  0.8,
		"documentation": 0.8,
		"formatted":     "TDD: 60% | Constitution: 80% | Docs: 80%",
	}
	got := renderScoreBadge(scores)
	if got == "" {
		t.Fatal("expected non-empty badge")
	}
}

func TestRenderScoreBadge_HighTDDLowDocs(t *testing.T) {
	// tdd >= 0.8, con >= 0.5, doc < 0.5 => warning
	scores := map[string]any{
		"tdd":           0.9,
		"constitution":  0.9,
		"documentation": 0.3,
		"formatted":     "TDD: 90% | Constitution: 90% | Docs: 30%",
	}
	got := renderScoreBadge(scores)
	if got == "" {
		t.Fatal("expected non-empty badge")
	}
}

// ---------------------------------------------------------------------------
// taskScoreBadge edge cases
// ---------------------------------------------------------------------------

func TestTaskScoreBadge_NilContext(t *testing.T) {
	got := taskScoreBadge(nil)
	if got != "" {
		t.Errorf("expected empty for nil context, got %q", got)
	}
}

func TestTaskScoreBadge_NoScoresKey(t *testing.T) {
	ctx := map[string]any{
		"other_key": "value",
	}
	got := taskScoreBadge(ctx)
	if got != "" {
		t.Errorf("expected empty for context without scores, got %q", got)
	}
}

func TestTaskScoreBadge_ScoresWrongType(t *testing.T) {
	ctx := map[string]any{
		"scores": "not a map",
	}
	got := taskScoreBadge(ctx)
	if got != "" {
		t.Errorf("expected empty for scores of wrong type, got %q", got)
	}
}

func TestTaskScoreBadge_ZeroScores(t *testing.T) {
	ctx := map[string]any{
		"scores": map[string]any{
			"tdd":           0.0,
			"constitution":  0.0,
			"documentation": 0.0,
		},
	}
	got := taskScoreBadge(ctx)
	if got == "" {
		t.Fatal("expected non-empty badge for zero scores")
	}
	if !strings.Contains(got, "T:0") {
		t.Errorf("badge should contain T:0, got %q", got)
	}
	if !strings.Contains(got, "C:0") {
		t.Errorf("badge should contain C:0, got %q", got)
	}
	if !strings.Contains(got, "D:0") {
		t.Errorf("badge should contain D:0, got %q", got)
	}
}

func TestTaskScoreBadge_AllPerfect(t *testing.T) {
	ctx := map[string]any{
		"scores": map[string]any{
			"tdd":           1.0,
			"constitution":  1.0,
			"documentation": 1.0,
		},
	}
	got := taskScoreBadge(ctx)
	if got == "" {
		t.Fatal("expected non-empty badge for perfect scores")
	}
	if !strings.Contains(got, "T:100") {
		t.Errorf("badge should contain T:100, got %q", got)
	}
}

func TestTaskScoreBadge_LowTDD(t *testing.T) {
	ctx := map[string]any{
		"scores": map[string]any{
			"tdd":           0.3,
			"constitution":  1.0,
			"documentation": 1.0,
		},
	}
	got := taskScoreBadge(ctx)
	if got == "" {
		t.Fatal("expected non-empty badge")
	}
	if !strings.Contains(got, "T:30") {
		t.Errorf("badge should contain T:30, got %q", got)
	}
}
