package csuite

import (
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestWatcherSourceSnapshotUsesTokenAliasesWithoutDoubleCounting(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&turnMetricRow{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	started := time.Now().Add(-time.Minute)
	ended := time.Now()
	if err := db.Create(&turnMetricRow{
		Agent:        "mike",
		StartedAt:    started,
		EndedAt:      ended,
		TokensIn:     100,
		InputTokens:  100,
		TokensOut:    40,
		OutputTokens: 40,
	}).Error; err != nil {
		t.Fatalf("create metric: %v", err)
	}

	snap, err := NewWatcherSourceFromDB(db, nil).Snapshot()
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
