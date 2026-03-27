package tui

import (
	"testing"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
)

// ---------------------------------------------------------------------------
// Helper Function Tests: extractModelID and extractCost
// ---------------------------------------------------------------------------

func TestExtractModelID(t *testing.T) {
	tests := []struct {
		name        string
		agent       *model.Agent
		wantValue   string
		description string
	}{
		{
			name:        "nil agent returns dash",
			agent:       nil,
			wantValue:   "-",
			description: "nil agent should return dash",
		},
		{
			name: "nil Config returns dash",
			agent: &model.Agent{
				ID:     uuid.New(),
				Config: nil,
			},
			wantValue:   "-",
			description: "nil Config should return dash",
		},
		{
			name: "empty Config returns dash",
			agent: &model.Agent{
				ID:     uuid.New(),
				Config: model.JSONField{},
			},
			wantValue:   "-",
			description: "empty Config should return dash",
		},
		{
			name: "Config without model_id key returns dash",
			agent: &model.Agent{
				ID:     uuid.New(),
				Config: model.JSONField{"other_field": "value"},
			},
			wantValue:   "-",
			description: "missing model_id key should return dash",
		},
		{
			name: "empty string model_id returns dash",
			agent: &model.Agent{
				ID:     uuid.New(),
				Config: model.JSONField{"model_id": ""},
			},
			wantValue:   "-",
			description: "empty string model_id should return dash",
		},
		{
			name: "valid model_id returns the value",
			agent: &model.Agent{
				ID:     uuid.New(),
				Config: model.JSONField{"model_id": "claude-opus-4"},
			},
			wantValue:   "claude-opus-4",
			description: "valid model_id should return as-is",
		},
		{
			name: "model_id with special characters",
			agent: &model.Agent{
				ID:     uuid.New(),
				Config: model.JSONField{"model_id": "claude-opus-4-20250101"},
			},
			wantValue:   "claude-opus-4-20250101",
			description: "model_id with dashes and numbers should return as-is",
		},
		{
			name: "wrong type for model_id returns dash",
			agent: &model.Agent{
				ID:     uuid.New(),
				Config: model.JSONField{"model_id": 123},
			},
			wantValue:   "-",
			description: "non-string model_id should return dash",
		},
		{
			name: "model_id with whitespace is preserved",
			agent: &model.Agent{
				ID:     uuid.New(),
				Config: model.JSONField{"model_id": "  claude-opus  "},
			},
			wantValue:   "  claude-opus  ",
			description: "model_id with whitespace should be preserved",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractModelID(tt.agent)
			if got != tt.wantValue {
				t.Errorf("extractModelID() = %q, want %q", got, tt.wantValue)
			}
		})
	}
}

func TestExtractCost(t *testing.T) {
	tests := []struct {
		name        string
		agent       *model.Agent
		wantValue   string
		description string
	}{
		{
			name:        "nil agent returns dash",
			agent:       nil,
			wantValue:   "-",
			description: "nil agent should return dash",
		},
		{
			name: "nil Config returns dash",
			agent: &model.Agent{
				ID:     uuid.New(),
				Config: nil,
			},
			wantValue:   "-",
			description: "nil Config should return dash",
		},
		{
			name: "empty Config returns dash",
			agent: &model.Agent{
				ID:     uuid.New(),
				Config: model.JSONField{},
			},
			wantValue:   "-",
			description: "empty Config should return dash",
		},
		{
			name: "Config without total_cost_usd key returns dash",
			agent: &model.Agent{
				ID:     uuid.New(),
				Config: model.JSONField{"model_id": "test"},
			},
			wantValue:   "-",
			description: "missing total_cost_usd key should return dash",
		},
		{
			name: "zero cost returns formatted zero",
			agent: &model.Agent{
				ID:     uuid.New(),
				Config: model.JSONField{"total_cost_usd": 0.0},
			},
			wantValue:   "$0.00",
			description: "zero cost should return $0.00",
		},
		{
			name: "float64 cost with two decimals",
			agent: &model.Agent{
				ID:     uuid.New(),
				Config: model.JSONField{"total_cost_usd": 1.50},
			},
			wantValue:   "$1.50",
			description: "cost with two decimals should format correctly",
		},
		{
			name: "float64 cost with one decimal",
			agent: &model.Agent{
				ID:     uuid.New(),
				Config: model.JSONField{"total_cost_usd": 5.5},
			},
			wantValue:   "$5.50",
			description: "cost with one decimal should pad to two",
		},
		{
			name: "float64 cost with three decimals (rounds down)",
			agent: &model.Agent{
				ID:     uuid.New(),
				Config: model.JSONField{"total_cost_usd": 1.234},
			},
			wantValue:   "$1.23",
			description: "three decimals should round correctly",
		},
		{
			name: "float64 cost with three decimals (rounds up)",
			agent: &model.Agent{
				ID:     uuid.New(),
				Config: model.JSONField{"total_cost_usd": 1.235},
			},
			wantValue:   "$1.24",
			description: "three decimals should round up when appropriate",
		},
		{
			name: "large cost value",
			agent: &model.Agent{
				ID:     uuid.New(),
				Config: model.JSONField{"total_cost_usd": 123.45},
			},
			wantValue:   "$123.45",
			description: "large cost should format correctly",
		},
		{
			name: "float32 cost",
			agent: &model.Agent{
				ID:     uuid.New(),
				Config: model.JSONField{"total_cost_usd": float32(2.5)},
			},
			wantValue:   "$2.50",
			description: "float32 should be handled",
		},
		{
			name: "int cost",
			agent: &model.Agent{
				ID:     uuid.New(),
				Config: model.JSONField{"total_cost_usd": int(5)},
			},
			wantValue:   "$5.00",
			description: "int should be handled",
		},
		{
			name: "int64 cost",
			agent: &model.Agent{
				ID:     uuid.New(),
				Config: model.JSONField{"total_cost_usd": int64(10)},
			},
			wantValue:   "$10.00",
			description: "int64 should be handled",
		},
		{
			name: "string cost (wrong type) returns dash",
			agent: &model.Agent{
				ID:     uuid.New(),
				Config: model.JSONField{"total_cost_usd": "1.50"},
			},
			wantValue:   "-",
			description: "string type should return dash",
		},
		{
			name: "very small positive cost",
			agent: &model.Agent{
				ID:     uuid.New(),
				Config: model.JSONField{"total_cost_usd": 0.01},
			},
			wantValue:   "$0.01",
			description: "very small cost should format correctly",
		},
		{
			name: "cost with many decimals",
			agent: &model.Agent{
				ID:     uuid.New(),
				Config: model.JSONField{"total_cost_usd": 0.123456789},
			},
			wantValue:   "$0.12",
			description: "many decimals should round to two",
		},
		{
			name: "nil value for cost returns dash",
			agent: &model.Agent{
				ID:     uuid.New(),
				Config: model.JSONField{"total_cost_usd": nil},
			},
			wantValue:   "-",
			description: "nil value should return dash",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractCost(tt.agent)
			if got != tt.wantValue {
				t.Errorf("extractCost() = %q, want %q", got, tt.wantValue)
			}
		})
	}
}
