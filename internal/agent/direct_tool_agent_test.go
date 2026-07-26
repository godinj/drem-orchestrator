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
	"time"

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

func TestRepairMalformedScopedMutation_InfersOnlyUnambiguousPath(t *testing.T) {
	call := toolCall{Function: toolCallFunction{
		Name:      "edit\n<parameter=path",
		Arguments: `{"old":"before","new":"after"}`,
	}}

	repaired, ok := repairMalformedScopedMutation(call, []string{"tests/integration/canary.cpp"})
	require.True(t, ok)
	require.Equal(t, "edit", repaired.Function.Name)
	require.JSONEq(t, `{"path":"tests/integration/canary.cpp","old":"before","new":"after"}`, repaired.Function.Arguments)

	_, ok = repairMalformedScopedMutation(call, []string{"one.cpp", "two.cpp"})
	require.False(t, ok, "ambiguous writable scope must remain fail-closed")
}

func TestRepairMalformedScopedMutationNormalizesCommonEditAliases(t *testing.T) {
	call := toolCall{Function: toolCallFunction{
		Name:      "edit",
		Arguments: `{"path":"src/file.cpp","old_string":"before","new_string":"after"}`,
	}}

	repaired, ok := repairMalformedScopedMutation(call, []string{"src/file.cpp", "src/other.cpp"})
	require.True(t, ok)
	require.Equal(t, "edit", repaired.Function.Name)
	require.JSONEq(t, `{"path":"src/file.cpp","old":"before","new":"after"}`, repaired.Function.Arguments)
}

func TestToolsForRole_Unknown(t *testing.T) {
	tools := ToolsForRole("unknown-role")
	names := toolNames(tools)
	// Unknown roles get read-only tools.
	assert.ElementsMatch(t, []string{"read", "grep", "glob"}, names)
}

func TestToolsForRoleScope_BoundedCoderOmitsDiscoveryTools(t *testing.T) {
	names := toolNames(ToolsForRoleScope("coder", []string{"src/Main.cpp"}))
	assert.ElementsMatch(t, []string{"read", "edit", "write", "bash"}, names)
	assert.NotContains(t, names, "grep")
	assert.NotContains(t, names, "glob")
}

func TestToolsForRoleScope_UnscopedAndReadOnlyRolesKeepDiscoveryTools(t *testing.T) {
	assert.ElementsMatch(t,
		toolNames(ToolsForRole("coder")),
		toolNames(ToolsForRoleScope("coder", nil)))
	assert.ElementsMatch(t,
		toolNames(ToolsForRole("reviewer")),
		toolNames(ToolsForRoleScope("reviewer", []string{"src/Main.cpp"})))
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

func TestExecRead_DefaultsToCompleteBoundedScopedArtifact(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "shared-test.cpp")
	var content strings.Builder
	for i := 1; i <= 696; i++ {
		fmt.Fprintf(&content, "line %d\n", i)
	}
	require.NoError(t, os.WriteFile(path, []byte(content.String()), 0o644))

	unscoped := &toolExecutor{workDir: dir}
	result, err := unscoped.execRead(`{"path":"shared-test.cpp"}`)
	require.NoError(t, err)
	require.Contains(t, result, "truncated at 200 lines")
	require.NotContains(t, result, " 696|line 696")

	scoped := &toolExecutor{workDir: dir, scopedFiles: map[string]struct{}{path: {}}}
	result, err = scoped.execRead(`{"path":"shared-test.cpp"}`)
	require.NoError(t, err)
	require.NotContains(t, result, "truncated")
	require.Contains(t, result, " 696|line 696")
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

func TestCPlusPlusMutationRejectsInventedQuotedHeader(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "src", "Widget.cpp")
	require.NoError(t, os.MkdirAll(filepath.Dir(source), 0o755))
	require.NoError(t, os.WriteFile(source, []byte("int widget() { return 1; }\n"), 0o644))
	te := &toolExecutor{workDir: dir, scopedFiles: map[string]struct{}{source: {}}}

	_, err := te.execEdit(`{"path":"src/Widget.cpp","old":"int widget() { return 1; }","new":"#include \"utils/EditorContext.h\"\nint widget() { return 1; }"}`)
	require.ErrorContains(t, err, "rejected new quoted include")
	content, readErr := os.ReadFile(source)
	require.NoError(t, readErr)
	require.NotContains(t, string(content), "EditorContext.h")
}

func TestCPlusPlusMutationAllowsExactAndPlannedQuotedHeaders(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "src", "Widget.cpp")
	header := filepath.Join(dir, "src", "Widget.h")
	planned := filepath.Join(dir, "src", "Planned.h")
	require.NoError(t, os.MkdirAll(filepath.Dir(source), 0o755))
	require.NoError(t, os.WriteFile(source, []byte("int widget() { return 1; }\n"), 0o644))
	require.NoError(t, os.WriteFile(header, []byte("#pragma once\n"), 0o644))
	te := &toolExecutor{workDir: dir, scopedFiles: map[string]struct{}{source: {}, planned: {}}}

	_, err := te.execEdit(`{"path":"src/Widget.cpp","old":"int widget() { return 1; }","new":"#include \"Widget.h\"\n#include \"Planned.h\"\nint widget() { return 1; }"}`)
	require.NoError(t, err)
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

func TestBashFileOpRedirect(t *testing.T) {
	tests := []struct {
		name     string
		cmd      string
		blocked  bool
		contains string // substring expected in redirect message
	}{
		{"heredoc write", `cat <<'EOF' > file.go\npackage main\nEOF`, true, "write tool"},
		{"heredoc unquoted", `cat <<EOF > file.go\nstuff\nEOF`, true, "write tool"},
		{"echo redirect", `echo "hello" > output.txt`, true, "write tool"},
		{"printf redirect", `printf '%s' data > out.txt`, true, "write tool"},
		{"echo to stderr", `echo out && echo err >&2`, false, ""},
		{"echo no redirect", `echo hello`, false, ""},
		{"tee pipe", `go test | tee results.txt`, true, "write tool"},
		{"sed in-place", `sed -i 's/old/new/' file.go`, true, "edit tool"},
		{"cat read", `cat internal/foo.go`, true, "read tool"},
		{"head read", `head -n 50 internal/foo.go`, true, "read tool"},
		{"tail read", `tail -n 20 internal/foo.go`, true, "read tool"},
		{"cat in pipeline", `cat file.go | wc -l`, false, ""}, // piped cat is allowed
		{"go test", `go test ./...`, false, ""},
		{"go vet", `go vet ./...`, false, ""},
		{"ls", `ls -la`, false, ""},
		{"make", `make build`, false, ""},
		{"grep via bash", `grep -rn pattern dir/`, false, ""}, // not intercepted (grep is a structured tool but bash grep isn't blocked, just discouraged via description)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			redirect := bashFileOpRedirect(tt.cmd)
			if tt.blocked {
				assert.NotEmpty(t, redirect, "expected command to be blocked: %s", tt.cmd)
				assert.Contains(t, redirect, tt.contains)
			} else {
				assert.Empty(t, redirect, "expected command to be allowed: %s", tt.cmd)
			}
		})
	}
}

func TestBashFileOpRedirect_ActualExecution(t *testing.T) {
	// Verify that intercepted commands return the redirect message
	// instead of executing the bash command.
	dir := t.TempDir()
	te := &toolExecutor{workDir: dir, bashTimeout: 5 * time.Second}

	t.Run("heredoc intercepted", func(t *testing.T) {
		result, err := te.execBash(`{"cmd":"cat <<'EOF' > test.go\npackage main\nEOF"}`)
		require.NoError(t, err)
		assert.Contains(t, result, "[HARNESS]")
		assert.Contains(t, result, "write tool")
		// File should NOT have been created
		_, statErr := os.Stat(filepath.Join(dir, "test.go"))
		assert.True(t, os.IsNotExist(statErr), "heredoc should not have executed")
	})

	t.Run("go test allowed", func(t *testing.T) {
		result, err := te.execBash(`{"cmd":"echo build_ok"}`)
		require.NoError(t, err)
		assert.Contains(t, result, "build_ok")
		assert.NotContains(t, result, "[HARNESS]")
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

func TestRunDirectToolAgent_SendsGQRoutingHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "coder", r.Header.Get("X-GQ-Caller"))
		assert.Equal(t, "normal", r.Header.Get("X-GQ-Priority"))
		resp := toolChatResponse{Choices: []toolChatChoice{{
			Message: toolChatMsg{Role: "assistant", Content: "done"}, FinishReason: "stop",
		}}}
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	}))
	defer server.Close()

	cfg := DefaultDirectToolAgentConfig()
	cfg.Endpoint = server.URL
	cfg.WorkDir = t.TempDir()
	cfg.GQCaller = "coder"
	cfg.GQPriority = "normal"
	_, err := RunDirectToolAgent(cfg, "system", "task", nil, "")
	require.NoError(t, err)
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

	result, err := RunDirectToolAgent(cfg, "You are a coder.", "Do something.", ToolsForRole("coder"), "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeded max iterations (3)")
	require.NotNil(t, result)
	assert.Equal(t, DirectToolStopReasonMaxIterations, result.StopReason)
}

func TestRunDirectToolAgent_NoProgressStopsRepeatedIdenticalToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := toolChatResponse{
			Choices: []toolChatChoice{{
				Message: toolChatMsg{Role: "assistant", ToolCalls: []toolCall{{
					ID:   "call_loop",
					Type: "function",
					Function: toolCallFunction{
						Name:      "write",
						Arguments: `{"path":"same.txt","content":"same"}`,
					},
				}}},
				FinishReason: "tool_calls",
			}},
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
	cfg.MaxIterations = 20

	result, err := RunDirectToolAgent(cfg, "You are a coder.", "Do something.", ToolsForRole("coder"), "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no progress")
	require.NotNil(t, result)
	assert.Equal(t, DirectToolStopReasonNoProgress, result.StopReason)
	assert.Less(t, result.Iterations, cfg.MaxIterations)
}

