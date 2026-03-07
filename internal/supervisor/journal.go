package supervisor

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// JournalEntry records a single supervisor intervention for later review.
type JournalEntry struct {
	Timestamp  time.Time
	TaskID     string
	TaskTitle  string
	Type       string // failure_diagnosis, empty_work_diagnosis, build_failure, merge_conflict, on_demand_session
	Summary    string
	Details    map[string]string
	Outcome    string
}

var journalMu sync.Mutex

// AppendJournal appends a formatted entry to the supervisor journal file.
// The file is created if it does not exist.
func AppendJournal(path string, entry JournalEntry) error {
	journalMu.Lock()
	defer journalMu.Unlock()

	// Create the file with a header if it doesn't exist.
	if _, err := os.Stat(path); os.IsNotExist(err) {
		header := `# Supervisor Journal

This file records supervisor interventions in the orchestrator workflow.
Review these entries to identify patterns and iterate on orchestrator behavior.

---

`
		if err := os.WriteFile(path, []byte(header), 0o644); err != nil {
			return fmt.Errorf("create supervisor journal: %w", err)
		}
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open supervisor journal: %w", err)
	}
	defer f.Close()

	var b strings.Builder
	fmt.Fprintf(&b, "## %s — %s\n\n", entry.Timestamp.Format("2006-01-02 15:04:05"), entry.Type)
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
	b.WriteString("\n---\n\n")

	if _, err := f.WriteString(b.String()); err != nil {
		return fmt.Errorf("write supervisor journal entry: %w", err)
	}
	return nil
}
