package agent

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// ToolsForRole tests
// ---------------------------------------------------------------------------

func TestToolsForRole_Coder(t *testing.T) {
	tools := ToolsForRole("coder")
	names := toolNames(tools)
	assert.ElementsMatch(t, []string{"read", "edit", "write", "bash", "grep", "glob"}, names)
}

func TestToolsForRole_Reviewer(t *testing.T) {
	tools := ToolsForRole("reviewer")
	names := toolNames(tools)
	assert.ElementsMatch(t, []string{"read", "bash", "grep", "glob"}, names)
	// Reviewer must NOT have edit or write.
	assert.NotContains(t, names, "edit")
	assert.NotContains(t, names, "write")
}

func TestToolsForRole_Fixer(t *testing.T) {
	tools := ToolsForRole("fixer")
	names := toolNames(tools)
	assert.ElementsMatch(t, []string{"read", "edit", "write", "bash", "grep", "glob"}, names)
}

func TestToolsForRole_Unknown(t *testing.T) {
	tools := ToolsForRole("unknown-role")
	names := toolNames(tools)
	// Unknown roles get read-only tools.
	assert.ElementsMatch(t, []string{"read", "grep", "glob"}, names)
}

func TestToolsForRole_ToolDefinitionsAreValid(t *testing.T) {
	for _, role := range []string{"coder", "reviewer", "fixer"} {
		tools := ToolsForRole(role)
		for _, td := range tools {
			assert.Equal(t, "function", td.Type, "role=%s tool=%s", role, td.Function.Name)
			assert.NotEmpty(t, td.Function.Name, "role=%s", role)
			assert.NotEmpty(t, td.Function.Description, "role=%s tool=%s", role, td.Function.Name)
			// Parameters should be valid JSON.
			var params map[string]any
			err := json.Unmarshal(td.Function.Parameters, &params)
			assert.NoError(t, err, "role=%s tool=%s params should be valid JSON", role, td.Function.Name)
		}
	}
}

// ---------------------------------------------------------------------------
// Tool execution tests
// ---------------------------------------------------------------------------

func TestExecRead(t *testing.T) {
	dir := t.TempDir()
	te := &toolExecutor{workDir: dir}

	// Create a test file.
	content := "line 1\nline 2\nline 3\nline 4\nline 5\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "test.txt"), []byte(content), 0o644))

	t.Run("read entire file", func(t *testing.T) {
		result, err := te.execRead(`{"path":"test.txt"}`)
		require.NoError(t, err)
		assert.Contains(t, result, "   1|line 1")
		assert.Contains(t, result, "   5|line 5")
	})

	t.Run("read with offset", func(t *testing.T) {
		result, err := te.execRead(`{"path":"test.txt","offset":2}`)
		require.NoError(t, err)
		assert.Contains(t, result, "   3|line 3")
		assert.NotContains(t, result, "   1|line 1")
	})

	t.Run("read with limit", func(t *testing.T) {
		result, err := te.execRead(`{"path":"test.txt","limit":2}`)
		require.NoError(t, err)
		assert.Contains(t, result, "   1|line 1")
		assert.Contains(t, result, "   2|line 2")
		assert.NotContains(t, result, "   3|line 3")
	})

	t.Run("read nonexistent file", func(t *testing.T) {
		_, err := te.execRead(`{"path":"nope.txt"}`)
		assert.Error(t, err)
	})
}

func TestExecEdit(t *testing.T) {
	dir := t.TempDir()
	te := &toolExecutor{workDir: dir}

	original := "func hello() {\n\tfmt.Println(\"hello\")\n}\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "test.go"), []byte(original), 0o644))

	t.Run("successful edit", func(t *testing.T) {
		result, err := te.execEdit(`{"path":"test.go","old":"\"hello\"","new":"\"world\""}`)
		require.NoError(t, err)
		assert.Contains(t, result, "edited")

		data, _ := os.ReadFile(filepath.Join(dir, "test.go"))
		assert.Contains(t, string(data), "\"world\"")
		assert.NotContains(t, string(data), "\"hello\"")
	})

	t.Run("old string not found", func(t *testing.T) {
		_, err := te.execEdit(`{"path":"test.go","old":"nonexistent","new":"replacement"}`)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
	})

	t.Run("ambiguous old string", func(t *testing.T) {
		content := "aaa\naaa\n"
		require.NoError(t, os.WriteFile(filepath.Join(dir, "dup.txt"), []byte(content), 0o644))
		_, err := te.execEdit(`{"path":"dup.txt","old":"aaa","new":"bbb"}`)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "2 times")
	})
}

