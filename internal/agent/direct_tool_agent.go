// direct_tool_agent.go implements a synchronous tool-calling agent that calls
// SGLang's OpenAI-compatible API directly. It extends the direct_classifier.go
// pattern to support agentic tool loops for coder, reviewer, and fixer roles.
//
// SGLang must be running with --tool-call-parser gemma4 to handle bidirectional
// conversion between OpenAI tool format and Gemma 4's native tool call tokens.
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

// DirectToolAgentConfig holds connection, generation, and execution parameters
// for the direct tool-calling agent.
type DirectToolAgentConfig struct {
	Endpoint      string        // Full URL for chat completions endpoint
	Model         string        // Model identifier sent in the API request
	MaxTokens     int           // Maximum tokens per response
	Temperature   float64       // Sampling temperature
	Timeout       time.Duration // HTTP request timeout per API call
	MaxIterations int           // Maximum tool-call loop iterations
	WorkDir       string        // Working directory; tools restricted to paths under this
	BashTimeout   time.Duration // Timeout for bash commands (default 30s)
}

// DefaultDirectToolAgentConfig returns a config targeting the local SGLang
// server with sensible defaults.
func DefaultDirectToolAgentConfig() DirectToolAgentConfig {
	return DirectToolAgentConfig{
		Endpoint:      "http://localhost:8081/v1/chat/completions",
		Model:         "gemma4-26b",
		MaxTokens:     2048,
		Temperature:   0.1,
		Timeout:       120 * time.Second,
		MaxIterations: 20,
		BashTimeout:   30 * time.Second,
	}
}

// ---------------------------------------------------------------------------
// Result
// ---------------------------------------------------------------------------

// DirectToolAgentResult holds the outcome of a tool-agent run.
type DirectToolAgentResult struct {
	Output     string        // Final text output from the model
	OutputPath string        // Path to the written output file (if any)
	TokensIn   int           // Total prompt tokens across all iterations
	TokensOut  int           // Total completion tokens across all iterations
	Iterations int           // Number of loop iterations used
	Duration   time.Duration // Wall-clock time for the entire run
}

// ---------------------------------------------------------------------------
// OpenAI tool-calling API types (extend the base types in direct_classifier.go)
// ---------------------------------------------------------------------------

// toolDefinition is the OpenAI-format tool definition sent in requests.
type toolDefinition struct {
	Type     string       `json:"type"`
	Function toolFunction `json:"function"`
}

// toolFunction describes a function within a tool definition.
type toolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// toolChatRequest extends chatRequest with tool definitions.
type toolChatRequest struct {
	Model       string           `json:"model"`
	Messages    []toolChatMsg    `json:"messages"`
	MaxTokens   int              `json:"max_tokens"`
	Temperature float64          `json:"temperature"`
	Tools       []toolDefinition `json:"tools,omitempty"`
}

// toolChatMsg is a chat message that supports all roles including tool
// responses. Only the fields relevant to a given role are populated.
type toolChatMsg struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`   // present when role=assistant with tool calls
	ToolCallID string     `json:"tool_call_id,omitempty"` // present when role=tool
	Name       string     `json:"name,omitempty"`         // present when role=tool (function name)
}

// toolCall represents a tool call in the assistant's response.
type toolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function toolCallFunction `json:"function"`
}

// toolCallFunction holds the function name and JSON-encoded arguments.
type toolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// toolChatResponse is the API response that includes tool call information.
type toolChatResponse struct {
	Choices []toolChatChoice `json:"choices"`
	Usage   struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// toolChatChoice is one choice in the response.
type toolChatChoice struct {
	Message      toolChatMsg `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

// ---------------------------------------------------------------------------
// Tool schemas
// ---------------------------------------------------------------------------

