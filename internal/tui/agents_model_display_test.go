package tui

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
)

// ---------------------------------------------------------------------------
// AgentsModel.View() — ModelID and Cost Display Tests
// ---------------------------------------------------------------------------

func TestAgentsViewDisplay_ModelIDAndCostColumns(t *testing.T) {
	tests := []struct {
		name        string
		agents      []model.Agent
		width       int
		wantModelID string
		wantCost    string
	}{
		{
			name: "agent with valid ModelID and cost",
			agents: []model.Agent{
				{
					ID:           uuid.New(),
					Name:         "test-agent",
					Status:       model.AgentWorking,
					ModelID:      "claude-opus-4",
					TotalCostUSD: 1.50,
				},
			},
			width:       120,
			wantModelID: "claude-opus-4",
			wantCost:    "$1.50",
		},
		{
			name: "agent with empty ModelID shows dash",
			agents: []model.Agent{
				{
					ID:           uuid.New(),
					Name:         "test-agent",
					Status:       model.AgentWorking,
					ModelID:      "",
					TotalCostUSD: 0.42,
				},
			},
			width:       120,
			wantModelID: "-",
			wantCost:    "$0.42",
		},
		{
			name: "agent with zero cost shows dollar zero",
			agents: []model.Agent{
				{
					ID:           uuid.New(),
					Name:         "test-agent",
					Status:       model.AgentWorking,
					ModelID:      "claude-haiku",
					TotalCostUSD: 0.0,
				},
			},
			width:       120,
			wantModelID: "claude-haiku",
			wantCost:    "$0.00",
		},
		{
			name: "nil agent list renders without panic",
			agents: []model.Agent{
				{
					ID:     uuid.New(),
					Name:   "test-agent",
					Status: model.AgentWorking,
				},
			},
			width:       120,
			wantModelID: "-",
			wantCost:    "$0.00",
		},
		{
			name: "cost with three decimal places rounds correctly",
			agents: []model.Agent{
				{
					ID:           uuid.New(),
					Name:         "test-agent",
					Status:       model.AgentWorking,
					ModelID:      "claude-opus",
					TotalCostUSD: 0.123456,
				},
			},
			width:       120,
			wantModelID: "claude-opus",
			wantCost:    "$0.12",
		},
		{
			name: "large cost displays with proper formatting",
			agents: []model.Agent{
				{
					ID:           uuid.New(),
					Name:         "test-agent",
					Status:       model.AgentWorking,
					ModelID:      "claude-opus-4",
					TotalCostUSD: 123.45,
				},
			},
			width:       120,
			wantModelID: "claude-opus-4",
			wantCost:    "$123.45",
		},
		{
			name: "long ModelID displayed at narrow width",
			agents: []model.Agent{
				{
					ID:           uuid.New(),
					Name:         "test-agent",
					Status:       model.AgentWorking,
					ModelID:      "claude-opus-4-20250101-very-long-version-string",
					TotalCostUSD: 1.0,
				},
			},
			width:       80,
			wantModelID: "claude-opus-4-20250101-very-long-version-string",
			wantCost:    "$1.00",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := AgentsModel{
				agents:     tt.agents,
				cursor:     0,
				width:      tt.width,
				height:     40,
				autoFilter: true,
			}

			view := a.View()
			if view == "" {
				t.Fatalf("View() returned empty string")
			}

			output := strings.Join(strings.Split(view, "\n"), " ")

			if !strings.Contains(output, tt.wantModelID) {
				t.Errorf("View output missing model ID %q in:\n%s", tt.wantModelID, view)
			}
			if !strings.Contains(output, tt.wantCost) {
				t.Errorf("View output missing cost %q in:\n%s", tt.wantCost, view)
			}
		})
	}
}

