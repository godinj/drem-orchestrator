package watcher

import (
	"log"
	"path/filepath"
)

// TurnPrecheck determines whether an agent has actionable work before
// a subprocess is launched. Implementations should be fast (filesystem
// stat + optional DB query) since they run on every trigger.
//
// HasWork returns true if the agent should run a turn (e.g., unarchived
// inbox messages or unacked event bus deliveries exist). Return false to
// skip the turn with a log message.
//
// Returning true on error is the safe default — it avoids false negatives
// that could delay message processing.
type TurnPrecheck interface {
	HasWork(agent string) bool
}

// HasInboxMessages reports whether the agent's inbox directory at
// <baseDir>/<agent>/inbox/ contains at least one unarchived .md file.
// Files in the archive/ subdirectory are not matched because filepath.Glob
// only matches files directly in the inbox directory.
//
// On glob error, returns true to err on the side of running the turn.
func HasInboxMessages(baseDir, agent string) bool {
	pattern := filepath.Join(baseDir, agent, "inbox", "*.md")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		log.Printf("precheck: glob inbox for %s: %v", agent, err)
		return true
	}
	return len(matches) > 0
}