func TestRunDirectToolAgent_MaxTokensStopReason(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := toolChatResponse{
			Choices: []toolChatChoice{{
				Message:      toolChatMsg{Role: "assistant", Content: "partial"},
				FinishReason: "length",
			}},
			Usage: struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
				TotalTokens      int `json:"total_tokens"`
			}{PromptTokens: 50, CompletionTokens: 8, TotalTokens: 58},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := DefaultDirectToolAgentConfig()
	cfg.Endpoint = server.URL
	cfg.WorkDir = t.TempDir()
	cfg.MaxTokens = 8

	result, err := RunDirectToolAgent(cfg, "You are a coder.", "Do something.", ToolsForRole("coder"), "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "response truncated at max_tokens=8")
	require.NotNil(t, result)
	assert.Equal(t, DirectToolStopReasonMaxTokens, result.StopReason)
	assert.Equal(t, "partial", result.Output)
	assert.Equal(t, 50, result.TokensIn)
	assert.Equal(t, 8, result.TokensOut)
}

func TestRunDirectToolAgent_TimeoutStopReason(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
	}))
	defer server.Close()

	cfg := DefaultDirectToolAgentConfig()
	cfg.Endpoint = server.URL
	cfg.WorkDir = t.TempDir()
	cfg.Timeout = time.Nanosecond

	result, err := RunDirectToolAgent(cfg, "You are a coder.", "Do something.", nil, "")
	require.Error(t, err)
	require.NotNil(t, result)
	assert.Equal(t, DirectToolStopReasonTimeout, result.StopReason)
	assert.Equal(t, 0, result.Iterations)
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

func TestToolCallFunctionSerializationFormats(t *testing.T) {
	standard, err := json.Marshal(toolCallFunction{
		Name: "read", Arguments: `{"path":"main.cpp"}`,
	})
	require.NoError(t, err)
	var standardWire struct {
		Arguments any `json:"arguments"`
	}
	require.NoError(t, json.Unmarshal(standard, &standardWire))
	require.Equal(t, `{"path":"main.cpp"}`, standardWire.Arguments)

	gemma, err := json.Marshal(toolCallFunction{
		Name: "read", Arguments: `{"path":"main.cpp"}`, argumentsAsObject: true,
	})
	require.NoError(t, err)
	var gemmaWire struct {
		Arguments any `json:"arguments"`
	}
	require.NoError(t, json.Unmarshal(gemma, &gemmaWire))
	require.Equal(t, map[string]any{"path": "main.cpp"}, gemmaWire.Arguments)
}

// TestDefaultDirectToolAgentConfig verifies default values are sensible.
func TestDefaultDirectToolAgentConfig(t *testing.T) {
	cfg := DefaultDirectToolAgentConfig()
	assert.Equal(t, "http://localhost:8081/v1/chat/completions", cfg.Endpoint)
	assert.Equal(t, "qwen3-coder-30b", cfg.Model)
	assert.Equal(t, 8192, cfg.MaxTokens)
	assert.Equal(t, 20, cfg.MaxIterations)
	assert.Equal(t, 60000, cfg.MaxCumulativeInputTokens)
	assert.Equal(t, ToolArgumentsString, cfg.ToolArgumentsFormat)
	assert.Equal(t, 30*1e9, float64(cfg.BashTimeout)) // 30s in nanoseconds
	assert.Empty(t, cfg.ToolHistoryMode, "the zero/default policy stays aggressive until a mutating workload is selected")
}

func TestRunDirectToolAgent_CumulativeInputBudgetCheckpointsToolBatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := toolChatResponse{
			Choices: []toolChatChoice{{
				Message: toolChatMsg{Role: "assistant", ToolCalls: []toolCall{{
					ID: "write-1", Type: "function",
					Function: toolCallFunction{Name: "write", Arguments: `{"path":"checkpoint.txt","content":"preserved"}`},
				}}},
				FinishReason: "tool_calls",
			}},
			Usage: struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
				TotalTokens      int `json:"total_tokens"`
			}{PromptTokens: 120, CompletionTokens: 5, TotalTokens: 125},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	dir := t.TempDir()
	cfg := DefaultDirectToolAgentConfig()
	cfg.Endpoint = server.URL
	cfg.WorkDir = dir
	cfg.MaxCumulativeInputTokens = 100

	result, err := RunDirectToolAgent(cfg, "sys", "user", ToolsForRole("coder"), "")
	require.NoError(t, err)
	require.Equal(t, DirectToolStopReasonTokenBudget, result.StopReason)
	require.Equal(t, 120, result.TokensIn)
	require.True(t, result.MutationObserved)
	content, readErr := os.ReadFile(filepath.Join(dir, "checkpoint.txt"))
	require.NoError(t, readErr)
	require.Equal(t, "preserved", string(content))
}

func TestRunDirectToolAgent_ScopedReadBudgetAllowsOneCorrectiveMutationTurn(t *testing.T) {
	requestCount := 0
	requestToolNames := make([][]string, 0, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		var req toolChatRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		names := make([]string, 0, len(req.Tools))
		for _, tool := range req.Tools {
			names = append(names, tool.Function.Name)
		}
		requestToolNames = append(requestToolNames, names)
		if requestCount == 2 {
			conversation := ""
			for _, message := range req.Messages {
				conversation += message.Content
			}
			require.Contains(t, conversation, "content-3")
			require.Contains(t, conversation, "single scoped final-look read")
		}
		resp := toolChatResponse{}
		switch requestCount {
		case 1:
			calls := make([]toolCall, 0, 4)
			for i := 1; i <= 4; i++ {
				calls = append(calls, toolCall{
					ID: fmt.Sprintf("read-%d", i), Type: "function",
					Function: toolCallFunction{Name: "read", Arguments: fmt.Sprintf(`{"path":"file-%d.txt"}`, i)},
				})
			}
			resp.Choices = []toolChatChoice{{Message: toolChatMsg{Role: "assistant", ToolCalls: calls}, FinishReason: "tool_calls"}}
		case 2:
			resp.Choices = []toolChatChoice{{Message: toolChatMsg{Role: "assistant", ToolCalls: []toolCall{{
				ID: "edit-1", Type: "function",
				Function: toolCallFunction{Name: "edit", Arguments: `{"path":"file-1.txt","old":"content-1","new":"content-1\nmutated"}`},
			}}}, FinishReason: "tool_calls"}}
		default:
			resp.Choices = []toolChatChoice{{Message: toolChatMsg{Role: "assistant", Content: "done"}, FinishReason: "stop"}}
		}
		resp.Usage.PromptTokens = 20
		resp.Usage.CompletionTokens = 5
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	dir := t.TempDir()
	for i := 1; i <= 4; i++ {
		require.NoError(t, os.WriteFile(filepath.Join(dir, fmt.Sprintf("file-%d.txt", i)), []byte(fmt.Sprintf("content-%d", i)), 0o644))
	}
	cfg := DefaultDirectToolAgentConfig()
	cfg.Endpoint = server.URL
	cfg.WorkDir = dir
	cfg.MaxReadsBeforeMutation = 2
	cfg.ProtectExistingFiles = true
	cfg.ScopedFiles = []string{"file-1.txt", "file-2.txt", "file-3.txt", "file-4.txt"}

	result, err := RunDirectToolAgent(cfg, "sys", "make the scoped edit", ToolsForRole("coder"), "")
	require.NoError(t, err)
	require.True(t, result.MutationObserved)
	require.Equal(t, 3, requestCount)
	require.ElementsMatch(t, []string{"read", "edit", "write", "grep", "glob"}, requestToolNames[0])
	require.Equal(t, []string{"edit"}, requestToolNames[1])
	require.ElementsMatch(t, []string{"read", "edit", "write", "bash", "grep", "glob"}, requestToolNames[2])
	content, readErr := os.ReadFile(filepath.Join(dir, "file-1.txt"))
	require.NoError(t, readErr)
	require.Equal(t, "content-1\nmutated", string(content))
}

func TestRunDirectToolAgent_FinalScopedReadDoesNotConsumeDeniedResponseAllowance(t *testing.T) {
	requestCount := 0
	requestToolNames := make([][]string, 0, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		var req toolChatRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		names := make([]string, 0, len(req.Tools))
		for _, tool := range req.Tools {
			names = append(names, tool.Function.Name)
		}
		requestToolNames = append(requestToolNames, names)

		resp := toolChatResponse{}
		switch requestCount {
		case 1:
			resp.Choices = []toolChatChoice{{Message: toolChatMsg{Role: "assistant", ToolCalls: []toolCall{
				{ID: "reference-read", Type: "function", Function: toolCallFunction{Name: "read", Arguments: `{"path":"reference.cpp"}`}},
				{ID: "final-scoped-read", Type: "function", Function: toolCallFunction{Name: "read", Arguments: `{"path":"existing.cpp"}`}},
			}}, FinishReason: "tool_calls"}}
		case 2:
			require.Equal(t, []string{"edit"}, names)
			resp.Choices = []toolChatChoice{{Message: toolChatMsg{Role: "assistant", ToolCalls: []toolCall{{
				ID: "hallucinated-read", Type: "function",
				Function: toolCallFunction{Name: "read", Arguments: `{"path":"existing.cpp"}`},
			}}}, FinishReason: "tool_calls"}}
		case 3:
			require.Equal(t, []string{"edit"}, names)
			conversation := ""
			for _, message := range req.Messages {
				conversation += message.Content
			}
			require.Contains(t, conversation, "unavailable before the first scoped mutation")
			resp.Choices = []toolChatChoice{{Message: toolChatMsg{Role: "assistant", ToolCalls: []toolCall{{
				ID: "corrective-edit", Type: "function",
				Function: toolCallFunction{Name: "edit", Arguments: `{"path":"existing.cpp","old":"original","new":"changed"}`},
			}}}, FinishReason: "tool_calls"}}
		default:
			resp.Choices = []toolChatChoice{{Message: toolChatMsg{Role: "assistant", Content: "done"}, FinishReason: "stop"}}
		}
		resp.Usage.PromptTokens = 20
		resp.Usage.CompletionTokens = 5
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	}))
	defer server.Close()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "reference.cpp"), []byte("reference"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "existing.cpp"), []byte("original"), 0o644))
	cfg := DefaultDirectToolAgentConfig()
	cfg.Endpoint = server.URL
	cfg.WorkDir = dir
	cfg.MaxReadsBeforeMutation = 1
	cfg.MaxInputTokensBeforeMutation = 1_000
	cfg.ProtectExistingFiles = true
	cfg.ScopedFiles = []string{"existing.cpp"}

	result, err := RunDirectToolAgent(cfg, "sys", "make the scoped edit", ToolsForRole("coder"), "")
	require.NoError(t, err)
	require.True(t, result.MutationObserved)
	require.Equal(t, 4, requestCount)
	require.Contains(t, requestToolNames[0], "read")
	require.Equal(t, []string{"edit"}, requestToolNames[1])
	require.Equal(t, []string{"edit"}, requestToolNames[2])
	content, readErr := os.ReadFile(filepath.Join(dir, "existing.cpp"))
	require.NoError(t, readErr)
	require.Equal(t, "changed", string(content))
}