func TestExecWrite(t *testing.T) {
	dir := t.TempDir()
	te := &toolExecutor{workDir: dir}

	t.Run("write new file", func(t *testing.T) {
		result, err := te.execWrite(`{"path":"new.txt","content":"hello world"}`)
		require.NoError(t, err)
		assert.Contains(t, result, "wrote")

		data, _ := os.ReadFile(filepath.Join(dir, "new.txt"))
		assert.Equal(t, "hello world", string(data))
	})

	t.Run("write creates subdirectories", func(t *testing.T) {
		result, err := te.execWrite(`{"path":"sub/dir/file.txt","content":"nested"}`)
		require.NoError(t, err)
		assert.Contains(t, result, "wrote")

		data, _ := os.ReadFile(filepath.Join(dir, "sub", "dir", "file.txt"))
		assert.Equal(t, "nested", string(data))
	})
}

func TestExecBash(t *testing.T) {
	dir := t.TempDir()
	te := &toolExecutor{workDir: dir, bashTimeout: 5 * 1e9} // 5s

	t.Run("simple command", func(t *testing.T) {
		result, err := te.execBash(`{"cmd":"echo hello"}`)
		require.NoError(t, err)
		assert.Contains(t, result, "hello")
	})

	t.Run("command with stderr", func(t *testing.T) {
		result, err := te.execBash(`{"cmd":"echo out && echo err >&2"}`)
		require.NoError(t, err)
		assert.Contains(t, result, "out")
		assert.Contains(t, result, "STDERR")
		assert.Contains(t, result, "err")
	})

	t.Run("failing command", func(t *testing.T) {
		result, err := te.execBash(`{"cmd":"exit 1"}`)
		require.NoError(t, err) // tool errors are returned as content, not Go errors
		assert.Contains(t, result, "exit error")
	})

	t.Run("working directory is set", func(t *testing.T) {
		result, err := te.execBash(`{"cmd":"pwd"}`)
		require.NoError(t, err)
		assert.Contains(t, result, dir)
	})
}

func TestExecGlob(t *testing.T) {
	dir := t.TempDir()
	te := &toolExecutor{workDir: dir}

	// Create some files.
	for _, name := range []string{"a.go", "b.go", "c.txt"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644))
	}

	t.Run("match go files", func(t *testing.T) {
		result, err := te.execGlob(`{"pattern":"*.go"}`)
		require.NoError(t, err)
		assert.Contains(t, result, "a.go")
		assert.Contains(t, result, "b.go")
		assert.NotContains(t, result, "c.txt")
	})

	t.Run("no matches", func(t *testing.T) {
		result, err := te.execGlob(`{"pattern":"*.rs"}`)
		require.NoError(t, err)
		assert.Contains(t, result, "no matches")
	})
}

func TestPathSecurity(t *testing.T) {
	dir := t.TempDir()
	te := &toolExecutor{workDir: dir}

	t.Run("path traversal blocked", func(t *testing.T) {
		_, err := te.execRead(`{"path":"../../etc/passwd"}`)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "outside working directory")
	})

	t.Run("absolute path outside workdir blocked", func(t *testing.T) {
		_, err := te.execRead(`{"path":"/etc/passwd"}`)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "outside working directory")
	})
}

// ---------------------------------------------------------------------------
// Tool-call loop / message construction tests (mock HTTP endpoint)
// ---------------------------------------------------------------------------

