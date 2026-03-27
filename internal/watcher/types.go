// Package watcher implements the C-Suite agent lifecycle manager.
// It launches agent turns as subprocesses, parses their JSON output,
// and records metrics to the database.
package watcher

import (
	"errors"
	"time"
)

// ErrKyleException is returned by RunTurn when the agent is "kyle".
// Kyle is not permitted to run turns under any circumstances.
var ErrKyleException = errors.New("watcher: kyle is not permitted to run turns")

// LifecycleResult carries the outcome of a single RunTurn call.
// It is populated after the subprocess exits and JSON output is parsed.
type LifecycleResult struct {
	Agent        string
	InputTokens  int
	OutputTokens int
	ExitStatus   int
	ErrorDetails string
	Duration     time.Duration
	StartedAt    time.Time
	EndedAt      time.Time
}

// claudeResponse is the JSON structure emitted by claude --output-format json.
type claudeResponse struct {
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}