func TestRunDirectToolAgent_RefusesHallucinatedShellBeforeScopedMutation(t *testing.T) {
	requestCount := 0
	requestToolNames := make([][]string, 0, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		var req toolChatRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		var names []string
		for _, tool := range req.Tools {
			names = append(names, tool.Function.Name)
		}
		requestToolNames = append(requestToolNames, names)
		resp := toolChatResponse{}
		switch requestCount {
		case 1:
			resp.Choices = []toolChatChoice{{Message: toolChatMsg{Role: "assistant", ToolCalls: []toolCall{{
				ID: "hallucinated-bash", Type: "function",
				Function: toolCallFunction{Name: "bash", Arguments: `{"cmd":"printf bad > existing.cpp"}`},
			}}}, FinishReason: "tool_calls"}}
		case 2:
			conversation := ""
			for _, message := range req.Messages {
				conversation += message.Content
			}
			require.Contains(t, conversation, "unavailable before the first scoped mutation")
			resp.Choices = []toolChatChoice{{Message: toolChatMsg{Role: "assistant", ToolCalls: []toolCall{{
				ID: "scoped-edit", Type: "function",
				Function: toolCallFunction{Name: "edit", Arguments: `{"path":"existing.cpp","old":"original","new":"changed"}`},
			}}}, FinishReason: "tool_calls"}}
		default:
			resp.Choices = []toolChatChoice{{Message: toolChatMsg{Role: "assistant", Content: "done"}, FinishReason: "stop"}}
		}
		resp.Usage.PromptTokens = 20
		resp.Usage.CompletionTokens = 5
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	}))
	defer server.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "existing.cpp")
	require.NoError(t, os.WriteFile(path, []byte("original"), 0o644))
	cfg := DefaultDirectToolAgentConfig()
	cfg.Endpoint = server.URL
	cfg.WorkDir = dir
	cfg.ScopedFiles = []string{"existing.cpp"}
	cfg.MaxReadsBeforeMutation = 2
	cfg.MaxInputTokensBeforeMutation = 100

	result, err := RunDirectToolAgent(cfg, "sys", "make the scoped edit", ToolsForRole("coder"), "")
	require.NoError(t, err)
	require.True(t, result.MutationObserved)
	require.Equal(t, 3, requestCount)
	require.NotContains(t, requestToolNames[0], "bash")
	require.Equal(t, []string{"edit", "write"}, requestToolNames[1])
	require.Contains(t, requestToolNames[2], "bash", "shell verification returns after a checkpoint")
	content, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	require.Equal(t, "changed", string(content))
}

func TestRunDirectToolAgent_ReservesCumulativeBudgetForMutationOnlyTurn(t *testing.T) {
	requestCount := 0
	requestToolNames := make([][]string, 0, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		var req toolChatRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		var names []string
		for _, tool := range req.Tools {
			names = append(names, tool.Function.Name)
		}
		requestToolNames = append(requestToolNames, names)

		resp := toolChatResponse{}
		switch requestCount {
		case 1:
			resp.Choices = []toolChatChoice{{Message: toolChatMsg{Role: "assistant", ToolCalls: []toolCall{{
				ID: "reference-read", Type: "function",
				Function: toolCallFunction{Name: "read", Arguments: `{"path":"reference.cpp"}`},
			}}}, FinishReason: "tool_calls"}}
			resp.Usage.PromptTokens = 20_000
		case 2:
			resp.Choices = []toolChatChoice{{Message: toolChatMsg{Role: "assistant", ToolCalls: []toolCall{{
				ID: "final-scoped-read", Type: "function",
				Function: toolCallFunction{Name: "read", Arguments: `{"path":"existing.cpp"}`},
			}}}, FinishReason: "tool_calls"}}
			resp.Usage.PromptTokens = 28_000
		case 3:
			require.Equal(t, []string{"edit"}, names)
			conversation := ""
			for _, message := range req.Messages {
				conversation += message.Content
			}
			require.Contains(t, conversation, "next turn is mutation-only")
			resp.Choices = []toolChatChoice{{Message: toolChatMsg{Role: "assistant", ToolCalls: []toolCall{{
				ID: "budgeted-edit", Type: "function",
				Function: toolCallFunction{Name: "edit", Arguments: `{"path":"existing.cpp","old":"original","new":"changed"}`},
			}}}, FinishReason: "tool_calls"}}
			resp.Usage.PromptTokens = 17_000
		default:
			t.Fatal("unexpected extra model turn")
		}
		resp.Usage.CompletionTokens = 5
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	}))
	defer server.Close()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "reference.cpp"), []byte("reference"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "existing.cpp"), []byte("original"), 0o644))
	cfg := DefaultDirectToolAgentConfig()
	cfg.Endpoint = server.URL
	cfg.WorkDir = dir
	cfg.ScopedFiles = []string{"existing.cpp"}
	cfg.ProtectExistingFiles = true
	cfg.MaxCumulativeInputTokens = 65_000
	cfg.MaxInputTokensBeforeMutation = 45_000

	result, err := RunDirectToolAgent(cfg, "sys", "make the scoped edit", ToolsForRole("coder"), "")
	require.NoError(t, err)
	require.True(t, result.MutationObserved)
	require.Equal(t, DirectToolStopReasonTokenBudget, result.StopReason)
	require.Equal(t, 65_000, result.TokensIn)
	require.Equal(t, 3, requestCount)
	require.Contains(t, requestToolNames[0], "read")
	require.Contains(t, requestToolNames[1], "read")
	content, readErr := os.ReadFile(filepath.Join(dir, "existing.cpp"))
	require.NoError(t, readErr)
	require.Equal(t, "changed", string(content))
}

func TestRunDirectToolAgent_ScopedReadBudgetStopsAfterForcedMutationRecovery(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		calls := make([]toolCall, 0, 4)
		for i := 1; i <= 4; i++ {
			calls = append(calls, toolCall{
				ID: fmt.Sprintf("read-%d-%d", requestCount, i), Type: "function",
				Function: toolCallFunction{Name: "read", Arguments: fmt.Sprintf(`{"path":"file-%d.txt"}`, i)},
			})
		}
		resp := toolChatResponse{Choices: []toolChatChoice{{Message: toolChatMsg{Role: "assistant", ToolCalls: calls}, FinishReason: "tool_calls"}}}
		resp.Usage.PromptTokens = 20
		resp.Usage.CompletionTokens = 5
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	}))
	defer server.Close()

	dir := t.TempDir()
	for i := 1; i <= 4; i++ {
		require.NoError(t, os.WriteFile(filepath.Join(dir, fmt.Sprintf("file-%d.txt", i)), []byte(fmt.Sprintf("content-%d", i)), 0o644))
	}
	cfg := DefaultDirectToolAgentConfig()
	cfg.Endpoint = server.URL
	cfg.WorkDir = dir
	cfg.MaxReadsBeforeMutation = 2

	result, err := RunDirectToolAgent(cfg, "sys", "make the scoped edit", ToolsForRole("coder"), "")
	require.Error(t, err)
	require.Equal(t, DirectToolStopReasonNoProgress, result.StopReason)
	require.Equal(t, 3, requestCount)
	require.Contains(t, err.Error(), "3 blocked response batches")
}

func TestRunDirectToolAgent_RecoversOneSecondDeniedReadWithForcedMutationTurn(t *testing.T) {
	requestCount := 0
	requestToolNames := make([][]string, 0, 5)
	forcedRecoverySeen := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		var req toolChatRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		names := make([]string, 0, len(req.Tools))
		for _, tool := range req.Tools {
			names = append(names, tool.Function.Name)
		}
		requestToolNames = append(requestToolNames, names)
		for _, message := range req.Messages {
			if message.Role == "system" && message.Content == mutationOnlyBlockedRecoveryPrompt {
				forcedRecoverySeen = true
			}
		}

		resp := toolChatResponse{}
		switch requestCount {
		case 1:
			resp.Choices = []toolChatChoice{{Message: toolChatMsg{Role: "assistant", ToolCalls: []toolCall{{
				ID: "read-in-scope", Type: "function",
				Function: toolCallFunction{Name: "read", Arguments: "{\"path\":\"existing.cpp\"}"},
			}}}, FinishReason: "tool_calls"}}
		case 2:
			resp.Choices = []toolChatChoice{{Message: toolChatMsg{Role: "assistant", ToolCalls: []toolCall{{
				ID: "read-denied-first", Type: "function",
				Function: toolCallFunction{Name: "read", Arguments: "{\"path\":\"unneeded.cpp\"}"},
			}}}, FinishReason: "tool_calls"}}
		case 3:
			resp.Choices = []toolChatChoice{{Message: toolChatMsg{Role: "assistant", ToolCalls: []toolCall{{
				ID: "read-denied-second", Type: "function",
				Function: toolCallFunction{Name: "read", Arguments: "{\"path\":\"existing.cpp\"}"},
			}}}, FinishReason: "tool_calls"}}
		case 4:
			resp.Choices = []toolChatChoice{{Message: toolChatMsg{Role: "assistant", ToolCalls: []toolCall{{
				ID: "edit", Type: "function",
				Function: toolCallFunction{Name: "edit", Arguments: "{\"path\":\"existing.cpp\",\"old\":\"original\",\"new\":\"changed\"}"},
			}}}, FinishReason: "tool_calls"}}
		default:
			resp.Choices = []toolChatChoice{{Message: toolChatMsg{Role: "assistant", Content: "done"}, FinishReason: "stop"}}
		}
		resp.Usage.PromptTokens = 20
		resp.Usage.CompletionTokens = 5
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	}))
	defer server.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "existing.cpp")
	require.NoError(t, os.WriteFile(path, []byte("original"), 0o644))
	cfg := DefaultDirectToolAgentConfig()
	cfg.Endpoint = server.URL
	cfg.WorkDir = dir
	cfg.ScopedFiles = []string{"existing.cpp"}
	cfg.ProtectExistingFiles = true
	cfg.MaxReadsBeforeMutation = 1

	result, err := RunDirectToolAgent(cfg, "sys", "make the scoped edit", ToolsForRole("coder"), "")
	require.NoError(t, err)
	require.True(t, result.MutationObserved)
	require.Equal(t, 5, requestCount)
	require.Equal(t, []string{"edit"}, requestToolNames[2])
	require.Equal(t, []string{"edit"}, requestToolNames[3])
	require.True(t, forcedRecoverySeen)
	content, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	require.Equal(t, "changed", string(content))
}

