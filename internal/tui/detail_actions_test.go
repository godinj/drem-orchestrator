package tui

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
)

// ---------------------------------------------------------------------------
// availableActions tests
// ---------------------------------------------------------------------------

func TestAvailableActions_NilTask(t *testing.T) {
	d := DetailModel{task: nil}
	result := d.availableActions()
	if result != "" {
		t.Errorf("expected empty string for nil task, got %q", result)
	}
}

func TestAvailableActions_PlanReview(t *testing.T) {
	d := DetailModel{
		task: &model.Task{
			ID:     uuid.New(),
			Status: model.StatusPlanReview,
		},
	}
	result := d.availableActions()

	wantParts := []string{"pprove", "eject", "review", "omment", "elete"}
	for _, part := range wantParts {
		if !strings.Contains(result, part) {
			t.Errorf("availableActions for plan_review should contain %q, got %q", part, result)
		}
	}
}

func TestAvailableActions_TestingReady(t *testing.T) {
	d := DetailModel{
		task: &model.Task{
			ID:     uuid.New(),
			Status: model.StatusTestingReady,
		},
	}
	result := d.availableActions()

	wantParts := []string{"est pass", "ail test", "review", "fix", "omment", "elete"}
	for _, part := range wantParts {
		if !strings.Contains(result, part) {
			t.Errorf("availableActions for testing_ready should contain %q, got %q", part, result)
		}
	}
}

func TestAvailableActions_InProgress(t *testing.T) {
	d := DetailModel{
		task: &model.Task{
			ID:     uuid.New(),
			Status: model.StatusInProgress,
		},
	}
	result := d.availableActions()

	if !strings.Contains(result, "ause") {
		t.Errorf("availableActions for in_progress should contain 'pause', got %q", result)
	}
	if !strings.Contains(result, "fix") {
		t.Errorf("availableActions for in_progress should contain 'fix', got %q", result)
	}
}

func TestAvailableActions_Paused(t *testing.T) {
	d := DetailModel{
		task: &model.Task{
			ID:     uuid.New(),
			Status: model.StatusPaused,
		},
	}
	result := d.availableActions()

	if !strings.Contains(strings.ToLower(result), "resume") {
		t.Errorf("availableActions for paused should contain 'resume', got %q", result)
	}
}

func TestAvailableActions_Failed(t *testing.T) {
	d := DetailModel{
		task: &model.Task{
			ID:     uuid.New(),
			Status: model.StatusFailed,
		},
	}
	result := d.availableActions()

	wantParts := []string{"etry", "fix"}
	for _, part := range wantParts {
		if !strings.Contains(result, part) {
			t.Errorf("availableActions for failed should contain %q, got %q", part, result)
		}
	}
}

func TestAvailableActions_SupervisorAlwaysPresent(t *testing.T) {
	statuses := []model.TaskStatus{
		model.StatusBacklog,
		model.StatusPlanning,
		model.StatusPlanReview,
		model.StatusInProgress,
		model.StatusTestingReady,
		model.StatusPaused,
		model.StatusDone,
		model.StatusFailed,
	}

	for _, s := range statuses {
		t.Run(string(s), func(t *testing.T) {
			d := DetailModel{
				task: &model.Task{ID: uuid.New(), Status: s},
			}
			result := d.availableActions()
			if !strings.Contains(result, "upervisor") {
				t.Errorf("availableActions for %s should contain Supervisor, got %q", s, result)
			}
		})
	}
}

func TestAvailableActions_AgentActions(t *testing.T) {
	agentID := uuid.New()

	t.Run("agent with tmux session shows jump and log", func(t *testing.T) {
		d := DetailModel{
			task: &model.Task{ID: uuid.New(), Status: model.StatusInProgress},
			agent: &model.Agent{
				ID:          agentID,
				TmuxSession: "session:sess-1",
			},
		}
		result := d.availableActions()
		if !strings.Contains(strings.ToLower(result), "log") {
			t.Errorf("should contain log action with agent, got %q", result)
		}
		if !strings.Contains(strings.ToLower(result), "jump") {
			t.Errorf("should contain jump action with tmux session, got %q", result)
		}
	})

	t.Run("agent without tmux session shows log only", func(t *testing.T) {
		d := DetailModel{
			task: &model.Task{ID: uuid.New(), Status: model.StatusInProgress},
			agent: &model.Agent{
				ID:          agentID,
				TmuxSession: "",
			},
		}
		result := d.availableActions()
		if !strings.Contains(strings.ToLower(result), "log") {
			t.Errorf("should contain log action with agent, got %q", result)
		}
		if strings.Contains(strings.ToLower(result), "jump") {
			t.Errorf("should NOT contain jump action without tmux session, got %q", result)
		}
	})

	t.Run("no agent hides log and jump", func(t *testing.T) {
		d := DetailModel{
			task:  &model.Task{ID: uuid.New(), Status: model.StatusInProgress},
			agent: nil,
		}
		result := d.availableActions()
		if strings.Contains(result, "[l]") {
			t.Errorf("should not contain log action without agent, got %q", result)
		}
		if strings.Contains(result, "[g]") {
			t.Errorf("should not contain jump action without agent, got %q", result)
		}
	})
}

