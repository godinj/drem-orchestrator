package cli

// kyle_inbox_test.go covers the `drem cli kyle inbox` subcommand.
// The CLI is filesystem-rooted (no DB); each test populates a
// tempdir with representative .md files and exercises one flag
// combination end-to-end.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeMsg is a tiny test helper that drops a .md file with the
// supplied body into dir.
func writeMsg(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

const sampleFrontmatter = `---
from: seth
to: kyle
timestamp: 2026-04-22T13:00:00Z
subject: "test subject"
priority: high
type: report
tldr: "test tldr"
---

Body content here.
`

// TestRunKyleInbox_ListEmpty exercises the empty-inbox case: output
// should be the "kyle inbox is empty." friendly message.
func TestRunKyleInbox_ListEmpty(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	if err := RunKyleInbox(dir, []string{"--list"}, &buf, false); err != nil {
		t.Fatalf("RunKyleInbox: %v", err)
	}
	if !strings.Contains(buf.String(), "empty") {
		t.Errorf("empty output = %q, want 'empty' somewhere", buf.String())
	}
}

// TestRunKyleInbox_ListTable checks the table-mode output includes
// the frontmatter fields.
func TestRunKyleInbox_ListTable(t *testing.T) {
	dir := t.TempDir()
	writeMsg(t, dir, "20260422T130000Z-seth-reply.md", sampleFrontmatter)

	var buf bytes.Buffer
	if err := RunKyleInbox(dir, []string{"--list"}, &buf, false); err != nil {
		t.Fatalf("RunKyleInbox: %v", err)
	}
	out := buf.String()
	for _, n := range []string{"seth", "test subject", "high", "report"} {
		if !strings.Contains(out, n) {
			t.Errorf("output missing %q in %q", n, out)
		}
	}
}

// TestRunKyleInbox_ListJSON confirms --json emits a parseable JSON array.
func TestRunKyleInbox_ListJSON(t *testing.T) {
	dir := t.TempDir()
	writeMsg(t, dir, "20260422T130000Z-a.md", sampleFrontmatter)
	writeMsg(t, dir, "20260422T140000Z-b.md", sampleFrontmatter)

	var buf bytes.Buffer
	if err := RunKyleInbox(dir, []string{"--list"}, &buf, true); err != nil {
		t.Fatalf("RunKyleInbox: %v", err)
	}
	var msgs []KyleInboxMessage
	if err := json.Unmarshal(buf.Bytes(), &msgs); err != nil {
		t.Fatalf("unmarshal: %v; raw=%q", err, buf.String())
	}
	if len(msgs) != 2 {
		t.Fatalf("len(msgs) = %d, want 2", len(msgs))
	}
	if msgs[0].From != "seth" {
		t.Errorf("msgs[0].From = %q, want seth", msgs[0].From)
	}
	if msgs[0].Index != 1 || msgs[1].Index != 2 {
		t.Errorf("indices = %d,%d, want 1,2", msgs[0].Index, msgs[1].Index)
	}
}

// TestRunKyleInbox_Read prints the body of a specific message.
func TestRunKyleInbox_Read(t *testing.T) {
	dir := t.TempDir()
	writeMsg(t, dir, "m.md", sampleFrontmatter)

	var buf bytes.Buffer
	if err := RunKyleInbox(dir, []string{"--read", "1"}, &buf, false); err != nil {
		t.Fatalf("RunKyleInbox: %v", err)
	}
	if !strings.Contains(buf.String(), "Body content here.") {
		t.Errorf("read output missing body: %q", buf.String())
	}
}

// TestRunKyleInbox_ReadOutOfRange covers the index-out-of-range
// error path.
func TestRunKyleInbox_ReadOutOfRange(t *testing.T) {
	dir := t.TempDir()
	writeMsg(t, dir, "m.md", sampleFrontmatter)

	var buf bytes.Buffer
	err := RunKyleInbox(dir, []string{"--read", "99"}, &buf, false)
	if err == nil {
		t.Error("expected error for out-of-range index")
	}
	if !strings.Contains(err.Error(), "out of range") {
		t.Errorf("err = %v, want 'out of range'", err)
	}
}

// TestRunKyleInbox_Count emits a single integer (unread count).
func TestRunKyleInbox_Count(t *testing.T) {
	dir := t.TempDir()
	writeMsg(t, dir, "a.md", sampleFrontmatter)
	writeMsg(t, dir, "b.md", sampleFrontmatter)
	writeMsg(t, dir, "c.md", sampleFrontmatter)

	var buf bytes.Buffer
	if err := RunKyleInbox(dir, []string{"--count"}, &buf, false); err != nil {
		t.Fatalf("RunKyleInbox: %v", err)
	}
	if strings.TrimSpace(buf.String()) != "3" {
		t.Errorf("count output = %q, want '3'", buf.String())
	}
}

// TestRunKyleInbox_CountJSON emits {"count": N} when --json is set.
func TestRunKyleInbox_CountJSON(t *testing.T) {
	dir := t.TempDir()
	writeMsg(t, dir, "a.md", sampleFrontmatter)

	var buf bytes.Buffer
	if err := RunKyleInbox(dir, []string{"--count"}, &buf, true); err != nil {
		t.Fatalf("RunKyleInbox: %v", err)
	}
	var got map[string]int
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["count"] != 1 {
		t.Errorf("count = %d, want 1", got["count"])
	}
}

// TestRunKyleInbox_Archive moves a message into .archive/ and it no
// longer appears in --list on the next run.
func TestRunKyleInbox_Archive(t *testing.T) {
	dir := t.TempDir()
	writeMsg(t, dir, "a.md", sampleFrontmatter)
	writeMsg(t, dir, "b.md", sampleFrontmatter)

	var buf bytes.Buffer
	if err := RunKyleInbox(dir, []string{"--archive", "1"}, &buf, false); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if !strings.Contains(buf.String(), "archived:") {
		t.Errorf("archive stdout missing 'archived:' token: %q", buf.String())
	}
	// The archived file should now live under .archive/
	if _, err := os.Stat(filepath.Join(dir, ".archive", "a.md")); err != nil {
		t.Errorf("archived file not found: %v", err)
	}
	// Inbox should now contain only b.md.
	if _, err := os.Stat(filepath.Join(dir, "a.md")); !os.IsNotExist(err) {
		t.Errorf("a.md still in inbox: %v", err)
	}
	// A follow-up --list should show 1 message.
	buf.Reset()
	if err := RunKyleInbox(dir, []string{"--list"}, &buf, true); err != nil {
		t.Fatalf("list after archive: %v", err)
	}
	var msgs []KyleInboxMessage
	if err := json.Unmarshal(buf.Bytes(), &msgs); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(msgs) != 1 {
		t.Errorf("after archive, len(msgs) = %d, want 1", len(msgs))
	}
	if msgs[0].Filename != "b.md" {
		t.Errorf("remaining msg = %q, want b.md", msgs[0].Filename)
	}
}

// TestRunKyleInbox_DefaultIsList exercises the no-flag default.
func TestRunKyleInbox_DefaultIsList(t *testing.T) {
	dir := t.TempDir()
	writeMsg(t, dir, "m.md", sampleFrontmatter)

	var buf bytes.Buffer
	if err := RunKyleInbox(dir, []string{}, &buf, false); err != nil {
		t.Fatalf("RunKyleInbox: %v", err)
	}
	if !strings.Contains(buf.String(), "seth") {
		t.Errorf("no-flag default did not list messages: %q", buf.String())
	}
}

// TestRunKyleInbox_UnknownFlag returns an error with a clear message.
func TestRunKyleInbox_UnknownFlag(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	err := RunKyleInbox(dir, []string{"--nonsense"}, &buf, false)
	if err == nil {
		t.Error("expected error for unknown flag")
	}
	if !strings.Contains(err.Error(), "unknown flag") {
		t.Errorf("err = %v, want 'unknown flag'", err)
	}
}

// TestRunKyleInbox_NoFrontmatter tolerates a message without
// frontmatter (legacy hand-deposited files): the listing still
// includes the filename.
func TestRunKyleInbox_NoFrontmatter(t *testing.T) {
	dir := t.TempDir()
	writeMsg(t, dir, "legacy.md", "just plain text, no frontmatter\n")

	var buf bytes.Buffer
	if err := RunKyleInbox(dir, []string{"--list"}, &buf, true); err != nil {
		t.Fatalf("RunKyleInbox: %v", err)
	}
	var msgs []KyleInboxMessage
	if err := json.Unmarshal(buf.Bytes(), &msgs); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("len = %d, want 1", len(msgs))
	}
	if msgs[0].Filename != "legacy.md" {
		t.Errorf("filename = %q, want legacy.md", msgs[0].Filename)
	}
	if msgs[0].From != "" {
		t.Errorf("From should be empty for no-frontmatter file, got %q", msgs[0].From)
	}
}