func TestRunDirectToolAgent_RecoversOneToollessStopInMutationOnlyPhase(t *testing.T) {
	requestCount := 0
	requestToolNames := make([][]string, 0, 5)
	forcedRecoverySeen := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		var req toolChatRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		names := make([]string, 0, len(req.Tools))
		for _, tool := range req.Tools {
			names = append(names, tool.Function.Name)
		}
		requestToolNames = append(requestToolNames, names)
		for _, message := range req.Messages {
			if message.Role == "system" && message.Content == mutationOnlyToollessRecoveryPrompt {
				forcedRecoverySeen = true
			}
		}

		resp := toolChatResponse{}
		switch requestCount {
		case 1, 2:
			resp.Choices = []toolChatChoice{{Message: toolChatMsg{Role: "assistant", ToolCalls: []toolCall{{
				ID: fmt.Sprintf("read-%d", requestCount), Type: "function",
				Function: toolCallFunction{Name: "read", Arguments: "{\"path\":\"existing.cpp\"}"},
			}}}, FinishReason: "tool_calls"}}
		case 3:
			resp.Choices = []toolChatChoice{{Message: toolChatMsg{
				Role: "assistant", Content: "I need to make the first mutation now.",
			}, FinishReason: "stop"}}
		case 4:
			resp.Choices = []toolChatChoice{{Message: toolChatMsg{Role: "assistant", ToolCalls: []toolCall{{
				ID: "edit", Type: "function",
				Function: toolCallFunction{Name: "edit", Arguments: "{\"path\":\"existing.cpp\",\"old\":\"original\",\"new\":\"changed\"}"},
			}}}, FinishReason: "tool_calls"}}
		default:
			resp.Choices = []toolChatChoice{{Message: toolChatMsg{Role: "assistant", Content: "done"}, FinishReason: "stop"}}
		}
		resp.Usage.PromptTokens = 20
		resp.Usage.CompletionTokens = 5
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	}))
	defer server.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "existing.cpp")
	require.NoError(t, os.WriteFile(path, []byte("original"), 0o644))
	cfg := DefaultDirectToolAgentConfig()
	cfg.Endpoint = server.URL
	cfg.WorkDir = dir
	cfg.ScopedFiles = []string{"existing.cpp"}
	cfg.ProtectExistingFiles = true
	cfg.MaxReadsBeforeMutation = 1

	result, err := RunDirectToolAgent(cfg, "sys", "make the scoped edit", ToolsForRole("coder"), "")
	require.NoError(t, err)
	require.True(t, result.MutationObserved)
	require.Equal(t, 5, requestCount)
	require.Equal(t, []string{"edit"}, requestToolNames[2])
	require.Equal(t, []string{"edit"}, requestToolNames[3])
	require.True(t, forcedRecoverySeen)
	content, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	require.Equal(t, "changed", string(content))
}

func TestRunDirectToolAgent_DoesNotStackMutationOnlyRecoveries(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		var req toolChatRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))

		resp := toolChatResponse{}
		switch requestCount {
		case 1, 2:
			resp.Choices = []toolChatChoice{{Message: toolChatMsg{Role: "assistant", ToolCalls: []toolCall{{
				ID: fmt.Sprintf("read-in-scope-%d", requestCount), Type: "function",
				Function: toolCallFunction{Name: "read", Arguments: "{\"path\":\"existing.cpp\"}"},
			}}}, FinishReason: "tool_calls"}}
		case 3:
			resp.Choices = []toolChatChoice{{Message: toolChatMsg{
				Role: "assistant", Content: "I will edit next.",
			}, FinishReason: "stop"}}
		case 4, 5:
			resp.Choices = []toolChatChoice{{Message: toolChatMsg{Role: "assistant", ToolCalls: []toolCall{{
				ID: fmt.Sprintf("denied-read-%d", requestCount), Type: "function",
				Function: toolCallFunction{Name: "read", Arguments: "{\"path\":\"existing.cpp\"}"},
			}}}, FinishReason: "tool_calls"}}
		default:
			t.Fatal("the harness granted more than one mutation-only recovery")
		}
		resp.Usage.PromptTokens = 20
		resp.Usage.CompletionTokens = 5
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	}))
	defer server.Close()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "existing.cpp"), []byte("original"), 0o644))
	cfg := DefaultDirectToolAgentConfig()
	cfg.Endpoint = server.URL
	cfg.WorkDir = dir
	cfg.ScopedFiles = []string{"existing.cpp"}
	cfg.ProtectExistingFiles = true
	cfg.MaxReadsBeforeMutation = 1

	result, err := RunDirectToolAgent(cfg, "sys", "make the scoped edit", ToolsForRole("coder"), "")
	require.Error(t, err)
	require.Equal(t, DirectToolStopReasonNoProgress, result.StopReason)
	require.Contains(t, err.Error(), "2 blocked response batches")
	require.Equal(t, 5, requestCount)
}

func TestRunDirectToolAgent_FailsAfterSecondToollessStopInMutationOnlyPhase(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		resp := toolChatResponse{}
		switch requestCount {
		case 1, 2:
			resp.Choices = []toolChatChoice{{Message: toolChatMsg{Role: "assistant", ToolCalls: []toolCall{{
				ID: fmt.Sprintf("read-%d", requestCount), Type: "function",
				Function: toolCallFunction{Name: "read", Arguments: "{\"path\":\"existing.cpp\"}"},
			}}}, FinishReason: "tool_calls"}}
		default:
			resp.Choices = []toolChatChoice{{Message: toolChatMsg{
				Role: "assistant", Content: "I will make the edit.",
			}, FinishReason: "stop"}}
		}
		resp.Usage.PromptTokens = 20
		resp.Usage.CompletionTokens = 5
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	}))
	defer server.Close()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "existing.cpp"), []byte("original"), 0o644))
	cfg := DefaultDirectToolAgentConfig()
	cfg.Endpoint = server.URL
	cfg.WorkDir = dir
	cfg.ScopedFiles = []string{"existing.cpp"}
	cfg.ProtectExistingFiles = true
	cfg.MaxReadsBeforeMutation = 1

	result, err := RunDirectToolAgent(cfg, "sys", "make the scoped edit", ToolsForRole("coder"), "")
	require.Error(t, err)
	require.Equal(t, DirectToolStopReasonNoProgress, result.StopReason)
	require.Equal(t, 4, requestCount)
	require.Contains(t, err.Error(), "after the forced mutation recovery")
}

func TestRunDirectToolAgent_AllowsOneRecoveryReadAfterFailedSurgicalEdit(t *testing.T) {
	requestCount := 0
	requestToolNames := make([][]string, 0, 5)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		var req toolChatRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		names := make([]string, 0, len(req.Tools))
		for _, tool := range req.Tools {
			names = append(names, tool.Function.Name)
		}
		requestToolNames = append(requestToolNames, names)

		resp := toolChatResponse{}
		switch requestCount {
		case 1:
			resp.Choices = []toolChatChoice{{Message: toolChatMsg{Role: "assistant", ToolCalls: []toolCall{
				{ID: "read-1", Type: "function", Function: toolCallFunction{Name: "read", Arguments: `{"path":"existing.cpp"}`}},
				{ID: "read-2", Type: "function", Function: toolCallFunction{Name: "read", Arguments: `{"path":"existing.cpp","offset":1}`}},
			}}, FinishReason: "tool_calls"}}
		case 2:
			resp.Choices = []toolChatChoice{{Message: toolChatMsg{Role: "assistant", ToolCalls: []toolCall{{
				ID: "edit-miss", Type: "function",
				Function: toolCallFunction{Name: "edit", Arguments: `{"path":"existing.cpp","old":"stale text","new":"changed"}`},
			}}}, FinishReason: "tool_calls"}}
		case 3:
			resp.Choices = []toolChatChoice{{Message: toolChatMsg{Role: "assistant", ToolCalls: []toolCall{{
				ID: "recovery-read", Type: "function",
				Function: toolCallFunction{Name: "read", Arguments: `{"path":"existing.cpp"}`},
			}}}, FinishReason: "tool_calls"}}
		case 4:
			resp.Choices = []toolChatChoice{{Message: toolChatMsg{Role: "assistant", ToolCalls: []toolCall{{
				ID: "edit-corrected", Type: "function",
				Function: toolCallFunction{Name: "edit", Arguments: `{"path":"existing.cpp","old":"original","new":"corrected"}`},
			}}}, FinishReason: "tool_calls"}}
		default:
			resp.Choices = []toolChatChoice{{Message: toolChatMsg{Role: "assistant", Content: "done"}, FinishReason: "stop"}}
		}
		resp.Usage.PromptTokens = 20
		resp.Usage.CompletionTokens = 5
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	}))
	defer server.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "existing.cpp")
	require.NoError(t, os.WriteFile(path, []byte("original"), 0o644))
	cfg := DefaultDirectToolAgentConfig()
	cfg.Endpoint = server.URL
	cfg.WorkDir = dir
	cfg.MaxReadsBeforeMutation = 1
	cfg.MaxInputTokensBeforeMutation = 10
	cfg.ProtectExistingFiles = true
	cfg.ScopedFiles = []string{"existing.cpp"}

	result, err := RunDirectToolAgent(cfg, "sys", "repair the scoped file", ToolsForRole("coder"), "")
	require.NoError(t, err)
	require.True(t, result.MutationObserved)
	require.Equal(t, 5, requestCount)
	require.Equal(t, []string{"edit"}, requestToolNames[1])
	require.Equal(t, []string{"read", "edit"}, requestToolNames[2])
	require.Equal(t, []string{"edit"}, requestToolNames[3])
	content, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	require.Equal(t, "corrected", string(content))
}

