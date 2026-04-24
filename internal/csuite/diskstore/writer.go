package diskstore

// Inbox writer for the diskstore — lifted from cmd/drem/csuite_send_writer.go
// so the bridge HTTP server can stage messages with the exact same
// frontmatter + filename convention `drem csuite send` uses. Keeping the
// writer in-package (instead of importing it) avoids dragging the cmd/drem
// main package into the watcher build graph; a follow-up can dedupe.
//
// Atomicity: the write goes to a sibling tempfile in the same dir,
// fsyncs, then renames into place. A crash mid-write never leaves a
// half-formed file for the persona's poller (or the watcher's
// 5-minute rescan) to pick up.

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
	"unicode/utf8"
)

// MaxBodyBytes mirrors frontmatterCap in internal/deliver/classify.go.
// A body larger than this would trip the watcher's own cap downstream
// anyway; refusing here keeps the diagnostic clean and local.
const MaxBodyBytes = 64 * 1024

// WriterConfig carries the knobs the writer needs. Zero value is
// unusable — every field is required unless noted. Mirrors the
// cmd/drem/csuite_send_writer.go shape.
type WriterConfig struct {
	// CsuiteHomeRoot is the host-side root holding per-persona
	// directories, typically ~/.drem-csuite or /csuite (in-container).
	CsuiteHomeRoot string

	// From is the frontmatter `from:` value (e.g. "operator", "mike").
	From string

	// To is the destination persona/operator. Used for both filename
	// (`-to-<to>-`) and the frontmatter `to:` field.
	To string

	// Topic is the frontmatter `topic:` value.
	Topic string

	// Body is the message body (no frontmatter, no trailing newline
	// required — the writer handles both). Rejected if > MaxBodyBytes
	// or non-UTF-8.
	Body string

	// Now is the injectable clock. Used for both the filename timestamp
	// (UTC 20060102T150405Z) and the `sent_at:` frontmatter field
	// (RFC3339 UTC). Must return a UTC time.
	Now func() time.Time

	// CorrelationID is an 8-char hex string. The writer does not
	// generate it; the caller supplies it so a downstream waiter has
	// the same value without a second call path.
	CorrelationID string
}

// WriteInboxFile stages cfg into the To agent's inbox dir. On success
// returns the final on-disk path. The file's content is the YAML
// frontmatter block followed by cfg.Body exactly as supplied
// (trailing newline ensured).
//
// Filename convention: `<UTC20060102T150405Z>-<from>-to-<to>-<corrid>.md`.
// The `-<persona>-to-` substring matches the recipient-convention
// check in internal/csuite/persona/poller.go's
// filenameLooksLikePersonaToRecipient, so the persona's poller treats
// this as a well-formed incoming message.
func WriteInboxFile(cfg WriterConfig) (string, error) {
	if cfg.CsuiteHomeRoot == "" {
		return "", fmt.Errorf("writer: CsuiteHomeRoot is required")
	}
	if cfg.From == "" {
		return "", fmt.Errorf("writer: From is required")
	}
	if cfg.To == "" {
		return "", fmt.Errorf("writer: To is required")
	}
	if cfg.CorrelationID == "" {
		return "", fmt.Errorf("writer: CorrelationID is required")
	}
	if cfg.Now == nil {
		return "", fmt.Errorf("writer: Now clock is required")
	}

	if n := len(cfg.Body); n > MaxBodyBytes {
		return "", fmt.Errorf("body too large (got %d bytes, max %d)", n, MaxBodyBytes)
	}
	if !utf8.ValidString(cfg.Body) {
		return "", fmt.Errorf("body is not valid UTF-8")
	}

	now := cfg.Now().UTC()
	filename := fmt.Sprintf(
		"%s-%s-to-%s-%s.md",
		now.Format("20060102T150405Z"),
		cfg.From,
		cfg.To,
		cfg.CorrelationID,
	)

	inboxDir := filepath.Join(cfg.CsuiteHomeRoot, cfg.To, "inbox")
	if err := os.MkdirAll(inboxDir, 0o755); err != nil {
		return "", fmt.Errorf("writer: mkdir inbox %q: %w", inboxDir, err)
	}

	finalPath := filepath.Join(inboxDir, filename)
	content := renderFrontmatter(cfg, now) + ensureTrailingNewline(cfg.Body)

	// Atomic stage: tempfile in the same dir, fsync, rename.
	tmp, err := os.CreateTemp(inboxDir, ".csuite-send-*.md.tmp")
	if err != nil {
		return "", fmt.Errorf("writer: create temp: %w", err)
	}
	tmpPath := tmp.Name()
	cleanedUp := false
	defer func() {
		if !cleanedUp {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("writer: write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("writer: fsync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("writer: close temp: %w", err)
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		return "", fmt.Errorf("writer: rename %q -> %q: %w", tmpPath, finalPath, err)
	}
	cleanedUp = true
	return finalPath, nil
}

// renderFrontmatter produces the YAML frontmatter block matching
// plans/drem-csuite-send-cli.md §7. Hand-rolled so the output order
// and quoting are stable across Go YAML library revisions.
func renderFrontmatter(cfg WriterConfig, now time.Time) string {
	return "---\n" +
		"from: " + cfg.From + "\n" +
		"to: " + cfg.To + "\n" +
		"topic: " + yamlScalar(cfg.Topic) + "\n" +
		"sent_at: " + now.Format(time.RFC3339) + "\n" +
		"correlation_id: " + cfg.CorrelationID + "\n" +
		"---\n\n"
}

// yamlScalar quotes s when necessary so the topic line round-trips
// through a strict YAML parser. Matches cmd/drem/csuite_send_writer.go.
func yamlScalar(s string) string {
	needsQuote := s == ""
	for _, r := range s {
		switch r {
		case ':', '#', '\'', '"', '[', ']', '{', '}', ',', '&', '*', '?', '|', '>', '!', '%', '@', '`', '\n', '\r', '\t':
			needsQuote = true
		}
		if needsQuote {
			break
		}
	}
	if !needsQuote && (len(s) > 0 && (s[0] == '-' || s[0] == ' ')) {
		needsQuote = true
	}
	if !needsQuote {
		return s
	}
	esc := make([]byte, 0, len(s)+2)
	esc = append(esc, '"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '\\', '"':
			esc = append(esc, '\\', c)
		case '\n':
			esc = append(esc, '\\', 'n')
		case '\r':
			esc = append(esc, '\\', 'r')
		case '\t':
			esc = append(esc, '\\', 't')
		default:
			esc = append(esc, c)
		}
	}
	esc = append(esc, '"')
	return string(esc)
}

// ensureTrailingNewline guarantees the body ends with exactly one '\n'.
// A body of "" returns "\n".
func ensureTrailingNewline(body string) string {
	if body == "" {
		return "\n"
	}
	if body[len(body)-1] == '\n' {
		return body
	}
	return body + "\n"
}
