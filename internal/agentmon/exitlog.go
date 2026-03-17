package agentmon

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// ExitLogEntry represents a structured record of an agent session's exit.
type ExitLogEntry struct {
	AgentID       string    `json:"agent_id"`
	TaskID        string    `json:"task_id"`
	Timestamp     time.Time `json:"timestamp"`
	ExitReason    string    `json:"exit_reason"`
	LastTool      string    `json:"last_tool"`
	LastTarget    string    `json:"last_target"`
	FilesModified []string  `json:"files_modified"`
	CommitsMade   int       `json:"commits_made"`
	Summary       string    `json:"summary"`
}

// ParseExitLogEntry parses a single JSONL line into an ExitLogEntry.
func ParseExitLogEntry(line string) (ExitLogEntry, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return ExitLogEntry{}, fmt.Errorf("empty line")
	}
	var entry ExitLogEntry
	if err := json.Unmarshal([]byte(line), &entry); err != nil {
		return ExitLogEntry{}, fmt.Errorf("parse exit log entry: %w", err)
	}
	return entry, nil
}

// WriteExitLogEntry appends an ExitLogEntry as a JSONL line to the given file path.
func WriteExitLogEntry(path string, entry ExitLogEntry) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open exit log: %w", err)
	}
	defer f.Close()

	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal exit log entry: %w", err)
	}
	data = append(data, '\n')
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("write exit log entry: %w", err)
	}
	return nil
}

// ReadExitLog reads all ExitLogEntry records from a JSONL file.
// Returns an empty slice (not error) if the file does not exist.
// Malformed lines are silently skipped.
func ReadExitLog(path string) ([]ExitLogEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []ExitLogEntry{}, nil
		}
		return nil, fmt.Errorf("open exit log: %w", err)
	}
	defer f.Close()

	var entries []ExitLogEntry
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		entry, err := ParseExitLogEntry(scanner.Text())
		if err != nil {
			continue // skip malformed lines
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read exit log: %w", err)
	}
	if entries == nil {
		entries = []ExitLogEntry{}
	}
	return entries, nil
}
