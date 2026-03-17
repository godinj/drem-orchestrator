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
	return strings.ReplaceAll(abs, "/", "-")
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

// parseTranscriptUsage reads a JSONL transcript file and extracts the latest
// usage data from the last assistant entry.
func parseTranscriptUsage(path string) (*Usage, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("parse transcript: %w", err)
	}
	defer f.Close()

	var lastUsageEntry *transcriptEntry
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB line buffer
	for scanner.Scan() {
		line := scanner.Bytes()
		var entry transcriptEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}
		if entry.Type == "assistant" && entry.Message.Usage.InputTokens+
			entry.Message.Usage.CacheCreationInputTokens+
			entry.Message.Usage.CacheReadInputTokens > 0 {
			e := entry
			lastUsageEntry = &e
		}
	}

	if lastUsageEntry == nil {
		return nil, nil
	}

	u := lastUsageEntry.Message.Usage
	totalInput := u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
	ctxSize := defaultContextWindowSize

	usedPct := totalInput * 100 / ctxSize
	if usedPct > 100 {
		usedPct = 100
	}

	return &Usage{
		TotalInputTokens:  totalInput,
		TotalOutputTokens: u.OutputTokens,
		ContextWindowSize: ctxSize,
		UsedPercent:       usedPct,
		RemainingPercent:  100 - usedPct,
		LastUpdated:       time.Now(),
	}, nil
}
