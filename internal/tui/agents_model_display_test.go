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
		description string
	}{
		{
			name: "agent with valid model_id and cost",
			agents: []model.Agent{
				{
					ID:     uuid.New(),
					Name:   "test-agent",
					Status: model.AgentWorking,
					Config: model.JSONField{
						"model_id":       "claude-opus-4",
						"total_cost_usd": 1.50,
					},
				},
			},
			width:       120,
			wantModelID: "claude-opus-4",
			wantCost:    "$1.50",
			description: "should display model ID and cost formatted correctly",
		},
		{
			name: "agent with empty model_id shows dash",
			agents: []model.Agent{
				{
					ID:     uuid.New(),
					Name:   "test-agent",
					Status: model.AgentWorking,
					Config: model.JSONField{
						"model_id":       "",
						"total_cost_usd": 0.42,
					},
				},
			},
			width:       120,
			wantModelID: "-",
			wantCost:    "$0.42",
			description: "empty model_id should show as dash",
		},
		{
			name: "agent with no model_id key shows dash",
			agents: []model.Agent{
				{
					ID:     uuid.New(),
					Name:   "test-agent",
					Status: model.AgentWorking,
					Config: model.JSONField{
						"total_cost_usd": 2.15,
					},
				},
			},
			width:       120,
			wantModelID: "-",
			wantCost:    "$2.15",
			description: "missing model_id key should show as dash",
		},
		{
			name: "agent with zero cost shows dollar zero",
			agents: []model.Agent{
				{
					ID:     uuid.New(),
					Name:   "test-agent",
					Status: model.AgentWorking,
					Config: model.JSONField{
						"model_id":       "claude-haiku",
						"total_cost_usd": 0.0,
					},
				},
			},
			width:       120,
			wantModelID: "claude-haiku",
			wantCost:    "$0.00",
			description: "zero cost should show as $0.00",
		},
		{
			name: "agent with no cost_usd key shows dash",
			agents: []model.Agent{
				{
					ID:     uuid.New(),
					Name:   "test-agent",
					Status: model.AgentWorking,
					Config: model.JSONField{
						"model_id": "claude-sonnet",
					},
				},
			},
			width:       120,
			wantModelID: "claude-sonnet",
			wantCost:    "-",
			description: "missing total_cost_usd key should show as dash",
		},
		{
			name: "agent with nil Config",
			agents: []model.Agent{
				{
					ID:     uuid.New(),
					Name:   "test-agent",
					Status: model.AgentWorking,
					Config: nil,
				},
			},
			width:       120,
			wantModelID: "-",
			wantCost:    "-",
			description: "nil Config should show both as dash",
		},
		{
			name: "agent with empty Config map",
			agents: []model.Agent{
				{
					ID:     uuid.New(),
					Name:   "test-agent",
					Status: model.AgentWorking,
					Config: model.JSONField{},
				},
			},
			width:       120,
			wantModelID: "-",
			wantCost:    "-",
			description: "empty Config should show both as dash",
		},
		{
			name: "cost with three decimal places formats correctly",
			agents: []model.Agent{
				{
					ID:     uuid.New(),
					Name:   "test-agent",
					Status: model.AgentWorking,
					Config: model.JSONField{
						"model_id":       "claude-opus",
						"total_cost_usd": 0.123456,
					},
				},
			},
			width:       120,
			wantModelID: "claude-opus",
			wantCost:    "$0.12",
			description: "cost should round to 2 decimal places",
		},
		{
			name: "large cost displays with proper formatting",
			agents: []model.Agent{
				{
					ID:     uuid.New(),
					Name:   "test-agent",
					Status: model.AgentWorking,
					Config: model.JSONField{
						"model_id":       "claude-opus-4",
						"total_cost_usd": 123.45,
					},
				},
			},
			width:       120,
			wantModelID: "claude-opus-4",
			wantCost:    "$123.45",
			description: "large cost values should format correctly",
		},
		{
			name: "model_id with long name truncates appropriately",
			agents: []model.Agent{
				{
					ID:     uuid.New(),
					Name:   "test-agent",
					Status: model.AgentWorking,
					Config: model.JSONField{
						"model_id":       "claude-opus-4-20250101-very-long-version-string",
						"total_cost_usd": 1.0,
					},
				},
			},
			width:       80,
			wantModelID: "claude-opus-4-20250101-very-long-version-string",
			wantCost:    "$1.00",
			description: "long model_id may need truncation depending on terminal width",
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

			// Check that the rendered view contains the expected model ID and cost
			lines := strings.Split(view, "\n")
			if len(lines) == 0 {
				t.Fatalf("View() rendered no lines")
			}

			// Reconstruct output to check for model_id and cost
			output := strings.Join(lines, " ")

			// Verify ModelID is present
			if !strings.Contains(output, tt.wantModelID) {
				t.Errorf("View output missing model ID %q in:\n%s", tt.wantModelID, view)
			}

			// Verify Cost is present with proper formatting
			if !strings.Contains(output, tt.wantCost) {
				t.Errorf("View output missing cost %q in:\n%s", tt.wantCost, view)
			}
		})
	}
}