func TestAgentsViewDisplay_MultipleAgentsWithDifferentValues(t *testing.T) {
	t.Run("multiple agents with varied ModelID and TotalCostUSD values", func(t *testing.T) {
		agents := []model.Agent{
			{
				ID:           uuid.New(),
				Name:         "agent-1",
				Status:       model.AgentWorking,
				ModelID:      "claude-opus-4",
				TotalCostUSD: 5.25,
			},
			{
				ID:           uuid.New(),
				Name:         "agent-2",
				Status:       model.AgentWorking,
				ModelID:      "claude-haiku",
				TotalCostUSD: 0.05,
			},
			{
				ID:           uuid.New(),
				Name:         "agent-3",
				Status:       model.AgentWorking,
				ModelID:      "",
				TotalCostUSD: 1.11,
			},
			{
				ID:           uuid.New(),
				Name:         "agent-4",
				Status:       model.AgentWorking,
				ModelID:      "claude-sonnet",
				TotalCostUSD: 0.0,
			},
		}

		a := AgentsModel{
			agents:     agents,
			cursor:     0,
			width:      120,
			height:     40,
			autoFilter: true,
		}

		view := a.View()

		if !strings.Contains(view, "agent-1") {
			t.Error("agent-1 not found in output")
		}
		if !strings.Contains(view, "claude-opus-4") {
			t.Error("agent-1 ModelID not found")
		}
		if !strings.Contains(view, "$5.25") {
			t.Error("agent-1 cost not found")
		}

		if !strings.Contains(view, "agent-2") {
			t.Error("agent-2 not found in output")
		}
		if !strings.Contains(view, "claude-haiku") {
			t.Error("agent-2 ModelID not found")
		}
		if !strings.Contains(view, "$0.05") {
			t.Error("agent-2 cost not found")
		}

		if !strings.Contains(view, "agent-3") {
			t.Error("agent-3 not found in output")
		}
		if !strings.Contains(view, "$1.11") {
			t.Error("agent-3 cost not found")
		}

		if !strings.Contains(view, "agent-4") {
			t.Error("agent-4 not found in output")
		}
		if !strings.Contains(view, "claude-sonnet") {
			t.Error("agent-4 ModelID not found")
		}
	})
}

func TestAgentsViewDisplay_ModelIDAndCostForArchivedAgents(t *testing.T) {
	t.Run("dead agent values visible when showArchived true", func(t *testing.T) {
		a := AgentsModel{
			agents: []model.Agent{
				{
					ID:           uuid.New(),
					Name:         "dead-agent",
					Status:       model.AgentDead,
					ModelID:      "claude-opus-4",
					TotalCostUSD: 3.14,
				},
			},
			cursor:       0,
			width:        120,
			height:       40,
			showArchived: true,
			autoFilter:   true,
		}

		view := a.View()
		if !strings.Contains(view, "dead-agent") {
			t.Error("dead agent should be visible when showArchived is true")
		}
		if !strings.Contains(view, "claude-opus-4") {
			t.Error("ModelID should be visible for dead agent")
		}
		if !strings.Contains(view, "$3.14") {
			t.Error("cost should be visible for dead agent")
		}
	})

	t.Run("dead agent hidden when showArchived false", func(t *testing.T) {
		a := AgentsModel{
			agents: []model.Agent{
				{
					ID:           uuid.New(),
					Name:         "dead-agent",
					Status:       model.AgentDead,
					ModelID:      "claude-opus-4",
					TotalCostUSD: 3.14,
				},
			},
			cursor:       0,
			width:        120,
			height:       40,
			showArchived: false,
			autoFilter:   true,
		}

		view := a.View()
		if strings.Contains(view, "dead-agent") {
			t.Error("dead agent should not be visible when showArchived is false")
		}
	})
}

func TestAgentsViewDisplay_ColumnFormatting(t *testing.T) {
	t.Run("model ID and cost appear for both agents", func(t *testing.T) {
		a := AgentsModel{
			agents: []model.Agent{
				{
					ID:           uuid.New(),
					Name:         "short-name",
					Status:       model.AgentWorking,
					ModelID:      "claude-opus-4",
					TotalCostUSD: 1.5,
				},
				{
					ID:           uuid.New(),
					Name:         "very-long-agent-name-that-might-truncate",
					Status:       model.AgentWorking,
					ModelID:      "claude-haiku",
					TotalCostUSD: 0.1,
				},
			},
			cursor:     0,
			width:      120,
			height:     40,
			autoFilter: true,
		}

		view := a.View()

		if !strings.Contains(view, "claude-opus-4") {
			t.Error("first agent ModelID not found")
		}
		if !strings.Contains(view, "$1.50") {
			t.Error("first agent cost not found")
		}
		if !strings.Contains(view, "claude-haiku") {
			t.Error("second agent ModelID not found")
		}
		if !strings.Contains(view, "$0.10") {
			t.Error("second agent cost not found")
		}
	})
}
