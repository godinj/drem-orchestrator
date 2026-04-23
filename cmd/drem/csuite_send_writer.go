package main

// drem csuite send — inbox writer.
//
// The writer is isolated from flag parsing + wait logic so tests can
// exercise it with a fixed clock and a tempdir. It is responsible for
// a single thing: stage an operator→persona inbox message atomically,
// with the exact frontmatter + filename convention spelled out in
// plans/drem-csuite-send-cli.md §7.
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

// maxBodyBytes mirrors frontmatterCap in internal/deliver/classify.go.
// A body larger than this would trip the watcher's own cap downstream
// anyway; refusing here keeps the diagnostic clean and local.
const maxBodyBytes = 64 * 1024

// writerConfig carries the knobs the writer needs. Zero value is
// unusable — every field is required unless noted. The struct is
// populated by the command-line parser in csuite_send.go; the writer
// itself never reads flags or env vars.
type writerConfig struct {
	// CsuiteHomeRoot is the host-side root holding per-persona
	// directories, typically ~/.drem-csuite. Tests inject a tempdir.
	CsuiteHomeRoot string

	// Persona is the destination (mike | alex | seth | kyle). Validated
	// upstream against persona.AllowedPersonas.
	Persona string

	// Topic is the frontmatter `topic:` value.
	Topic string

	// Body is the message body (no frontmatter, no trailing newline
	// required — the writer handles both). Rejected if > 64 KiB or
	// non-UTF-8.
	Body string

	// Now is the injectable clock. Used for both the filename timestamp
	// (UTC 20060102T150405Z) and the `sent_at:` frontmatter field
	// (RFC3339 UTC). Must return a UTC time.
	Now func() time.Time

	// CorrelationID is an 8-char hex string (or whatever the caller
	// supplied via --correlation-id). The writer does not generate it;
	// the CLI does, so the waiter has the same value without a second
	// call path.
	CorrelationID string
}

// writeInboxFile stages cfg into the persona's inbox dir. On success
// returns the final on-disk path. The file's content is the YAML
// frontmatter block followed by cfg.Body exactly as supplied
// (trailing newline ensured).
//
// Filename convention: `<UTC20060102T150405Z>-operator-to-<persona>-<corrid>.md`.
// The `-operator-to-` substring matches the recipient-convention
// check in internal/csuite/persona/poller.go's
// filenameLooksLikePersonaToRecipient, so the persona's poller treats
// this as a well-formed incoming message.
func writeInboxFile(cfg writerConfig) (string, error) {
	if cfg.CsuiteHomeRoot == "" {
		return "", fmt.Errorf("writer: CsuiteHomeRoot is required")
	}
	if cfg.Persona == "" {
		return "", fmt.Errorf("writer: Persona is required")
	}
	if cfg.CorrelationID == "" {
		return "", fmt.Errorf("writer: CorrelationID is required")
	}
	if cfg.Now == nil {
		return "", fmt.Errorf("writer: Now clock is required")
	}

	if n := len(cfg.Body); n > maxBodyBytes {
		return "", fmt.Errorf("body too large (got %d bytes, max %d)", n, maxBodyBytes)
	}
	if !utf8.ValidString(cfg.Body) {
		return "", fmt.Errorf("body is not valid UTF-8")
	}

	now := cfg.Now().UTC()
	filename := fmt.Sprintf(
		"%s-operator-to-%s-%s.md",
		now.Format("20060102T150405Z"),
		cfg.Persona,
		cfg.CorrelationID,
	)

	inboxDir := filepath.Join(cfg.CsuiteHomeRoot, cfg.Persona, "inbox")
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
	// Best-effort cleanup if we abort before rename.
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
		return "", fmt.Errorf("writer: rename %q → %q: %w", tmpPath, finalPath, err)
	}
	cleanedUp = true
	return finalPath, nil
}

// renderFrontmatter produces the exact YAML frontmatter block the plan
// pins in §7. Hand-rolled (no yaml.Marshal) so the output order and
// quoting are stable across Go YAML library revisions — the watcher
// reads this back via yaml.Unmarshal, not diff, but operators do read
// it by eye.
func renderFrontmatter(cfg writerConfig, now time.Time) string {
	return "---\n" +
		"from: operator\n" +
		"to: " + cfg.Persona + "\n" +
		"topic: " + yamlScalar(cfg.Topic) + "\n" +
		"sent_at: " + now.Format(time.RFC3339) + "\n" +
		"correlation_id: " + cfg.CorrelationID + "\n" +
		"---\n\n"
}

// yamlScalar quotes s when necessary so the topic line round-trips
// through a strict YAML parser. The plan leaves topic free-form, so
// defensive double-quoting covers colons, leading dashes, and empty
// strings. No backslash escaping needed for typical 60-char topics;
// the function escapes only embedded `"` and `\`.
func yamlScalar(s string) string {
	needsQuote := s == ""
	for _, r := range s {
		// YAML 1.2: quote if the scalar would parse as anything other
		// than a plain scalar. Conservative list — colons, hashes,
		// leading-dashes, quotes, brackets, and anything resembling a
		// flow-indicator triggers quoting.
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
	// Escape for double-quoted YAML.
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

// ensureTrailingNewline guarantees the body ends with exactly one '\n'
// so downstream `cat`/`less` don't splice the next shell prompt onto
// the last line. A body of "" returns "\n".
func ensureTrailingNewline(body string) string {
	if body == "" {
		return "\n"
	}
	if body[len(body)-1] == '\n' {
		return body
	}
	return body + "\n"
}
