package tui

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
)

// ---------------------------------------------------------------------------
// AgentsModel.View() — ModelID and Tokens Display Tests
// ---------------------------------------------------------------------------

func TestAgentsViewDisplay_ModelIDAndTokensColumns(t *testing.T) {
	tests := []struct {
		name        string
		agents      []model.Agent
		width       int
		wantModelID string
		wantTokens  string
	}{
		{
			name: "agent with valid ModelID and tokens",
			agents: []model.Agent{
				{
					ID:        uuid.New(),
					Name:      "test-agent",
					Status:    model.AgentWorking,
					ModelID:   "claude-opus-4",
					TokensIn:  12400,
					TokensOut: 3100,
				},
			},
			width:       120,
			wantModelID: "claude-opus-4",
			wantTokens:  "12.4k↑ 3.1k↓",
		},
		{
			name: "agent with empty ModelID shows dash",
			agents: []model.Agent{
				{
					ID:        uuid.New(),
					Name:      "test-agent",
					Status:    model.AgentWorking,
					ModelID:   "",
					TokensIn:  420,
					TokensOut: 80,
				},
			},
			width:       120,
			wantModelID: "-",
			wantTokens:  "420↑ 80↓",
		},
		{
			name: "agent with zero tokens shows zero arrows",
			agents: []model.Agent{
				{
					ID:        uuid.New(),
					Name:      "test-agent",
					Status:    model.AgentWorking,
					ModelID:   "claude-haiku",
					TokensIn:  0,
					TokensOut: 0,
				},
			},
			width:       120,
			wantModelID: "claude-haiku",
			wantTokens:  "0↑ 0↓",
		},
		{
			name: "empty fields render with dashes and zeros",
			agents: []model.Agent{
				{
					ID:     uuid.New(),
					Name:   "test-agent",
					Status: model.AgentWorking,
				},
			},
			width:       120,
			wantModelID: "-",
			wantTokens:  "0↑ 0↓",
		},
		{
			name: "small token counts render as plain integers",
			agents: []model.Agent{
				{
					ID:        uuid.New(),
					Name:      "test-agent",
					Status:    model.AgentWorking,
					ModelID:   "claude-opus",
					TokensIn:  123,
					TokensOut: 45,
				},
			},
			width:       120,
			wantModelID: "claude-opus",
			wantTokens:  "123↑ 45↓",
		},
		{
			name: "large token counts keep k abbreviation",
			agents: []model.Agent{
				{
					ID:        uuid.New(),
					Name:      "test-agent",
					Status:    model.AgentWorking,
					ModelID:   "claude-opus-4",
					TokensIn:  125000,
					TokensOut: 4200,
				},
			},
			width:       120,
			wantModelID: "claude-opus-4",
			wantTokens:  "125.0k↑ 4.2k↓",
		},
		{
			name: "long ModelID displayed at narrow width",
			agents: []model.Agent{
				{
					ID:        uuid.New(),
					Name:      "test-agent",
					Status:    model.AgentWorking,
					ModelID:   "claude-opus-4-20250101-very-long-version-string",
					TokensIn:  1000,
					TokensOut: 500,
				},
			},
			width:       80,
			wantModelID: "claude-opus-4-20250101-very-long-version-string",
			wantTokens:  "1.0k↑ 500↓",
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
			if !strings.Contains(output, tt.wantTokens) {
				t.Errorf("View output missing tokens %q in:\n%s", tt.wantTokens, view)
			}
		})
	}
}

