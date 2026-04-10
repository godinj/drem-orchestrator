package ctxmon

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// DefaultOpenCodeContextWindow is the default context window size (in tokens)
// for vLLM-backed OpenCode agents. This corresponds to the Qwen 3.5 27B
// model's context window on our vLLM instance.
const DefaultOpenCodeContextWindow = 43904

// DefaultOpenCodeDBPath returns the default path for the OpenCode SQLite
// database at ~/.local/share/opencode/opencode.db.
func DefaultOpenCodeDBPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "share", "opencode", "opencode.db")
}

// OpenCodeLogPath returns the path for the OpenCode JSONL output log
// in the given worktree.
func OpenCodeLogPath(worktreePath string) string {
	return filepath.Join(worktreePath, ".opencode", "agent-output.jsonl")
}

// openCodeStepEvent is the minimal JSON structure needed from an OpenCode
// JSONL step_finish event for context usage tracking.
type openCodeStepEvent struct {
	Type string `json:"type"`
	Part struct {
		Tokens struct {
			Input  int `json:"input"`
			Output int `json:"output"`
		} `json:"tokens"`
	} `json:"part"`
}

// ReadOpenCodeJSONLUsage reads token usage from the OpenCode agent-output.jsonl
// file. It scans step_finish events, accumulates total tokens, and computes
// context percentage from the last turn's input tokens (which represents the
// current context window fill level since each turn re-ingests the full
// conversation).
//
// contextWindowSize controls the denominator for percentage calculation;
// pass 0 to use DefaultOpenCodeContextWindow.
//
// Returns nil, nil if the file does not exist or contains no token data.
func ReadOpenCodeJSONLUsage(logPath string, contextWindowSize int) (*Usage, error) {
	f, err := os.Open(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open opencode log: %w", err)
	}
	defer f.Close()

	var totalIn, totalOut, lastIn int
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var evt openCodeStepEvent
		if err := json.Unmarshal(line, &evt); err != nil {
			continue
		}
		if evt.Type == "step_finish" {
			totalIn += evt.Part.Tokens.Input
			totalOut += evt.Part.Tokens.Output
			if evt.Part.Tokens.Input > 0 {
				lastIn = evt.Part.Tokens.Input
			}
		}
	}

	if totalIn == 0 && totalOut == 0 {
		return nil, nil
	}

	return buildOpenCodeUsage(totalIn, totalOut, lastIn, contextWindowSize), nil
}

// openCodeMsgData is the minimal JSON structure for an OpenCode message
// record's data column, used when reading from the SQLite database.
type openCodeMsgData struct {
	Tokens struct {
		Input  int `json:"input"`
		Output int `json:"output"`
	} `json:"tokens"`
}

// ReadOpenCodeDBUsage reads token usage from the OpenCode SQLite database
// for the most recent session whose directory matches worktreePath. It
// accumulates token counts across all messages and computes context percentage
// from the last turn's input tokens.
//
// contextWindowSize controls the denominator for percentage calculation;
// pass 0 to use DefaultOpenCodeContextWindow.
//
// Returns nil, nil if the database does not exist, contains no matching
// session, or has no token data.
func ReadOpenCodeDBUsage(dbPath, worktreePath string, contextWindowSize int) (*Usage, error) {
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return nil, nil
	}

	db, err := sql.Open("sqlite3", dbPath+"?mode=ro&_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open opencode db: %w", err)
	}
	defer db.Close()

	// Find the most recent session for this directory and read all its messages
	// in chronological order.
	rows, err := db.Query(`
		SELECT m.data
		FROM message m
		WHERE m.session_id = (
			SELECT s.id FROM session s
			WHERE s.directory = ?
			ORDER BY s.time_updated DESC
			LIMIT 1
		)
		ORDER BY m.time_created ASC
	`, worktreePath)
	if err != nil {
		return nil, fmt.Errorf("query opencode db: %w", err)
	}
	defer rows.Close()

	var totalIn, totalOut, lastIn int
	var found bool
	for rows.Next() {
		var dataStr string
		if err := rows.Scan(&dataStr); err != nil {
			continue
		}
		var msg openCodeMsgData
		if err := json.Unmarshal([]byte(dataStr), &msg); err != nil {
			continue
		}
		if msg.Tokens.Input > 0 || msg.Tokens.Output > 0 {
			found = true
			totalIn += msg.Tokens.Input
			totalOut += msg.Tokens.Output
			if msg.Tokens.Input > 0 {
				lastIn = msg.Tokens.Input
			}
		}
	}

	if !found {
		return nil, nil
	}

	return buildOpenCodeUsage(totalIn, totalOut, lastIn, contextWindowSize), nil
}

// buildOpenCodeUsage constructs a Usage value from accumulated OpenCode token
// counts. lastIn is the most recent turn's input token count, used to compute
// context window percentage.
func buildOpenCodeUsage(totalIn, totalOut, lastIn, contextWindowSize int) *Usage {
	if contextWindowSize <= 0 {
		contextWindowSize = DefaultOpenCodeContextWindow
	}

	usedPct := 0
	if lastIn > 0 && contextWindowSize > 0 {
		usedPct = lastIn * 100 / contextWindowSize
		if usedPct > 100 {
			usedPct = 100
		}
	}

	return &Usage{
		TotalInputTokens:  totalIn,
		TotalOutputTokens: totalOut,
		ContextWindowSize: contextWindowSize,
		UsedPercent:       usedPct,
		RemainingPercent:  100 - usedPct,
		TotalCostUSD:      0, // local model, no API cost
		LastUpdated:       time.Now(),
	}
}

// VLLMMetrics holds parsed vLLM Prometheus metrics relevant to context
// and load monitoring.
type VLLMMetrics struct {
	PromptTokensTotal     int64
	GenerationTokensTotal int64
	RequestsRunning       int
	RequestsWaiting       int
}

// ReadVLLMMetrics fetches and parses the vLLM Prometheus metrics endpoint.
// The endpoint should be the full URL, e.g. "http://localhost:8081/metrics".
func ReadVLLMMetrics(endpoint string) (*VLLMMetrics, error) {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(endpoint)
	if err != nil {
		return nil, fmt.Errorf("fetch vllm metrics: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read vllm metrics body: %w", err)
	}

	return ParseVLLMMetrics(string(body))
}

// ParseVLLMMetrics parses vLLM Prometheus text-format metrics into a
// VLLMMetrics struct. It extracts prompt_tokens_total, generation_tokens_total,
// num_requests_running, and num_requests_waiting.
func ParseVLLMMetrics(text string) (*VLLMMetrics, error) {
	m := &VLLMMetrics{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Parse Prometheus text format: "metric_name{labels} value"
		// or "metric_name value" (no labels).
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}

		name := parts[0]
		// Strip labels: "vllm:prompt_tokens_total{engine=\"0\",...}" → "vllm:prompt_tokens_total"
		if idx := strings.Index(name, "{"); idx >= 0 {
			name = name[:idx]
		}

		val, err := strconv.ParseFloat(parts[len(parts)-1], 64)
		if err != nil {
			continue
		}

		switch name {
		case "vllm:prompt_tokens_total":
			m.PromptTokensTotal = int64(val)
		case "vllm:generation_tokens_total":
			m.GenerationTokensTotal = int64(val)
		case "vllm:num_requests_running":
			m.RequestsRunning = int(val)
		case "vllm:num_requests_waiting":
			m.RequestsWaiting = int(val)
		}
	}
	return m, nil
}
