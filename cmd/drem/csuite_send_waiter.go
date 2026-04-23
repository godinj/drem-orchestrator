package main

// drem csuite send — reply waiter.
//
// Polls <CsuiteHomeRoot>/operator/inbox/ for a reply file whose
// frontmatter either (a) carries `in_reply_to: <corrid>` matching the
// send, or (b) is addressed `to: operator` and has an mtime strictly
// greater than the send's sent_at (degraded fallback — see plan §12
// "Persona prompts do not honor in_reply_to yet").
//
// Strictly read-only on the inbox: the waiter never moves, archives,
// or deletes. Phase 4's `drem csuite inbox archive` handles lifecycle.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// waiterFrontmatterCap mirrors internal/deliver/classify.go's
// frontmatterCap. Replies larger than this would trip the watcher's
// own cap anyway; reading a 64-KiB prefix is always enough to find
// the frontmatter + body boundary.
const waiterFrontmatterCap = 64 * 1024

// defaultPollInterval is the fallback cadence when waiterConfig.
// PollInterval is zero. Chosen to be responsive without hammering the
// filesystem; tests inject tighter values.
const defaultPollInterval = 2 * time.Second

// errWaiterTimeout is the sentinel returned from waitForReply when
// the configured Timeout elapses before a reply arrives. Callers in
// csuite_send.go translate this into exit code 3 so automation
// scripts can tell "silent persona" apart from generic failures.
var errWaiterTimeout = errors.New("waiter: timeout waiting for reply")

// waiterConfig carries the knobs the waiter needs. Zero value is
// unusable — the caller populates every field.
type waiterConfig struct {
	// OperatorInboxDir is <CsuiteHomeRoot>/operator/inbox. Tests
	// inject a tempdir.
	OperatorInboxDir string

	// CorrelationID is the 8-hex ID the waiter matches on against the
	// reply's frontmatter `in_reply_to:` field.
	CorrelationID string

	// SentAt is the lower-bound filter: only files whose mtime is
	// strictly after SentAt are candidates. Prevents stale replies
	// from earlier runs (same corrid collision, accidental reuse)
	// from being mis-matched.
	SentAt time.Time

	// Timeout is the total wait budget. waitForReply returns
	// errWaiterTimeout when this elapses without a match.
	Timeout time.Duration

	// PollInterval spaces successive directory scans. Default
	// defaultPollInterval when zero.
	PollInterval time.Duration

	// Now is the injectable clock used for timeout arithmetic.
	// Defaults to time.Now when nil.
	Now func() time.Time
}

// replyFrontmatter is the subset of a reply file's frontmatter the
// waiter needs. Extra fields (topic, from, sent_at, etc.) are parsed
// by yaml.v3 but not inspected here.
type replyFrontmatter struct {
	To        string `yaml:"to"`
	InReplyTo string `yaml:"in_reply_to"`
}

