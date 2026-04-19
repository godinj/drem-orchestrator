package extract

import (
	"bytes"
	"time"
)

// ParseLine inspects a single log line and returns a typed Event describing
// it, or nil if the line matches none of the recognised patterns. The src
// tag is copied verbatim into the returned event's Source field so that
// downstream consumers know where the line came from (for example "stdout",
// "stderr", "transcript"). The now timestamp is injected for testability;
// production callers pass time.Now().
//
// Line bytes are examined but never retained: any strings copied into the
// returned event are freshly allocated so the caller is free to reuse or
// mutate the input buffer after the call returns.
func ParseLine(src string, line []byte, now time.Time) Event {
	trimmed := bytes.TrimRight(line, "\r\n")
	if len(trimmed) == 0 {
		return nil
	}

	// Try transcript (JSON tool_use) first because the JSON braces are a
	// very cheap prefix check and the matcher is the most selective.
	if ev, ok := matchToolCall(src, trimmed, now); ok {
		return ev
	}

	// Structured heartbeat markers next — they are a fixed prefix.
	if ev, ok := matchHeartbeat(src, trimmed, now); ok {
		return ev
	}

	// Crash indicators (panics, signals, exit status) are high-value and
	// cheap to detect with prefix/substring tests.
	if ev, ok := matchCrash(src, trimmed, now); ok {
		return ev
	}

	// Git operations (commits, pushes) ahead of test/build error matching
	// because their phrases ("To ", "[<branch> <sha>]") are distinctive.
	if ev, ok := matchCommit(src, trimmed, now); ok {
		return ev
	}
	if ev, ok := matchPush(src, trimmed, now); ok {
		return ev
	}

	// Test result lines (go test / --- PASS / --- FAIL) are recognisable
	// by their leading token.
	if ev, ok := matchTestResult(src, trimmed, now); ok {
		return ev
	}

	// Build errors are the most permissive matcher and run last so that a
	// line that happens to look like "file:line:col: msg" inside a test
	// summary cannot swallow the more specific test-result match.
	if ev, ok := matchBuildError(src, trimmed, now); ok {
		return ev
	}

	return nil
}