func TestRunDirectToolAgent_FailedEditRecoveryOutranksExhaustedReconnaissance(t *testing.T) {
	requestCount := 0
	requestToolNames := make([][]string, 0, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		var req toolChatRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		names := make([]string, 0, len(req.Tools))
		for _, tool := range req.Tools {
			names = append(names, tool.Function.Name)
		}
		requestToolNames = append(requestToolNames, names)

		var message toolChatMsg
		finishReason := "tool_calls"
		switch requestCount {
		case 1, 2, 3, 4:
			message = toolChatMsg{Role: "assistant", ToolCalls: []toolCall{{
				ID: fmt.Sprintf("read-%d", requestCount), Type: "function",
				Function: toolCallFunction{Name: "read", Arguments: `{"path":"tests/integration/test_jump_motions.cpp"}`},
			}}}
		case 5:
			message = toolChatMsg{Role: "assistant", ToolCalls: []toolCall{{
				ID: "edit-stale-anchor", Type: "function",
				Function: toolCallFunction{Name: "edit", Arguments: `{"path":"tests/integration/test_jump_motions.cpp","old":"} // namespace imagined","new":"marker regression"}`},
			}}}
		case 6:
			message = toolChatMsg{Role: "assistant", ToolCalls: []toolCall{{
				ID: "read-after-anchor-miss", Type: "function",
				Function: toolCallFunction{Name: "read", Arguments: `{"path":"tests/integration/test_jump_motions.cpp"}`},
			}}}
		case 7:
			message = toolChatMsg{Role: "assistant", ToolCalls: []toolCall{{
				ID: "edit-correct-anchor", Type: "function",
				Function: toolCallFunction{Name: "edit", Arguments: `{"path":"tests/integration/test_jump_motions.cpp","old":"existing jump tests","new":"existing jump tests\nmarker regression"}`},
			}}}
		default:
			message = toolChatMsg{Role: "assistant", Content: "done"}
			finishReason = "stop"
		}
		resp := toolChatResponse{Choices: []toolChatChoice{{Message: message, FinishReason: finishReason}}}
		resp.Usage.PromptTokens = 2_000
		resp.Usage.CompletionTokens = 50
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	}))
	defer server.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "tests/integration/test_jump_motions.cpp")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("existing jump tests"), 0o644))
	cfg := DefaultDirectToolAgentConfig().ForWorkload("coder", "test")
	cfg.Endpoint = server.URL
	cfg.WorkDir = dir
	cfg.ScopedFiles = []string{"tests/integration/test_jump_motions.cpp"}
	cfg.RequiredMutationFiles = append([]string(nil), cfg.ScopedFiles...)
	cfg.ProtectExistingFiles = true
	cfg.MaxReadsBeforeMutation = 1
	cfg.MaxInputTokensBeforeMutation = 100_000
	cfg.MaxCumulativeInputTokens = 100_000
	cfg.MaxToolCalls = 20

	result, err := RunDirectToolAgent(cfg, "sys", "write the marker regression",
		ToolsForRoleScope("coder", cfg.ScopedFiles), "")
	require.NoError(t, err)
	require.True(t, result.MutationObserved)
	require.Empty(t, result.PendingMutationRepairs)
	require.Empty(t, result.MissingRequiredMutations)
	require.Equal(t, 8, requestCount)
	require.Equal(t, []string{"edit"}, requestToolNames[4])
	require.Equal(t, []string{"read", "edit"}, requestToolNames[5])
	require.Equal(t, []string{"edit"}, requestToolNames[6])
	content, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	require.Equal(t, "existing jump tests\nmarker regression", string(content))
}

func TestRunDirectToolAgent_ClassifiesExhaustedEditAnchorRecovery(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		var message toolChatMsg
		switch requestCount {
		case 1:
			message = toolChatMsg{Role: "assistant", ToolCalls: []toolCall{{
				ID: "edit-stale-anchor", Type: "function",
				Function: toolCallFunction{Name: "edit", Arguments: `{"path":"existing.cpp","old":"imagined tail","new":"changed"}`},
			}}}
		case 2:
			message = toolChatMsg{Role: "assistant", ToolCalls: []toolCall{{
				ID: "recovery-read", Type: "function",
				Function: toolCallFunction{Name: "read", Arguments: `{"path":"existing.cpp"}`},
			}}}
		default:
			message = toolChatMsg{Role: "assistant", ToolCalls: []toolCall{{
				ID: "edit-second-stale-anchor", Type: "function",
				Function: toolCallFunction{Name: "edit", Arguments: `{"path":"existing.cpp","old":"still imagined","new":"changed"}`},
			}}}
		}
		resp := toolChatResponse{Choices: []toolChatChoice{{Message: message, FinishReason: "tool_calls"}}}
		resp.Usage.PromptTokens = 100
		resp.Usage.CompletionTokens = 10
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	}))
	defer server.Close()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "existing.cpp"), []byte("actual tail"), 0o644))
	cfg := DefaultDirectToolAgentConfig().ForWorkload("coder", "implementation")
	cfg.Endpoint = server.URL
	cfg.WorkDir = dir
	cfg.ScopedFiles = []string{"existing.cpp"}
	cfg.RequiredMutationFiles = append([]string(nil), cfg.ScopedFiles...)
	cfg.ProtectExistingFiles = true
	cfg.MaxToolCalls = 10

	result, err := RunDirectToolAgent(cfg, "sys", "make the scoped edit", ToolsForRoleScope("coder", cfg.ScopedFiles), "")
	require.ErrorContains(t, err, "mutation anchor mismatch after bounded recovery")
	require.Equal(t, DirectToolStopReasonAnchorMismatch, result.StopReason)
	require.Equal(t, 3, requestCount)
}

func TestRunDirectToolAgent_TokenBudgetDoesNotCompletePartialMultiFileCheckpoint(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		resp := toolChatResponse{}
		switch requestCount {
		case 1:
			resp.Choices = []toolChatChoice{{Message: toolChatMsg{Role: "assistant", ToolCalls: []toolCall{{
				ID: "edit-declaration", Type: "function",
				Function: toolCallFunction{Name: "edit", Arguments: `{"path":"EditorAdapter.h","old":"declarations","new":"declarations\nmarkerAddWithArgs"}`},
			}}}, FinishReason: "tool_calls"}}
		case 2:
			resp.Choices = []toolChatChoice{{Message: toolChatMsg{Role: "assistant", ToolCalls: []toolCall{{
				ID: "edit-handler-miss", Type: "function",
				Function: toolCallFunction{Name: "edit", Arguments: `{"path":"EditorAdapterActionHandlers.inc","old":"obsolete end marker","new":"marker implementation"}`},
			}}}, FinishReason: "tool_calls"}}
		default:
			resp.Choices = []toolChatChoice{{Message: toolChatMsg{Role: "assistant", ToolCalls: []toolCall{{
				ID: "read-handler-recovery", Type: "function",
				Function: toolCallFunction{Name: "read", Arguments: `{"path":"EditorAdapterActionHandlers.inc"}`},
			}}}, FinishReason: "tool_calls"}}
		}
		resp.Usage.PromptTokens = 25
		resp.Usage.CompletionTokens = 5
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	}))
	defer server.Close()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "EditorAdapter.h"), []byte("declarations"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "EditorAdapterActionHandlers.inc"), []byte("real file ending"), 0o644))
	cfg := DefaultDirectToolAgentConfig()
	cfg.Endpoint = server.URL
	cfg.WorkDir = dir
	cfg.ScopedFiles = []string{"EditorAdapter.h", "EditorAdapterActionHandlers.inc"}
	cfg.RequiredMutationFiles = append([]string(nil), cfg.ScopedFiles...)
	cfg.ProtectExistingFiles = true
	cfg.MaxCumulativeInputTokens = 75
	cfg.MaxToolCalls = 10

	result, err := RunDirectToolAgent(cfg, "sys", "implement both marker files", ToolsForRoleScope("coder", cfg.ScopedFiles), "")
	require.ErrorContains(t, err, "token budget reached with incomplete mutation checkpoint")
	require.Equal(t, DirectToolStopReasonTokenBudget, result.StopReason)
	require.True(t, result.MutationObserved)
	require.Equal(t, []string{"EditorAdapterActionHandlers.inc"}, result.PendingMutationRepairs)
	require.Equal(t, []string{"EditorAdapterActionHandlers.inc"}, result.MissingRequiredMutations)
	require.Equal(t, 3, requestCount)
	header, readErr := os.ReadFile(filepath.Join(dir, "EditorAdapter.h"))
	require.NoError(t, readErr)
	handler, readErr := os.ReadFile(filepath.Join(dir, "EditorAdapterActionHandlers.inc"))
	require.NoError(t, readErr)
	require.Equal(t, "declarations\nmarkerAddWithArgs", string(header))
	require.Equal(t, "real file ending", string(handler))
}