// waitForReply polls cfg.OperatorInboxDir until a matching reply
// appears or cfg.Timeout elapses. Returns the reply's body (everything
// after the closing `---\n`) and the full on-disk path. On timeout
// returns errWaiterTimeout. Respects ctx cancellation.
func waitForReply(ctx context.Context, cfg waiterConfig) (body string, path string, err error) {
	if cfg.OperatorInboxDir == "" {
		return "", "", fmt.Errorf("waiter: OperatorInboxDir is required")
	}
	if cfg.CorrelationID == "" {
		return "", "", fmt.Errorf("waiter: CorrelationID is required")
	}
	if cfg.Timeout <= 0 {
		return "", "", fmt.Errorf("waiter: Timeout must be positive")
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	poll := cfg.PollInterval
	if poll <= 0 {
		poll = defaultPollInterval
	}

	deadline := now().Add(cfg.Timeout)

	// Do an immediate first scan so a reply that already landed in the
	// tiny window between write + wait-start isn't delayed one full
	// poll tick.
	for {
		body, path, ok, err := scanForReply(cfg)
		if err != nil {
			return "", "", err
		}
		if ok {
			return body, path, nil
		}
		if !now().Before(deadline) {
			return "", "", errWaiterTimeout
		}
		remaining := deadline.Sub(now())
		wait := poll
		if remaining < wait {
			wait = remaining
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", "", ctx.Err()
		case <-timer.C:
		}
	}
}

// scanForReply walks cfg.OperatorInboxDir once, looking for a
// frontmatter-valid .md file whose in_reply_to matches
// cfg.CorrelationID (preferred) OR whose `to:` is "operator" and
// mtime > cfg.SentAt (degraded fallback). Returns body, path, ok.
// A non-nil error is an IO-level failure (inbox unreadable); a
// missing directory is treated as "no candidates yet" so the waiter
// tolerates the operator tree being created between send and first
// poll.
func scanForReply(cfg waiterConfig) (string, string, bool, error) {
	entries, err := os.ReadDir(cfg.OperatorInboxDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", "", false, nil
		}
		return "", "", false, fmt.Errorf("waiter: read inbox dir: %w", err)
	}

	// Two-bucket collection: preferred (in_reply_to match) beats any
	// fallback (to:operator without in_reply_to). If both exist in
	// the same tick, preferred wins; within each bucket, lexically
	// smallest filename wins (deterministic, plan §9).
	var preferred, fallback []string

	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		name := ent.Name()
		if !strings.HasSuffix(name, ".md") {
			continue
		}
		// Skip our own atomic-write tempfiles and archive siblings.
		if strings.HasPrefix(name, ".") {
			continue
		}

		full := filepath.Join(cfg.OperatorInboxDir, name)
		info, err := ent.Info()
		if err != nil {
			// Entry vanished between ReadDir and Info (race with
			// watcher rename). Skip; it'll reappear on the next tick.
			continue
		}
		// mtime must be strictly after SentAt so stale files from a
		// prior run can't poison a fresh wait.
		if !info.ModTime().After(cfg.SentAt) {
			continue
		}

		fm, ok := readFrontmatter(full)
		if !ok {
			continue
		}
		switch {
		case fm.InReplyTo != "" && fm.InReplyTo == cfg.CorrelationID:
			preferred = append(preferred, name)
		case fm.InReplyTo == "" && fm.To == "operator":
			fallback = append(fallback, name)
		}
	}

	pick := func(names []string) (string, string, bool, error) {
		sort.Strings(names)
		path := filepath.Join(cfg.OperatorInboxDir, names[0])
		body, ok, err := extractReplyBody(path)
		if err != nil {
			return "", "", false, err
		}
		if !ok {
			// File raced-away between scan and body read; caller will
			// retry on the next tick.
			return "", "", false, nil
		}
		return body, path, true, nil
	}

	if len(preferred) > 0 {
		return pick(preferred)
	}
	if len(fallback) > 0 {
		return pick(fallback)
	}
	return "", "", false, nil
}

// readFrontmatter returns the parsed frontmatter of path. ok=false
// means the file has no frontmatter, is unreadable, or its YAML is
// malformed — all of which cause the waiter to skip it quietly.
func readFrontmatter(path string) (replyFrontmatter, bool) {
	data, err := readFilePrefix(path, waiterFrontmatterCap)
	if err != nil {
		return replyFrontmatter{}, false
	}
	fmBytes, ok := extractFrontmatterBytes(data)
	if !ok {
		return replyFrontmatter{}, false
	}
	var fm replyFrontmatter
	if err := yaml.Unmarshal(fmBytes, &fm); err != nil {
		return replyFrontmatter{}, false
	}
	return fm, true
}

// extractReplyBody returns the text after the closing `---\n`
// delimiter, with leading blank lines trimmed. Returns ok=false if
// the file can't be read or has no frontmatter close — the file may
// have been truncated or the writer hasn't yet renamed it into place.
func extractReplyBody(path string) (string, bool, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path under configured inbox dir
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("waiter: read reply %q: %w", path, err)
	}
	body, ok := splitBody(data)
	if !ok {
		return "", false, nil
	}
	return body, true, nil
}

// splitBody locates the closing frontmatter delimiter and returns the
// trimmed body. Matches the delimiter shape used in
// internal/deliver/classify.go: leading "---\n" and closing "\n---"
// followed by optional trailing newlines.
func splitBody(data []byte) (string, bool) {
	const open = "---\n"
	if len(data) < len(open) || string(data[:len(open)]) != open {
		return "", false
	}
	rest := data[len(open):]
	idx := strings.Index(string(rest), "\n---")
	if idx < 0 {
		return "", false
	}
	after := rest[idx+len("\n---"):]
	// Consume the newline following the close, if present, plus one
	// optional blank line. Trim leading newlines up to the body; do
	// not trim internal whitespace.
	s := strings.TrimLeft(string(after), "\n")
	return s, true
}

// extractFrontmatterBytes mirrors deliver.extractFrontmatter but is
// kept local so cmd/drem has no runtime dependency on the deliver
// package (the CLI builds cleanly on a host that never runs the
// watcher binary).
func extractFrontmatterBytes(data []byte) ([]byte, bool) {
	const open = "---\n"
	const close = "\n---"
	if len(data) < len(open) || string(data[:len(open)]) != open {
		return nil, false
	}
	rest := data[len(open):]
	idx := strings.Index(string(rest), close)
	if idx < 0 {
		return nil, false
	}
	return rest[:idx], true
}

// readFilePrefix reads up to max bytes from path. Short files return
// all bytes; large files are truncated. EOF is not an error.
func readFilePrefix(path string, max int) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec // path under configured inbox dir
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, max)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return buf[:n], nil
}
