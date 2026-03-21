package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
)

// ---------------------------------------------------------------------------
// AgentsModel.Update tests
// ---------------------------------------------------------------------------

func TestAgentsUpdate_CursorDown(t *testing.T) {
	a := AgentsModel{
		agents: []model.Agent{
			{ID: uuid.New(), Name: "A", Status: model.AgentWorking},
			{ID: uuid.New(), Name: "B", Status: model.AgentWorking},
			{ID: uuid.New(), Name: "C", Status: model.AgentWorking},
		},
		cursor: 0,
	}

	got, _ := a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if got.cursor != 1 {
		t.Errorf("cursor = %d, want 1 after j", got.cursor)
	}
}

func TestAgentsUpdate_CursorUp(t *testing.T) {
	a := AgentsModel{
		agents: []model.Agent{
			{ID: uuid.New(), Name: "A", Status: model.AgentWorking},
			{ID: uuid.New(), Name: "B", Status: model.AgentWorking},
		},
		cursor: 1,
	}

	got, _ := a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	if got.cursor != 0 {
		t.Errorf("cursor = %d, want 0 after k", got.cursor)
	}
}

func TestAgentsUpdate_CursorClampAtEnd(t *testing.T) {
	a := AgentsModel{
		agents: []model.Agent{
			{ID: uuid.New(), Name: "A", Status: model.AgentWorking},
			{ID: uuid.New(), Name: "B", Status: model.AgentWorking},
		},
		cursor: 1,
	}

	got, _ := a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if got.cursor != 1 {
		t.Errorf("cursor = %d, want 1 (clamped at end)", got.cursor)
	}
}

func TestAgentsUpdate_CursorClampAtStart(t *testing.T) {
	a := AgentsModel{
		agents: []model.Agent{
			{ID: uuid.New(), Name: "A", Status: model.AgentWorking},
		},
		cursor: 0,
	}

	got, _ := a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	if got.cursor != 0 {
		t.Errorf("cursor = %d, want 0 (clamped at start)", got.cursor)
	}
}

func TestAgentsUpdate_DownArrow(t *testing.T) {
	a := AgentsModel{
		agents: []model.Agent{
			{ID: uuid.New(), Name: "A", Status: model.AgentWorking},
			{ID: uuid.New(), Name: "B", Status: model.AgentWorking},
		},
		cursor: 0,
	}

	got, _ := a.Update(tea.KeyMsg{Type: tea.KeyDown})
	if got.cursor != 1 {
		t.Errorf("cursor = %d, want 1 after down arrow", got.cursor)
	}
}

func TestAgentsUpdate_UpArrow(t *testing.T) {
	a := AgentsModel{
		agents: []model.Agent{
			{ID: uuid.New(), Name: "A", Status: model.AgentWorking},
			{ID: uuid.New(), Name: "B", Status: model.AgentWorking},
		},
		cursor: 1,
	}

	got, _ := a.Update(tea.KeyMsg{Type: tea.KeyUp})
	if got.cursor != 0 {
		t.Errorf("cursor = %d, want 0 after up arrow", got.cursor)
	}
}

func TestAgentsUpdate_RespectsVisibleFilter(t *testing.T) {
	taskA := uuid.New()
	taskB := uuid.New()
	a := AgentsModel{
		agents: []model.Agent{
			{ID: uuid.New(), Name: "A", Status: model.AgentWorking, CurrentTaskID: &taskA},
			{ID: uuid.New(), Name: "B", Status: model.AgentWorking, CurrentTaskID: &taskB},
		},
		autoFilter:   true,
		filterTaskID: &taskA,
		cursor:       0,
	}

	// Only 1 visible agent, so j should not move cursor
	got, _ := a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if got.cursor != 0 {
		t.Errorf("cursor = %d, want 0 (only 1 visible agent)", got.cursor)
	}
}

func TestAgentsUpdate_EmptyAgents(t *testing.T) {
	a := AgentsModel{
		agents: nil,
		cursor: 0,
	}

	// Should not panic
	got, _ := a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if got.cursor != 0 {
		t.Errorf("cursor = %d, want 0 for empty agents", got.cursor)
	}
}

// ---------------------------------------------------------------------------
// NewAgentsModel tests
// ---------------------------------------------------------------------------

func TestNewAgentsModel(t *testing.T) {
	a := NewAgentsModel()
	if !a.autoFilter {
		t.Error("autoFilter should default to true")
	}
	if a.showArchived {
		t.Error("showArchived should default to false")
	}
	if a.cursor != 0 {
		t.Errorf("cursor = %d, want 0", a.cursor)
	}
	if len(a.agents) != 0 {
		t.Errorf("agents length = %d, want 0", len(a.agents))
	}
}
