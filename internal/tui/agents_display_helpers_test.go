package tui

import (
	"testing"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
)

// ---------------------------------------------------------------------------
// Helper Function Tests: extractModelID and extractTokens
// ---------------------------------------------------------------------------
// These functions extract directly from Agent struct fields (ModelID string,
// TokensIn / TokensOut int), not from Config JSONField.
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

func TestExtractTokens(t *testing.T) {
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
			name:      "zero tokens format as 0↑ 0↓",
			agent:     &model.Agent{ID: uuid.New(), TokensIn: 0, TokensOut: 0},
			wantValue: "0↑ 0↓",
		},
		{
			name:      "small counts render as plain integers",
			agent:     &model.Agent{ID: uuid.New(), TokensIn: 42, TokensOut: 7},
			wantValue: "42↑ 7↓",
		},
		{
			name:      "counts at 999 stay plain",
			agent:     &model.Agent{ID: uuid.New(), TokensIn: 999, TokensOut: 500},
			wantValue: "999↑ 500↓",
		},
		{
			name:      "counts >= 1000 get k suffix",
			agent:     &model.Agent{ID: uuid.New(), TokensIn: 1000, TokensOut: 1500},
			wantValue: "1.0k↑ 1.5k↓",
		},
		{
			name:      "typical agent counts abbreviate with one decimal",
			agent:     &model.Agent{ID: uuid.New(), TokensIn: 12400, TokensOut: 3100},
			wantValue: "12.4k↑ 3.1k↓",
		},
		{
			name:      "large counts keep k abbreviation",
			agent:     &model.Agent{ID: uuid.New(), TokensIn: 250000, TokensOut: 42000},
			wantValue: "250.0k↑ 42.0k↓",
		},
		{
			name:      "mixed small and large counts",
			agent:     &model.Agent{ID: uuid.New(), TokensIn: 5000, TokensOut: 250},
			wantValue: "5.0k↑ 250↓",
		},
		{
			name: "struct fields used even when Config has different values",
			agent: &model.Agent{
				ID:        uuid.New(),
				TokensIn:  1234,
				TokensOut: 567,
				Config:    model.JSONField{"tokens_in": 99, "tokens_out": 99},
			},
			wantValue: "1.2k↑ 567↓",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractTokens(tt.agent)
			if got != tt.wantValue {
				t.Errorf("extractTokens() = %q, want %q", got, tt.wantValue)
			}
		})
	}
}
