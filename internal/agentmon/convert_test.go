package agentmon

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/internal/extract"
)

// TestToIngestRecord walks every extract.Event variant and asserts that
// the resulting IngestRecord has the expected discriminant and payload.
// A table-driven test keeps the variant-to-record mapping auditable at
// a glance; adding a new event type without updating the converter is
// caught by the "Type is empty" fallthrough at the end.
func TestToIngestRecord(t *testing.T) {
	now := time.Date(2026, 4, 19, 12, 0, 0, 0, time.UTC)
	const cid = "cont-123"
	const wid = "worker-abc"

	t.Run("commit", func(t *testing.T) {
		ev := &extract.Commit{Timestamp: now, Source: "s", SHA: "abc", Branch: "main", Message: "wip"}
		rec := toIngestRecord(cid, wid, ev)
		require.Equal(t, recordTypeCommit, rec.Type)
		require.Equal(t, cid, rec.ContainerID)
		require.Equal(t, wid, rec.WorkerID)
		require.Equal(t, now, rec.Timestamp)
		require.Equal(t, "abc", rec.Payload["sha"])
		require.Equal(t, "main", rec.Payload["branch"])
		require.Equal(t, "wip", rec.Payload["message"])
	})

	t.Run("push", func(t *testing.T) {
		ev := &extract.Push{Timestamp: now, Source: "s", Branch: "main", Remote: "origin"}
		rec := toIngestRecord(cid, wid, ev)
		require.Equal(t, recordTypePush, rec.Type)
		require.Equal(t, "main", rec.Payload["branch"])
		require.Equal(t, "origin", rec.Payload["remote"])
	})

	t.Run("test_result", func(t *testing.T) {
		ev := &extract.TestResult{Timestamp: now, Source: "s", Passed: true, Summary: "ok 3"}
		rec := toIngestRecord(cid, wid, ev)
		require.Equal(t, recordTypeTestResult, rec.Type)
		require.Equal(t, true, rec.Payload["passed"])
		require.Equal(t, "ok 3", rec.Payload["summary"])
	})

	t.Run("build_error", func(t *testing.T) {
		ev := &extract.BuildError{Timestamp: now, Source: "s", Tool: "go", Message: "boom", File: "a.go", Line: 42}
		rec := toIngestRecord(cid, wid, ev)
		require.Equal(t, recordTypeBuildError, rec.Type)
		require.Equal(t, "go", rec.Payload["tool"])
		require.Equal(t, "boom", rec.Payload["message"])
		require.Equal(t, "a.go", rec.Payload["file"])
		require.Equal(t, 42, rec.Payload["line"])
	})

	t.Run("heartbeat", func(t *testing.T) {
		ev := &extract.Heartbeat{Timestamp: now, Source: "s", AgentID: "a1"}
		rec := toIngestRecord(cid, wid, ev)
		require.Equal(t, recordTypeHeartbeat, rec.Type)
		require.Equal(t, "a1", rec.Payload["agent_id"])
	})

	t.Run("crash", func(t *testing.T) {
		ev := &extract.Crash{Timestamp: now, Source: "s", Reason: "panic: x", ExitCode: 2}
		rec := toIngestRecord(cid, wid, ev)
		require.Equal(t, recordTypeCrash, rec.Type)
		require.Equal(t, "panic: x", rec.Payload["reason"])
		require.Equal(t, 2, rec.Payload["exit_code"])
	})

	t.Run("tool_call", func(t *testing.T) {
		ev := &extract.ToolCall{Timestamp: now, Source: "s", Tool: "Bash", Target: "ls"}
		rec := toIngestRecord(cid, wid, ev)
		require.Equal(t, recordTypeToolCall, rec.Type)
		require.Equal(t, "Bash", rec.Payload["tool"])
		require.Equal(t, "ls", rec.Payload["target"])
	})
}

// TestToIngestRecordUnknownEvent covers the defensive zero-return path
// so the caller's drop-when-empty-Type branch is exercised.
func TestToIngestRecordUnknownEvent(t *testing.T) {
	// An unknown implementation of extract.Event cannot be constructed
	// from outside the extract package (isEvent is unexported), so we
	// feed a nil and exercise the type-switch's fallthrough.
	rec := toIngestRecord("c", "w", nil)
	require.Equal(t, "", rec.Type)
	require.Nil(t, rec.Payload)
}
