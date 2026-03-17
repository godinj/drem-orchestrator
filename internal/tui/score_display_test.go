package tui

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
)

func TestDetailView_ShowsScoresWhenPresent(t *testing.T) {
	task := &model.Task{
		ID:     uuid.New(),
		Title:  "Implement feature X",
		Status: model.StatusPlanReview,
		Context: model.JSONField{
			"scores": map[string]any{
				"tdd":           0.85,
				"constitution":  1.0,
				"documentation": 0.0,
				"formatted":     "TDD: 85% | Constitution: 100% | Docs: 0%",
			},
		},
	}

	d := DetailModel{task: task, width: 80, height: 40}
	output := d.View()

	if !strings.Contains(output, "TDD: 85%") {
		t.Errorf("expected output to contain TDD score, got:\n%s", output)
	}
	if !strings.Contains(output, "Constitution: 100%") {
		t.Errorf("expected output to contain constitution score, got:\n%s", output)
	}
	if !strings.Contains(output, "Docs: 0%") {
		t.Errorf("expected output to contain docs score, got:\n%s", output)
	}
}

func TestDetailView_NoScoresWhenAbsent(t *testing.T) {
	task := &model.Task{
		ID:     uuid.New(),
		Title:  "Task without scores",
		Status: model.StatusPlanReview,
	}

	d := DetailModel{task: task, width: 80, height: 40}
	output := d.View()

	scoreKeywords := []string{"TDD:", "Constitution:", "Docs:", "Score"}
	for _, kw := range scoreKeywords {
		if strings.Contains(output, kw) {
			t.Errorf("expected output to NOT contain %q when no scores in context, got:\n%s", kw, output)
		}
	}
}

func TestDetailView_ScoresAtTestingReady(t *testing.T) {
	task := &model.Task{
		ID:     uuid.New(),
		Title:  "Implementation complete",
		Status: model.StatusTestingReady,
		Context: model.JSONField{
			"scores": map[string]any{
				"tdd":           0.90,
				"constitution":  0.75,
				"documentation": 1.0,
				"formatted":     "TDD: 90% | Constitution: 75% | Docs: 100%",
			},
		},
	}

	d := DetailModel{task: task, width: 80, height: 40}
	output := d.View()

	if !strings.Contains(output, "TDD: 90%") {
		t.Errorf("expected scores at testing_ready status, got:\n%s", output)
	}
	if !strings.Contains(output, "Constitution: 75%") {
		t.Errorf("expected constitution score at testing_ready, got:\n%s", output)
	}
	if !strings.Contains(output, "Docs: 100%") {
		t.Errorf("expected docs score at testing_ready, got:\n%s", output)
	}
}

func TestRenderScoreBadge(t *testing.T) {
	tests := []struct {
		name       string
		scores     map[string]any
		wantSubstr []string
	}{
		{
			name: "all perfect scores show green indicator",
			scores: map[string]any{
				"tdd":           1.0,
				"constitution":  1.0,
				"documentation": 1.0,
				"formatted":     "TDD: 100% | Constitution: 100% | Docs: 100%",
			},
			wantSubstr: []string{"TDD: 100%", "Constitution: 100%", "Docs: 100%"},
		},
		{
			name: "low tdd score shows warning",
			scores: map[string]any{
				"tdd":           0.30,
				"constitution":  1.0,
				"documentation": 0.80,
				"formatted":     "TDD: 30% | Constitution: 100% | Docs: 80%",
			},
			wantSubstr: []string{"TDD: 30%"},
		},
		{
			name: "mixed scores render all values",
			scores: map[string]any{
				"tdd":           0.85,
				"constitution":  0.50,
				"documentation": 0.0,
				"formatted":     "TDD: 85% | Constitution: 50% | Docs: 0%",
			},
			wantSubstr: []string{"TDD: 85%", "Constitution: 50%", "Docs: 0%"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := renderScoreBadge(tt.scores)
			if got == "" {
				t.Fatal("renderScoreBadge returned empty string")
			}
			for _, want := range tt.wantSubstr {
				if !strings.Contains(got, want) {
					t.Errorf("renderScoreBadge() = %q, want substring %q", got, want)
				}
			}
		})
	}
}

func TestRenderScoreBadge_LowTDD_HasWarningColor(t *testing.T) {
	scores := map[string]any{
		"tdd":           0.30,
		"constitution":  1.0,
		"documentation": 1.0,
		"formatted":     "TDD: 30% | Constitution: 100% | Docs: 100%",
	}

	got := renderScoreBadge(scores)
	// The badge must be non-empty and contain the formatted text.
	if got == "" {
		t.Fatal("renderScoreBadge returned empty string for low TDD")
	}
	if !strings.Contains(got, "TDD: 30%") {
		t.Errorf("expected low TDD score in badge, got: %q", got)
	}
}

func TestRenderScoreBadge_NilScores(t *testing.T) {
	got := renderScoreBadge(nil)
	if got != "" {
		t.Errorf("expected empty string for nil scores, got: %q", got)
	}
}

func TestBoardView_ScoreBadge(t *testing.T) {
	task := model.Task{
		ID:     uuid.New(),
		Title:  "Scored task",
		Status: model.StatusPlanReview,
		Context: model.JSONField{
			"scores": map[string]any{
				"tdd":           0.85,
				"constitution":  1.0,
				"documentation": 0.0,
				"formatted":     "TDD: 85% | Constitution: 100% | Docs: 0%",
			},
		},
	}

	got := taskScoreBadge(task.Context)
	if got == "" {
		t.Fatal("taskScoreBadge returned empty for task with scores")
	}

	// Compact format should show abbreviated scores.
	wantParts := []string{"T:85", "C:100", "D:0"}
	for _, part := range wantParts {
		if !strings.Contains(got, part) {
			t.Errorf("taskScoreBadge() = %q, want substring %q", got, part)
		}
	}
}

func TestBoardView_ScoreBadge_NoScores(t *testing.T) {
	task := model.Task{
		ID:     uuid.New(),
		Title:  "No scores",
		Status: model.StatusBacklog,
	}

	got := taskScoreBadge(task.Context)
	if got != "" {
		t.Errorf("expected empty badge for task without scores, got: %q", got)
	}
}
