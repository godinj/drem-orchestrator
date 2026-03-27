// Package eventbus provides a persistent SQLite-backed event bus for
// publishing and querying structured events across C-Suite watcher processes.
//
// Events are published with Bus.Publish and stored in a local SQLite database.
// The bus auto-generates a UUID for each event; callers must not set Event.ID.
//
// Usage:
//
//	bus, err := eventbus.New("/path/to/csuite.db")
//	if err != nil { ... }
//	defer bus.Close()
//	err = bus.Publish(eventbus.Event{
//	    Type:       "task_status_changed",
//	    TaskID:     "42",
//	    FromStatus: "planning",
//	    ToStatus:   "failed",
//	    Details:    "context exhaustion",
//	    CreatedAt:  time.Now(),
//	    Source:     "csuite-watcher",
//	})
package eventbus

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// EventType is the discriminator for event kinds.
type EventType = string

// Event is the canonical representation of a published event. The ID field is
// auto-assigned by Publish; callers must leave it empty.
type Event struct {
	ID         string    `gorm:"primaryKey"`
	Type       EventType `gorm:"column:type;not null"`
	TaskID     string    `gorm:"column:task_id"`
	FromStatus string    `gorm:"column:from_status"`
	ToStatus   string    `gorm:"column:to_status"`
	Details    string    `gorm:"column:details"`
	CreatedAt  time.Time `gorm:"column:created_at"`
	Source     string    `gorm:"column:source"`
}

// Bus is a persistent event bus backed by a local SQLite database.
type Bus struct {
	db *gorm.DB
}

// New opens (or creates) the event bus database at dbPath and runs
// auto-migration to ensure the events table exists.
func New(dbPath string) (*Bus, error) {
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, err
	}
	if err := db.AutoMigrate(&Event{}); err != nil {
		return nil, err
	}
	return &Bus{db: db}, nil
}

// Publish stores the event in the database. It assigns a new UUID to e.ID
// before inserting; any caller-supplied ID is overwritten.
func (b *Bus) Publish(e Event) error {
	e.ID = uuid.New().String()
	return b.db.Create(&e).Error
}

// Events returns all events stored in the bus, ordered by creation time.
func (b *Bus) Events() ([]Event, error) {
	var events []Event
	if err := b.db.Order("created_at asc").Find(&events).Error; err != nil {
		return nil, err
	}
	return events, nil
}

// Close releases the underlying database connection.
func (b *Bus) Close() error {
	sqlDB, err := b.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
