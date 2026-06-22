// Package opsrelay bridges read-only orchestrator events into the C-Suite
// inbox disk protocol.
package opsrelay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/godinj/drem-orchestrator/internal/csuite/diskstore"
	"github.com/godinj/drem-orchestrator/pkg/orchdto"
)

const (
	defaultLimit = 100
	defaultTo    = "mike"
	fromOpsRelay = "operator"
)

// EventSource is the read-only orchestrator event surface used by the relay.
// *orchclient.Client satisfies this interface.
type EventSource interface {
	Events(ctx context.Context, since time.Time, limit int) ([]orchdto.EventDTO, error)
}

// Config controls a single relay poll.
type Config struct {
	Source         EventSource
	CsuiteRoot     string
	CursorPath     string
	Since          time.Time
	Limit          int
	Now            func() time.Time
	OrchURL        string
	Project        string
	Recipient      string
	IncludeTypes   map[string]struct{}
	IncludeTargets map[string]struct{}
	CursorAdvance  time.Duration
}

// Result summarizes a poll pass.
type Result struct {
	Fetched int
	Written int
	Cursor  time.Time
	Paths   []string
}

// PollOnce fetches orchestrator events and writes each selected event to the
// recipient's inbox using the existing frontmatter writer.
func PollOnce(ctx context.Context, cfg Config) (Result, error) {
	if cfg.Source == nil {
		return Result{}, errors.New("opsrelay: Source is required")
	}
	if cfg.CsuiteRoot == "" {
		return Result{}, errors.New("opsrelay: CsuiteRoot is required")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Limit <= 0 {
		cfg.Limit = defaultLimit
	}
	if cfg.Recipient == "" {
		cfg.Recipient = defaultTo
	}
	if cfg.CursorAdvance == 0 {
		cfg.CursorAdvance = time.Nanosecond
	}

	since := cfg.Since
	if since.IsZero() && cfg.CursorPath != "" {
		loaded, err := loadCursor(cfg.CursorPath)
		if err != nil {
			return Result{}, err
		}
		since = loaded
	}

	events, err := cfg.Source.Events(ctx, since, cfg.Limit)
	if err != nil {
		return Result{}, err
	}
	if since.IsZero() {
		// The orch API returns newest-first without ?since=. Write the first
		// catch-up batch oldest-first so Mike's inbox poller reads it naturally.
		reverseEvents(events)
	}

	res := Result{Fetched: len(events), Cursor: since}
	for _, ev := range events {
		if !includeEvent(cfg, ev) {
			res.Cursor = advanceCursor(res.Cursor, ev.Timestamp, cfg.CursorAdvance)
			continue
		}
		path, err := writeEvent(ctx, cfg, ev)
		if err != nil {
			return res, err
		}
		res.Written++
		res.Paths = append(res.Paths, path)
		res.Cursor = advanceCursor(res.Cursor, ev.Timestamp, cfg.CursorAdvance)
	}

	if cfg.CursorPath != "" && !res.Cursor.IsZero() && res.Cursor.After(since) {
		if err := saveCursor(cfg.CursorPath, res.Cursor); err != nil {
			return res, err
		}
	}
	return res, nil
}

func includeEvent(cfg Config, ev orchdto.EventDTO) bool {
	if len(cfg.IncludeTypes) > 0 {
		if _, ok := cfg.IncludeTypes[ev.Type]; !ok {
			return false
		}
	}
	if len(cfg.IncludeTargets) > 0 {
		newValue, ok := eventNewValue(ev)
		if !ok {
			return false
		}
		if _, ok := cfg.IncludeTargets[newValue]; !ok {
			return false
		}
	}
	return true
}

func eventNewValue(ev orchdto.EventDTO) (string, bool) {
	var payload struct {
		NewValue string `json:"new_value"`
		ToStatus string `json:"to_status"`
	}
	if len(ev.Payload) == 0 || json.Unmarshal(ev.Payload, &payload) != nil {
		return "", false
	}
	if payload.NewValue != "" {
		return payload.NewValue, true
	}
	if payload.ToStatus != "" {
		return payload.ToStatus, true
	}
	return "", false
}

func writeEvent(ctx context.Context, cfg Config, ev orchdto.EventDTO) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	now := ev.Timestamp.UTC()
	if now.IsZero() {
		now = cfg.Now().UTC()
	}
	return diskstore.WriteInboxFile(diskstore.WriterConfig{
		CsuiteHomeRoot: cfg.CsuiteRoot,
		From:           fromOpsRelay,
		To:             cfg.Recipient,
		Topic:          topicFor(ev),
		Body:           bodyFor(cfg, ev),
		Now:            func() time.Time { return now },
		CorrelationID:  correlationID(ev),
	})
}

func topicFor(ev orchdto.EventDTO) string {
	if ev.Type == "" {
		return "orchestrator event"
	}
	return "orchestrator event: " + ev.Type
}

func bodyFor(cfg Config, ev orchdto.EventDTO) string {
	var payload any
	if len(ev.Payload) > 0 && json.Unmarshal(ev.Payload, &payload) == nil {
		pretty, _ := json.MarshalIndent(payload, "", "  ")
		return bodyHeader(cfg, ev) + "\nPayload:\n```json\n" + string(pretty) + "\n```\n"
	}
	return bodyHeader(cfg, ev) + "\nPayload:\n```json\n" + strings.TrimSpace(string(ev.Payload)) + "\n```\n"
}

func bodyHeader(cfg Config, ev orchdto.EventDTO) string {
	lines := []string{
		"Operational orchestrator event routed for C-Suite review by ops-relay.",
		"Reply to: operator.",
		"",
		"Type: " + ev.Type,
		"Timestamp: " + ev.Timestamp.UTC().Format(time.RFC3339Nano),
	}
	if cfg.Project != "" {
		lines = append(lines, "Project: "+cfg.Project)
	}
	if cfg.OrchURL != "" {
		lines = append(lines, "Orchestrator: "+cfg.OrchURL)
	}
	return strings.Join(lines, "\n")
}

func correlationID(ev orchdto.EventDTO) string {
	h := sha256.New()
	h.Write([]byte(ev.Timestamp.UTC().Format(time.RFC3339Nano)))
	h.Write([]byte("\x00"))
	h.Write([]byte(ev.Type))
	h.Write([]byte("\x00"))
	h.Write(ev.Payload)
	return hex.EncodeToString(h.Sum(nil))[:8]
}

func advanceCursor(current, eventTime time.Time, step time.Duration) time.Time {
	next := eventTime.UTC().Add(step)
	if next.After(current) {
		return next
	}
	return current
}

func reverseEvents(events []orchdto.EventDTO) {
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}
}

func loadCursor(path string) (time.Time, error) {
	b, err := os.ReadFile(path) //nolint:gosec // operator-configured local state path
	if os.IsNotExist(err) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("opsrelay: read cursor: %w", err)
	}
	s := strings.TrimSpace(string(b))
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("opsrelay: parse cursor %q: %w", s, err)
	}
	return t, nil
}

func saveCursor(path string, cursor time.Time) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("opsrelay: create cursor dir: %w", err)
	}
	return os.WriteFile(path, []byte(cursor.UTC().Format(time.RFC3339Nano)+"\n"), 0o600)
}
