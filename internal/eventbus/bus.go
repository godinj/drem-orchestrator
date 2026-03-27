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

// Bus owns a SQLite event-bus database and exposes publish/poll/deliver/ack
// semantics. The underlying database is opened with WAL mode and a busy
// timeout so concurrent readers and writers do not block each other.
type Bus struct {
	db *gorm.DB
}

// New opens (or creates) the SQLite event-bus database at dbPath with WAL
// mode and a 5-second busy timeout, then auto-migrates the schema.
// Returns an error when dbPath is invalid or the database cannot be opened.
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

// Publish persists e to the database. If e.ID is the zero UUID, a new UUID
// is generated before insertion.
func (b *Bus) Publish(e *Event) error {
	if e.ID == uuid.Nil {
		e.ID = uuid.New()
	}
	return b.db.Create(e).Error
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

// Deliver records that agent has received event eventID, stamping
// delivered_at with the current time.
func (b *Bus) Deliver(eventID uuid.UUID, agent string) error {
	delivery := EventDelivery{
		EventID:     eventID,
		Agent:       agent,
		DeliveredAt: time.Now(),
	}
	return b.db.Create(&delivery).Error
}

// Ack records that agent has acknowledged event eventID, stamping
// acked_at with the current time.
func (b *Bus) Ack(eventID uuid.UUID, agent string) error {
	now := time.Now()
	return b.db.Model(&EventDelivery{}).
		Where("event_id = ? AND agent = ?", eventID.String(), agent).
		Update("acked_at", now).Error
}
