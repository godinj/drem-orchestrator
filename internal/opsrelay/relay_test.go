package opsrelay

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/pkg/orchdto"
)

type fakeSource struct {
	since  time.Time
	limit  int
	events []orchdto.EventDTO
}

func (f *fakeSource) Events(_ context.Context, since time.Time, limit int) ([]orchdto.EventDTO, error) {
	f.since = since
	f.limit = limit
	return f.events, nil
}

func TestPollOnceWritesMikeInboxMessage(t *testing.T) {
	root := t.TempDir()
	ts := time.Date(2026, 4, 24, 12, 30, 0, 123, time.UTC)
	payload := json.RawMessage(`{"task_id":"task-1","actor":"orch","details":{"reason":"worker_spawn_failed"}}`)
	source := &fakeSource{events: []orchdto.EventDTO{{
		Timestamp: ts,
		Type:      "worker_spawn_failed",
		Payload:   payload,
	}}}

	res, err := PollOnce(context.Background(), Config{
		Source:     source,
		CsuiteRoot: root,
		OrchURL:    "http://orch:8080",
		Project:    "canvas",
	})
	require.NoError(t, err)
	require.Equal(t, 1, res.Fetched)
	require.Equal(t, 1, res.Written)
	require.Len(t, res.Paths, 1)
	require.Equal(t, filepath.Join(root, "mike", "inbox"), filepath.Dir(res.Paths[0]))
	require.Contains(t, filepath.Base(res.Paths[0]), "-ops-relay-to-mike-")
	require.Equal(t, defaultLimit, source.limit)

	body, err := os.ReadFile(res.Paths[0])
	require.NoError(t, err)
	msg := string(body)
	require.True(t, strings.HasPrefix(msg, "---\n"), msg)
	require.Contains(t, msg, "from: ops-relay\n")
	require.Contains(t, msg, "to: mike\n")
	require.Contains(t, msg, "topic: \"orchestrator event: worker_spawn_failed\"\n")
	require.Contains(t, msg, "Operational orchestrator event routed for C-Suite review.")
	require.Contains(t, msg, "Project: canvas")
	require.Contains(t, msg, "Orchestrator: http://orch:8080")
	require.Contains(t, msg, "worker_spawn_failed")
	require.Contains(t, msg, "```json")
}

func TestPollOnceUsesCursorAndFiltersTypes(t *testing.T) {
	root := t.TempDir()
	cursorPath := filepath.Join(root, "state", "ops-relay.cursor")
	start := time.Date(2026, 4, 24, 9, 0, 0, 0, time.UTC)
	require.NoError(t, os.MkdirAll(filepath.Dir(cursorPath), 0o700))
	require.NoError(t, os.WriteFile(cursorPath, []byte(start.Format(time.RFC3339Nano)+"\n"), 0o600))

	ignoredAt := start.Add(time.Minute)
	writtenAt := start.Add(2 * time.Minute)
	source := &fakeSource{events: []orchdto.EventDTO{
		{Timestamp: ignoredAt, Type: "heartbeat", Payload: json.RawMessage(`{"ok":true}`)},
		{Timestamp: writtenAt, Type: "task_failed", Payload: json.RawMessage(`{"task_id":"task-2"}`)},
	}}

	res, err := PollOnce(context.Background(), Config{
		Source:       source,
		CsuiteRoot:   root,
		CursorPath:   cursorPath,
		IncludeTypes: map[string]struct{}{"task_failed": {}},
		Limit:        7,
	})
	require.NoError(t, err)
	require.Equal(t, start, source.since)
	require.Equal(t, 7, source.limit)
	require.Equal(t, 2, res.Fetched)
	require.Equal(t, 1, res.Written)
	require.Equal(t, writtenAt.Add(time.Nanosecond), res.Cursor)

	cursorBytes, err := os.ReadFile(cursorPath)
	require.NoError(t, err)
	require.Equal(t, writtenAt.Add(time.Nanosecond).Format(time.RFC3339Nano)+"\n", string(cursorBytes))

	entries, err := os.ReadDir(filepath.Join(root, "mike", "inbox"))
	require.NoError(t, err)
	require.Len(t, entries, 1)
}

func TestPollOnceRequiresSourceAndRoot(t *testing.T) {
	_, err := PollOnce(context.Background(), Config{CsuiteRoot: t.TempDir()})
	require.ErrorContains(t, err, "Source is required")

	_, err = PollOnce(context.Background(), Config{Source: &fakeSource{}})
	require.ErrorContains(t, err, "CsuiteRoot is required")
}