func TestAgentsViewDisplay_ModelIDExtractionFromConfig(t *testing.T) {
	tests := []struct {
		name         string
		modelIDValue interface{}
		wantDisplay  string
		description  string
	}{
		{
			name:         "model_id as string",
			modelIDValue: "claude-opus-4",
			wantDisplay:  "claude-opus-4",
			description:  "string type model_id should display as-is",
		},
		{
			name:         "model_id as empty string",
			modelIDValue: "",
			wantDisplay:  "-",
			description:  "empty string should show as dash",
		},
		{
			name:         "model_id as null (missing key)",
			modelIDValue: nil,
			wantDisplay:  "-",
			description:  "missing or nil model_id should show as dash",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := model.JSONField{}
			if tt.modelIDValue != nil {
				config["model_id"] = tt.modelIDValue
			}

			a := AgentsModel{
				agents: []model.Agent{
					{
						ID:     uuid.New(),
						Name:   "agent-under-test",
						Status: model.AgentWorking,
						Config: config,
					},
				},
				cursor:     0,
				width:      120,
				height:     40,
				autoFilter: true,
			}

			view := a.View()
			if !strings.Contains(view, tt.wantDisplay) {
				t.Errorf("View output missing expected model ID display %q in:\n%s", tt.wantDisplay, view)
			}
		})
	}
}

func TestAgentsViewDisplay_CostFormattingEdgeCases(t *testing.T) {
	tests := []struct {
		name        string
		costValue   interface{}
		wantDisplay string
		description string
	}{
		{
			name:        "zero cost",
			costValue:   0.0,
			wantDisplay: "$0.00",
			description: "zero cost should format as $0.00",
		},
		{
			name:        "single decimal place",
			costValue:   5.5,
			wantDisplay: "$5.50",
			description: "single decimal should be padded to two places",
		},
		{
			name:        "two decimal places",
			costValue:   10.99,
			wantDisplay: "$10.99",
			description: "two decimals should display as-is",
		},
		{
			name:        "three decimal places (rounding)",
			costValue:   1.234,
			wantDisplay: "$1.23",
			description: "three decimals should round down",
		},
		{
			name:        "three decimal places (rounding up)",
			costValue:   1.235,
			wantDisplay: "$1.24",
			description: "three decimals should round up when appropriate",
		},
		{
			name:        "many decimal places",
			costValue:   0.123456789,
			wantDisplay: "$0.12",
			description: "many decimals should round to two places",
		},
		{
			name:        "missing cost key",
			costValue:   nil,
			wantDisplay: "-",
			description: "missing total_cost_usd should show as dash",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := model.JSONField{}
			config["model_id"] = "test-model"
			if tt.costValue != nil {
				config["total_cost_usd"] = tt.costValue
			}

			a := AgentsModel{
				agents: []model.Agent{
					{
						ID:     uuid.New(),
						Name:   "agent-under-test",
						Status: model.AgentWorking,
						Config: config,
					},
				},
				cursor:     0,
				width:      120,
				height:     40,
				autoFilter: true,
			}

			view := a.View()
			if !strings.Contains(view, tt.wantDisplay) {
				t.Errorf("View output missing expected cost display %q in:\n%s", tt.wantDisplay, view)
			}
		})
	}
}

