package csuite

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

var (
	// ErrUnknownPersona indicates an inbox queue request targeted an agent
	// without a managed persona inbox tree.
	ErrUnknownPersona = errors.New("unknown persona")

	// ErrInboxItemNotFound indicates the requested queue item is not present
	// in the live inbox. Archived and ignored items are intentionally excluded.
	ErrInboxItemNotFound = errors.New("inbox item not found")
)

// InboxQueueItem is a live on-disk persona inbox item exposed for operator
// restart review.
type InboxQueueItem struct {
	ID        uuid.UUID `json:"id"`
	Filename  string    `json:"filename"`
	FromAgent string    `json:"from_agent"`
	ToAgent   string    `json:"to_agent"`
	Subject   string    `json:"subject"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
