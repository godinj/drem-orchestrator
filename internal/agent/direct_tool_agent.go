// direct_tool_agent.go implements a synchronous tool-calling agent that calls
// SGLang's OpenAI-compatible API directly. It extends the direct_classifier.go
// pattern to support agentic tool loops for coder, reviewer, and fixer roles.
//
// SGLang must be running with --tool-call-parser qwen25 to handle bidirectional
// conversion between OpenAI tool format and Qwen3-Coder's native tool call tokens.
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

// TraceEvent is a single entry in the optional agent trace log. One is written
// per iteration capturing the assistant reply and every tool call/result pair.
type TraceEvent struct {
	Iteration    int         `json:"iteration"`
	FinishReason string      `json:"finish_reason"`
	TokensIn     int         `json:"tokens_in"`
	TokensOut    int         `json:"tokens_out"`
	Assistant    string      `json:"assistant_content,omitempty"`
	ToolCalls    []TraceCall `json:"tool_calls,omitempty"`
}

// TraceCall is one tool invocation and its result as seen by the loop.
type TraceCall struct {
	Name   string `json:"name"`
	Args   string `json:"args"`
	Result string `json:"result"`
	Error  string `json:"error,omitempty"`
}

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
	TraceWriter   io.Writer     // Optional: JSON-lines trace, one TraceEvent per iteration
	// ChatTemplateKwargs is forwarded to SGLang as `chat_template_kwargs` in the
	// request body. Use for model-specific template flags (e.g. Gemma-4's
	// `enable_thinking: true`). Nil/empty ⇒ field omitted.
	ChatTemplateKwargs map[string]any
}