func TestAvailableActions_DeleteMode(t *testing.T) {
	d := DetailModel{
		task: &model.Task{
			ID:     uuid.New(),
			Status: model.StatusBacklog,
		},
		deleteMode:   true,
		deleteCursor: 0,
	}
	result := d.availableActions()

	if !strings.Contains(strings.ToLower(result), "navigate") {
		t.Errorf("delete mode should contain navigate hint, got %q", result)
	}
	if !strings.Contains(strings.ToLower(result), "cancel") {
		t.Errorf("delete mode should contain cancel hint, got %q", result)
	}
	// Should not contain normal actions
	if strings.Contains(strings.ToLower(result), "approve") {
		t.Errorf("delete mode should not contain approve, got %q", result)
	}
}

func TestAvailableActions_DeleteModeWithPlanStep(t *testing.T) {
	d := DetailModel{
		task: &model.Task{
			ID:     uuid.New(),
			Status: model.StatusPlanReview,
			Plan: model.JSONField{
				"subtasks": []any{
					map[string]any{"title": "Step 1"},
				},
			},
		},
		deleteMode:   true,
		deleteCursor: 0,
	}
	result := d.availableActions()

	if !strings.Contains(strings.ToLower(result), "plan step") {
		t.Errorf("delete mode on plan step should mention 'plan step', got %q", result)
	}
}

func TestAvailableActions_DeleteModeWithComment(t *testing.T) {
	d := DetailModel{
		task: &model.Task{
			ID:     uuid.New(),
			Status: model.StatusBacklog,
		},
		comments: []model.TaskComment{
			{ID: uuid.New(), Author: "user", Body: "Hello"},
		},
		deleteMode:   true,
		deleteCursor: 0, // comment is at index 0
	}
	result := d.availableActions()

	if !strings.Contains(strings.ToLower(result), "comment") {
		t.Errorf("delete mode on comment should mention 'comment', got %q", result)
	}
}

// ---------------------------------------------------------------------------
// renderScoreSection tests
// ---------------------------------------------------------------------------

func TestRenderScoreSection_ValidScores(t *testing.T) {
	scores := map[string]any{
		"tdd":           0.9,
		"constitution":  1.0,
		"documentation": 0.5,
		"formatted":     "TDD: 90% | Constitution: 100% | Docs: 50%",
	}
	result := renderScoreSection(scores)
	if result == "" {
		t.Error("renderScoreSection should return non-empty for valid scores")
	}
	if !strings.Contains(result, "TDD: 90%") {
		t.Errorf("result should contain formatted score text, got %q", result)
	}
}

func TestRenderScoreSection_NoFormattedField(t *testing.T) {
	scores := map[string]any{
		"tdd":          0.9,
		"constitution": 1.0,
	}
	result := renderScoreSection(scores)
	if result != "" {
		t.Errorf("renderScoreSection without 'formatted' should return empty, got %q", result)
	}
}

func TestRenderScoreSection_LowTDD(t *testing.T) {
	scores := map[string]any{
		"tdd":           0.3,
		"constitution":  1.0,
		"documentation": 1.0,
		"formatted":     "TDD: 30%",
	}
	result := renderScoreSection(scores)
	if result == "" {
		t.Error("renderScoreSection should return non-empty for low TDD")
	}
}

func TestRenderScoreSection_LowConstitution(t *testing.T) {
	scores := map[string]any{
		"tdd":           1.0,
		"constitution":  0.3,
		"documentation": 1.0,
		"formatted":     "Constitution: 30%",
	}
	result := renderScoreSection(scores)
	if result == "" {
		t.Error("renderScoreSection should return non-empty for low constitution")
	}
}

func TestRenderScoreSection_MediumTDD(t *testing.T) {
	scores := map[string]any{
		"tdd":           0.7,
		"constitution":  1.0,
		"documentation": 1.0,
		"formatted":     "TDD: 70%",
	}
	result := renderScoreSection(scores)
	if result == "" {
		t.Error("renderScoreSection should return non-empty for medium TDD")
	}
}

func TestRenderScoreSection_LowDocs(t *testing.T) {
	scores := map[string]any{
		"tdd":           1.0,
		"constitution":  1.0,
		"documentation": 0.3,
		"formatted":     "Docs: 30%",
	}
	result := renderScoreSection(scores)
	if result == "" {
		t.Error("renderScoreSection should return non-empty for low docs")
	}
}
