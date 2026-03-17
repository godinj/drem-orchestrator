package agentmon

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseExitLogEntry(t *testing.T) {
	ts := time.Date(2026, 3, 15, 10, 30, 0, 0, time.UTC)

	line := `{"agent_id":"agent-abc","task_id":"task-123","timestamp":"2026-03-15T10:30:00Z","exit_reason":"success","last_tool":"Edit","last_target":"main.go","files_modified":["main.go","util.go"],"commits_made":2,"summary":"Implemented feature X"}`

	entry, err := ParseExitLogEntry(line)
	if err != nil {
		t.Fatalf("ParseExitLogEntry returned error: %v", err)
	}

	if entry.AgentID != "agent-abc" {
		t.Errorf("AgentID = %q, want %q", entry.AgentID, "agent-abc")
	}
	if entry.TaskID != "task-123" {
		t.Errorf("TaskID = %q, want %q", entry.TaskID, "task-123")
	}
	if !entry.Timestamp.Equal(ts) {
		t.Errorf("Timestamp = %v, want %v", entry.Timestamp, ts)
	}
	if entry.ExitReason != "success" {
		t.Errorf("ExitReason = %q, want %q", entry.ExitReason, "success")
	}
	if entry.LastTool != "Edit" {
		t.Errorf("LastTool = %q, want %q", entry.LastTool, "Edit")
	}
	if entry.LastTarget != "main.go" {
		t.Errorf("LastTarget = %q, want %q", entry.LastTarget, "main.go")
	}
	if len(entry.FilesModified) != 2 {
		t.Fatalf("FilesModified len = %d, want 2", len(entry.FilesModified))
	}
	if entry.FilesModified[0] != "main.go" || entry.FilesModified[1] != "util.go" {
		t.Errorf("FilesModified = %v, want [main.go util.go]", entry.FilesModified)
	}
	if entry.CommitsMade != 2 {
		t.Errorf("CommitsMade = %d, want 2", entry.CommitsMade)
	}
	if entry.Summary != "Implemented feature X" {
		t.Errorf("Summary = %q, want %q", entry.Summary, "Implemented feature X")
	}
}

func TestParseExitLogEntry_AllExitReasons(t *testing.T) {
	reasons := []string{"success", "error", "context_limit", "user_interrupt", "unknown"}
	for _, reason := range reasons {
		t.Run(reason, func(t *testing.T) {
			line := `{"agent_id":"a","task_id":"t","timestamp":"2026-03-15T10:00:00Z","exit_reason":"` + reason + `","last_tool":"","last_target":"","files_modified":[],"commits_made":0,"summary":""}`
			entry, err := ParseExitLogEntry(line)
			if err != nil {
				t.Fatalf("ParseExitLogEntry returned error: %v", err)
			}
			if entry.ExitReason != reason {
				t.Errorf("ExitReason = %q, want %q", entry.ExitReason, reason)
			}
		})
	}
}

func TestParseExitLogEntry_InvalidJSON(t *testing.T) {
	_, err := ParseExitLogEntry(`{not valid json}`)
	if err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}

func TestParseExitLogEntry_EmptyLine(t *testing.T) {
	_, err := ParseExitLogEntry("")
	if err == nil {
		t.Error("expected error for empty line, got nil")
	}
}

func TestWriteExitLogEntry(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "exit-log.jsonl")

	entry := ExitLogEntry{
		AgentID:       "agent-xyz",
		TaskID:        "task-456",
		Timestamp:     time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC),
		ExitReason:    "error",
		LastTool:      "Bash",
		LastTarget:    "go test ./...",
		FilesModified: []string{"foo.go"},
		CommitsMade:   1,
		Summary:       "Tests failed",
	}

	if err := WriteExitLogEntry(logPath, entry); err != nil {
		t.Fatalf("WriteExitLogEntry returned error: %v", err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("failed to read log file: %v", err)
	}

	// Verify the written line can be parsed back
	lines := string(data)
	// Should end with a newline
	if lines[len(lines)-1] != '\n' {
		t.Error("JSONL line should end with newline")
	}

	parsed, err := ParseExitLogEntry(lines[:len(lines)-1])
	if err != nil {
		t.Fatalf("failed to parse written entry: %v", err)
	}
	if parsed.AgentID != entry.AgentID {
		t.Errorf("round-trip AgentID = %q, want %q", parsed.AgentID, entry.AgentID)
	}
	if parsed.ExitReason != entry.ExitReason {
		t.Errorf("round-trip ExitReason = %q, want %q", parsed.ExitReason, entry.ExitReason)
	}
	if !parsed.Timestamp.Equal(entry.Timestamp) {
		t.Errorf("round-trip Timestamp = %v, want %v", parsed.Timestamp, entry.Timestamp)
	}
	if parsed.CommitsMade != entry.CommitsMade {
		t.Errorf("round-trip CommitsMade = %d, want %d", parsed.CommitsMade, entry.CommitsMade)
	}
}

