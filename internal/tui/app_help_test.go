package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
)

func TestHelpOverlayToggle(t *testing.T) {
	tests := []struct {
		name     string
		initial  bool // initial showHelp state
		key      tea.KeyMsg
		wantHelp bool
	}{
		{
			name:     "? toggles help on when off",
			initial:  false,
			key:      tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}},
			wantHelp: true,
		},
		{
			name:     "? toggles help off when on",
			initial:  true,
			key:      tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}},
			wantHelp: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := Model{
				showHelp: tt.initial,
				keys:     defaultKeyMap(),
				focus:    FocusBoard,
				width:    120,
				height:   40,
			}
			m.updatePanelSizes()

			result, _ := m.Update(tt.key)
			got := result.(Model)
			if got.showHelp != tt.wantHelp {
				t.Errorf("showHelp = %v, want %v", got.showHelp, tt.wantHelp)
			}
		})
	}
}

func TestHelpOverlayDismissOnEsc(t *testing.T) {
	m := Model{
		showHelp: true,
		keys:     defaultKeyMap(),
		focus:    FocusBoard,
		width:    120,
		height:   40,
	}
	m.updatePanelSizes()

	result, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	got := result.(Model)
	if got.showHelp {
		t.Error("showHelp should be false after Esc, got true")
	}
}

func TestHelpOverlayDismissOnActionKey(t *testing.T) {
	tests := []struct {
		name   string
		key    tea.KeyMsg
		verify func(t *testing.T, got Model)
	}{
		{
			name: "approve key consumed",
			key:  tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}},
		},
		{
			name: "reject key consumed",
			key:  tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}},
		},
		{
			name: "archive toggle consumed without side effect",
			key:  tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'A'}},
			verify: func(t *testing.T, got Model) {
				t.Helper()
				if got.agents.showArchived {
					t.Error("archive toggle should not execute when help is shown")
				}
			},
		},
		{
			name: "filter toggle consumed without side effect",
			key:  tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'F'}},
			verify: func(t *testing.T, got Model) {
				t.Helper()
				if !got.agents.autoFilter {
					t.Error("filter toggle should not execute when help is shown")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := Model{
				showHelp: true,
				keys:     defaultKeyMap(),
				focus:    FocusBoard,
				agents:   AgentsModel{autoFilter: true},
				width:    120,
				height:   40,
			}
			m.updatePanelSizes()

			result, cmd := m.Update(tt.key)
			got := result.(Model)
			if got.showHelp {
				t.Error("showHelp should be false after action key, got true")
			}
			if cmd != nil {
				t.Error("action key should be consumed by help overlay (cmd should be nil)")
			}
			if tt.verify != nil {
				tt.verify(t, got)
			}
		})
	}
}

func TestHelpToggleFromAllFocusStates(t *testing.T) {
	tests := []struct {
		name  string
		focus Focus
	}{
		{name: "FocusBoard", focus: FocusBoard},
		{name: "FocusAgents", focus: FocusAgents},
		{name: "FocusDetail", focus: FocusDetail},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := Model{
				showHelp: false,
				keys:     defaultKeyMap(),
				focus:    tt.focus,
				width:    120,
				height:   40,
			}
			m.updatePanelSizes()

			result, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
			got := result.(Model)
			if !got.showHelp {
				t.Errorf("showHelp should be true after ? from %s, got false", tt.name)
			}
		})
	}
}

func TestHelpBarRemovedFromView(t *testing.T) {
	m := Model{
		showHelp: false,
		keys:     defaultKeyMap(),
		focus:    FocusBoard,
		width:    120,
		height:   40,
	}
	m.board.tasks = []model.Task{
		{ID: uuid.New(), Title: "Test Task", Status: model.StatusBacklog},
	}
	m.updatePanelSizes()

	output := m.View()

	if strings.Contains(output, "C:clean-sessions") {
		t.Error("View should not contain old static legend 'C:clean-sessions'")
	}
	if !strings.Contains(output, "?") {
		t.Error("View should contain '?' as a help hint")
	}
}

func TestHelpOverlayAppearsInView(t *testing.T) {
	tests := []struct {
		name        string
		focus       Focus
		wantContain string
	}{
		{
			name:        "FocusBoard shows board actions",
			focus:       FocusBoard,
			wantContain: "approve",
		},
		{
			name:        "FocusAgents shows agent actions",
			focus:       FocusAgents,
			wantContain: "jump",
		},
		{
			name:        "FocusDetail shows detail actions",
			focus:       FocusDetail,
			wantContain: "scroll",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := Model{
				showHelp: true,
				keys:     defaultKeyMap(),
				focus:    tt.focus,
				width:    120,
				height:   40,
			}
			taskStatus := model.StatusPlanReview
			if tt.focus == FocusDetail {
				taskStatus = model.StatusPlanReview
			}
			m.board.tasks = []model.Task{
				{ID: uuid.New(), Title: "Test Task", Status: taskStatus},
			}
			m.updatePanelSizes()

			output := m.View()

			// Help overlay should replace the normal dashboard layout.
			if strings.Contains(output, "Drem Orchestrator") {
				t.Error("help overlay should replace normal dashboard view, but title bar is present")
			}

			lower := strings.ToLower(output)
			if !strings.Contains(lower, tt.wantContain) {
				t.Errorf("help overlay for %s should contain %q", tt.name, tt.wantContain)
			}
		})
	}
}
