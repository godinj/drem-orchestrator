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
	// ID is the UUID primary key, stored as text.
	ID uuid.UUID `gorm:"type:text;primaryKey"`
	// EventType describes the kind of event (e.g. "task.status_changed").
	EventType string `gorm:"type:text;not null"`
	// Source identifies the agent or component that emitted the event.
	Source string `gorm:"type:text;not null"`
	// TaskID is the optional task identifier associated with this event.
	TaskID *string `gorm:"type:text"`
	// FromStatus is the previous task status, if this is a status transition.
	FromStatus *string `gorm:"type:text"`
	// ToStatus is the next task status, if this is a status transition.
	ToStatus *string `gorm:"type:text"`
	// Details is an optional JSON payload with additional event context.
	Details string `gorm:"type:text"`
	// CreatedAt is set automatically by GORM on insert.
	CreatedAt time.Time
}

// EventDelivery records that a specific agent received an event. The composite
// primary key (event_id, agent) prevents duplicate delivery records for the
// same (event, consumer) pair.
type EventDelivery struct {
	// EventID references the events.id of the delivered event.
	EventID uuid.UUID `gorm:"type:text;primaryKey"`
	// Agent identifies the consuming agent.
	Agent string `gorm:"type:text;primaryKey"`
	// DeliveredAt is when the event was marked as delivered.
	DeliveredAt time.Time
	// AckedAt is when the agent acknowledged processing the event (nullable).
	AckedAt *time.Time
}