// DefaultDirectToolAgentConfig returns a config targeting the local SGLang
// server with sensible defaults.
func DefaultDirectToolAgentConfig() DirectToolAgentConfig {
	return DirectToolAgentConfig{
		Endpoint:      "http://localhost:8081/v1/chat/completions",
		Model:         "qwen3-coder-30b",
		MaxTokens:     8192,
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
	Model              string           `json:"model"`
	Messages           []toolChatMsg    `json:"messages"`
	MaxTokens          int              `json:"max_tokens"`
	Temperature        float64          `json:"temperature"`
	Tools              []toolDefinition `json:"tools,omitempty"`
	ChatTemplateKwargs map[string]any   `json:"chat_template_kwargs,omitempty"`
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
//
// Arguments is stored as a string to match the OpenAI tool-call protocol
// (servers return it that way). For outbound serialization, MarshalJSON
// re-emits Arguments as a JSON object when it parses as one. This is
// required for Gemma-family chat templates: the template branches on
// `function.arguments is mapping` vs `is string`. The mapping branch
// renders native `{key:<|"|>val<|"|>}` syntax (what Gemma was trained on);
// the string branch does a literal insert producing malformed
// `{{"key":"val"}}` double-brace output. Feeding a Gemma model its own
// prior tool calls in the string form causes it to fail to recognize
// them as tool calls in conversation history, which manifests as
// edit-loops and no-progress patterns. Templates that expect a string
// (e.g. OpenAI's own schema) still receive valid JSON — either a string
// or an object containing the same data.
type toolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// UnmarshalJSON accepts `arguments` as either a JSON string (OpenAI
// spec) or a JSON object (some servers — and our own test fixtures —
// emit it that way). Both are normalized into the string field so the
// rest of the agent can treat Arguments uniformly.
func (tcf *toolCallFunction) UnmarshalJSON(data []byte) error {
	var raw struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	tcf.Name = raw.Name
	if len(raw.Arguments) == 0 {
		tcf.Arguments = ""
		return nil
	}
	// If arguments is a JSON string, unquote it into the field.
	var asString string
	if err := json.Unmarshal(raw.Arguments, &asString); err == nil {
		tcf.Arguments = asString
		return nil
	}
	// Otherwise treat as object/array/other — store the raw JSON text.
	tcf.Arguments = string(raw.Arguments)
	return nil
}

// MarshalJSON emits Arguments as a JSON object when it parses as one,
// and as a string otherwise. See type-level doc for rationale.
func (tcf toolCallFunction) MarshalJSON() ([]byte, error) {
	if tcf.Arguments != "" {
		var argsObj map[string]any
		if err := json.Unmarshal([]byte(tcf.Arguments), &argsObj); err == nil {
			return json.Marshal(struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}{
				Name:      tcf.Name,
				Arguments: argsObj,
			})
		}
	}
	// Fallback: arguments did not parse as a JSON object. Emit as string
	// so the request remains schema-valid on the server side.
	return json.Marshal(struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}{
		Name:      tcf.Name,
		Arguments: tcf.Arguments,
	})
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
// Descriptions are deliberately detailed to steer models (especially smaller
// ones like Gemma-4 26B) toward the structured tools instead of routing
// everything through bash. Bash is restricted to build/test/run commands.
var builtinTools = map[string]toolDefinition{
	"read": {
		Type: "function",
		Function: toolFunction{
			Name:        "read",
			Description: "Read file contents with line numbers. ALWAYS use this instead of cat/head/tail/sed -n. Returns formatted output with line numbers for precise editing. Supports offset and limit for large files.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"File path to read"},"offset":{"type":"integer","description":"Line number to start from (0-based)"},"limit":{"type":"integer","description":"Max lines to return (default 200)"}},"required":["path"]}`),
		},
	},
	"edit": {
		Type: "function",
		Function: toolFunction{
			Name:        "edit",
			Description: "Replace exact text in a file. Use for surgical changes — provide the exact existing text and its replacement. The old text must appear exactly once in the file. Safer than write for modifications to existing files.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"File path"},"old":{"type":"string","description":"Exact existing text to find (must be unique in file)"},"new":{"type":"string","description":"Replacement text"}},"required":["path","old","new"]}`),
		},
	},
	"write": {
		Type: "function",
		Function: toolFunction{
			Name:        "write",
			Description: "Write content to a file (creates or overwrites). ALWAYS use this instead of bash heredocs (cat <<EOF), echo >, or tee. Handles all special characters safely — no shell escaping issues with backticks, quotes, or dollar signs. Creates parent directories automatically.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"File path"},"content":{"type":"string","description":"Complete file content to write"}},"required":["path","content"]}`),
		},
	},
	"bash": {
		Type: "function",
		Function: toolFunction{
			Name:        "bash",
			Description: "Run a shell command for building, testing, and running programs (go test, go vet, make, etc.). Do NOT use bash for file operations — use the dedicated tools instead: read (not cat/head/tail), write (not cat <<EOF or echo >), edit (not sed -i), grep (not grep/rg), glob (not find/ls).",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"cmd":{"type":"string","description":"Shell command to execute. Must not be used for file reading (use read), file writing (use write), file editing (use edit), or searching (use grep/glob)."}},"required":["cmd"]}`),
		},
	},
	"grep": {
		Type: "function",
		Function: toolFunction{
			Name:        "grep",
			Description: "Search file contents with regex. ALWAYS use this instead of bash grep/rg. Returns matched lines with file paths and line numbers. Supports glob filtering to narrow search scope.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string","description":"Regex pattern to search for"},"path":{"type":"string","description":"Directory or file to search (default: working directory)"},"glob":{"type":"string","description":"File glob filter (e.g. *.go)"}},"required":["pattern"]}`),
		},
	},
	"glob": {
		Type: "function",
		Function: toolFunction{
			Name:        "glob",
			Description: "Find files by glob pattern. ALWAYS use this instead of find or ls for locating files. Returns matching file paths.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string","description":"Glob pattern (e.g. **/*.go)"},"path":{"type":"string","description":"Base directory (default: working directory)"}},"required":["pattern"]}`),
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
	maxReadLines        = 200
	maxBashOutput       = 2000 // characters
	maxGrepMatches      = 50
	maxGlobResults      = 100
	maxToolCallsPerTurn = 8 // cap parallel tool calls; prevents degenerate generation explosions
	maxFileReads        = 3 // warn after N reads of the same file path
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

	// Format with line numbers. Use a non-whitespace separator ('|') between the
	// line number and content so models do not confuse the separator with the
	// file's own indentation when constructing edit strings. (Diagnosis:
	// a tab-separator triggers a deterministic edit-retry loop where the model
	// copies "<N>\t\t<content>" into edit.old and strips only "<N>", leaving an
	// extra phantom tab that never matches the file.)
	var buf strings.Builder
	for i, line := range lines {
		fmt.Fprintf(&buf, "%4d|%s\n", offset+i+1, line)
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

// bashFileOpPatterns detects bash commands that should use structured tools.
// Returns a non-empty redirect message if the command matches a file-op pattern.
func bashFileOpRedirect(cmd string) string {
	trimmed := strings.TrimSpace(cmd)
	lower := strings.ToLower(trimmed)

	// Detect heredoc writes: cat <<EOF > file, cat <<'EOF' > file
	if strings.Contains(trimmed, "<<") && strings.Contains(trimmed, ">") {
		return "[HARNESS] bash heredocs are not supported for writing files — they break on backticks, quotes, and special characters. Use the write tool instead:\n" +
			"  write({\"path\": \"<file>\", \"content\": \"<content>\"})\n" +
			"The write tool handles all escaping safely. Do NOT retry this command with bash."
	}
	// Detect echo/printf redirects to files: echo "..." > file, printf > file
	// Allow echo ... >&2 (stderr redirect) and echo with no redirect.
	if (strings.HasPrefix(lower, "echo ") || strings.HasPrefix(lower, "printf ")) &&
		strings.Contains(trimmed, "> ") && !strings.Contains(trimmed, ">&") {
		return "[HARNESS] Do not use echo/printf to write files. Use the write tool instead:\n" +
			"  write({\"path\": \"<file>\", \"content\": \"<content>\"})"
	}
	// Detect tee writes: ... | tee file
	if strings.Contains(lower, "| tee ") {
		return "[HARNESS] Do not use tee to write files. Use the write tool instead."
	}
	// Detect sed -i (in-place edit)
	if strings.Contains(lower, "sed -i") || strings.Contains(lower, "sed -e") {
		return "[HARNESS] Do not use sed for file editing. Use the edit tool instead:\n" +
			"  edit({\"path\": \"<file>\", \"old\": \"<exact text>\", \"new\": \"<replacement>\"})"
	}
	// Detect cat/head/tail for reading (but allow cat in pipelines for other purposes)
	if (strings.HasPrefix(lower, "cat ") || strings.HasPrefix(lower, "head ") || strings.HasPrefix(lower, "tail ")) &&
		!strings.Contains(trimmed, "|") && !strings.Contains(trimmed, ">") {
		return "[HARNESS] Do not use cat/head/tail to read files. Use the read tool instead:\n" +
			"  read({\"path\": \"<file>\", \"offset\": N, \"limit\": M})\n" +
			"The read tool returns formatted output with line numbers for precise editing."
	}

	return "" // Not a file-op pattern, allow execution
}

func (te *toolExecutor) execBash(argsJSON string) (string, error) {
	var args struct {
		Cmd string `json:"cmd"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("parse bash args: %w", err)
	}

	// Intercept file-op patterns that should use structured tools.
	if redirect := bashFileOpRedirect(args.Cmd); redirect != "" {
		slog.Warn("direct tool agent: bash file-op intercepted",
			"cmd_prefix", truncateForStub(args.Cmd, 80),
		)
		return redirect, nil
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
	if output == "" {
		return "(exit 0, no output)", nil
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

	// Loop detector state: remember the signature of the last tool call
	// (name+args) AND its result so we can recognise "no forward progress"
	// patterns and nudge the model toward a different approach. We consider
	// a call "stuck" when it repeats with identical args AND produces
	// identical output as the immediately preceding call — this catches both
	// failing-edit loops (result is the same error each time) and
	// successful-but-useless grep/ls loops (result is the same empty or
	// same-match text each time).
	var prevToolKey, prevToolOut string
	var consecutiveRepeats int

	// File re-read tracker: counts how many times each resolved file path
	// has been read via the read tool. After maxFileReads, the tool result
	// includes a warning nudging the model to stop re-reading and act.
	fileReadCounts := map[string]int{}

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

		traceEvent := TraceEvent{
			Iteration:    iteration,
			FinishReason: choice.FinishReason,
			TokensIn:     resp.Usage.PromptTokens,
			TokensOut:    resp.Usage.CompletionTokens,
			Assistant:    choice.Message.Content,
		}

		// If the model is done, return the final content.
		if choice.FinishReason == "stop" || choice.FinishReason == "end_of_turn" {
			writeTrace(cfg.TraceWriter, traceEvent)
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
			// Per-turn cap: if the model emits more than maxToolCallsPerTurn
			// calls, only keep the first N and stub the rest. This prevents
			// degenerate generation (observed: model starts paginating a file
			// with 28 valid reads, then generation breaks down into 159
			// malformed calls, consuming 58k context tokens on garbage).
			callsToProcess := choice.Message.ToolCalls
			var cappedCalls []toolCall
			if len(callsToProcess) > maxToolCallsPerTurn {
				slog.Warn("direct tool agent: capping tool calls per turn",
					"iteration", iteration,
					"total", len(callsToProcess),
					"cap", maxToolCallsPerTurn,
				)
				cappedCalls = callsToProcess[maxToolCallsPerTurn:]
				callsToProcess = callsToProcess[:maxToolCallsPerTurn]
				// Rewrite the assistant message to only contain the kept calls,
				// so capped calls don't pollute conversation history.
				cappedMsg := choice.Message
				cappedMsg.ToolCalls = callsToProcess
				messages = append(messages, cappedMsg)
			} else {
				// Append the assistant message with tool calls.
				messages = append(messages, choice.Message)
			}

			// Stub the capped calls with a redirect message. Each tool_call_id
			// still gets a response so the OpenAI protocol is satisfied.
			for _, tc := range cappedCalls {
				stub := fmt.Sprintf("[HARNESS: Too many tool calls in one turn (%d). Only the first %d were executed. "+
					"Break your work across multiple turns — call a few tools, review the results, then continue.]",
					len(choice.Message.ToolCalls), maxToolCallsPerTurn)
				messages = append(messages, toolChatMsg{
					Role:       "tool",
					Content:    stub,
					ToolCallID: tc.ID,
					Name:       tc.Function.Name,
				})
			}

			// Intra-iteration dedup: if the model emits multiple tool calls
			// with identical name+args in a single turn, execute only the first
			// and stub the rest. Observed in bench: a single turn contained 60
			// identical `ls -R` calls. Each stub still preserves tool_call_id
			// so the OpenAI protocol (one result per call) is satisfied.
			seenThisIter := map[string]string{}

			for _, tc := range callsToProcess {
				slog.Info("direct tool agent: executing tool",
					"iteration", iteration,
					"tool", tc.Function.Name,
					"call_id", tc.ID,
				)

				curKey := tc.Function.Name + "\x00" + tc.Function.Arguments

				// If an identical call already ran this turn, stub the rest.
				if firstResult, dup := seenThisIter[curKey]; dup {
					slog.Warn("direct tool agent: duplicate tool call in same turn (stubbed)",
						"iteration", iteration,
						"tool", tc.Function.Name,
					)
					stub := "[HARNESS: duplicate tool call in the same turn — executed once above, re-using result.\n" +
						"First result was:\n" + truncateForStub(firstResult, 400) +
						"\n\nDo not emit multiple identical tool calls in one turn. If you need to re-check, do it on the next turn after considering the first result.]"
					messages = append(messages, toolChatMsg{
						Role:       "tool",
						Content:    stub,
						ToolCallID: tc.ID,
						Name:       tc.Function.Name,
					})
					traceEvent.ToolCalls = append(traceEvent.ToolCalls, TraceCall{
						Name:   tc.Function.Name,
						Args:   tc.Function.Arguments,
						Result: stub,
					})
					continue
				}

				result, toolErr := exec.execute(tc.Function.Name, tc.Function.Arguments)
				traceCall := TraceCall{
					Name: tc.Function.Name,
					Args: tc.Function.Arguments,
				}
				if toolErr != nil {
					traceCall.Error = toolErr.Error()
					result = "ERROR: " + toolErr.Error()
				}

				// File re-read tracker: warn when the same file is read
				// too many times, nudging the model to use what it has.
				if tc.Function.Name == "read" {
					var readArgs struct {
						Path string `json:"path"`
					}
					if json.Unmarshal([]byte(tc.Function.Arguments), &readArgs) == nil && readArgs.Path != "" {
						resolved, _ := filepath.Abs(filepath.Join(cfg.WorkDir, readArgs.Path))
						fileReadCounts[resolved]++
						if fileReadCounts[resolved] > maxFileReads {
							result += fmt.Sprintf("\n\n[HARNESS NOTE: You have read this file %d times. "+
								"Its contents have not changed. Stop re-reading and take action — "+
								"call write or edit to make your changes.]", fileReadCounts[resolved])
						}
					}
				}

				// Loop detector: if this call has identical name+args AND
				// produces identical output to the immediately preceding call,
				// the model is making no forward progress. Inject a nudge.
				// We store the pre-note result in prevToolOut so the comparison
				// fires every consecutive repeat, not just every other one.
				preNoteResult := result
				if curKey == prevToolKey && result == prevToolOut {
					consecutiveRepeats++
					slog.Warn("direct tool agent: loop detector triggered",
						"iteration", iteration,
						"tool", tc.Function.Name,
						"repeats", consecutiveRepeats,
					)
					if consecutiveRepeats >= 3 {
						result += "\n\n[HARNESS NOTE: You have repeated this EXACT call " +
							fmt.Sprintf("%d", consecutiveRepeats+1) + " times with identical results. " +
							"The task may already be complete. If so, respond with DONE. " +
							"Otherwise you MUST try a completely different approach NOW.]"
					} else {
						result += "\n\n[HARNESS NOTE: This is the 2nd consecutive identical call producing identical output. You are making no forward progress. Try a different approach:\n" +
							" - if searching, broaden or narrow the query, or try a different tool (e.g. glob vs grep, or read a known file directly);\n" +
							" - if editing and the tool reports an error, re-read the file to confirm its current contents and check whitespace/indentation in your 'old' string, or use 'write' for a full-file replace;\n" +
							" - if the information you need is not findable, proceed with a reasonable assumption, document it in a code comment, and continue.]"
					}
				} else {
					consecutiveRepeats = 0
				}
				prevToolKey = curKey
				prevToolOut = preNoteResult
				seenThisIter[curKey] = result
				traceCall.Result = result
				traceEvent.ToolCalls = append(traceEvent.ToolCalls, traceCall)

				// Append the tool result message.
				messages = append(messages, toolChatMsg{
					Role:       "tool",
					Content:    result,
					ToolCallID: tc.ID,
					Name:       tc.Function.Name,
				})
			}
			writeTrace(cfg.TraceWriter, traceEvent)
			continue
		}

		// Length-truncated response: the model hit max_tokens before completing
		// its turn. SGLang in this case may return an empty message (no content,
		// no tool_calls) because a partial tool-call cannot be returned as
		// structured JSON. Do NOT silently treat this as a successful stop —
		// surface as an error so callers (drembench, orchestrator) can retry or
		// classify distinctly.
		if choice.FinishReason == "length" {
			slog.Warn("direct tool agent: response truncated at max_tokens",
				"iteration", iteration,
				"max_tokens", cfg.MaxTokens,
				"tokens_out", resp.Usage.CompletionTokens,
				"had_content", choice.Message.Content != "",
				"had_tool_calls", len(choice.Message.ToolCalls) > 0,
			)
			writeTrace(cfg.TraceWriter, traceEvent)
			return &DirectToolAgentResult{
					Output:     choice.Message.Content,
					TokensIn:   totalTokensIn,
					TokensOut:  totalTokensOut,
					Iterations: iteration + 1,
					Duration:   time.Since(start),
				}, fmt.Errorf("response truncated at max_tokens=%d (iteration %d); increase MaxTokens or reduce tool-call payload size",
					cfg.MaxTokens, iteration)
		}

		// Unexpected finish reason — treat any content as final output.
		slog.Warn("direct tool agent: unexpected finish_reason",
			"finish_reason", choice.FinishReason,
			"iteration", iteration,
		)
		writeTrace(cfg.TraceWriter, traceEvent)
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

// truncateForStub clamps a result string to n characters so dedup stubs do
// not blow the context when the original result was large. Used when a
// duplicate tool call is elided — the second+ result just points back at the
// first and does not need the full body repeated.
func truncateForStub(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n[...truncated]"
}

// writeTrace emits one JSON-line TraceEvent if the trace writer is configured.
// Failures are logged but never surface — tracing is strictly opt-in diagnostic.
func writeTrace(w io.Writer, ev TraceEvent) {
	if w == nil {
		return
	}
	data, err := json.Marshal(ev)
	if err != nil {
		slog.Warn("direct tool agent: trace marshal failed", "err", err)
		return
	}
	if _, err := w.Write(append(data, '\n')); err != nil {
		slog.Warn("direct tool agent: trace write failed", "err", err)
	}
}

// callToolAPI makes a single chat completions request with tool definitions.
func callToolAPI(cfg DirectToolAgentConfig, messages []toolChatMsg, tools []toolDefinition) (*toolChatResponse, error) {
	reqBody := toolChatRequest{
		Model:              cfg.Model,
		Messages:           messages,
		MaxTokens:          cfg.MaxTokens,
		Temperature:        cfg.Temperature,
		Tools:              tools,
		ChatTemplateKwargs: cfg.ChatTemplateKwargs,
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