func TestRunDirectToolAgent_StopImmediately(t *testing.T) {
	// Mock server that returns a stop response immediately.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := toolChatResponse{
			Choices: []toolChatChoice{
				{
					Message:      toolChatMsg{Role: "assistant", Content: "Done with the task."},
					FinishReason: "stop",
				},
			},
			Usage: struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
				TotalTokens      int `json:"total_tokens"`
			}{
				PromptTokens:     100,
				CompletionTokens: 20,
				TotalTokens:      120,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := DefaultDirectToolAgentConfig()
	cfg.Endpoint = server.URL
	cfg.WorkDir = t.TempDir()

	result, err := RunDirectToolAgent(cfg, "You are a coder.", "Fix the bug.", nil, "")
	require.NoError(t, err)
	assert.Equal(t, "Done with the task.", result.Output)
	assert.Equal(t, 1, result.Iterations)
	assert.Equal(t, 100, result.TokensIn)
	assert.Equal(t, 20, result.TokensOut)
}

func TestRunDirectToolAgent_ToolCallLoop(t *testing.T) {
	dir := t.TempDir()
	// Create a file that the agent will "read".
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644))

	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		var resp toolChatResponse

		switch callCount {
		case 1:
			// First response: model requests to read a file.
			resp = toolChatResponse{
				Choices: []toolChatChoice{
					{
						Message: toolChatMsg{
							Role: "assistant",
							ToolCalls: []toolCall{
								{
									ID:   "call_1",
									Type: "function",
									Function: toolCallFunction{
										Name:      "read",
										Arguments: `{"path":"main.go"}`,
									},
								},
							},
						},
						FinishReason: "tool_calls",
					},
				},
				Usage: struct {
					PromptTokens     int `json:"prompt_tokens"`
					CompletionTokens int `json:"completion_tokens"`
					TotalTokens      int `json:"total_tokens"`
				}{PromptTokens: 100, CompletionTokens: 30, TotalTokens: 130},
			}
		case 2:
			// Verify the request includes tool result from previous iteration.
			var reqBody toolChatRequest
			json.NewDecoder(r.Body).Decode(&reqBody)

			// Should have: system, user, assistant (with tool_calls), tool result.
			assert.GreaterOrEqual(t, len(reqBody.Messages), 4,
				"expected at least 4 messages (system, user, assistant, tool)")

			// Second response: model is done.
			resp = toolChatResponse{
				Choices: []toolChatChoice{
					{
						Message:      toolChatMsg{Role: "assistant", Content: "The file contains package main."},
						FinishReason: "stop",
					},
				},
				Usage: struct {
					PromptTokens     int `json:"prompt_tokens"`
					CompletionTokens int `json:"completion_tokens"`
					TotalTokens      int `json:"total_tokens"`
				}{PromptTokens: 200, CompletionTokens: 15, TotalTokens: 215},
			}
		default:
			t.Fatalf("unexpected call count: %d", callCount)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := DefaultDirectToolAgentConfig()
	cfg.Endpoint = server.URL
	cfg.WorkDir = dir

	tools := ToolsForRole("coder")
	result, err := RunDirectToolAgent(cfg, "You are a coder.", "Read main.go", tools, "")
	require.NoError(t, err)
	assert.Equal(t, "The file contains package main.", result.Output)
	assert.Equal(t, 2, result.Iterations)
	assert.Equal(t, 300, result.TokensIn) // 100 + 200
	assert.Equal(t, 45, result.TokensOut) // 30 + 15
	assert.Equal(t, 2, callCount)
}

func TestRunDirectToolAgent_MaxIterations(t *testing.T) {
	// Mock server that always requests tool calls (never stops).
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := toolChatResponse{
			Choices: []toolChatChoice{
				{
					Message: toolChatMsg{
						Role: "assistant",
						ToolCalls: []toolCall{
							{
								ID:   "call_loop",
								Type: "function",
								Function: toolCallFunction{
									Name:      "bash",
									Arguments: `{"cmd":"echo loop"}`,
								},
							},
						},
					},
					FinishReason: "tool_calls",
				},
			},
			Usage: struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
				TotalTokens      int `json:"total_tokens"`
			}{PromptTokens: 50, CompletionTokens: 10, TotalTokens: 60},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := DefaultDirectToolAgentConfig()
	cfg.Endpoint = server.URL
	cfg.WorkDir = t.TempDir()
	cfg.MaxIterations = 3

	_, err := RunDirectToolAgent(cfg, "You are a coder.", "Do something.", ToolsForRole("coder"), "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeded max iterations (3)")
}

