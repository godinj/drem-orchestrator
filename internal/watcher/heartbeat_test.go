package watcher_test

import (
	"testing"
	"time"

	"github.com/godinj/drem-orchestrator/internal/testutil"
	"github.com/godinj/drem-orchestrator/internal/watcher"
)

// TestHeartbeatWriter_WritesImmediately verifies that Start writes a heartbeat
// immediately (before the first tick interval elapses).
func TestHeartbeatWriter_WritesImmediately(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &watcher.Heartbeat{})
	hw := watcher.NewHeartbeatWriter(db, time.Minute) // long interval
	hw.Start()

	// Give the goroutine a moment to write the initial heartbeat.
	time.Sleep(50 * time.Millisecond)
	hw.Stop()

	var count int64
	if err := db.Model(&watcher.Heartbeat{}).Count(&count).Error; err != nil {
		t.Fatalf("count heartbeats: %v", err)
	}
	if count == 0 {
		t.Fatal("expected at least 1 heartbeat written immediately, got 0")
	}
}

// TestHeartbeatWriter_WritesMultiple verifies that the heartbeat writer
// produces multiple heartbeat rows over several tick intervals.
func TestHeartbeatWriter_WritesMultiple(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &watcher.Heartbeat{})
	hw := watcher.NewHeartbeatWriter(db, 15*time.Millisecond)
	hw.Start()

	// Wait for several ticks plus the initial write.
	time.Sleep(100 * time.Millisecond)
	hw.Stop()

	var count int64
	if err := db.Model(&watcher.Heartbeat{}).Count(&count).Error; err != nil {
		t.Fatalf("count heartbeats: %v", err)
	}
	// Initial + at least 2 ticks = at least 3.
	if count < 3 {
		t.Fatalf("expected at least 3 heartbeats, got %d", count)
	}
}

// TestHeartbeatWriter_StopsCleanly verifies that Stop halts the writer and
// no further heartbeats are written after Stop returns.
func TestHeartbeatWriter_StopsCleanly(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &watcher.Heartbeat{})
	hw := watcher.NewHeartbeatWriter(db, 10*time.Millisecond)
	hw.Start()

	// Let it write a few heartbeats.
	time.Sleep(50 * time.Millisecond)
	hw.Stop()

	var countAtStop int64
	if err := db.Model(&watcher.Heartbeat{}).Count(&countAtStop).Error; err != nil {
		t.Fatalf("count heartbeats at stop: %v", err)
	}

	// Wait and confirm no new heartbeats arrive.
	time.Sleep(50 * time.Millisecond)

	var countAfter int64
	if err := db.Model(&watcher.Heartbeat{}).Count(&countAfter).Error; err != nil {
		t.Fatalf("count heartbeats after wait: %v", err)
	}
	if countAfter != countAtStop {
		t.Fatalf("heartbeat written after Stop: had %d, now %d", countAtStop, countAfter)
	}
}

// TestHeartbeatWriter_DefaultInterval verifies that NewHeartbeatWriter with
// interval=0 defaults to 30 seconds — no heartbeat should be written by tick
// within 50ms (only the immediate initial one).
func TestHeartbeatWriter_DefaultInterval(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &watcher.Heartbeat{})
	hw := watcher.NewHeartbeatWriter(db, 0) // 0 → default 30s
	hw.Start()

	// Wait enough for the initial write but not for a 30s tick.
	time.Sleep(50 * time.Millisecond)
	hw.Stop()

	var count int64
	if err := db.Model(&watcher.Heartbeat{}).Count(&count).Error; err != nil {
		t.Fatalf("count heartbeats: %v", err)
	}
	// Exactly 1 from the immediate write; no tick at 30s interval.
	if count != 1 {
		t.Fatalf("expected exactly 1 heartbeat (immediate only, 30s tick too slow), got %d", count)
	}
}

// TestHeartbeatWriter_TimestampPopulated verifies that the Timestamp field on
// each heartbeat row is non-zero and recent.
func TestHeartbeatWriter_TimestampPopulated(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &watcher.Heartbeat{})
	before := time.Now().Add(-time.Second)

	hw := watcher.NewHeartbeatWriter(db, time.Minute)
	hw.Start()
	time.Sleep(50 * time.Millisecond)
	hw.Stop()

	var hbs []watcher.Heartbeat
	if err := db.Find(&hbs).Error; err != nil {
		t.Fatalf("query heartbeats: %v", err)
	}
	if len(hbs) == 0 {
		t.Fatal("expected at least 1 heartbeat")
	}
	for _, hb := range hbs {
		if hb.Timestamp.IsZero() {
			t.Error("heartbeat Timestamp is zero")
		}
		if hb.Timestamp.Before(before) {
			t.Errorf("heartbeat Timestamp %v is before test start %v", hb.Timestamp, before)
		}
	}
}