func TestRunDirectToolAgent_NaturalStopRequiresEveryDeclaredMutationFile(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		resp := toolChatResponse{}
		if requestCount == 1 {
			resp.Choices = []toolChatChoice{{Message: toolChatMsg{Role: "assistant", ToolCalls: []toolCall{{
				ID: "edit-first-only", Type: "function", Function: toolCallFunction{
					Name: "edit", Arguments: `{"path":"first.cpp","old":"first","new":"first changed"}`},
			}}}, FinishReason: "tool_calls"}}
		} else {
			resp.Choices = []toolChatChoice{{Message: toolChatMsg{Role: "assistant", Content: "done"}, FinishReason: "stop"}}
		}
		resp.Usage.PromptTokens = 20
		resp.Usage.CompletionTokens = 5
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	}))
	defer server.Close()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "first.cpp"), []byte("first"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "second.cpp"), []byte("second"), 0o644))
	cfg := DefaultDirectToolAgentConfig()
	cfg.Endpoint = server.URL
	cfg.WorkDir = dir
	cfg.ScopedFiles = []string{"first.cpp", "second.cpp"}
	cfg.RequiredMutationFiles = append([]string(nil), cfg.ScopedFiles...)

	result, err := RunDirectToolAgent(cfg, "sys", "change both files", ToolsForRoleScope("coder", cfg.ScopedFiles), "")
	require.ErrorContains(t, err, "required files without a successful mutation: second.cpp")
	require.Equal(t, DirectToolStopReasonNoProgress, result.StopReason)
	require.Equal(t, []string{"first.cpp"}, result.MutatedFiles)
	require.Equal(t, []string{"second.cpp"}, result.MissingRequiredMutations)
	require.Equal(t, 2, requestCount)
}

func TestToolCallTargetsAnyPathNormalizesAbsoluteScopedPath(t *testing.T) {
	workDir := t.TempDir()
	absolute := filepath.Join(workDir, "src", "ui", "LowerZoneLayout.h")
	args, err := json.Marshal(map[string]string{"path": absolute})
	require.NoError(t, err)
	require.True(t, toolCallTargetsAnyPath(workDir, string(args), []string{"src/ui/LowerZoneLayout.h"}))
	require.False(t, toolCallTargetsAnyPath(workDir, string(args), []string{"src/ui/LayoutCoordinator.cpp"}))
}

func TestRunDirectToolAgent_ProtectsExistingFileThenAllowsSurgicalEdit(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		resp := toolChatResponse{}
		switch requestCount {
		case 1:
			resp.Choices = []toolChatChoice{{Message: toolChatMsg{Role: "assistant", ToolCalls: []toolCall{{
				ID: "write-existing", Type: "function",
				Function: toolCallFunction{Name: "write", Arguments: `{"path":"existing.cpp","content":"replacement"}`},
			}}}, FinishReason: "tool_calls"}}
		case 2:
			resp.Choices = []toolChatChoice{{Message: toolChatMsg{Role: "assistant", ToolCalls: []toolCall{{
				ID: "edit-existing", Type: "function",
				Function: toolCallFunction{Name: "edit", Arguments: `{"path":"existing.cpp","old":"original","new":"original\nadded test"}`},
			}}}, FinishReason: "tool_calls"}}
		default:
			resp.Choices = []toolChatChoice{{Message: toolChatMsg{Role: "assistant", Content: "done"}, FinishReason: "stop"}}
		}
		resp.Usage.PromptTokens = 20
		resp.Usage.CompletionTokens = 5
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	}))
	defer server.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "existing.cpp")
	require.NoError(t, os.WriteFile(path, []byte("original"), 0o644))
	cfg := DefaultDirectToolAgentConfig()
	cfg.Endpoint = server.URL
	cfg.WorkDir = dir
	cfg.ProtectExistingFiles = true
	cfg.MaxInputTokensBeforeMutation = 10

	result, err := RunDirectToolAgent(cfg, "sys", "add coverage", ToolsForRole("coder"), "")
	require.NoError(t, err)
	require.True(t, result.MutationObserved)
	require.Equal(t, 3, requestCount)
	content, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	require.Equal(t, "original\nadded test", string(content))
}

func TestRunDirectToolAgent_BoundsHistoricalBashDiscoveryTraceByInputBudget(t *testing.T) {
	commands := []string{
		"ls tests/integration", "scripts/dev test --filter Transient", "find src -name '*Audio*'",
		"rg -n Slicing src", "ls src/model", "grep -R divideAtTransients .",
	}
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		command := commands[callCount%len(commands)]
		callCount++
		resp := toolChatResponse{
			Choices: []toolChatChoice{{
				Message: toolChatMsg{Role: "assistant", ToolCalls: []toolCall{{
					ID: fmt.Sprintf("search-%d", callCount), Type: "function",
					Function: toolCallFunction{Name: "bash", Arguments: fmt.Sprintf(`{"cmd":%q}`, command)},
				}}},
				FinishReason: "tool_calls",
			}},
			Usage: struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
				TotalTokens      int `json:"total_tokens"`
			}{PromptTokens: 5500, CompletionTokens: 25, TotalTokens: 5525},
		}
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	}))
	defer server.Close()

	cfg := DefaultDirectToolAgentConfig()
	cfg.Endpoint = server.URL
	cfg.WorkDir = t.TempDir()
	cfg.MaxIterations = 16
	cfg.MaxReadsBeforeMutation = 8
	cfg.MaxToolCalls = 12
	cfg.MaxInputTokensBeforeMutation = 18_000

	result, err := RunDirectToolAgent(cfg, "sys", "write the contracted red test", ToolsForRoleScope("coder", []string{"tests/test.cpp"}), "")
	require.Error(t, err)
	require.Equal(t, DirectToolStopReasonNoProgress, result.StopReason)
	// The second denied discovery batch receives one final mutation-only
	// recovery turn; the third denial remains bounded no-progress.
	require.Equal(t, 6, callCount)
	require.Equal(t, 33_000, result.TokensIn)
	require.False(t, result.MutationObserved)
}

func TestRunDirectToolAgent_AllowsBashDiscoveryBeforeMutationInSameBatch(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		resp := toolChatResponse{}
		if callCount == 1 {
			resp.Choices = []toolChatChoice{{
				Message: toolChatMsg{Role: "assistant", ToolCalls: []toolCall{
					{ID: "search-1", Type: "function", Function: toolCallFunction{Name: "bash", Arguments: `{"cmd":"ls"}`}},
					{ID: "search-2", Type: "function", Function: toolCallFunction{Name: "bash", Arguments: `{"cmd":"rg Missing"}`}},
					{ID: "write-1", Type: "function", Function: toolCallFunction{Name: "write", Arguments: `{"path":"test.cpp","content":"contracted test"}`}},
				}}, FinishReason: "tool_calls",
			}}
		} else {
			resp.Choices = []toolChatChoice{{Message: toolChatMsg{Role: "assistant", Content: "done"}, FinishReason: "stop"}}
		}
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	}))
	defer server.Close()

	dir := t.TempDir()
	cfg := DefaultDirectToolAgentConfig()
	cfg.Endpoint = server.URL
	cfg.WorkDir = dir
	cfg.MaxReadsBeforeMutation = 8
	cfg.MaxToolCalls = 12
	result, err := RunDirectToolAgent(cfg, "sys", "task", ToolsForRoleScope("coder", []string{"test.cpp"}), "")
	require.NoError(t, err)
	require.True(t, result.MutationObserved)
	require.Equal(t, 2, callCount)
	data, readErr := os.ReadFile(filepath.Join(dir, "test.cpp"))
	require.NoError(t, readErr)
	require.Equal(t, "contracted test", string(data))
}

func TestRunDirectToolAgent_BlocksOutOfScopeMutationAndPreservesAuthorizedCheckpoint(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		resp := toolChatResponse{}
		if callCount == 1 {
			resp.Choices = []toolChatChoice{{
				Message: toolChatMsg{Role: "assistant", ToolCalls: []toolCall{
					{ID: "write-test", Type: "function", Function: toolCallFunction{Name: "write", Arguments: `{"path":"tests/test.cpp","content":"authorized checkpoint"}`}},
					{ID: "edit-manifest", Type: "function", Function: toolCallFunction{Name: "edit", Arguments: `{"path":"tests/cmake/Manifest.cmake","old":"original","new":"changed"}`}},
				}}, FinishReason: "tool_calls",
			}}
		} else {
			resp.Choices = []toolChatChoice{{Message: toolChatMsg{Role: "assistant", Content: "done"}, FinishReason: "stop"}}
		}
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	}))
	defer server.Close()

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "tests", "cmake"), 0o755))
	manifest := filepath.Join(dir, "tests", "cmake", "Manifest.cmake")
	require.NoError(t, os.WriteFile(manifest, []byte("original"), 0o644))
	cfg := DefaultDirectToolAgentConfig()
	cfg.Endpoint = server.URL
	cfg.WorkDir = dir
	cfg.ScopedFiles = []string{"tests/test.cpp"}

	result, err := RunDirectToolAgent(cfg, "sys", "write the scoped test", ToolsForRoleScope("coder", cfg.ScopedFiles), "")
	require.NoError(t, err)
	require.True(t, result.MutationObserved)
	require.Equal(t, 2, callCount)
	data, readErr := os.ReadFile(filepath.Join(dir, "tests", "test.cpp"))
	require.NoError(t, readErr)
	require.Equal(t, "authorized checkpoint", string(data))
	manifestData, readErr := os.ReadFile(manifest)
	require.NoError(t, readErr)
	require.Equal(t, "original", string(manifestData))
}

