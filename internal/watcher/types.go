// Package watcher implements the C-Suite agent lifecycle manager.
// It launches agent turns as subprocesses, parses their JSON output,
// and records metrics to the database.
package watcher

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// TriggerResult is returned by TriggerAgent to indicate how the request was handled.
type TriggerResult int

const (
	// TriggerStarted means a new turn subprocess was launched immediately.
	TriggerStarted TriggerResult = iota + 1
	// TriggerQueued means the agent was already running; the trigger is
	// queued and will auto-start after the current turn completes.
	TriggerQueued
	// TriggerRefused means the agent name is not in Config.AllowedAgents.
	// No subprocess is launched and no metrics row is recorded.
	TriggerRefused
)

// Config holds Phase 2 LifecycleManager configuration.
type Config struct {
	// AllowedAgents lists the agent names permitted to run turns.
	// Any agent name not in this list will receive TriggerRefused.
	AllowedAgents []string
}

// CommandRunner abstracts agent turn subprocess execution so tests can inject
// a mock without spawning real processes. Implementations must be safe for
// concurrent use.
type CommandRunner interface {
	Run(ctx context.Context, agent string) (stdout []byte, exitCode int, err error)
}

// ErrKyleException is returned by RunTurn when the agent is "kyle".
// Kyle is not permitted to run turns under any circumstances.
var ErrKyleException = errors.New("watcher: kyle is not permitted to run turns")

// TurnMetric is the GORM model that persists a single agent turn record.
type TurnMetric struct {
	ID              uuid.UUID `gorm:"type:text;primaryKey"`
	Agent           string    `gorm:"not null;index"`
	InputTokens     int
	OutputTokens    int
	ExitStatus      int
	ErrorDetails    *string
	Duration        time.Duration
	StartedAt       time.Time
	EndedAt         time.Time
	EventsProcessed int
	MessagesSent    int
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

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
