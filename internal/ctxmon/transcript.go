package ctxmon

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// defaultContextWindowSize is the context window size for Claude models.
const defaultContextWindowSize = 200_000

// Model pricing per token (USD). These are Anthropic API rates as of 2026-03.
// Prices are per-token, not per-million-tokens.
var modelPricing = map[string]struct {
	inputPerToken              float64
	cacheCreationPerToken      float64
	cacheReadPerToken          float64
	outputPerToken             float64
}{
	"opus": {
		inputPerToken:         15.0 / 1_000_000,
		cacheCreationPerToken: 18.75 / 1_000_000,
		cacheReadPerToken:     1.875 / 1_000_000,
		outputPerToken:        75.0 / 1_000_000,
	},
	"sonnet": {
		inputPerToken:         3.0 / 1_000_000,
		cacheCreationPerToken: 3.75 / 1_000_000,
		cacheReadPerToken:     0.30 / 1_000_000,
		outputPerToken:        15.0 / 1_000_000,
	},
	"haiku": {
		inputPerToken:         0.80 / 1_000_000,
		cacheCreationPerToken: 1.0 / 1_000_000,
		cacheReadPerToken:     0.08 / 1_000_000,
		outputPerToken:        4.0 / 1_000_000,
	},
}

// modelFamily extracts the pricing family ("opus", "sonnet", "haiku") from a
// full model ID like "claude-opus-4-6" or "claude-sonnet-4-20250101".
func modelFamily(modelID string) string {
	lower := strings.ToLower(modelID)
	for _, family := range []string{"opus", "sonnet", "haiku"} {
		if strings.Contains(lower, family) {
			return family
		}
	}
	return ""
}

// transcriptEntry is the minimal structure we need from a JSONL transcript line.
type transcriptEntry struct {
	Type    string `json:"type"`
	Message struct {
		Model string `json:"model"`
		Usage struct {
			InputTokens              int `json:"input_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
			OutputTokens             int `json:"output_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// ProjectDirName returns the Claude Code project directory name for a
// given worktree path. Claude Code uses the absolute path with "/" replaced
// by "-" (with a leading dash).
func ProjectDirName(worktreePath string) string {
	abs, err := filepath.Abs(worktreePath)
	if err != nil {
		abs = worktreePath
	}
	// Claude Code replaces both "/" and "." with "-" when deriving
	// the project directory name from the absolute path.
	name := strings.ReplaceAll(abs, "/", "-")
	name = strings.ReplaceAll(name, ".", "-")
	return name
}

// ReadTranscriptUsage reads the latest context window usage from the Claude
// Code session transcript JSONL file. It scans the project directory for the
// most recently modified .jsonl file and reads the last assistant entry with
// usage data.
//
// Returns nil, nil if no transcript is found or no usage data exists yet.
func ReadTranscriptUsage(worktreePath string) (*Usage, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("read transcript: %w", err)
	}

	projectDir := filepath.Join(homeDir, ".claude", "projects", ProjectDirName(worktreePath))
	entries, err := os.ReadDir(projectDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read transcript: %w", err)
	}

	// Find the most recently modified .jsonl file.
	var latestPath string
	var latestMod time.Time
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(latestMod) {
			latestMod = info.ModTime()
			latestPath = filepath.Join(projectDir, e.Name())
		}
	}

	if latestPath == "" {
		return nil, nil
	}

	return parseTranscriptUsage(latestPath)
}

// parseTranscriptUsage reads a JSONL transcript file and accumulates token
// usage across all assistant entries to produce cumulative totals. It also
// estimates cost from the model name and token counts when the status line
// script has not provided cost data.
func parseTranscriptUsage(path string) (*Usage, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("parse transcript: %w", err)
	}
	defer f.Close()

	var (
		totalInput         int
		totalCacheCreation int
		totalCacheRead     int
		totalOutput        int
		model              string
		found              bool
	)

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB line buffer
	for scanner.Scan() {
		line := scanner.Bytes()
		var entry transcriptEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}
		if entry.Type != "assistant" {
			continue
		}
		u := entry.Message.Usage
		if u.InputTokens+u.CacheCreationInputTokens+u.CacheReadInputTokens == 0 {
			continue
		}
		found = true
		totalInput += u.InputTokens
		totalCacheCreation += u.CacheCreationInputTokens
		totalCacheRead += u.CacheReadInputTokens
		totalOutput += u.OutputTokens
		if entry.Message.Model != "" {
			model = entry.Message.Model
		}
	}

	if !found {
		return nil, nil
	}

	allInput := totalInput + totalCacheCreation + totalCacheRead
	ctxSize := defaultContextWindowSize

	usedPct := allInput * 100 / ctxSize
	if usedPct > 100 {
		usedPct = 100
	}

	// Estimate cost from model pricing and accumulated token counts.
	var costUSD float64
	if family := modelFamily(model); family != "" {
		if p, ok := modelPricing[family]; ok {
			costUSD = float64(totalInput)*p.inputPerToken +
				float64(totalCacheCreation)*p.cacheCreationPerToken +
				float64(totalCacheRead)*p.cacheReadPerToken +
				float64(totalOutput)*p.outputPerToken
		}
	}

	return &Usage{
		TotalInputTokens:  allInput,
		TotalOutputTokens: totalOutput,
		ContextWindowSize: ctxSize,
		UsedPercent:       usedPct,
		RemainingPercent:  100 - usedPct,
		TotalCostUSD:      costUSD,
		LastUpdated:       time.Now(),
	}, nil
}
