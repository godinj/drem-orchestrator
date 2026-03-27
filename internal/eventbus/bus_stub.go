package eventbus

import "github.com/google/uuid"

// Bus owns a SQLite event-bus database and exposes publish/poll/deliver/ack
// semantics. The underlying database is opened with WAL mode and a busy
// timeout so concurrent readers and writers do not block each other.
type Bus struct{}

// New opens (or creates) the SQLite event-bus database at dbPath with WAL
// mode and a 5-second busy timeout, then auto-migrates the schema.
// Returns an error when dbPath is invalid or the database cannot be opened.
func New(dbPath string) (*Bus, error) {
	return nil, nil
}

// Publish persists e to the database. If e.ID is the zero UUID, a new UUID
// is generated before insertion.
func (b *Bus) Publish(e *Event) error {
	return nil
}

// Poll returns all events that have not yet been delivered to agent.
func (b *Bus) Poll(agent string) ([]Event, error) {
	return nil, nil
}

// Deliver records that agent has received event eventID, stamping
// delivered_at with the current time.
func (b *Bus) Deliver(eventID uuid.UUID, agent string) error {
	return nil
}

// Ack records that agent has acknowledged event eventID, stamping
// acked_at with the current time.
func (b *Bus) Ack(eventID uuid.UUID, agent string) error {
	return nil
}