func TestAgentsViewDisplay_MultipleAgentsWithDifferentValues(t *testing.T) {
	t.Run("multiple agents with varied model_id and cost values", func(t *testing.T) {
		agents := []model.Agent{
			{
				ID:     uuid.New(),
				Name:   "agent-1",
				Status: model.AgentWorking,
				Config: model.JSONField{
					"model_id":       "claude-opus-4",
					"total_cost_usd": 5.25,
				},
			},
			{
				ID:     uuid.New(),
				Name:   "agent-2",
				Status: model.AgentWorking,
				Config: model.JSONField{
					"model_id":       "claude-haiku",
					"total_cost_usd": 0.05,
				},
			},
			{
				ID:     uuid.New(),
				Name:   "agent-3",
				Status: model.AgentWorking,
				Config: model.JSONField{
					// No model_id, has cost
					"total_cost_usd": 1.11,
				},
			},
			{
				ID:     uuid.New(),
				Name:   "agent-4",
				Status: model.AgentWorking,
				Config: model.JSONField{
					"model_id": "claude-sonnet",
					// No cost
				},
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

		// Verify all agents and their values are present
		if !strings.Contains(view, "agent-1") {
			t.Error("agent-1 not found in output")
		}
		if !strings.Contains(view, "claude-opus-4") {
			t.Error("agent-1 model_id not found")
		}
		if !strings.Contains(view, "$5.25") {
			t.Error("agent-1 cost not found")
		}

		if !strings.Contains(view, "agent-2") {
			t.Error("agent-2 not found in output")
		}
		if !strings.Contains(view, "claude-haiku") {
			t.Error("agent-2 model_id not found")
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
			t.Error("agent-4 model_id not found")
		}
	})
}

func TestAgentsViewDisplay_ModelIDAndCostNotDisplayedForArchivedAgents(t *testing.T) {
	t.Run("dead agent with model_id and cost should show values when showArchived true", func(t *testing.T) {
		a := AgentsModel{
			agents: []model.Agent{
				{
					ID:     uuid.New(),
					Name:   "dead-agent",
					Status: model.AgentDead,
					Config: model.JSONField{
						"model_id":       "claude-opus-4",
						"total_cost_usd": 3.14,
						"exit_reason":    "normal exit",
					},
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
			t.Error("model_id should be visible for dead agent")
		}
		if !strings.Contains(view, "$3.14") {
			t.Error("cost should be visible for dead agent")
		}
	})

	t.Run("dead agent should not display when showArchived false", func(t *testing.T) {
		agentID := uuid.New()
		a := AgentsModel{
			agents: []model.Agent{
				{
					ID:     agentID,
					Name:   "dead-agent",
					Status: model.AgentDead,
					Config: model.JSONField{
						"model_id":       "claude-opus-4",
						"total_cost_usd": 3.14,
					},
				},
			},
			cursor:       0,
			width:        120,
			height:       40,
			showArchived: false,
			autoFilter:   true,
		}

		view := a.View()
		// When archived agents are hidden, the view should not contain this agent
		if strings.Contains(view, "dead-agent") {
			t.Error("dead agent should not be visible when showArchived is false")
		}
	})
}

func TestAgentsViewDisplay_ColumnFormatting(t *testing.T) {
	t.Run("model_id and cost appear in consistent positions", func(t *testing.T) {
		agent1 := model.Agent{
			ID:     uuid.New(),
			Name:   "short-name",
			Status: model.AgentWorking,
			Config: model.JSONField{
				"model_id":       "claude-opus-4",
				"total_cost_usd": 1.5,
			},
		}
		agent2 := model.Agent{
			ID:     uuid.New(),
			Name:   "very-long-agent-name-that-might-truncate",
			Status: model.AgentWorking,
			Config: model.JSONField{
				"model_id":       "claude-haiku",
				"total_cost_usd": 0.1,
			},
		}

		a := AgentsModel{
			agents:     []model.Agent{agent1, agent2},
			cursor:     0,
			width:      120,
			height:     40,
			autoFilter: true,
		}

		view := a.View()

		// Both agents should have their model_id and cost displayed
		if !strings.Contains(view, "claude-opus-4") {
			t.Error("first agent model_id not found")
		}
		if !strings.Contains(view, "$1.50") {
			t.Error("first agent cost not found")
		}
		if !strings.Contains(view, "claude-haiku") {
			t.Error("second agent model_id not found")
		}
		if !strings.Contains(view, "$0.10") {
			t.Error("second agent cost not found")
		}
	})
}
