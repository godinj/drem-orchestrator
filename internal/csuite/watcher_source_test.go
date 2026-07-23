package csuite_test

import (
	"testing"
	"time"

	"github.com/godinj/drem-orchestrator/internal/csuite"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

func TestWatcherSourceSnapshotUsesTokenAliasesWithoutDoubleCounting(t *testing.T) {
	db := testutil.NewTestDB(t)
	if err := db.Exec(`CREATE TABLE turn_metrics (
        agent TEXT, started_at DATETIME, ended_at DATETIME, duration_ms INTEGER,
        tokens_in INTEGER, tokens_out INTEGER, input_tokens INTEGER,
        output_tokens INTEGER, events_processed INTEGER, exit_status INTEGER,
        error_details TEXT
    )`).Error; err != nil {
		t.Fatalf("create turn_metrics: %v", err)
	}

	started := time.Now().Add(-time.Minute)
	ended := time.Now()
	if err := db.Exec(`INSERT INTO turn_metrics
        (agent, started_at, ended_at, tokens_in, input_tokens, tokens_out, output_tokens)
        VALUES (?, ?, ?, ?, ?, ?, ?)`, "mike", started, ended, 100, 100, 40, 40).Error; err != nil {
		t.Fatalf("create metric: %v", err)
	}

	snap, err := csuite.NewWatcherSourceFromDB(db, nil).Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	metrics := snap.Metrics["mike"]
	if metrics.TokensInTotal != 100 {
		t.Fatalf("TokensInTotal = %d, want 100", metrics.TokensInTotal)
	}
	if metrics.TokensOutTotal != 40 {
		t.Fatalf("TokensOutTotal = %d, want 40", metrics.TokensOutTotal)
	}
	if metrics.LastTurn == nil {
		t.Fatal("LastTurn is nil")
	}
	if metrics.LastTurn.TokensIn != 100 || metrics.LastTurn.TokensOut != 40 {
		t.Fatalf("LastTurn tokens = %d/%d, want 100/40", metrics.LastTurn.TokensIn, metrics.LastTurn.TokensOut)
	}
}
