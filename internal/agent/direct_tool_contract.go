package agent

import "encoding/json"

// TraceEvent is a single entry in the optional agent trace log. One is written
// per iteration capturing the assistant reply and every tool call/result pair.
type TraceEvent struct {
	Iteration    int         `json:"iteration"`
	FinishReason string      `json:"finish_reason"`
	TokensIn     int         `json:"tokens_in"`
	TokensOut    int         `json:"tokens_out"`
	Assistant    string      `json:"assistant_content,omitempty"`
	ToolCalls    []TraceCall `json:"tool_calls,omitempty"`
	ElapsedMs    int64       `json:"elapsed_ms"`
}

// TraceCall is one tool invocation and its result as seen by the loop.
type TraceCall struct {
	Name   string `json:"name"`
	Args   string `json:"args"`
	Result string `json:"result"`
	Error  string `json:"error,omitempty"`
}

var builtinTools = map[string]toolDefinition{
	"read": {Type: "function", Function: toolFunction{
		Name:        "read",
		Description: "Read file contents with line numbers. ALWAYS use this instead of cat/head/tail/sed -n. Returns formatted output with line numbers for precise editing. A default read of an authorized scoped file returns up to 800 lines so dependency artifacts usually arrive in one turn; other files default to 200. Supports offset and limit for larger files.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"File path to read"},"offset":{"type":"integer","description":"Line number to start from (0-based)"},"limit":{"type":"integer","description":"Max lines to return (default 800 for an authorized scoped file, otherwise 200)"}},"required":["path"]}`),
	}},
	"edit": {Type: "function", Function: toolFunction{
		Name:        "edit",
		Description: "Replace exact text in a file. Use for surgical changes — provide the exact existing text and its replacement. The old text must appear exactly once in the file. Safer than write for modifications to existing files.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"File path"},"old":{"type":"string","description":"Exact existing text to find (must be unique in file)"},"new":{"type":"string","description":"Replacement text"}},"required":["path","old","new"]}`),
	}},
	"write": {Type: "function", Function: toolFunction{
		Name:        "write",
		Description: "Write content to a file (creates or overwrites). ALWAYS use this instead of bash heredocs (cat <<EOF), echo >, or tee. Handles all special characters safely — no shell escaping issues with backticks, quotes, or dollar signs. Creates parent directories automatically.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"File path"},"content":{"type":"string","description":"Complete file content to write"}},"required":["path","content"]}`),
	}},
	"bash": {Type: "function", Function: toolFunction{
		Name:        "bash",
		Description: "Run a shell command for building, testing, and running programs (go test, go vet, make, etc.). Do NOT use bash for file operations — use the dedicated tools instead: read (not cat/head/tail), write (not cat <<EOF or echo >), edit (not sed -i), grep (not grep/rg), glob (not find/ls).",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"cmd":{"type":"string","description":"Shell command to execute. Must not be used for file reading (use read), file writing (use write), file editing (use edit), or searching (use grep/glob)."}},"required":["cmd"]}`),
	}},
	"grep": {Type: "function", Function: toolFunction{
		Name:        "grep",
		Description: "Search file contents with regex. ALWAYS use this instead of bash grep/rg. Returns matched lines with file paths and line numbers. Supports glob filtering to narrow search scope.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string","description":"Regex pattern to search for"},"path":{"type":"string","description":"Directory or file to search (default: working directory)"},"glob":{"type":"string","description":"File glob filter (e.g. *.go)"}},"required":["pattern"]}`),
	}},
	"glob": {Type: "function", Function: toolFunction{
		Name:        "glob",
		Description: "Find files by glob pattern. ALWAYS use this instead of find or ls for locating files. Returns matching file paths.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string","description":"Glob pattern (e.g. **/*.go)"},"path":{"type":"string","description":"Base directory (default: working directory)"}},"required":["pattern"]}`),
	}},
}

var roleTools = map[string][]string{
	"coder":    {"read", "edit", "write", "bash", "grep", "glob"},
	"fixer":    {"read", "edit", "write", "bash", "grep", "glob"},
	"reviewer": {"read", "bash", "grep", "glob"},
	"prep":     {"read", "grep", "glob"},
}

// ToolsForRole returns the tool definitions permitted for the given agent role.
func ToolsForRole(role string) []toolDefinition {
	names, ok := roleTools[role]
	if !ok {
		names = []string{"read", "grep", "glob"}
	}
	return toolsByName(names)
}

// ToolsForRoleScope narrows bounded coder/fixer runs to named files.
func ToolsForRoleScope(role string, scopedFiles []string) []toolDefinition {
	if (role == "coder" || role == "fixer") && len(scopedFiles) > 0 {
		return toolsByName([]string{"read", "edit", "write", "bash"})
	}
	return ToolsForRole(role)
}

// ToolsForBenchmark removes Bash when structured-only isolation is required.
func ToolsForBenchmark(role string, scopedFiles []string, allowBash bool) []toolDefinition {
	tools := ToolsForRoleScope(role, scopedFiles)
	if allowBash {
		return tools
	}
	filtered := make([]toolDefinition, 0, len(tools))
	for _, tool := range tools {
		if tool.Function.Name != "bash" {
			filtered = append(filtered, tool)
		}
	}
	return filtered
}

func toolsByName(names []string) []toolDefinition {
	tools := make([]toolDefinition, 0, len(names))
	for _, name := range names {
		if tool, exists := builtinTools[name]; exists {
			tools = append(tools, tool)
		}
	}
	return tools
}
