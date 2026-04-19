// Package extract parses log lines into typed state events shared between
// agentmon's Docker-stdout subscription path and the existing Claude
// transcript-tailing path.
//
// The package is pure: ParseLine takes a byte slice in and returns a typed
// Event value (or nil). There is no I/O, no goroutines, no Docker access.
// Callers are responsible for reading log lines from whatever source they
// have and for shipping the returned events onward.
//
// The package has zero dependencies outside the Go standard library.
package extract

import "time"

// Event is the marker interface implemented by every typed event emitted by
// ParseLine. The isEvent method is unexported so that callers outside this
// package cannot extend the union; an exhaustive type switch over the
// concrete types defined below is always safe.
type Event interface {
	isEvent()
}

// Commit is emitted when a git commit is detected on a log stream.
type Commit struct {
	Timestamp time.Time
	Source    string
	SHA       string
	Branch    string
	Message   string
}

func (*Commit) isEvent() {}

// Push is emitted when a git push is detected on a log stream.
type Push struct {
	Timestamp time.Time
	Source    string
	Branch    string
	Remote    string
}

func (*Push) isEvent() {}

// TestResult is emitted when a test run's pass/fail outcome is detected.
type TestResult struct {
	Timestamp time.Time
	Source    string
	Passed    bool
	Summary   string
}

func (*TestResult) isEvent() {}

// BuildError is emitted when a compiler or build-tool error line is detected.
type BuildError struct {
	Timestamp time.Time
	Source    string
	Tool      string
	Message   string
	File      string
	Line      int
}

func (*BuildError) isEvent() {}

// Heartbeat is emitted when the watchdog's liveness marker is detected.
type Heartbeat struct {
	Timestamp time.Time
	Source    string
	AgentID   string
}

func (*Heartbeat) isEvent() {}

// Crash is emitted when a panic, fatal runtime error, signal, or non-zero
// process exit status is detected on a log stream.
type Crash struct {
	Timestamp time.Time
	Source    string
	Reason    string
	ExitCode  int
}

func (*Crash) isEvent() {}

// ToolCall is emitted when a Claude Code tool_use block is detected. It
// replaces the ToolCall type formerly defined in internal/agentmon/parse.go;
// agentmon.ToolCall is now a thin wrapper around this type.
type ToolCall struct {
	Timestamp time.Time
	Source    string
	Tool      string
	Target    string
}

func (*ToolCall) isEvent() {}
