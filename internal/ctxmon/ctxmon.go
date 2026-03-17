// Package ctxmon provides context window monitoring for Claude Code agents.
//
// It reads usage data written by a status line script and a PreCompact hook,
// allowing the orchestrator to track each agent's token usage and intervene
// before an agent exhausts its context window.
package ctxmon

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Usage represents the current context window state for an agent.
type Usage struct {
	TotalInputTokens    int       `json:"total_input_tokens"`
	TotalOutputTokens   int       `json:"total_output_tokens"`
	ContextWindowSize   int       `json:"context_window_size"`
	UsedPercent         int       `json:"used_percentage"`
	RemainingPercent    int       `json:"remaining_percentage"`
	TotalCostUSD        float64   `json:"total_cost_usd"`
	CompactionTriggered bool      `json:"compaction_triggered"`
	LastUpdated         time.Time `json:"-"`
}

// UsageFilePath returns the canonical path for the context usage JSON file.
func UsageFilePath(worktreePath string) string {
	return filepath.Join(worktreePath, ".claude", "context-usage.json")
}

// CompactionSignalPath returns the path for the compaction signal file.
func CompactionSignalPath(worktreePath string) string {
	return filepath.Join(worktreePath, ".claude", "compaction-triggered")
}

// ReadUsageFile reads and parses the context usage JSON file at path.
// Returns nil, nil if the file does not exist yet (agent hasn't made its
// first API call). Returns an error only on I/O errors or corrupt JSON.
func ReadUsageFile(path string) (*Usage, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read usage file: %w", err)
	}

	if len(data) == 0 {
		return nil, fmt.Errorf("read usage file: empty file")
	}

	var u Usage
	if err := json.Unmarshal(data, &u); err != nil {
		return nil, fmt.Errorf("read usage file: %w", err)
	}

	u.LastUpdated = time.Now()
	return &u, nil
}