// builtinTools defines the complete set of available tool definitions.
var builtinTools = map[string]toolDefinition{
	"read": {
		Type: "function",
		Function: toolFunction{
			Name:        "read",
			Description: "Read file contents",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"File path to read"},"offset":{"type":"integer","description":"Line number to start from (0-based)"},"limit":{"type":"integer","description":"Max lines to return"}},"required":["path"]}`),
		},
	},
	"edit": {
		Type: "function",
		Function: toolFunction{
			Name:        "edit",
			Description: "Replace exact text in a file",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"File path"},"old":{"type":"string","description":"Exact text to find"},"new":{"type":"string","description":"Replacement text"}},"required":["path","old","new"]}`),
		},
	},
	"write": {
		Type: "function",
		Function: toolFunction{
			Name:        "write",
			Description: "Write content to a file (creates or overwrites)",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"File path"},"content":{"type":"string","description":"File content"}},"required":["path","content"]}`),
		},
	},
	"bash": {
		Type: "function",
		Function: toolFunction{
			Name:        "bash",
			Description: "Run a shell command",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"cmd":{"type":"string","description":"Command to execute"}},"required":["cmd"]}`),
		},
	},
	"grep": {
		Type: "function",
		Function: toolFunction{
			Name:        "grep",
			Description: "Search file contents with regex (uses ripgrep)",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string","description":"Regex pattern"},"path":{"type":"string","description":"Directory or file to search"},"glob":{"type":"string","description":"File glob filter (e.g. *.go)"}},"required":["pattern"]}`),
		},
	},
	"glob": {
		Type: "function",
		Function: toolFunction{
			Name:        "glob",
			Description: "Find files by glob pattern",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string","description":"Glob pattern (e.g. **/*.go)"},"path":{"type":"string","description":"Base directory"}},"required":["pattern"]}`),
		},
	},
}

// roleTools maps agent roles to their permitted tool names.
var roleTools = map[string][]string{
	"coder":    {"read", "edit", "write", "bash", "grep", "glob"},
	"fixer":    {"read", "edit", "write", "bash", "grep", "glob"},
	"reviewer": {"read", "bash", "grep", "glob"},
	"prep":     {"read", "grep", "glob"},
}

// ToolsForRole returns the tool definitions permitted for the given agent role.
// Unknown roles get read-only tools (read, grep, glob).
func ToolsForRole(role string) []toolDefinition {
	names, ok := roleTools[role]
	if !ok {
		names = []string{"read", "grep", "glob"}
	}
	tools := make([]toolDefinition, 0, len(names))
	for _, name := range names {
		if td, exists := builtinTools[name]; exists {
			tools = append(tools, td)
		}
	}
	return tools
}

// ---------------------------------------------------------------------------
// Tool execution limits
// ---------------------------------------------------------------------------

const (
	maxReadLines   = 200
	maxBashOutput  = 2000 // characters
	maxGrepMatches = 50
	maxGlobResults = 100
)

// ---------------------------------------------------------------------------
// Tool execution
// ---------------------------------------------------------------------------

// toolExecutor bundles the working directory and config needed to run tools.
type toolExecutor struct {
	workDir     string
	bashTimeout time.Duration
}

// execute dispatches a tool call by name and returns the result string.
func (te *toolExecutor) execute(name string, argsJSON string) (string, error) {
	switch name {
	case "read":
		return te.execRead(argsJSON)
	case "edit":
		return te.execEdit(argsJSON)
	case "write":
		return te.execWrite(argsJSON)
	case "bash":
		return te.execBash(argsJSON)
	case "grep":
		return te.execGrep(argsJSON)
	case "glob":
		return te.execGlob(argsJSON)
	default:
		return "", fmt.Errorf("unknown tool: %s", name)
	}
}

// resolvePath resolves a path relative to the working directory and validates
// it stays within bounds.
func (te *toolExecutor) resolvePath(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("empty path")
	}
	var resolved string
	if filepath.IsAbs(p) {
		resolved = filepath.Clean(p)
	} else {
		resolved = filepath.Clean(filepath.Join(te.workDir, p))
	}
	// Verify the resolved path is under the working directory.
	if !strings.HasPrefix(resolved, te.workDir) {
		return "", fmt.Errorf("path %q is outside working directory %q", p, te.workDir)
	}
	return resolved, nil
}

func (te *toolExecutor) execRead(argsJSON string) (string, error) {
	var args struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("parse read args: %w", err)
	}
	resolved, err := te.resolvePath(args.Path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", args.Path, err)
	}
	lines := strings.Split(string(data), "\n")

	// Apply offset.
	offset := args.Offset
	if offset < 0 {
		offset = 0
	}
	if offset >= len(lines) {
		return fmt.Sprintf("[file has %d lines, offset %d is past end]", len(lines), offset), nil
	}
	lines = lines[offset:]

	// Apply limit.
	limit := args.Limit
	if limit <= 0 {
		limit = maxReadLines
	}
	if limit > maxReadLines {
		limit = maxReadLines
	}
	truncated := false
	if len(lines) > limit {
		lines = lines[:limit]
		truncated = true
	}

	// Format with line numbers.
	var buf strings.Builder
	for i, line := range lines {
		fmt.Fprintf(&buf, "%d\t%s\n", offset+i+1, line)
	}
	if truncated {
		fmt.Fprintf(&buf, "[... truncated at %d lines]", limit)
	}
	return buf.String(), nil
}

func (te *toolExecutor) execEdit(argsJSON string) (string, error) {
	var args struct {
		Path string `json:"path"`
		Old  string `json:"old"`
		New  string `json:"new"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("parse edit args: %w", err)
	}
	resolved, err := te.resolvePath(args.Path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return "", fmt.Errorf("read %s for edit: %w", args.Path, err)
	}
	content := string(data)
	if !strings.Contains(content, args.Old) {
		return "", fmt.Errorf("old string not found in %s", args.Path)
	}
	count := strings.Count(content, args.Old)
	if count > 1 {
		return "", fmt.Errorf("old string appears %d times in %s (must be unique)", count, args.Path)
	}
	newContent := strings.Replace(content, args.Old, args.New, 1)
	if err := os.WriteFile(resolved, []byte(newContent), 0o644); err != nil {
		return "", fmt.Errorf("write %s after edit: %w", args.Path, err)
	}
	return fmt.Sprintf("edited %s: replaced %d bytes with %d bytes", args.Path, len(args.Old), len(args.New)), nil
}

func (te *toolExecutor) execWrite(argsJSON string) (string, error) {
	var args struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("parse write args: %w", err)
	}
	resolved, err := te.resolvePath(args.Path)
	if err != nil {
		return "", err
	}
	// Create parent directories if needed.
	dir := filepath.Dir(resolved)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create directory %s: %w", dir, err)
	}
	if err := os.WriteFile(resolved, []byte(args.Content), 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", args.Path, err)
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(args.Content), args.Path), nil
}

func (te *toolExecutor) execBash(argsJSON string) (string, error) {
	var args struct {
		Cmd string `json:"cmd"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("parse bash args: %w", err)
	}
	timeout := te.bashTimeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", "-c", args.Cmd)
	cmd.Dir = te.workDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	var result strings.Builder
	if stdout.Len() > 0 {
		result.WriteString(stdout.String())
	}
	if stderr.Len() > 0 {
		if result.Len() > 0 {
			result.WriteString("\n")
		}
		result.WriteString("STDERR:\n")
		result.WriteString(stderr.String())
	}

	output := result.String()
	if len(output) > maxBashOutput {
		output = output[:maxBashOutput] + "\n[... truncated]"
	}

	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return output + "\n[timeout after " + timeout.String() + "]", nil
		}
		return output + "\n[exit error: " + err.Error() + "]", nil
	}
	return output, nil
}

func (te *toolExecutor) execGrep(argsJSON string) (string, error) {
	var args struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
		Glob    string `json:"glob"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("parse grep args: %w", err)
	}

	rgArgs := []string{"-n", "--max-count", fmt.Sprintf("%d", maxGrepMatches)}
	if args.Glob != "" {
		rgArgs = append(rgArgs, "--glob", args.Glob)
	}
	rgArgs = append(rgArgs, args.Pattern)

	searchPath := te.workDir
	if args.Path != "" {
		resolved, err := te.resolvePath(args.Path)
		if err != nil {
			return "", err
		}
		searchPath = resolved
	}
	rgArgs = append(rgArgs, searchPath)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "rg", rgArgs...)
	cmd.Dir = te.workDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	output := stdout.String()
	if len(output) > maxBashOutput {
		output = output[:maxBashOutput] + "\n[... truncated]"
	}

	// ripgrep returns exit code 1 for no matches — not an error.
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return "[no matches]", nil
		}
		if stderr.Len() > 0 {
			return "", fmt.Errorf("grep failed: %s", stderr.String())
		}
		return "", fmt.Errorf("grep failed: %w", err)
	}
	return output, nil
}

func (te *toolExecutor) execGlob(argsJSON string) (string, error) {
	var args struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("parse glob args: %w", err)
	}

	baseDir := te.workDir
	if args.Path != "" {
		resolved, err := te.resolvePath(args.Path)
		if err != nil {
			return "", err
		}
		baseDir = resolved
	}

	// Use filepath.Glob for simple patterns, or walk for ** patterns.
	fullPattern := filepath.Join(baseDir, args.Pattern)

	var matches []string
	if strings.Contains(args.Pattern, "**") {
		// Walk the directory tree for double-star patterns.
		// Convert ** pattern to a walk-based search.
		matches = walkGlob(baseDir, args.Pattern)
	} else {
		var err error
		matches, err = filepath.Glob(fullPattern)
		if err != nil {
			return "", fmt.Errorf("glob %s: %w", args.Pattern, err)
		}
	}

	if len(matches) == 0 {
		return "[no matches]", nil
	}

	// Make paths relative to the working directory.
	var relMatches []string
	for _, m := range matches {
		rel, err := filepath.Rel(te.workDir, m)
		if err != nil {
			rel = m
		}
		relMatches = append(relMatches, rel)
	}

	if len(relMatches) > maxGlobResults {
		relMatches = relMatches[:maxGlobResults]
		return strings.Join(relMatches, "\n") + fmt.Sprintf("\n[... truncated at %d results]", maxGlobResults), nil
	}
	return strings.Join(relMatches, "\n"), nil
}

// walkGlob walks a directory tree and returns paths matching a double-star glob
// pattern. It uses filepath.Match on the relative path segments.
func walkGlob(baseDir, pattern string) []string {
	var matches []string
	// Split pattern on ** and use a simplified approach:
	// for "**/*.go", match any file ending in .go
	parts := strings.SplitN(pattern, "**", 2)
	suffix := ""
	if len(parts) > 1 {
		suffix = strings.TrimPrefix(parts[1], "/")
		suffix = strings.TrimPrefix(suffix, string(filepath.Separator))
	}

	_ = filepath.Walk(baseDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip errors
		}
		if info.IsDir() {
			return nil
		}
		if len(matches) >= maxGlobResults {
			return filepath.SkipAll
		}
		if suffix == "" {
			matches = append(matches, path)
			return nil
		}
		rel, relErr := filepath.Rel(baseDir, path)
		if relErr != nil {
			return nil
		}
		// Match the suffix against the filename or relative path.
		matched, _ := filepath.Match(suffix, filepath.Base(rel))
		if !matched {
			// Try matching against the relative path for patterns like "internal/*.go".
			matched, _ = filepath.Match(suffix, rel)
		}
		if matched {
			matches = append(matches, path)
		}
		return nil
	})
	return matches
}

