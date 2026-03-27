package watcher_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/testutil"
	"github.com/godinj/drem-orchestrator/internal/watcher"
)

// ---------------------------------------------------------------------------
// RecordTurn
// ---------------------------------------------------------------------------

// TestRecordTurn_InsertsRow verifies that RecordTurn persists a row with all
// fields correctly mapped. We query the DB directly to avoid coupling this
// test to QueryTurns.
func TestRecordTurn_InsertsRow(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &watcher.TurnMetric{})
	store := watcher.NewStore(db)

	started := time.Now().UTC().Add(-2 * time.Second).Truncate(time.Second)
	ended := started.Add(2 * time.Second)
	errMsg := "none"
	result := watcher.TurnResult{
		Agent:           "agent-alpha",
		StartedAt:       started,
		EndedAt:         ended,
		DurationMs:      2000,
		TokensIn:        100,
		TokensOut:       200,
		EventsProcessed: 5,
		MessagesSent:    3,
		ExitStatus:      0,
		ErrorDetails:    &errMsg,
	}

	if err := store.RecordTurn(result); err != nil {
		t.Fatalf("RecordTurn: %v", err)
	}

	var metrics []watcher.TurnMetric
	if err := db.Find(&metrics).Error; err != nil {
		t.Fatalf("direct DB query: %v", err)
	}
	if len(metrics) != 1 {
		t.Fatalf("expected 1 row, got %d", len(metrics))
	}

	got := metrics[0]
	if got.Agent != result.Agent {
		t.Errorf("Agent: got %q, want %q", got.Agent, result.Agent)
	}
	if got.DurationMs != result.DurationMs {
		t.Errorf("DurationMs: got %d, want %d", got.DurationMs, result.DurationMs)
	}
	if got.TokensIn != result.TokensIn {
		t.Errorf("TokensIn: got %d, want %d", got.TokensIn, result.TokensIn)
	}
	if got.TokensOut != result.TokensOut {
		t.Errorf("TokensOut: got %d, want %d", got.TokensOut, result.TokensOut)
	}
	if got.EventsProcessed != result.EventsProcessed {
		t.Errorf("EventsProcessed: got %d, want %d", got.EventsProcessed, result.EventsProcessed)
	}
	if got.MessagesSent != result.MessagesSent {
		t.Errorf("MessagesSent: got %d, want %d", got.MessagesSent, result.MessagesSent)
	}
	if got.ExitStatus != result.ExitStatus {
		t.Errorf("ExitStatus: got %d, want %d", got.ExitStatus, result.ExitStatus)
	}
	if got.ErrorDetails == nil {
		t.Error("ErrorDetails: got nil, want non-nil")
	} else if *got.ErrorDetails != errMsg {
		t.Errorf("ErrorDetails: got %q, want %q", *got.ErrorDetails, errMsg)
	}
	if got.StartedAt.UTC().Truncate(time.Second) != started {
		t.Errorf("StartedAt: got %v, want %v", got.StartedAt.UTC().Truncate(time.Second), started)
	}
	if got.EndedAt.UTC().Truncate(time.Second) != ended {
		t.Errorf("EndedAt: got %v, want %v", got.EndedAt.UTC().Truncate(time.Second), ended)
	}
}

// TestRecordTurn_GeneratesUUID verifies that RecordTurn assigns a non-nil UUID
// to the inserted row via the GORM BeforeCreate callback.
func TestRecordTurn_GeneratesUUID(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &watcher.TurnMetric{})
	store := watcher.NewStore(db)

	result := watcher.TurnResult{
		Agent:      "agent-beta",
		StartedAt:  time.Now().UTC().Add(-1 * time.Second),
		EndedAt:    time.Now().UTC(),
		DurationMs: 1000,
	}

	if err := store.RecordTurn(result); err != nil {
		t.Fatalf("RecordTurn: %v", err)
	}

	var metrics []watcher.TurnMetric
	if err := db.Find(&metrics).Error; err != nil {
		t.Fatalf("direct DB query: %v", err)
	}
	if len(metrics) != 1 {
		t.Fatalf("expected 1 row, got %d", len(metrics))
	}
	if metrics[0].ID == uuid.Nil {
		t.Error("expected non-nil UUID on inserted row")
	}
}

// ---------------------------------------------------------------------------
// QueryTurns
// ---------------------------------------------------------------------------

