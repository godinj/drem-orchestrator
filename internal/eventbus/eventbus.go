// Package eventbus provides a persistent SQLite-backed event bus for
// publishing and querying structured events across C-Suite watcher processes.
//
// Events are published with Bus.Publish and stored in a local SQLite database
// opened with WAL journal mode for concurrent read/write access.
// The bus auto-generates a UUID for each event; callers must not set Event.ID.
//
// Usage:
//
//	bus, err := eventbus.New("/path/to/csuite.db")
//	if err != nil { ... }
//	defer bus.Close()
//	err = bus.Publish(eventbus.Event{
//	    Type:      "task_status_changed",
//	    TaskID:    "42",
//	    Source:    "csuite-watcher",
//	    CreatedAt: time.Now(),
//	})
package eventbus

import (
	"fmt"
	"log"
	"os"
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
	Type       EventType `gorm:"column:event_type;not null"`
	Source     string    `gorm:"column:source"`
	TaskID     string    `gorm:"column:task_id"`
	FromStatus string    `gorm:"column:from_status"`
	ToStatus   string    `gorm:"column:to_status"`
	Details    string    `gorm:"column:details"`
	CreatedAt  time.Time `gorm:"column:created_at"`
}

// EventDelivery records that a specific agent received an event. The composite
// primary key (event_id, agent) prevents duplicate delivery records.
type EventDelivery struct {
	EventID     string     `gorm:"primaryKey;column:event_id"`
	Agent       string     `gorm:"primaryKey;column:agent"`
	DeliveredAt time.Time  `gorm:"column:delivered_at"`
	AckedAt     *time.Time `gorm:"column:acked_at"`
}

// Bus is a persistent event bus backed by a local SQLite database.
type Bus struct {
	db *gorm.DB
}

// New opens (or creates) the event bus database at dbPath with WAL journal
// mode and a 5-second busy timeout, then auto-migrates the schema.
func New(dbPath string) (*Bus, error) {
	dsn := fmt.Sprintf("%s?_journal_mode=WAL&_busy_timeout=5000", dbPath)

	gormLogger := logger.New(log.New(os.Stderr, "", 0), logger.Config{
		LogLevel: logger.Silent,
	})

	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: gormLogger})
	if err != nil {
		return nil, fmt.Errorf("open database %q: %w", dbPath, err)
	}

	if err := db.AutoMigrate(&Event{}, &EventDelivery{}); err != nil {
		return nil, fmt.Errorf("auto-migrate: %w", err)
	}

	return &Bus{db: db}, nil
}

// Publish stores the event in the database. It assigns a new UUID to e.ID
// before inserting; any caller-supplied ID is overwritten.
func (b *Bus) Publish(e Event) error {
	if e.ID == "" {
		e.ID = uuid.New().String()
	}
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

// Poll returns all events that have not yet been delivered to agent.
func (b *Bus) Poll(agent string) ([]Event, error) {
	var events []Event
	err := b.db.Raw(`
		SELECT * FROM events
		WHERE id NOT IN (
			SELECT event_id FROM event_deliveries WHERE agent = ?
		)
	`, agent).Scan(&events).Error
	return events, err
}

// Deliver records that agent has received the event identified by eventID,
// stamping delivered_at with the current time.
func (b *Bus) Deliver(eventID string, agent string) error {
	delivery := EventDelivery{
		EventID:     eventID,
		Agent:       agent,
		DeliveredAt: time.Now(),
	}
	return b.db.Create(&delivery).Error
}

// Ack records that agent has acknowledged the event identified by eventID,
// stamping acked_at with the current time.
func (b *Bus) Ack(eventID string, agent string) error {
	now := time.Now()
	return b.db.Model(&EventDelivery{}).
		Where("event_id = ? AND agent = ?", eventID, agent).
		Update("acked_at", now).Error
}

// Close releases the underlying database connection.
func (b *Bus) Close() error {
	sqlDB, err := b.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