func TestRunDirectToolAgent_WritesOutputFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := toolChatResponse{
			Choices: []toolChatChoice{
				{
					Message:      toolChatMsg{Role: "assistant", Content: `{"result":"success"}`},
					FinishReason: "stop",
				},
			},
			Usage: struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
				TotalTokens      int `json:"total_tokens"`
			}{PromptTokens: 80, CompletionTokens: 10, TotalTokens: 90},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	dir := t.TempDir()
	outputPath := filepath.Join(dir, "output.json")

	cfg := DefaultDirectToolAgentConfig()
	cfg.Endpoint = server.URL
	cfg.WorkDir = dir

	result, err := RunDirectToolAgent(cfg, "system", "user", nil, outputPath)
	require.NoError(t, err)
	assert.Equal(t, outputPath, result.OutputPath)

	data, err := os.ReadFile(outputPath)
	require.NoError(t, err)
	assert.Equal(t, `{"result":"success"}`, string(data))
}

func TestRunDirectToolAgent_MultipleToolCallsInOneResponse(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("file a"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.txt"), []byte("file b"), 0o644))

	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		var resp toolChatResponse

		switch callCount {
		case 1:
			// Model requests two reads in parallel.
			resp = toolChatResponse{
				Choices: []toolChatChoice{
					{
						Message: toolChatMsg{
							Role: "assistant",
							ToolCalls: []toolCall{
								{
									ID:   "call_a",
									Type: "function",
									Function: toolCallFunction{
										Name:      "read",
										Arguments: `{"path":"a.txt"}`,
									},
								},
								{
									ID:   "call_b",
									Type: "function",
									Function: toolCallFunction{
										Name:      "read",
										Arguments: `{"path":"b.txt"}`,
									},
								},
							},
						},
						FinishReason: "tool_calls",
					},
				},
				Usage: struct {
					PromptTokens     int `json:"prompt_tokens"`
					CompletionTokens int `json:"completion_tokens"`
					TotalTokens      int `json:"total_tokens"`
				}{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120},
			}
		case 2:
			// Verify both tool results are present.
			var reqBody toolChatRequest
			json.NewDecoder(r.Body).Decode(&reqBody)

			// Count tool messages.
			toolMsgCount := 0
			for _, msg := range reqBody.Messages {
				if msg.Role == "tool" {
					toolMsgCount++
				}
			}
			assert.Equal(t, 2, toolMsgCount, "expected 2 tool result messages")

			resp = toolChatResponse{
				Choices: []toolChatChoice{
					{
						Message:      toolChatMsg{Role: "assistant", Content: "Read both files."},
						FinishReason: "stop",
					},
				},
				Usage: struct {
					PromptTokens     int `json:"prompt_tokens"`
					CompletionTokens int `json:"completion_tokens"`
					TotalTokens      int `json:"total_tokens"`
				}{PromptTokens: 200, CompletionTokens: 10, TotalTokens: 210},
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := DefaultDirectToolAgentConfig()
	cfg.Endpoint = server.URL
	cfg.WorkDir = dir

	result, err := RunDirectToolAgent(cfg, "system", "read both files", ToolsForRole("coder"), "")
	require.NoError(t, err)
	assert.Equal(t, "Read both files.", result.Output)
	assert.Equal(t, 2, result.Iterations)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func toolNames(tools []toolDefinition) []string {
	names := make([]string, len(tools))
	for i, td := range tools {
		names[i] = td.Function.Name
	}
	return names
}

// Verify the tool call request format by marshaling and checking JSON structure.
func TestToolChatMsgSerialization(t *testing.T) {
	// Assistant message with tool calls.
	msg := toolChatMsg{
		Role: "assistant",
		ToolCalls: []toolCall{
			{
				ID:   "call_123",
				Type: "function",
				Function: toolCallFunction{
					Name:      "read",
					Arguments: `{"path":"main.go"}`,
				},
			},
		},
	}
	data, err := json.Marshal(msg)
	require.NoError(t, err)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(data, &parsed))
	assert.Equal(t, "assistant", parsed["role"])
	assert.NotNil(t, parsed["tool_calls"])

	// Tool result message.
	toolMsg := toolChatMsg{
		Role:       "tool",
		Content:    "file contents here",
		ToolCallID: "call_123",
		Name:       "read",
	}
	data, err = json.Marshal(toolMsg)
	require.NoError(t, err)

	require.NoError(t, json.Unmarshal(data, &parsed))
	assert.Equal(t, "tool", parsed["role"])
	assert.Equal(t, "call_123", parsed["tool_call_id"])
	assert.Equal(t, "read", parsed["name"])
}

// TestDefaultDirectToolAgentConfig verifies default values are sensible.
func TestDefaultDirectToolAgentConfig(t *testing.T) {
	cfg := DefaultDirectToolAgentConfig()
	assert.Equal(t, "http://localhost:8081/v1/chat/completions", cfg.Endpoint)
	assert.Equal(t, "gemma4-26b", cfg.Model)
	assert.Equal(t, 2048, cfg.MaxTokens)
	assert.Equal(t, 20, cfg.MaxIterations)
	assert.Equal(t, 30*1e9, float64(cfg.BashTimeout)) // 30s in nanoseconds
}

// TestToolSchemaTokenBudget verifies that tool schemas stay compact.
func TestToolSchemaTokenBudget(t *testing.T) {
	tools := ToolsForRole("coder")
	data, err := json.Marshal(tools)
	require.NoError(t, err)

	// Rough token estimate: ~4 chars per token for JSON.
	estimatedTokens := len(data) / 4
	t.Logf("tool schemas: %d bytes, ~%d estimated tokens", len(data), estimatedTokens)
	assert.Less(t, estimatedTokens, 2000, "tool schemas should be under 2000 tokens")
}

// TestWalkGlob verifies the double-star glob implementation.
func TestWalkGlob(t *testing.T) {
	dir := t.TempDir()

	// Create nested structure.
	for _, path := range []string{
		"a.go",
		"b.txt",
		"sub/c.go",
		"sub/deep/d.go",
		"sub/deep/e.txt",
	} {
		full := filepath.Join(dir, path)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte("x"), 0o644))
	}

	matches := walkGlob(dir, "**/*.go")
	// Should find a.go, sub/c.go, sub/deep/d.go
	assert.Len(t, matches, 3, "expected 3 .go files, got: %v", matches)

	goCount := 0
	for _, m := range matches {
		if strings.HasSuffix(m, ".go") {
			goCount++
		}
	}
	assert.Equal(t, 3, goCount)
}

// TestExecToolDispatchUnknown verifies unknown tool names produce errors.
func TestExecToolDispatchUnknown(t *testing.T) {
	te := &toolExecutor{workDir: t.TempDir()}
	_, err := te.execute("nonexistent", "{}")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown tool")
}

// TestExecGrep_NoRipgrep handles the case where rg might not be available.
func TestExecGrep_BasicSearch(t *testing.T) {
	// Skip if ripgrep is not installed.
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("ripgrep (rg) not installed")
	}

	dir := t.TempDir()
	te := &toolExecutor{workDir: dir}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "test.go"), []byte("func main() {\n\tfmt.Println(\"hello\")\n}\n"), 0o644))

	t.Run("match found", func(t *testing.T) {
		result, err := te.execGrep(`{"pattern":"Println"}`)
		require.NoError(t, err)
		assert.Contains(t, result, "Println")
	})

	t.Run("no match", func(t *testing.T) {
		result, err := te.execGrep(`{"pattern":"nonexistent_string_xyz"}`)
		require.NoError(t, err)
		assert.Contains(t, result, "no matches")
	})

	t.Run("with glob filter", func(t *testing.T) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "other.txt"), []byte("Println in txt\n"), 0o644))
		result, err := te.execGrep(fmt.Sprintf(`{"pattern":"Println","glob":"*.go"}`))
		require.NoError(t, err)
		assert.Contains(t, result, "test.go")
		assert.NotContains(t, result, "other.txt")
	})
}
