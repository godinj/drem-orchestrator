package csuite

import (
	"time"

	"github.com/godinj/drem-orchestrator/internal/csuite"
)

// FilterMessages filters a slice of CsuiteInboxMessage based on archive status.
// If showArchived is true, it returns only archived messages.
// If showArchived is false, it returns only unarchived messages.
// The order of messages is preserved.
func FilterMessages(messages []csuite.CsuiteInboxMessage, showArchived bool) []csuite.CsuiteInboxMessage {
	var result []csuite.CsuiteInboxMessage
	for _, msg := range messages {
		if showArchived && msg.Archived {
			result = append(result, msg)
		} else if !showArchived && !msg.Archived {
			result = append(result, msg)
		}
	}
	return result
}

// ArchiveToggle tracks the state of the archive display toggle.
// It manages whether archived messages are shown or hidden.
type ArchiveToggle struct {
	showArchived bool
	lastToggled  time.Time
}

// NewArchiveToggle creates a new ArchiveToggle with archived messages hidden by default.
func NewArchiveToggle() *ArchiveToggle {
	return &ArchiveToggle{
		showArchived: false,
		lastToggled:  time.Now(),
	}
}

// ShowArchived returns the current state of the archive toggle.
// Returns true if archived messages should be shown, false if they should be hidden.
func (a *ArchiveToggle) ShowArchived() bool {
	return a.showArchived
}

// Toggle switches the current state of the archive toggle.
func (a *ArchiveToggle) Toggle() {
	a.showArchived = !a.showArchived
	a.lastToggled = time.Now()
}

// Set explicitly sets the archive toggle state.
func (a *ArchiveToggle) Set(state bool) {
	a.showArchived = state
	a.lastToggled = time.Now()
}

// String returns a string representation of the toggle state.
func (a *ArchiveToggle) String() string {
	if a.showArchived {
		return "archive: visible"
	}
	return "archive: hidden"
}

// LastToggled returns the timestamp of the last toggle operation.
func (a *ArchiveToggle) LastToggled() time.Time {
	return a.lastToggled
}
