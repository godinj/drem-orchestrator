package watcher

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const defaultHeartbeatInterval = 30 * time.Second

// Heartbeat is the GORM model for the heartbeats table. Each row records
// a single heartbeat written by the watcher main loop to prove it is alive.
type Heartbeat struct {
	ID        uuid.UUID `gorm:"type:text;primaryKey"`
	Timestamp time.Time `gorm:"not null"`
	CreatedAt time.Time
}

// HeartbeatWriter periodically inserts Heartbeat rows into the database.
// It proves the watcher process is alive and can be queried by external
// monitoring to detect stale/dead watchers.
//
// Use NewHeartbeatWriter to create, Start to begin writing, and Stop to halt.
type HeartbeatWriter struct {
	db       *gorm.DB
	interval time.Duration
	stop     chan struct{}
	done     chan struct{}
}

// NewHeartbeatWriter creates a HeartbeatWriter that writes heartbeats to db
// at the given interval. If interval is 0, the default of 30 seconds is used.
// The caller must call Start to begin writing heartbeats.
func NewHeartbeatWriter(db *gorm.DB, interval time.Duration) *HeartbeatWriter {
	if interval == 0 {
		interval = defaultHeartbeatInterval
	}
	return &HeartbeatWriter{
		db:       db,
		interval: interval,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
}

// Start begins writing heartbeats in a background goroutine. The first
// heartbeat is written immediately. Start must not be called more than once.
func (w *HeartbeatWriter) Start() {
	go w.run()
}

// Stop halts the heartbeat writer and waits for the background goroutine to exit.
func (w *HeartbeatWriter) Stop() {
	close(w.stop)
	<-w.done
}

// run is the background goroutine. It writes a heartbeat immediately, then
// on each tick until Stop is called.
func (w *HeartbeatWriter) run() {
	defer close(w.done)

	// Write an initial heartbeat immediately on startup.
	w.write()

	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			w.write()
		case <-w.stop:
			return
		}
	}
}

// write inserts a single heartbeat row. Errors are silently ignored —
// a missed heartbeat is not fatal; the next tick will retry.
func (w *HeartbeatWriter) write() {
	hb := Heartbeat{
		ID:        uuid.New(),
		Timestamp: time.Now(),
	}
	_ = w.db.Create(&hb).Error
}
