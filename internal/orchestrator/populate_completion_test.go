package orchestrator

import (
	"testing"
	"time"

	"github.com/godinj/drem-orchestrator/internal/agent"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/google/uuid"
)

// TestPopulateAgentCompletionFields_TokensFromConfig verifies that
// populateAgentCompletionFields extracts tokens_in and tokens_out from the
// agent's Config JSON field.
func TestPopulateAgentCompletionFields_TokensFromConfig(t *testing.T) {
	ag := &model.Agent{
		ID: uuid.New(),
		Config: model.JSONField{
			"tokens_in":        float64(15000),
			"tokens_out":       float64(3500),
			"total_cost_usd":   float64(1.25),
			"context_used_pct": float64(72),
		},
	}

	comp := agent.Completion{
		AgentID:    ag.ID,
		ReturnCode: 0,
		ExitInfo: &agent.ExitInfo{
			ExitReason: "success",
		},
	}

	populateAgentCompletionFields(ag, comp)

	if ag.TokensIn != 15000 {
		t.Errorf("TokensIn = %d, want 15000", ag.TokensIn)
	}
	if ag.TokensOut != 3500 {
		t.Errorf("TokensOut = %d, want 3500", ag.TokensOut)
	}
	if ag.TotalCostUSD != 1.25 {
		t.Errorf("TotalCostUSD = %v, want 1.25", ag.TotalCostUSD)
	}
	if ag.FinalContextPct != 72 {
		t.Errorf("FinalContextPct = %d, want 72", ag.FinalContextPct)
	}
	if ag.ExitReason != model.ExitReasonSuccess {
		t.Errorf("ExitReason = %q, want %q", ag.ExitReason, model.ExitReasonSuccess)
	}
	if ag.CompletedAt == nil {
		t.Error("CompletedAt should be non-nil")
	}
}

// TestPopulateAgentCompletionFields_TokensZeroWhenMissing verifies that
// tokens remain zero when Config does not contain token fields.
func TestPopulateAgentCompletionFields_TokensZeroWhenMissing(t *testing.T) {
	ag := &model.Agent{
		ID: uuid.New(),
		Config: model.JSONField{
			"total_cost_usd":   float64(0.5),
			"context_used_pct": float64(40),
		},
	}

	comp := agent.Completion{
		AgentID:    ag.ID,
		ReturnCode: 0,
	}

	populateAgentCompletionFields(ag, comp)

	if ag.TokensIn != 0 {
		t.Errorf("TokensIn = %d, want 0", ag.TokensIn)
	}
	if ag.TokensOut != 0 {
		t.Errorf("TokensOut = %d, want 0", ag.TokensOut)
	}
}

// TestPopulateAgentCompletionFields_NilConfig verifies that a nil Config
// does not cause a panic and leaves token fields at zero.
func TestPopulateAgentCompletionFields_NilConfig(t *testing.T) {
	ag := &model.Agent{
		ID:     uuid.New(),
		Config: nil,
	}

	comp := agent.Completion{
		AgentID:    ag.ID,
		ReturnCode: 0,
	}

	populateAgentCompletionFields(ag, comp)

	if ag.TokensIn != 0 {
		t.Errorf("TokensIn = %d, want 0", ag.TokensIn)
	}
	if ag.TokensOut != 0 {
		t.Errorf("TokensOut = %d, want 0", ag.TokensOut)
	}
	if ag.CompletedAt == nil {
		t.Error("CompletedAt should be non-nil even with nil Config")
	}
}

// TestPopulateAgentCompletionFields_TokensAsInt verifies that integer-typed
// token values in Config (from direct in-process assignment) are handled.
func TestPopulateAgentCompletionFields_TokensAsInt(t *testing.T) {
	ag := &model.Agent{
		ID: uuid.New(),
		Config: model.JSONField{
			"tokens_in":  int(8000),
			"tokens_out": int(2000),
		},
	}

	comp := agent.Completion{
		AgentID:    ag.ID,
		ReturnCode: 0,
	}

	populateAgentCompletionFields(ag, comp)

	if ag.TokensIn != 8000 {
		t.Errorf("TokensIn = %d, want 8000", ag.TokensIn)
	}
	if ag.TokensOut != 2000 {
		t.Errorf("TokensOut = %d, want 2000", ag.TokensOut)
	}
}

// TestPopulateAgentCompletionFields_CompletedAtSet verifies that CompletedAt
// is set to approximately the current time.
func TestPopulateAgentCompletionFields_CompletedAtSet(t *testing.T) {
	ag := &model.Agent{
		ID:     uuid.New(),
		Config: model.JSONField{},
	}

	before := time.Now()
	populateAgentCompletionFields(ag, agent.Completion{AgentID: ag.ID})
	after := time.Now()

	if ag.CompletedAt == nil {
		t.Fatal("CompletedAt is nil")
	}
	if ag.CompletedAt.Before(before) || ag.CompletedAt.After(after) {
		t.Errorf("CompletedAt %v not between %v and %v", ag.CompletedAt, before, after)
	}
}