// ---------------------------------------------------------------------------
// Core tool-call loop
// ---------------------------------------------------------------------------

// RunDirectToolAgent runs a tool-calling agent loop against the SGLang API.
// It sends the system prompt and user message, then iterates: if the model
// responds with tool calls, it executes them and feeds results back. The loop
// ends when the model returns finish_reason "stop" or max iterations are hit.
//
// The final text output is written to outputPath if provided.
func RunDirectToolAgent(cfg DirectToolAgentConfig, systemPrompt, userMessage string, tools []toolDefinition, outputPath string) (*DirectToolAgentResult, error) {
	start := time.Now()

	exec := &toolExecutor{
		workDir:     cfg.WorkDir,
		bashTimeout: cfg.BashTimeout,
	}

	messages := []toolChatMsg{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userMessage},
	}

	var totalTokensIn, totalTokensOut int

	maxIter := cfg.MaxIterations
	if maxIter <= 0 {
		maxIter = 20
	}

	for iteration := 0; iteration < maxIter; iteration++ {
		slog.Info("direct tool agent: calling API",
			"iteration", iteration,
			"messages", len(messages),
			"tokens_in_total", totalTokensIn,
			"tokens_out_total", totalTokensOut,
		)

		resp, err := callToolAPI(cfg, messages, tools)
		if err != nil {
			return &DirectToolAgentResult{
				TokensIn:   totalTokensIn,
				TokensOut:  totalTokensOut,
				Iterations: iteration,
				Duration:   time.Since(start),
			}, fmt.Errorf("API call failed at iteration %d: %w", iteration, err)
		}

		totalTokensIn += resp.Usage.PromptTokens
		totalTokensOut += resp.Usage.CompletionTokens

		if len(resp.Choices) == 0 {
			return &DirectToolAgentResult{
				TokensIn:   totalTokensIn,
				TokensOut:  totalTokensOut,
				Iterations: iteration + 1,
				Duration:   time.Since(start),
			}, fmt.Errorf("no choices in response at iteration %d", iteration)
		}

		choice := resp.Choices[0]

		// If the model is done, return the final content.
		if choice.FinishReason == "stop" || choice.FinishReason == "end_of_turn" {
			finalOutput := choice.Message.Content
			result := &DirectToolAgentResult{
				Output:     finalOutput,
				TokensIn:   totalTokensIn,
				TokensOut:  totalTokensOut,
				Iterations: iteration + 1,
				Duration:   time.Since(start),
			}

			// Write output file if requested.
			if outputPath != "" && finalOutput != "" {
				if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
					return result, fmt.Errorf("create output dir: %w", err)
				}
				if err := os.WriteFile(outputPath, []byte(finalOutput), 0o644); err != nil {
					return result, fmt.Errorf("write output file: %w", err)
				}
				result.OutputPath = outputPath
			}

			slog.Info("direct tool agent: completed",
				"iterations", iteration+1,
				"tokens_in", totalTokensIn,
				"tokens_out", totalTokensOut,
				"duration", time.Since(start).Round(time.Millisecond),
			)
			return result, nil
		}

		// If the model wants to call tools, execute them.
		if choice.FinishReason == "tool_calls" || len(choice.Message.ToolCalls) > 0 {
			// Append the assistant message with tool calls.
			messages = append(messages, choice.Message)

			for _, tc := range choice.Message.ToolCalls {
				slog.Info("direct tool agent: executing tool",
					"iteration", iteration,
					"tool", tc.Function.Name,
					"call_id", tc.ID,
				)

				result, toolErr := exec.execute(tc.Function.Name, tc.Function.Arguments)
				if toolErr != nil {
					result = "ERROR: " + toolErr.Error()
				}

				// Append the tool result message.
				messages = append(messages, toolChatMsg{
					Role:       "tool",
					Content:    result,
					ToolCallID: tc.ID,
					Name:       tc.Function.Name,
				})
			}
			continue
		}

		// Unexpected finish reason — treat any content as final output.
		slog.Warn("direct tool agent: unexpected finish_reason",
			"finish_reason", choice.FinishReason,
			"iteration", iteration,
		)
		return &DirectToolAgentResult{
			Output:     choice.Message.Content,
			TokensIn:   totalTokensIn,
			TokensOut:  totalTokensOut,
			Iterations: iteration + 1,
			Duration:   time.Since(start),
		}, nil
	}

	return &DirectToolAgentResult{
		TokensIn:   totalTokensIn,
		TokensOut:  totalTokensOut,
		Iterations: maxIter,
		Duration:   time.Since(start),
	}, fmt.Errorf("exceeded max iterations (%d)", maxIter)
}

// callToolAPI makes a single chat completions request with tool definitions.
func callToolAPI(cfg DirectToolAgentConfig, messages []toolChatMsg, tools []toolDefinition) (*toolChatResponse, error) {
	reqBody := toolChatRequest{
		Model:       cfg.Model,
		Messages:    messages,
		MaxTokens:   cfg.MaxTokens,
		Temperature: cfg.Temperature,
		Tools:       tools,
	}

	reqJSON, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequest(http.MethodPost, cfg.Endpoint, bytes.NewReader(reqJSON))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: cfg.Timeout}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("API call: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, truncateBody(body, 500))
	}

	var chatResp toolChatResponse
	if err := json.Unmarshal(body, &chatResp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	return &chatResp, nil
}