// TestQueryTurns_RetrievesCorrectFields inserts a turn via RecordTurn and
// verifies that QueryTurns returns the same field values.
func TestQueryTurns_RetrievesCorrectFields(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &watcher.TurnMetric{})
	store := watcher.NewStore(db)

	started := time.Now().UTC().Add(-3 * time.Second).Truncate(time.Second)
	ended := started.Add(3 * time.Second)
	result := watcher.TurnResult{
		Agent:           "agent-gamma",
		StartedAt:       started,
		EndedAt:         ended,
		DurationMs:      3000,
		TokensIn:        50,
		TokensOut:       150,
		EventsProcessed: 4,
		MessagesSent:    2,
		ExitStatus:      0,
	}

	if err := store.RecordTurn(result); err != nil {
		t.Fatalf("RecordTurn: %v", err)
	}

	turns, err := store.QueryTurns("agent-gamma", 10)
	if err != nil {
		t.Fatalf("QueryTurns: %v", err)
	}
	if len(turns) != 1 {
		t.Fatalf("expected 1 turn, got %d", len(turns))
	}

	got := turns[0]
	if got.Agent != result.Agent {
		t.Errorf("Agent: got %q, want %q", got.Agent, result.Agent)
	}
	if got.DurationMs != result.DurationMs {
		t.Errorf("DurationMs: got %d, want %d", got.DurationMs, result.DurationMs)
	}
	if got.TokensIn != result.TokensIn {
		t.Errorf("TokensIn: got %d, want %d", got.TokensIn, result.TokensIn)
	}
	if got.TokensOut != result.TokensOut {
		t.Errorf("TokensOut: got %d, want %d", got.TokensOut, result.TokensOut)
	}
	if got.EventsProcessed != result.EventsProcessed {
		t.Errorf("EventsProcessed: got %d, want %d", got.EventsProcessed, result.EventsProcessed)
	}
	if got.MessagesSent != result.MessagesSent {
		t.Errorf("MessagesSent: got %d, want %d", got.MessagesSent, result.MessagesSent)
	}
	if got.ExitStatus != result.ExitStatus {
		t.Errorf("ExitStatus: got %d, want %d", got.ExitStatus, result.ExitStatus)
	}
}

// TestQueryTurns_DescendingOrder inserts 3 turns with distinct StartedAt times
// and verifies QueryTurns returns them newest-first.
func TestQueryTurns_DescendingOrder(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &watcher.TurnMetric{})
	store := watcher.NewStore(db)

	base := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	rows := []watcher.TurnMetric{
		{ID: uuid.New(), Agent: "agent-order", StartedAt: base.Add(1 * time.Hour), EndedAt: base.Add(2 * time.Hour)},
		{ID: uuid.New(), Agent: "agent-order", StartedAt: base.Add(2 * time.Hour), EndedAt: base.Add(3 * time.Hour)},
		{ID: uuid.New(), Agent: "agent-order", StartedAt: base.Add(3 * time.Hour), EndedAt: base.Add(4 * time.Hour)},
	}
	for i := range rows {
		if err := db.Create(&rows[i]).Error; err != nil {
			t.Fatalf("setup row %d: %v", i, err)
		}
	}

	turns, err := store.QueryTurns("agent-order", 10)
	if err != nil {
		t.Fatalf("QueryTurns: %v", err)
	}
	if len(turns) != 3 {
		t.Fatalf("expected 3 turns, got %d", len(turns))
	}

	// Expect descending order: row[2] first, row[0] last.
	for i := 1; i < len(turns); i++ {
		if !turns[i-1].StartedAt.After(turns[i].StartedAt) {
			t.Errorf("turns[%d].StartedAt (%v) is not after turns[%d].StartedAt (%v)",
				i-1, turns[i-1].StartedAt, i, turns[i].StartedAt)
		}
	}
}

// TestQueryTurns_RespectsLimit inserts 5 turns and verifies that QueryTurns
// with limit=2 returns exactly 2 rows.
func TestQueryTurns_RespectsLimit(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &watcher.TurnMetric{})
	store := watcher.NewStore(db)

	for i := 0; i < 5; i++ {
		testutil.CreateTurnMetric(t, db, "agent-limit", 0)
	}

	turns, err := store.QueryTurns("agent-limit", 2)
	if err != nil {
		t.Fatalf("QueryTurns: %v", err)
	}
	if len(turns) != 2 {
		t.Errorf("expected 2 turns with limit=2, got %d", len(turns))
	}
}

// TestQueryTurns_FiltersByAgent inserts turns for two agents and verifies that
// QueryTurns for agent A does not return agent B's turns.
func TestQueryTurns_FiltersByAgent(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &watcher.TurnMetric{})
	store := watcher.NewStore(db)

	testutil.CreateTurnMetric(t, db, "agent-A", 0)
	testutil.CreateTurnMetric(t, db, "agent-A", 0)
	testutil.CreateTurnMetric(t, db, "agent-B", 1)

	turns, err := store.QueryTurns("agent-A", 10)
	if err != nil {
		t.Fatalf("QueryTurns: %v", err)
	}
	if len(turns) != 2 {
		t.Errorf("expected 2 turns for agent-A, got %d", len(turns))
	}
	for _, turn := range turns {
		if turn.Agent != "agent-A" {
			t.Errorf("unexpected agent in result: got %q, want %q", turn.Agent, "agent-A")
		}
	}
}

// TestQueryTurns_EmptyResult verifies that querying for a nonexistent agent
// returns an empty (non-nil) slice rather than nil.
func TestQueryTurns_EmptyResult(t *testing.T) {
	store := testutil.NewTestWatcherStore(t)

	turns, err := store.QueryTurns("nonexistent-agent", 10)
	if err != nil {
		t.Fatalf("QueryTurns: %v", err)
	}
	if turns == nil {
		t.Error("expected empty slice, got nil")
	}
	if len(turns) != 0 {
		t.Errorf("expected 0 turns, got %d", len(turns))
	}
}