func TestRunDirectToolAgent_StopsRepeatedOutOfScopeMutationsBeforeExecution(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		resp := toolChatResponse{Choices: []toolChatChoice{{
			Message: toolChatMsg{Role: "assistant", ToolCalls: []toolCall{{
				ID: fmt.Sprintf("edit-manifest-%d", callCount), Type: "function",
				Function: toolCallFunction{Name: "edit", Arguments: `{"path":"tests/cmake/Manifest.cmake","old":"original","new":"changed"}`},
			}}}, FinishReason: "tool_calls",
		}}}
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	}))
	defer server.Close()

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "tests", "cmake"), 0o755))
	manifest := filepath.Join(dir, "tests", "cmake", "Manifest.cmake")
	require.NoError(t, os.WriteFile(manifest, []byte("original"), 0o644))
	cfg := DefaultDirectToolAgentConfig()
	cfg.Endpoint = server.URL
	cfg.WorkDir = dir
	cfg.ScopedFiles = []string{"tests/test.cpp"}

	result, err := RunDirectToolAgent(cfg, "sys", "write the scoped test", ToolsForRoleScope("coder", cfg.ScopedFiles), "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "out-of-scope mutations")
	require.Equal(t, DirectToolStopReasonNoProgress, result.StopReason)
	require.False(t, result.MutationObserved)
	require.Equal(t, 2, callCount)
	manifestData, readErr := os.ReadFile(manifest)
	require.NoError(t, readErr)
	require.Equal(t, "original", string(manifestData))
}

func TestRunDirectToolAgent_AllowsReadOnlyIntegrationValidation(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		resp := toolChatResponse{}
		if callCount == 1 {
			resp.Choices = []toolChatChoice{{
				Message: toolChatMsg{Role: "assistant", ToolCalls: []toolCall{
					{ID: "read-1", Type: "function", Function: toolCallFunction{Name: "read", Arguments: `{"path":"assembled.cpp"}`}},
					{ID: "test-1", Type: "function", Function: toolCallFunction{Name: "bash", Arguments: `{"cmd":"test -f assembled.cpp"}`}},
				}}, FinishReason: "tool_calls",
			}}
		} else {
			resp.Choices = []toolChatChoice{{Message: toolChatMsg{Role: "assistant", Content: "validated"}, FinishReason: "stop"}}
		}
		resp.Usage.PromptTokens = 100
		resp.Usage.CompletionTokens = 10
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	}))
	defer server.Close()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "assembled.cpp"), []byte("correct"), 0o644))
	cfg := DefaultDirectToolAgentConfig()
	cfg.Endpoint = server.URL
	cfg.WorkDir = dir
	cfg.MaxReadsBeforeMutation = 1
	cfg.MaxInputTokensBeforeMutation = 1
	cfg.AllowReadOnlyCompletion = true

	result, err := RunDirectToolAgent(cfg, "sys", "validate assembled code", ToolsForRoleScope("coder", []string{"assembled.cpp"}), "")
	require.NoError(t, err)
	require.Equal(t, "validated", result.Output)
	require.False(t, result.MutationObserved)
	require.Equal(t, 2, callCount)
}

func TestRunDirectToolAgent_AllowsReadOnlyIntegrationAtToolBudget(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		resp := toolChatResponse{Choices: []toolChatChoice{{
			Message: toolChatMsg{Role: "assistant", Content: "assembled code is valid", ToolCalls: []toolCall{{
				ID: fmt.Sprintf("check-%d", callCount), Type: "function",
				Function: toolCallFunction{Name: "bash", Arguments: `{"cmd":"test -f assembled.cpp"}`},
			}}}, FinishReason: "tool_calls",
		}}}
		resp.Usage.PromptTokens = 100
		resp.Usage.CompletionTokens = 10
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	}))
	defer server.Close()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "assembled.cpp"), []byte("correct"), 0o644))
	cfg := DefaultDirectToolAgentConfig()
	cfg.Endpoint = server.URL
	cfg.WorkDir = dir
	cfg.MaxToolCalls = 3
	cfg.AllowReadOnlyCompletion = true

	result, err := RunDirectToolAgent(cfg, "sys", "validate assembled code", ToolsForRoleScope("coder", []string{"assembled.cpp"}), "")
	require.NoError(t, err)
	require.Equal(t, DirectToolStopReasonToolBudget, result.StopReason)
	require.False(t, result.MutationObserved)
	require.Equal(t, 3, callCount)
}

func TestRunDirectToolAgent_EnforcesTotalToolCallBudget(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		resp := toolChatResponse{Choices: []toolChatChoice{{
			Message: toolChatMsg{Role: "assistant", ToolCalls: []toolCall{{
				ID: fmt.Sprintf("call-%d", callCount), Type: "function",
				Function: toolCallFunction{Name: "bash", Arguments: fmt.Sprintf(`{"cmd":"echo step-%d"}`, callCount)},
			}}}, FinishReason: "tool_calls",
		}}}
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	}))
	defer server.Close()

	cfg := DefaultDirectToolAgentConfig()
	cfg.Endpoint = server.URL
	cfg.WorkDir = t.TempDir()
	cfg.MaxToolCalls = 3
	result, err := RunDirectToolAgent(cfg, "sys", "task", ToolsForRole("coder"), "")
	require.Error(t, err)
	require.Equal(t, DirectToolStopReasonToolBudget, result.StopReason)
	require.Equal(t, 3, callCount)
}

func TestRunDirectToolAgent_NaturalStopAtPreMutationCeilingGetsCorrectiveMutationTurn(t *testing.T) {
	requestCount := 0
	requestToolNames := make([][]string, 0, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		var req toolChatRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		names := make([]string, 0, len(req.Tools))
		for _, tool := range req.Tools {
			names = append(names, tool.Function.Name)
		}
		requestToolNames = append(requestToolNames, names)

		resp := toolChatResponse{}
		switch requestCount {
		case 1:
			resp.Choices = []toolChatChoice{{Message: toolChatMsg{
				Role: "assistant", Content: "The response was compacted before I made the change.",
			}, FinishReason: "stop"}}
		case 2:
			require.Equal(t, []string{"edit"}, names)
			conversation := ""
			for _, message := range req.Messages {
				conversation += message.Content
			}
			require.Contains(t, conversation, "single reserved corrective turn")
			resp.Choices = []toolChatChoice{{Message: toolChatMsg{Role: "assistant", ToolCalls: []toolCall{{
				ID: "corrective-edit", Type: "function",
				Function: toolCallFunction{Name: "edit", Arguments: `{"path":"existing.cpp","old":"original","new":"changed"}`},
			}}}, FinishReason: "tool_calls"}}
		default:
			resp.Choices = []toolChatChoice{{Message: toolChatMsg{Role: "assistant", Content: "done"}, FinishReason: "stop"}}
		}
		resp.Usage.PromptTokens = 60
		resp.Usage.CompletionTokens = 5
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	}))
	defer server.Close()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "existing.cpp"), []byte("original"), 0o644))
	cfg := DefaultDirectToolAgentConfig()
	cfg.Endpoint = server.URL
	cfg.WorkDir = dir
	cfg.MaxInputTokensBeforeMutation = 50
	cfg.MaxCumulativeInputTokens = 200
	cfg.ProtectExistingFiles = true
	cfg.ScopedFiles = []string{"existing.cpp"}

	result, err := RunDirectToolAgent(cfg, "sys", "make the scoped edit", ToolsForRole("coder"), "")
	require.NoError(t, err)
	require.True(t, result.MutationObserved)
	require.Equal(t, 3, requestCount)
	require.Equal(t, []string{"edit"}, requestToolNames[1])
	content, readErr := os.ReadFile(filepath.Join(dir, "existing.cpp"))
	require.NoError(t, readErr)
	require.Equal(t, "changed", string(content))
}

func TestRunDirectToolAgent_SecondNaturalStopAfterCorrectiveRequestFailsNoProgress(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		var req toolChatRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		if requestCount == 2 {
			names := make([]string, 0, len(req.Tools))
			for _, tool := range req.Tools {
				names = append(names, tool.Function.Name)
			}
			require.Equal(t, []string{"edit"}, names)
		}
		resp := toolChatResponse{Choices: []toolChatChoice{{Message: toolChatMsg{
			Role: "assistant", Content: "I did not make a change.",
		}, FinishReason: "end_of_turn"}}}
		resp.Usage.PromptTokens = 60
		resp.Usage.CompletionTokens = 5
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	}))
	defer server.Close()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "existing.cpp"), []byte("original"), 0o644))
	cfg := DefaultDirectToolAgentConfig()
	cfg.Endpoint = server.URL
	cfg.WorkDir = dir
	cfg.MaxInputTokensBeforeMutation = 50
	cfg.MaxCumulativeInputTokens = 200
	cfg.ProtectExistingFiles = true
	cfg.ScopedFiles = []string{"existing.cpp"}

	result, err := RunDirectToolAgent(cfg, "sys", "make the scoped edit", ToolsForRole("coder"), "")
	require.Error(t, err)
	require.Equal(t, DirectToolStopReasonNoProgress, result.StopReason)
	require.Contains(t, err.Error(), "reserved mutation-only corrective request")
	require.Equal(t, 2, requestCount)
}

func TestRunDirectToolAgent_ReadOnlyNaturalStopAtPreMutationCeilingRemainsSuccessful(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestCount++
		resp := toolChatResponse{Choices: []toolChatChoice{{Message: toolChatMsg{
			Role: "assistant", Content: "read-only validation complete",
		}, FinishReason: "stop"}}}
		resp.Usage.PromptTokens = 60
		resp.Usage.CompletionTokens = 5
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	}))
	defer server.Close()

	cfg := DefaultDirectToolAgentConfig()
	cfg.Endpoint = server.URL
	cfg.WorkDir = t.TempDir()
	cfg.MaxInputTokensBeforeMutation = 50
	cfg.AllowReadOnlyCompletion = true

	result, err := RunDirectToolAgent(cfg, "sys", "validate only", ToolsForRole("coder"), "")
	require.NoError(t, err)
	require.Equal(t, "read-only validation complete", result.Output)
	require.False(t, result.MutationObserved)
	require.Equal(t, 1, requestCount)
}

