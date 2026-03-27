package testutil_test

// Tests for CreateAgent functional options (AgentOption pattern).
//
// These tests are written in TDD red phase: stub With* functions in testutil.go
// are no-ops, so all field-verification tests here will FAIL until the stubs
// are replaced with real implementations that modify the agent before DB insert.

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

// TestCreateAgent_NoOptions verifies backward compatibility: calling CreateAgent
// with no options still produces a valid, persisted agent.
func TestCreateAgent_NoOptions(t *testing.T) {
	db := testutil.NewTestDB(t)
	ag := testutil.CreateAgent(t, db, uuid.Nil, model.AgentCoder, model.AgentIdle)

	if ag.ID == uuid.Nil {
		t.Fatal("expected non-nil agent ID")
	}
	if ag.AgentType != model.AgentCoder {
		t.Errorf("AgentType: want %q, got %q", model.AgentCoder, ag.AgentType)
	}
	if ag.Status != model.AgentIdle {
		t.Errorf("Status: want %q, got %q", model.AgentIdle, ag.Status)
	}

	// Verify persisted to DB
	var got model.Agent
	if err := db.First(&got, "id = ?", ag.ID).Error; err != nil {
		t.Fatalf("retrieve agent from DB: %v", err)
	}
	if got.ID != ag.ID {
		t.Errorf("retrieved agent ID mismatch: want %v, got %v", ag.ID, got.ID)
	}
}

// TestCreateAgent_WithModelID verifies that WithModelID sets ModelID on the
// persisted agent.
func TestCreateAgent_WithModelID(t *testing.T) {
	db := testutil.NewTestDB(t)
	want := "claude-sonnet-4-6"
	ag := testutil.CreateAgent(t, db, uuid.Nil, model.AgentCoder, model.AgentIdle,
		testutil.WithModelID(want))

	var got model.Agent
	if err := db.First(&got, "id = ?", ag.ID).Error; err != nil {
		t.Fatalf("retrieve agent: %v", err)
	}
	if got.ModelID != want {
		t.Errorf("ModelID: want %q, got %q", want, got.ModelID)
	}
}

// TestCreateAgent_WithEffort verifies that WithEffort sets Effort on the
// persisted agent.
func TestCreateAgent_WithEffort(t *testing.T) {
	db := testutil.NewTestDB(t)
	want := "high"
	ag := testutil.CreateAgent(t, db, uuid.Nil, model.AgentCoder, model.AgentIdle,
		testutil.WithEffort(want))

	var got model.Agent
	if err := db.First(&got, "id = ?", ag.ID).Error; err != nil {
		t.Fatalf("retrieve agent: %v", err)
	}
	if got.Effort != want {
		t.Errorf("Effort: want %q, got %q", want, got.Effort)
	}
}

// TestCreateAgent_WithCompletedAt verifies that WithCompletedAt sets
// CompletedAt on the persisted agent.
func TestCreateAgent_WithCompletedAt(t *testing.T) {
	db := testutil.NewTestDB(t)
	ts := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	ag := testutil.CreateAgent(t, db, uuid.Nil, model.AgentCoder, model.AgentIdle,
		testutil.WithCompletedAt(&ts))

	var got model.Agent
	if err := db.First(&got, "id = ?", ag.ID).Error; err != nil {
		t.Fatalf("retrieve agent: %v", err)
	}
	if got.CompletedAt == nil {
		t.Fatal("CompletedAt: want non-nil, got nil")
	}
	if !got.CompletedAt.Equal(ts) {
		t.Errorf("CompletedAt: want %v, got %v", ts, *got.CompletedAt)
	}
}

// TestCreateAgent_WithCompletedAt_Nil verifies that passing a nil pointer to
// WithCompletedAt leaves CompletedAt as nil (no-op for nullable field).
func TestCreateAgent_WithCompletedAt_Nil(t *testing.T) {
	db := testutil.NewTestDB(t)
	ag := testutil.CreateAgent(t, db, uuid.Nil, model.AgentCoder, model.AgentIdle,
		testutil.WithCompletedAt(nil))

	var got model.Agent
	if err := db.First(&got, "id = ?", ag.ID).Error; err != nil {
		t.Fatalf("retrieve agent: %v", err)
	}
	if got.CompletedAt != nil {
		t.Errorf("CompletedAt: want nil, got %v", got.CompletedAt)
	}
}

// TestCreateAgent_WithExitReason verifies that WithExitReason sets ExitReason
// on the persisted agent.
func TestCreateAgent_WithExitReason(t *testing.T) {
	db := testutil.NewTestDB(t)
	want := "context_limit"
	ag := testutil.CreateAgent(t, db, uuid.Nil, model.AgentCoder, model.AgentIdle,
		testutil.WithExitReason(want))

	var got model.Agent
	if err := db.First(&got, "id = ?", ag.ID).Error; err != nil {
		t.Fatalf("retrieve agent: %v", err)
	}
	if got.ExitReason != want {
		t.Errorf("ExitReason: want %q, got %q", want, got.ExitReason)
	}
}

// TestCreateAgent_WithTotalCostUSD verifies that WithTotalCostUSD sets
// TotalCostUSD on the persisted agent.
func TestCreateAgent_WithTotalCostUSD(t *testing.T) {
	db := testutil.NewTestDB(t)
	want := 1.23
	ag := testutil.CreateAgent(t, db, uuid.Nil, model.AgentCoder, model.AgentIdle,
		testutil.WithTotalCostUSD(want))

	var got model.Agent
	if err := db.First(&got, "id = ?", ag.ID).Error; err != nil {
		t.Fatalf("retrieve agent: %v", err)
	}
	if got.TotalCostUSD != want {
		t.Errorf("TotalCostUSD: want %v, got %v", want, got.TotalCostUSD)
	}
}