func TestWriteExitLogEntry_Append(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "exit-log.jsonl")

	entry1 := ExitLogEntry{
		AgentID:    "agent-1",
		TaskID:     "task-1",
		Timestamp:  time.Date(2026, 3, 15, 10, 0, 0, 0, time.UTC),
		ExitReason: "success",
	}
	entry2 := ExitLogEntry{
		AgentID:    "agent-2",
		TaskID:     "task-2",
		Timestamp:  time.Date(2026, 3, 15, 11, 0, 0, 0, time.UTC),
		ExitReason: "error",
	}

	if err := WriteExitLogEntry(logPath, entry1); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := WriteExitLogEntry(logPath, entry2); err != nil {
		t.Fatalf("second write: %v", err)
	}

	entries, err := ReadExitLog(logPath)
	if err != nil {
		t.Fatalf("ReadExitLog: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	if entries[0].AgentID != "agent-1" {
		t.Errorf("entries[0].AgentID = %q, want %q", entries[0].AgentID, "agent-1")
	}
	if entries[1].AgentID != "agent-2" {
		t.Errorf("entries[1].AgentID = %q, want %q", entries[1].AgentID, "agent-2")
	}
}

func TestReadExitLog(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "exit-log.jsonl")

	content := `{"agent_id":"a1","task_id":"t1","timestamp":"2026-03-15T10:00:00Z","exit_reason":"success","last_tool":"Read","last_target":"a.go","files_modified":["a.go"],"commits_made":1,"summary":"Done"}
{"agent_id":"a2","task_id":"t2","timestamp":"2026-03-15T11:00:00Z","exit_reason":"error","last_tool":"Bash","last_target":"make","files_modified":[],"commits_made":0,"summary":"Build failed"}
{"agent_id":"a3","task_id":"t3","timestamp":"2026-03-15T12:00:00Z","exit_reason":"context_limit","last_tool":"Edit","last_target":"b.go","files_modified":["b.go","c.go"],"commits_made":2,"summary":"Partial work"}
`
	if err := os.WriteFile(logPath, []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	entries, err := ReadExitLog(logPath)
	if err != nil {
		t.Fatalf("ReadExitLog returned error: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3", len(entries))
	}

	// Verify ordering matches file order
	if entries[0].AgentID != "a1" {
		t.Errorf("entries[0].AgentID = %q, want %q", entries[0].AgentID, "a1")
	}
	if entries[1].AgentID != "a2" {
		t.Errorf("entries[1].AgentID = %q, want %q", entries[1].AgentID, "a2")
	}
	if entries[2].AgentID != "a3" {
		t.Errorf("entries[2].AgentID = %q, want %q", entries[2].AgentID, "a3")
	}

	// Verify full parsing of first entry
	if entries[0].ExitReason != "success" {
		t.Errorf("entries[0].ExitReason = %q, want %q", entries[0].ExitReason, "success")
	}
	if entries[0].CommitsMade != 1 {
		t.Errorf("entries[0].CommitsMade = %d, want 1", entries[0].CommitsMade)
	}

	// Verify third entry's files
	if len(entries[2].FilesModified) != 2 {
		t.Fatalf("entries[2].FilesModified len = %d, want 2", len(entries[2].FilesModified))
	}
}

func TestReadExitLog_MissingFile(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "nonexistent.jsonl")

	entries, err := ReadExitLog(logPath)
	if err != nil {
		t.Fatalf("expected no error for missing file, got: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected empty slice for missing file, got %d entries", len(entries))
	}
}

func TestReadExitLog_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "exit-log.jsonl")

	if err := os.WriteFile(logPath, []byte(""), 0o644); err != nil {
		t.Fatalf("failed to write empty file: %v", err)
	}

	entries, err := ReadExitLog(logPath)
	if err != nil {
		t.Fatalf("expected no error for empty file, got: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected empty slice for empty file, got %d entries", len(entries))
	}
}

func TestReadExitLog_MalformedLines(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "exit-log.jsonl")

	content := `{"agent_id":"a1","task_id":"t1","timestamp":"2026-03-15T10:00:00Z","exit_reason":"success","last_tool":"Read","last_target":"a.go","files_modified":[],"commits_made":0,"summary":"OK"}
{this is not valid json
{"agent_id":"a2","task_id":"t2","timestamp":"2026-03-15T11:00:00Z","exit_reason":"error","last_tool":"Bash","last_target":"cmd","files_modified":[],"commits_made":0,"summary":"Failed"}

just plain text
{"agent_id":"a3","task_id":"t3","timestamp":"2026-03-15T12:00:00Z","exit_reason":"unknown","last_tool":"","last_target":"","files_modified":[],"commits_made":0,"summary":""}
`
	if err := os.WriteFile(logPath, []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	entries, err := ReadExitLog(logPath)
	if err != nil {
		t.Fatalf("ReadExitLog returned error: %v", err)
	}

	// Should have parsed 3 valid entries, skipping the malformed lines
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3 (malformed lines should be skipped)", len(entries))
	}
	if entries[0].AgentID != "a1" {
		t.Errorf("entries[0].AgentID = %q, want %q", entries[0].AgentID, "a1")
	}
	if entries[1].AgentID != "a2" {
		t.Errorf("entries[1].AgentID = %q, want %q", entries[1].AgentID, "a2")
	}
	if entries[2].AgentID != "a3" {
		t.Errorf("entries[2].AgentID = %q, want %q", entries[2].AgentID, "a3")
	}
}
