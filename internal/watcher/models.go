// Package watcher provides the C-Suite watcher event bus models, retention
// cleanup, and graceful shutdown management. The watcher binary manages
// turn-based agent lifecycles and records events, deliveries, and turn
// metrics in a SQLite database.
package watcher

import (
	"time"

	"github.com/google/uuid"
)

// Event represents an orchestrator event recorded on the event bus.
// All task state transitions and agent status changes are stored here
// regardless of routing. Routing determines which agents receive
// EventDelivery rows.
type Event struct {
	ID         uuid.UUID `gorm:"type:text;primaryKey"`
	EventType  string    `gorm:"not null;index"`
	Source     string    `gorm:"not null"`
	TaskID     string    `gorm:"index"`
	FromStatus string
	ToStatus   string
	Details    string    `gorm:"type:text"` // JSON blob
	CreatedAt  time.Time `gorm:"index"`
}

// EventDelivery tracks per-agent delivery and acknowledgment of an event.
// The watcher creates delivery rows when an event is published based on
// routing rules. Agents query unacked deliveries, process them, and set
// AckedAt.
type EventDelivery struct {
	EventID     uuid.UUID `gorm:"type:text;not null;index;uniqueIndex:idx_event_agent"`
	Agent       string    `gorm:"not null;index;uniqueIndex:idx_event_agent"`
	DeliveredAt time.Time `gorm:"not null"`
	AckedAt     *time.Time
	CreatedAt   time.Time
}

// TurnMetric is defined in store.go — not duplicated here.
// It records the outcome of a single agent turn with per-turn
// token counts and timing.
