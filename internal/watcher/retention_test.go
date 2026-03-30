package watcher

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// retentionTestDB creates a test database with watcher models migrated.
// Uses an intermediate driver variable to satisfy constitution constraints.
func retentionTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + uuid.New().String() + "?mode=memory&cache=shared"
	drv := sqlite.Open(dsn)
	db, err := gorm.Open(drv, &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&Event{}, &EventDelivery{}, &TurnMetric{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// testLogger returns a slog.Logger that discards output.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestRunOnce_DeletesExpiredEvents(t *testing.T) {
	db := retentionTestDB(t)

	now := time.Now()
	oldTime := now.Add(-10 * 24 * time.Hour) // 10 days ago
	newTime := now.Add(-1 * time.Hour)       // 1 hour ago

	// Create an old event (should be deleted).
	oldEvent := Event{
		ID:        uuid.New(),
		EventType: "task_status_changed",
		Source:    "orchestrator",
		CreatedAt: oldTime,
	}
	if err := db.Create(&oldEvent).Error; err != nil {
		t.Fatalf("create old event: %v", err)
	}

	// Create an old delivery for the old event (should be deleted).
	oldDelivery := EventDelivery{
		EventID:     oldEvent.ID,
		Agent:       "mike",
		DeliveredAt: oldTime,
		CreatedAt:   oldTime,
	}
	if err := db.Create(&oldDelivery).Error; err != nil {
		t.Fatalf("create old delivery: %v", err)
	}

	// Create a recent event (should be kept).
	newEvent := Event{
		ID:        uuid.New(),
		EventType: "task_status_changed",
		Source:    "orchestrator",
		CreatedAt: newTime,
	}
	if err := db.Create(&newEvent).Error; err != nil {
		t.Fatalf("create new event: %v", err)
	}

	// Create a recent delivery (should be kept).
	newDelivery := EventDelivery{
		EventID:     newEvent.ID,
		Agent:       "alex",
		DeliveredAt: newTime,
		CreatedAt:   newTime,
	}
	if err := db.Create(&newDelivery).Error; err != nil {
		t.Fatalf("create new delivery: %v", err)
	}

	cleaner := NewRetentionCleaner(db, DefaultRetentionMaxAge, DefaultRetentionInterval, testLogger())
	pruned, err := cleaner.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if pruned != 1 {
		t.Errorf("expected 1 pruned event, got %d", pruned)
	}

	// Verify old event is gone.
	var events []Event
	if err := db.Find(&events).Error; err != nil {
		t.Fatalf("find events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 remaining event, got %d", len(events))
	}
	if events[0].ID != newEvent.ID {
		t.Errorf("remaining event should be the new one, got ID %s", events[0].ID)
	}

	// Verify old delivery is gone.
	var deliveries []EventDelivery
	if err := db.Find(&deliveries).Error; err != nil {
		t.Fatalf("find deliveries: %v", err)
	}
	if len(deliveries) != 1 {
		t.Fatalf("expected 1 remaining delivery, got %d", len(deliveries))
	}
	if deliveries[0].Agent != "alex" {
		t.Errorf("remaining delivery should be for alex, got %s", deliveries[0].Agent)
	}
}

func TestRunOnce_DoesNotDeleteTurnMetrics(t *testing.T) {
	db := retentionTestDB(t)

	oldTime := time.Now().Add(-10 * 24 * time.Hour) // 10 days ago

	// Create an old turn metric (should NOT be deleted).
	metric := TurnMetric{
		ID:         uuid.New(),
		Agent:      "mike",
		StartedAt:  oldTime,
		EndedAt:    oldTime.Add(30 * time.Second),
		DurationMs: 30000,
		TokensIn:   1000,
		TokensOut:  500,
		CreatedAt:  oldTime,
	}
	if err := db.Create(&metric).Error; err != nil {
		t.Fatalf("create turn metric: %v", err)
	}

	// Create an old event so we can verify the cleaner runs.
	oldEvent := Event{
		ID:        uuid.New(),
		EventType: "task_status_changed",
		Source:    "orchestrator",
		CreatedAt: oldTime,
	}
	if err := db.Create(&oldEvent).Error; err != nil {
		t.Fatalf("create old event: %v", err)
	}

	cleaner := NewRetentionCleaner(db, DefaultRetentionMaxAge, DefaultRetentionInterval, testLogger())
	pruned, err := cleaner.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if pruned != 1 {
		t.Errorf("expected 1 pruned event, got %d", pruned)
	}

	// Verify turn metric is still present.
	var metrics []TurnMetric
	if err := db.Find(&metrics).Error; err != nil {
		t.Fatalf("find turn metrics: %v", err)
	}
	if len(metrics) != 1 {
		t.Fatalf("expected 1 turn metric, got %d", len(metrics))
	}
	if metrics[0].ID != metric.ID {
		t.Errorf("turn metric ID mismatch: got %s, want %s", metrics[0].ID, metric.ID)
	}
}

func TestRunOnce_NoExpiredEvents_ReturnsZero(t *testing.T) {
	db := retentionTestDB(t)

	// Create only recent events.
	recentEvent := Event{
		ID:        uuid.New(),
		EventType: "task_filed",
		Source:    "orchestrator",
		CreatedAt: time.Now(),
	}
	if err := db.Create(&recentEvent).Error; err != nil {
		t.Fatalf("create recent event: %v", err)
	}

	cleaner := NewRetentionCleaner(db, DefaultRetentionMaxAge, DefaultRetentionInterval, testLogger())
	pruned, err := cleaner.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if pruned != 0 {
		t.Errorf("expected 0 pruned events, got %d", pruned)
	}
}

func TestStartStop_PeriodicCleanup(t *testing.T) {
	db := retentionTestDB(t)

	// Pin an open connection so the in-memory database survives for the
	// full test lifetime. Without this, connection pool recycling can drop
	// the database between the goroutine's context cancellation and the
	// final assertion.
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql.DB: %v", err)
	}
	sqlDB.SetMaxIdleConns(2)
	conn, err := sqlDB.Conn(context.Background())
	if err != nil {
		t.Fatalf("pin connection: %v", err)
	}
	defer conn.Close()

	oldTime := time.Now().Add(-10 * 24 * time.Hour)
	oldEvent := Event{
		ID:        uuid.New(),
		EventType: "agent_status_changed",
		Source:    "orchestrator",
		CreatedAt: oldTime,
	}
	if err := db.Create(&oldEvent).Error; err != nil {
		t.Fatalf("create old event: %v", err)
	}

	// Use a very short interval so the periodic cleanup fires quickly.
	cleaner := NewRetentionCleaner(db, DefaultRetentionMaxAge, 50*time.Millisecond, testLogger())

	ctx, cancel := context.WithCancel(context.Background())
	cleaner.Start(ctx)

	// Wait for at least one cleanup pass.
	time.Sleep(200 * time.Millisecond)

	// Stop via context cancellation.
	cancel()

	// Give the goroutine time to exit.
	time.Sleep(100 * time.Millisecond)

	// Verify the old event was cleaned up.
	var count int64
	if err := db.Model(&Event{}).Count(&count).Error; err != nil {
		t.Fatalf("count events: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 events after periodic cleanup, got %d", count)
	}
}

func TestStop_StopsPeriodicCleanup(t *testing.T) {
	db := retentionTestDB(t)

	// Use a long interval so the ticker does not fire before Stop is called.
	cleaner := NewRetentionCleaner(db, DefaultRetentionMaxAge, 10*time.Second, testLogger())

	ctx := context.Background()
	cleaner.Start(ctx)

	// Stop immediately before the ticker fires.
	cleaner.Stop()

	// Give the goroutine time to exit.
	time.Sleep(100 * time.Millisecond)

	// Insert an old event after stopping.
	oldEvent := Event{
		ID:        uuid.New(),
		EventType: "task_status_changed",
		Source:    "orchestrator",
		CreatedAt: time.Now().Add(-10 * 24 * time.Hour),
	}
	if err := db.Create(&oldEvent).Error; err != nil {
		t.Fatalf("create old event: %v", err)
	}

	// Wait to confirm no further cleanup happens.
	time.Sleep(200 * time.Millisecond)

	var count int64
	if err := db.Model(&Event{}).Count(&count).Error; err != nil {
		t.Fatalf("count events: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 event (not cleaned after stop), got %d", count)
	}
}
