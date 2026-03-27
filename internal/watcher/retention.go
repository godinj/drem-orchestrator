package watcher

import (
	"context"
	"log/slog"
	"time"

	"gorm.io/gorm"
)

// DefaultRetentionMaxAge is the default maximum age for events before they
// are eligible for retention cleanup.
const DefaultRetentionMaxAge = 7 * 24 * time.Hour

// DefaultRetentionInterval is the default interval between retention
// cleanup passes.
const DefaultRetentionInterval = 1 * time.Hour

// retentionBatchSize controls how many rows are deleted per batch to limit
// transaction size and lock contention in WAL mode.
const retentionBatchSize = 500

// RetentionCleaner periodically deletes old events and their deliveries
// from the watcher database. It does NOT delete turn_metrics, which have
// a separate retention policy. The cleaner is safe for concurrent operation
// under SQLite WAL mode.
type RetentionCleaner struct {
	db       *gorm.DB
	maxAge   time.Duration
	interval time.Duration
	logger   *slog.Logger
	stopCh   chan struct{}
}

// NewRetentionCleaner creates a RetentionCleaner that deletes events and
// event deliveries older than maxAge. The interval parameter controls how
// often the periodic cleanup runs.
func NewRetentionCleaner(db *gorm.DB, maxAge, interval time.Duration, logger *slog.Logger) *RetentionCleaner {
	return &RetentionCleaner{
		db:       db,
		maxAge:   maxAge,
		interval: interval,
		logger:   logger,
		stopCh:   make(chan struct{}),
	}
}

// Start begins periodic retention cleanup in a goroutine. It runs until
// ctx is cancelled or Stop is called.
func (r *RetentionCleaner) Start(ctx context.Context) {
	go r.run(ctx)
}

// run is the internal loop that executes periodic cleanup passes.
func (r *RetentionCleaner) run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-r.stopCh:
			return
		case <-ticker.C:
			pruned, err := r.RunOnce(ctx)
			if err != nil {
				r.logger.Error("retention cleanup failed", "error", err)
				continue
			}
			if pruned > 0 {
				r.logger.Info("retention cleanup completed", "pruned_events", pruned)
			}
		}
	}
}

// RunOnce executes a single retention cleanup pass. It deletes event
// deliveries whose associated event is older than maxAge, then deletes
// the expired events themselves. Returns the total number of pruned
// events. Turn metrics are never touched by this cleaner.
func (r *RetentionCleaner) RunOnce(ctx context.Context) (int64, error) {
	cutoff := time.Now().Add(-r.maxAge)

	// Delete deliveries for expired events first (referential integrity).
	deliveryResult := r.db.WithContext(ctx).
		Where("event_id IN (?)",
			r.db.Model(&Event{}).Select("id").Where("created_at < ?", cutoff),
		).
		Delete(&EventDelivery{})
	if deliveryResult.Error != nil {
		return 0, deliveryResult.Error
	}

	// Delete expired events in batches.
	var totalPruned int64
	for {
		result := r.db.WithContext(ctx).
			Where("created_at < ?", cutoff).
			Limit(retentionBatchSize).
			Delete(&Event{})
		if result.Error != nil {
			return totalPruned, result.Error
		}
		totalPruned += result.RowsAffected
		if result.RowsAffected < retentionBatchSize {
			break
		}
	}

	return totalPruned, nil
}

// Stop signals the periodic cleanup goroutine to exit.
func (r *RetentionCleaner) Stop() {
	select {
	case r.stopCh <- struct{}{}:
	default:
	}
}
