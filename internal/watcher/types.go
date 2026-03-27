// Package watcher implements the C-Suite agent lifecycle manager.
// It launches agent turns as subprocesses, parses their JSON output,
// and records metrics to the database.
package watcher

import (
	"context"
	"errors"
	"time"
)

// Config holds Phase 2 LifecycleManager configuration.
type Config struct {
	// AllowedAgents lists the agent names permitted to run turns.
	// Any agent name not in this list will receive Refused.
	AllowedAgents []string
}

// CommandRunner abstracts agent turn subprocess execution so tests can inject
// a mock without spawning real processes. Implementations must be safe for
// concurrent use.
type CommandRunner interface {
	Run(ctx context.Context, agent string) (stdout []byte, exitCode int, err error)
}

// TriggerStarted, TriggerQueued, and TriggerRefused are aliases for the
// TriggerResult constants from dedup.go, used by LifecycleManager's trigger API.
const (
	TriggerStarted = Started
	TriggerQueued  = Queued
	TriggerRefused = Refused
)

// ErrKyleException is returned by RunTurn when the agent is "kyle".
// Kyle is not permitted to run turns under any circumstances.
var ErrKyleException = errors.New("watcher: kyle is not permitted to run turns")

// LifecycleResult carries the outcome of a single RunTurn call.
// It is populated after the subprocess exits and JSON output is parsed.
type LifecycleResult struct {
	Agent           string
	InputTokens     int
	OutputTokens    int
	ExitStatus      int
	ErrorDetails    string
	Duration        time.Duration
	StartedAt       time.Time
	EndedAt         time.Time
	EventsProcessed int
	MessagesSent    int
}

// claudeResponse is the JSON structure emitted by claude --output-format json.
type claudeResponse struct {
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	EventsProcessed int `json:"events_processed"`
	MessagesSent    int `json:"messages_sent"`
}