// TestCreateAgent_WithFinalContextPct verifies that WithFinalContextPct sets
// FinalContextPct on the persisted agent.
func TestCreateAgent_WithFinalContextPct(t *testing.T) {
	db := testutil.NewTestDB(t)
	want := 87
	ag := testutil.CreateAgent(t, db, uuid.Nil, model.AgentCoder, model.AgentIdle,
		testutil.WithFinalContextPct(want))

	var got model.Agent
	if err := db.First(&got, "id = ?", ag.ID).Error; err != nil {
		t.Fatalf("retrieve agent: %v", err)
	}
	if got.FinalContextPct != want {
		t.Errorf("FinalContextPct: want %v, got %v", want, got.FinalContextPct)
	}
}

// TestCreateAgent_MultipleOptions verifies that multiple options can be
// composed and all take effect on the same agent.
func TestCreateAgent_MultipleOptions(t *testing.T) {
	db := testutil.NewTestDB(t)
	ts := time.Date(2026, 3, 15, 9, 0, 0, 0, time.UTC)
	ag := testutil.CreateAgent(t, db, uuid.Nil, model.AgentCoder, model.AgentIdle,
		testutil.WithModelID("claude-opus-4-6"),
		testutil.WithEffort("medium"),
		testutil.WithCompletedAt(&ts),
		testutil.WithExitReason("success"),
		testutil.WithTotalCostUSD(4.56),
		testutil.WithFinalContextPct(42),
	)

	var got model.Agent
	if err := db.First(&got, "id = ?", ag.ID).Error; err != nil {
		t.Fatalf("retrieve agent: %v", err)
	}
	if got.ModelID != "claude-opus-4-6" {
		t.Errorf("ModelID: want %q, got %q", "claude-opus-4-6", got.ModelID)
	}
	if got.Effort != "medium" {
		t.Errorf("Effort: want %q, got %q", "medium", got.Effort)
	}
	if got.CompletedAt == nil || !got.CompletedAt.Equal(ts) {
		t.Errorf("CompletedAt: want %v, got %v", ts, got.CompletedAt)
	}
	if got.ExitReason != "success" {
		t.Errorf("ExitReason: want %q, got %q", "success", got.ExitReason)
	}
	if got.TotalCostUSD != 4.56 {
		t.Errorf("TotalCostUSD: want %v, got %v", 4.56, got.TotalCostUSD)
	}
	if got.FinalContextPct != 42.0 {
		t.Errorf("FinalContextPct: want %v, got %v", 42.0, got.FinalContextPct)
	}
}

// TestCreateAgent_ModelID_DBIndex verifies that the agents table has an index
// on the model_id column, enabling efficient filtering by model. The index is
// declared via the gorm:"index" tag on Agent.ModelID.
//
// This test fails until Agent.ModelID gains a gorm:"index" tag and migrations
// are re-run, because SQLite PRAGMA index_list will not report an index for
// an unindexed column.
func TestCreateAgent_ModelID_DBIndex(t *testing.T) {
	db := testutil.NewTestDB(t)

	// Create agents with different model IDs to confirm filter works.
	testutil.CreateAgent(t, db, uuid.Nil, model.AgentCoder, model.AgentIdle,
		testutil.WithModelID("claude-sonnet-4-6"))
	testutil.CreateAgent(t, db, uuid.Nil, model.AgentPlanner, model.AgentIdle,
		testutil.WithModelID("claude-opus-4-6"))
	testutil.CreateAgent(t, db, uuid.Nil, model.AgentFixer, model.AgentIdle,
		testutil.WithModelID("claude-sonnet-4-6"))

	// Verify filtering by model_id returns the correct subset.
	var sonnetAgents []model.Agent
	if err := db.Where("model_id = ?", "claude-sonnet-4-6").Find(&sonnetAgents).Error; err != nil {
		t.Fatalf("query by model_id: %v", err)
	}
	if len(sonnetAgents) != 2 {
		t.Errorf("query by model_id=claude-sonnet-4-6: want 2 results, got %d", len(sonnetAgents))
	}

	// Verify the database schema has an index on model_id. SQLite exposes
	// indexes via PRAGMA index_list('<table>').
	type indexInfo struct {
		Seq     int
		Name    string
		Unique  int
		Origin  string
		Partial int
	}
	var indexes []indexInfo
	if err := db.Raw("PRAGMA index_list('agents')").Scan(&indexes).Error; err != nil {
		t.Fatalf("PRAGMA index_list: %v", err)
	}

	// Look for an index that covers the model_id column.
	foundModelIDIndex := false
	for _, idx := range indexes {
		type colInfo struct {
			SeqNo int
			CID   int
			Name  string
		}
		var cols []colInfo
		if err := db.Raw(fmt.Sprintf("PRAGMA index_info(%s)", idx.Name)).Scan(&cols).Error; err != nil {
			t.Fatalf("PRAGMA index_info(%s): %v", idx.Name, err)
		}
		for _, col := range cols {
			if col.Name == "model_id" {
				foundModelIDIndex = true
				break
			}
		}
		if foundModelIDIndex {
			break
		}
	}

	if !foundModelIDIndex {
		t.Error("agents table has no index on model_id column; add gorm:\"index\" tag to Agent.ModelID")
	}
}
