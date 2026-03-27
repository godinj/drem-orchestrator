// Package eventbus provides a SQLite-backed event bus for C-Suite agent
// communication. Events are published by agents, polled by consumers, and
// acknowledged once processed.
package eventbus

import (
	"time"

	"github.com/google/uuid"
)

// Event is a domain event persisted in the event bus. Every event has a
// UUID primary key, a type and source, optional task context fields, a
// JSON details payload, and a creation timestamp.
type Event struct {
	ID         uuid.UUID `gorm:"type:text;primaryKey"`
	EventType  string    `gorm:"type:text;not null"`
	Source     string    `gorm:"type:text;not null"`
	TaskID     *string   `gorm:"type:text"`
	FromStatus *string   `gorm:"type:text"`
	ToStatus   *string   `gorm:"type:text"`
	Details    string    `gorm:"type:text"`
	CreatedAt  time.Time
}

// EventDelivery records that a specific agent received an event. The composite
// primary key (event_id, agent) prevents duplicate delivery records.
type EventDelivery struct {
	EventID     string `gorm:"type:text;primaryKey"`
	Agent       string `gorm:"type:text;primaryKey"`
	DeliveredAt time.Time
	AckedAt     *time.Time
}