func TestRunDirectToolAgent_NaturalStopWinsOverCumulativeInputBudget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := toolChatResponse{
			Choices: []toolChatChoice{{Message: toolChatMsg{Role: "assistant", Content: "done"}, FinishReason: "stop"}},
			Usage: struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
				TotalTokens      int `json:"total_tokens"`
			}{PromptTokens: 120, CompletionTokens: 2, TotalTokens: 122},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := DefaultDirectToolAgentConfig()
	cfg.Endpoint = server.URL
	cfg.WorkDir = t.TempDir()
	cfg.MaxCumulativeInputTokens = 100
	result, err := RunDirectToolAgent(cfg, "sys", "user", nil, "")
	require.NoError(t, err)
	require.Equal(t, "done", result.Output)
	require.Empty(t, result.StopReason)
}

func TestRunDirectToolAgent_CumulativeInputBudgetPreservesPriorMutationOnUnexpectedFinish(t *testing.T) {
	request := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request++
		resp := toolChatResponse{}
		if request == 1 {
			resp.Choices = []toolChatChoice{{
				Message: toolChatMsg{Role: "assistant", ToolCalls: []toolCall{{
					ID: "write-1", Type: "function",
					Function: toolCallFunction{Name: "write", Arguments: `{"path":"checkpoint.txt","content":"preserved"}`},
				}}},
				FinishReason: "tool_calls",
			}}
			resp.Usage.PromptTokens = 60
			resp.Usage.CompletionTokens = 5
		} else {
			// Some OpenAI-compatible servers have returned an empty/unknown
			// finish reason after a complete tool turn. A prior mutation remains
			// a valid deterministic checkpoint when the cumulative cap is crossed.
			resp.Choices = []toolChatChoice{{Message: toolChatMsg{Role: "assistant", Content: "checkpointed"}}}
			resp.Usage.PromptTokens = 50
			resp.Usage.CompletionTokens = 2
		}
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	}))
	defer server.Close()

	dir := t.TempDir()
	cfg := DefaultDirectToolAgentConfig()
	cfg.Endpoint = server.URL
	cfg.WorkDir = dir
	cfg.MaxCumulativeInputTokens = 100

	result, err := RunDirectToolAgent(cfg, "sys", "user", ToolsForRole("coder"), "")
	require.NoError(t, err)
	require.Equal(t, DirectToolStopReasonTokenBudget, result.StopReason)
	require.True(t, result.MutationObserved)
	require.Equal(t, 110, result.TokensIn)
	content, readErr := os.ReadFile(filepath.Join(dir, "checkpoint.txt"))
	require.NoError(t, readErr)
	require.Equal(t, "preserved", string(content))
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

// ---------------------------------------------------------------------------
// Context-window monitoring tests
// ---------------------------------------------------------------------------

// TestRunDirectToolAgent_ContextMonitor_Disabled verifies that when
// ContextLimit is zero, no monitoring runs and the loop terminates normally
// even when prompt_tokens would otherwise blow past the warn/stop thresholds.
func TestRunDirectToolAgent_ContextMonitor_Disabled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := toolChatResponse{
			Choices: []toolChatChoice{
				{
					Message:      toolChatMsg{Role: "assistant", Content: "all done"},
					FinishReason: "stop",
				},
			},
			Usage: struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
				TotalTokens      int `json:"total_tokens"`
			}{PromptTokens: 999_999, CompletionTokens: 5, TotalTokens: 1_000_004},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := DefaultDirectToolAgentConfig()
	cfg.Endpoint = server.URL
	cfg.WorkDir = t.TempDir()
	// ContextLimit unset → monitoring disabled.

	result, err := RunDirectToolAgent(cfg, "sys", "user", nil, "")
	require.NoError(t, err)
	assert.Equal(t, "all done", result.Output)
	assert.Equal(t, 0, result.FinalContextPct, "FinalContextPct should be 0 when monitoring disabled")
	assert.Empty(t, result.StopReason)
}

// TestRunDirectToolAgent_ContextMonitor_FinalPctReported verifies that
// FinalContextPct reflects the most recent prompt_tokens / ContextLimit
// when the loop terminates via natural stop.
func TestRunDirectToolAgent_ContextMonitor_FinalPctReported(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := toolChatResponse{
			Choices: []toolChatChoice{
				{
					Message:      toolChatMsg{Role: "assistant", Content: "ok"},
					FinishReason: "stop",
				},
			},
			Usage: struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
				TotalTokens      int `json:"total_tokens"`
			}{PromptTokens: 500, CompletionTokens: 5, TotalTokens: 505},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := DefaultDirectToolAgentConfig()
	cfg.Endpoint = server.URL
	cfg.WorkDir = t.TempDir()
	cfg.ContextLimit = 1000

	result, err := RunDirectToolAgent(cfg, "sys", "user", nil, "")
	require.NoError(t, err)
	assert.Equal(t, 50, result.FinalContextPct, "500/1000 = 50%%")
	assert.Empty(t, result.StopReason)
}

// TestRunDirectToolAgent_ContextMonitor_StopThreshold verifies the loop
// halts with StopReason="context_limit" when prompt_tokens exceeds the stop
// threshold during a tool-calls turn.
func TestRunDirectToolAgent_ContextMonitor_StopThreshold(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Always request another tool call (would loop forever without monitor).
		resp := toolChatResponse{
			Choices: []toolChatChoice{
				{
					Message: toolChatMsg{
						Role: "assistant",
						ToolCalls: []toolCall{
							{
								ID:   "c1",
								Type: "function",
								Function: toolCallFunction{
									Name:      "bash",
									Arguments: `{"cmd":"echo hi"}`,
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
			}{PromptTokens: 960, CompletionTokens: 10, TotalTokens: 970},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := DefaultDirectToolAgentConfig()
	cfg.Endpoint = server.URL
	cfg.WorkDir = t.TempDir()
	cfg.MaxIterations = 50
	cfg.ContextLimit = 1000
	cfg.ContextWarnPct = 80
	cfg.ContextStopPct = 95

	result, err := RunDirectToolAgent(cfg, "sys", "user", ToolsForRole("coder"), "")
	require.NoError(t, err, "context-stop should not return an error (it is a safety halt)")
	assert.Equal(t, "context_limit", result.StopReason)
	assert.Equal(t, 96, result.FinalContextPct, "960/1000 = 96%%")
	assert.LessOrEqual(t, result.Iterations, 2, "loop should halt within first iteration after stop threshold crossed")
}

// TestRunDirectToolAgent_ContextMonitor_WarnInjectsSystemMessage verifies that
// when prompt_tokens crosses the warn threshold (but not stop), a system
// message nudging wrap-up is appended exactly once for subsequent iterations.
func TestRunDirectToolAgent_ContextMonitor_WarnInjectsSystemMessage(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "f.txt"), []byte("data"), 0o644))

	callCount := 0
	var sawWarnMsg bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++

		// Inspect the inbound messages for an injected warn system message.
		if callCount >= 2 {
			var reqBody toolChatRequest
			_ = json.NewDecoder(r.Body).Decode(&reqBody)
			for _, m := range reqBody.Messages {
				if m.Role == "system" && strings.Contains(m.Content, "[HARNESS]") &&
					strings.Contains(strings.ToLower(m.Content), "wrap up") {
					sawWarnMsg = true
				}
			}
		}

		var resp toolChatResponse
		switch callCount {
		case 1:
			// Tool call — push prompt usage above warn (85) but below stop (95).
			resp = toolChatResponse{
				Choices: []toolChatChoice{
					{
						Message: toolChatMsg{
							Role: "assistant",
							ToolCalls: []toolCall{
								{
									ID:   "c1",
									Type: "function",
									Function: toolCallFunction{
										Name:      "read",
										Arguments: `{"path":"f.txt"}`,
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
				}{PromptTokens: 880, CompletionTokens: 10, TotalTokens: 890},
			}
		default:
			// Wrap up.
			resp = toolChatResponse{
				Choices: []toolChatChoice{
					{
						Message:      toolChatMsg{Role: "assistant", Content: "wrapping up"},
						FinishReason: "stop",
					},
				},
				Usage: struct {
					PromptTokens     int `json:"prompt_tokens"`
					CompletionTokens int `json:"completion_tokens"`
					TotalTokens      int `json:"total_tokens"`
				}{PromptTokens: 890, CompletionTokens: 5, TotalTokens: 895},
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := DefaultDirectToolAgentConfig()
	cfg.Endpoint = server.URL
	cfg.WorkDir = dir
	cfg.MaxIterations = 5
	cfg.ContextLimit = 1000
	cfg.ContextWarnPct = 85
	cfg.ContextStopPct = 95

	result, err := RunDirectToolAgent(cfg, "sys", "do work", ToolsForRole("coder"), "")
	require.NoError(t, err)
	assert.Equal(t, "wrapping up", result.Output)
	assert.True(t, sawWarnMsg, "expected a system warn message to be injected for iteration 2")
	assert.Equal(t, 89, result.FinalContextPct, "890/1000 = 89%%")
	assert.Empty(t, result.StopReason, "natural stop should leave StopReason empty")
}

// TestRunDirectToolAgent_ContextMonitor_NaturalStopWinsOverStopPct verifies
// that a natural finish_reason="stop" returns successfully even if the prompt
// usage already crossed the stop threshold — no point in marking
// context_limit when the model produced a final answer on its own.
func TestRunDirectToolAgent_ContextMonitor_NaturalStopWinsOverStopPct(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := toolChatResponse{
			Choices: []toolChatChoice{
				{
					Message:      toolChatMsg{Role: "assistant", Content: "final answer"},
					FinishReason: "stop",
				},
			},
			Usage: struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
				TotalTokens      int `json:"total_tokens"`
			}{PromptTokens: 990, CompletionTokens: 5, TotalTokens: 995},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := DefaultDirectToolAgentConfig()
	cfg.Endpoint = server.URL
	cfg.WorkDir = t.TempDir()
	cfg.ContextLimit = 1000
	cfg.ContextStopPct = 95

	result, err := RunDirectToolAgent(cfg, "sys", "user", nil, "")
	require.NoError(t, err)
	assert.Equal(t, "final answer", result.Output)
	assert.Equal(t, 99, result.FinalContextPct)
	assert.Empty(t, result.StopReason, "natural stop should not flag context_limit")
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
