package tui

import (
	"testing"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
)

// ---------------------------------------------------------------------------
// Helper Function Tests: extractModelID and extractCost
// ---------------------------------------------------------------------------
// These functions extract directly from Agent struct fields (ModelID string,
// TotalCostUSD float64), not from Config JSONField.
// ---------------------------------------------------------------------------

func TestExtractModelID(t *testing.T) {
	tests := []struct {
		name      string
		agent     *model.Agent
		wantValue string
	}{
		{
			name:      "nil agent returns dash",
			agent:     nil,
			wantValue: "-",
		},
		{
			name:      "empty ModelID returns dash",
			agent:     &model.Agent{ID: uuid.New(), ModelID: ""},
			wantValue: "-",
		},
		{
			name:      "valid ModelID returns the value",
			agent:     &model.Agent{ID: uuid.New(), ModelID: "claude-opus-4-6"},
			wantValue: "claude-opus-4-6",
		},
		{
			name:      "ModelID with dashes and numbers returned as-is",
			agent:     &model.Agent{ID: uuid.New(), ModelID: "claude-opus-4-20250101"},
			wantValue: "claude-opus-4-20250101",
		},
		{
			name:      "ModelID with leading and trailing whitespace preserved",
			agent:     &model.Agent{ID: uuid.New(), ModelID: "  claude-sonnet  "},
			wantValue: "  claude-sonnet  ",
		},
		{
			name:      "ModelID with only whitespace is not treated as empty",
			agent:     &model.Agent{ID: uuid.New(), ModelID: "   "},
			wantValue: "   ",
		},
		{
			name: "ModelID set alongside Config — struct field takes precedence",
			agent: &model.Agent{
				ID:      uuid.New(),
				ModelID: "claude-haiku-4-5",
				Config:  model.JSONField{"model_id": "wrong-value"},
			},
			wantValue: "claude-haiku-4-5",
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
		name      string
		agent     *model.Agent
		wantValue string
	}{
		{
			name:      "nil agent returns dash",
			agent:     nil,
			wantValue: "-",
		},
		{
			name:      "zero TotalCostUSD formats as $0.00",
			agent:     &model.Agent{ID: uuid.New(), TotalCostUSD: 0.0},
			wantValue: "$0.00",
		},
		{
			name:      "cost with two decimal places",
			agent:     &model.Agent{ID: uuid.New(), TotalCostUSD: 1.50},
			wantValue: "$1.50",
		},
		{
			name:      "cost with one decimal place pads to two",
			agent:     &model.Agent{ID: uuid.New(), TotalCostUSD: 5.5},
			wantValue: "$5.50",
		},
		{
			name:      "three decimals rounds down",
			agent:     &model.Agent{ID: uuid.New(), TotalCostUSD: 1.234},
			wantValue: "$1.23",
		},
		{
			name:      "three decimals rounds up",
			agent:     &model.Agent{ID: uuid.New(), TotalCostUSD: 1.235},
			wantValue: "$1.24",
		},
		{
			name:      "large cost value formats correctly",
			agent:     &model.Agent{ID: uuid.New(), TotalCostUSD: 123.45},
			wantValue: "$123.45",
		},
		{
			name:      "very small positive cost",
			agent:     &model.Agent{ID: uuid.New(), TotalCostUSD: 0.01},
			wantValue: "$0.01",
		},
		{
			name:      "many decimal places round to two",
			agent:     &model.Agent{ID: uuid.New(), TotalCostUSD: 0.123456789},
			wantValue: "$0.12",
		},
		{
			name: "TotalCostUSD used even when Config has different total_cost_usd",
			agent: &model.Agent{
				ID:           uuid.New(),
				TotalCostUSD: 9.99,
				Config:       model.JSONField{"total_cost_usd": 0.01},
			},
			wantValue: "$9.99",
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