func TestAgentsViewDisplay_MultipleAgentsWithDifferentValues(t *testing.T) {
	t.Run("multiple agents with varied ModelID and token values", func(t *testing.T) {
		agents := []model.Agent{
			{
				ID:        uuid.New(),
				Name:      "agent-1",
				Status:    model.AgentWorking,
				ModelID:   "claude-opus-4",
				TokensIn:  52500,
				TokensOut: 8200,
			},
			{
				ID:        uuid.New(),
				Name:      "agent-2",
				Status:    model.AgentWorking,
				ModelID:   "claude-haiku",
				TokensIn:  500,
				TokensOut: 50,
			},
			{
				ID:        uuid.New(),
				Name:      "agent-3",
				Status:    model.AgentWorking,
				ModelID:   "",
				TokensIn:  11100,
				TokensOut: 2200,
			},
			{
				ID:        uuid.New(),
				Name:      "agent-4",
				Status:    model.AgentWorking,
				ModelID:   "claude-sonnet",
				TokensIn:  0,
				TokensOut: 0,
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
		if !strings.Contains(view, "52.5k↑ 8.2k↓") {
			t.Error("agent-1 tokens not found")
		}

		if !strings.Contains(view, "agent-2") {
			t.Error("agent-2 not found in output")
		}
		if !strings.Contains(view, "claude-haiku") {
			t.Error("agent-2 ModelID not found")
		}
		if !strings.Contains(view, "500↑ 50↓") {
			t.Error("agent-2 tokens not found")
		}

		if !strings.Contains(view, "agent-3") {
			t.Error("agent-3 not found in output")
		}
		if !strings.Contains(view, "11.1k↑ 2.2k↓") {
			t.Error("agent-3 tokens not found")
		}

		if !strings.Contains(view, "agent-4") {
			t.Error("agent-4 not found in output")
		}
		if !strings.Contains(view, "claude-sonnet") {
			t.Error("agent-4 ModelID not found")
		}
	})
}

func TestAgentsViewDisplay_ModelIDAndTokensForArchivedAgents(t *testing.T) {
	t.Run("dead agent values visible when showArchived true", func(t *testing.T) {
		a := AgentsModel{
			agents: []model.Agent{
				{
					ID:        uuid.New(),
					Name:      "dead-agent",
					Status:    model.AgentDead,
					ModelID:   "claude-opus-4",
					TokensIn:  31400,
					TokensOut: 6200,
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
		if !strings.Contains(view, "31.4k↑ 6.2k↓") {
			t.Error("tokens should be visible for dead agent")
		}
	})

	t.Run("dead agent hidden when showArchived false", func(t *testing.T) {
		a := AgentsModel{
			agents: []model.Agent{
				{
					ID:        uuid.New(),
					Name:      "dead-agent",
					Status:    model.AgentDead,
					ModelID:   "claude-opus-4",
					TokensIn:  31400,
					TokensOut: 6200,
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

// TestAgentsViewDisplay_ContainerAndImageRendering pins the new
// container-centric rendering introduced alongside the tmux/worktree
// decouple: the agent row must show the 12-char short container ID and
// the image tag resolved from the agent type. The tmux-pane-ID column
// that previously rendered here is gone for good.
func TestAgentsViewDisplay_ContainerAndImageRendering(t *testing.T) {
	agents := []model.Agent{
		{
			ID:             uuid.New(),
			Name:           "0123456789abcdef0123456789abcdef",
			AgentType:      model.AgentCoder,
			Status:         model.AgentWorking,
			WorktreeBranch: "feature/login",
			ModelID:        "claude-opus-4",
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

	if !strings.Contains(view, "container: 0123456789ab") {
		t.Errorf("expected container short-id line, got:\n%s", view)
	}
	if !strings.Contains(view, "image: localhost:5000/drem-worker:latest") {
		t.Errorf("expected image tag line, got:\n%s", view)
	}
	if !strings.Contains(view, "branch: feature/login") {
		t.Errorf("expected branch line, got:\n%s", view)
	}
	// Ensure the legacy tmux-session column is truly gone.
	if strings.Contains(view, "session:") {
		t.Errorf("legacy 'session:' column should be removed, got:\n%s", view)
	}
}

func TestAgentsViewDisplay_ColumnFormatting(t *testing.T) {
	t.Run("model ID and tokens appear for both agents", func(t *testing.T) {
		a := AgentsModel{
			agents: []model.Agent{
				{
					ID:        uuid.New(),
					Name:      "short-name",
					Status:    model.AgentWorking,
					ModelID:   "claude-opus-4",
					TokensIn:  1500,
					TokensOut: 300,
				},
				{
					ID:        uuid.New(),
					Name:      "very-long-agent-name-that-might-truncate",
					Status:    model.AgentWorking,
					ModelID:   "claude-haiku",
					TokensIn:  100,
					TokensOut: 20,
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
		if !strings.Contains(view, "1.5k↑ 300↓") {
			t.Error("first agent tokens not found")
		}
		if !strings.Contains(view, "claude-haiku") {
			t.Error("second agent ModelID not found")
		}
		if !strings.Contains(view, "100↑ 20↓") {
			t.Error("second agent tokens not found")
		}
	})
}
