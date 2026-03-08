package supervisor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// JournalEntry records a single supervisor intervention for later review.
type JournalEntry struct {
	Timestamp time.Time
	AgentName string
	TaskID    string
	TaskTitle string
	Type      string // failure_diagnosis, empty_work_diagnosis, build_failure, merge_conflict, on_demand_session
	Summary   string
	Details   map[string]string
	Outcome   string
}

var journalMu sync.Mutex

// WriteJournalEntry writes a journal entry as an individual file in dir.
// The filename includes the agent name and a timestamp.
// The directory is created if it does not exist.
func WriteJournalEntry(dir string, entry JournalEntry) error {
	journalMu.Lock()
	defer journalMu.Unlock()

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create journal directory: %w", err)
	}

	agentName := entry.AgentName
	if agentName == "" {
		agentName = "unknown"
	}
	// Sanitize agent name for use in filenames.
	agentName = strings.ReplaceAll(agentName, "/", "-")
	agentName = strings.ReplaceAll(agentName, " ", "-")

	ts := entry.Timestamp.Format("20060102-150405")
	filename := fmt.Sprintf("%s-%s.md", agentName, ts)
	path := filepath.Join(dir, filename)

	var b strings.Builder
	fmt.Fprintf(&b, "# %s — %s\n\n", entry.Timestamp.Format("2006-01-02 15:04:05"), entry.Type)
	fmt.Fprintf(&b, "- **Agent**: %s\n", entry.AgentName)
	fmt.Fprintf(&b, "- **Task**: %s (`%s`)\n", entry.TaskTitle, entry.TaskID)
	if entry.Summary != "" {
		fmt.Fprintf(&b, "- **Summary**: %s\n", entry.Summary)
	}
	for k, v := range entry.Details {
		if v != "" {
			fmt.Fprintf(&b, "- **%s**: %s\n", k, v)
		}
	}
	if entry.Outcome != "" {
		fmt.Fprintf(&b, "- **Outcome**: %s\n", entry.Outcome)
	}

	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("write supervisor journal entry: %w", err)
	}
	return nil
}
