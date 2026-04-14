package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestToolsForRole_Prep verifies the prep role is restricted to read-only
// tools: read, grep, glob. No edit, write, or bash.
func TestToolsForRole_Prep(t *testing.T) {
	tools := ToolsForRole("prep")
	names := toolNames(tools)

	assert.Len(t, tools, 3, "prep role must have exactly 3 tools")
	assert.ElementsMatch(t, []string{"read", "grep", "glob"}, names)

	// Negative assertions: prep must NOT have any mutation/exec tools.
	assert.NotContains(t, names, "edit", "prep must not have edit")
	assert.NotContains(t, names, "write", "prep must not have write")
	assert.NotContains(t, names, "bash", "prep must not have bash")
}

// TestPrepSystemPrompt_Content verifies the prep system prompt instructs the
// model to produce PrepOutput-shaped JSON, describes the read-only toolset,
// lists the estimated files, and omits any mutation-tool instructions.
func TestPrepSystemPrompt_Content(t *testing.T) {
	opts := PrepPromptOpts{
		TaskTitle:       "add retry to classifier",
		TaskDescription: "wire exponential backoff into classifier dispatch",
		EstimatedFiles: []string{
			"internal/orchestrator/classifier.go",
			"internal/orchestrator/classifier_test.go",
		},
		WorkDir:           "/tmp/feature-worktree",
		ParentTitle:       "harden classifier",
		ParentDescription: "add resilience for flaky SGLang endpoint",
	}

	prompt := PrepSystemPrompt(opts)
	require.NotEmpty(t, prompt, "system prompt must not be empty")

	// Must describe the PrepOutput JSON schema the agent should emit.
	assert.Contains(t, prompt, "target_files", "prompt must reference target_files schema field")
	assert.Contains(t, prompt, "insertion_points", "prompt must reference insertion_points schema field")
	assert.Contains(t, prompt, "patterns_to_follow", "prompt must reference patterns_to_follow schema field")
	assert.Contains(t, prompt, "warnings", "prompt must reference warnings schema field")
	assert.Contains(t, prompt, "constructors", "prompt must reference constructors schema field")

	// Must list the estimated files so the model knows where to focus.
	assert.Contains(t, prompt, "internal/orchestrator/classifier.go")
	assert.Contains(t, prompt, "internal/orchestrator/classifier_test.go")

	// Must mention the read-only tool names.
	assert.Contains(t, prompt, "read")
	assert.Contains(t, prompt, "grep")
	assert.Contains(t, prompt, "glob")

	// Must include task title and description.
	assert.Contains(t, prompt, opts.TaskTitle)
	assert.Contains(t, prompt, opts.TaskDescription)

	// Must NOT instruct the model to edit or write files.
	lower := strings.ToLower(prompt)
	assert.NotContains(t, lower, "edit tool")
	assert.NotContains(t, lower, "write tool")
	// Must emphasize read-only.
	assert.Contains(t, lower, "read-only")
}

// TestPrepSystemPrompt_ParentContextIncluded verifies parent context is
// propagated when present.
func TestPrepSystemPrompt_ParentContextIncluded(t *testing.T) {
	opts := PrepPromptOpts{
		TaskTitle:         "subtask title",
		TaskDescription:   "subtask body",
		EstimatedFiles:    []string{"x.go"},
		ParentTitle:       "PARENT_TITLE_MARKER",
		ParentDescription: "PARENT_DESC_MARKER",
	}
	prompt := PrepSystemPrompt(opts)
	assert.Contains(t, prompt, "PARENT_TITLE_MARKER")
	assert.Contains(t, prompt, "PARENT_DESC_MARKER")
}

// TestRunDirectPrep_ValidOutput verifies RunDirectPrep returns a result with
// TokensIn/TokensOut populated when the SGLang mock returns a valid
// PrepOutput JSON body with finish_reason=stop.
func TestRunDirectPrep_ValidOutput(t *testing.T) {
	validPrepJSON := `{
		"target_files": [
			{"path": "internal/foo/bar.go", "relevant_definitions": "type Foo struct", "methods": ["NewFoo"], "notes": ""}
		],
		"insertion_points": [],
		"patterns_to_follow": [],
		"warnings": [],
		"constructors": []
	}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := toolChatResponse{
			Choices: []toolChatChoice{
				{
					Message:      toolChatMsg{Role: "assistant", Content: validPrepJSON},
					FinishReason: "stop",
				},
			},
			Usage: struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
				TotalTokens      int `json:"total_tokens"`
			}{
				PromptTokens:     500,
				CompletionTokens: 150,
				TotalTokens:      650,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	workDir := t.TempDir()
	outputPath := filepath.Join(workDir, "task-prep.json")

	cfg := DirectPrepConfig{DirectToolAgentConfig: DefaultDirectToolAgentConfig()}
	cfg.Endpoint = server.URL
	cfg.WorkDir = workDir

	opts := PrepPromptOpts{
		TaskTitle:       "test task",
		TaskDescription: "test desc",
		EstimatedFiles:  []string{"main.go"},
		WorkDir:         workDir,
	}

	result, err := RunDirectPrep(cfg, opts, outputPath)
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Equal(t, 500, result.TokensIn, "TokensIn should reflect mock prompt tokens")
	assert.Equal(t, 150, result.TokensOut, "TokensOut should reflect mock completion tokens")
	assert.Equal(t, 1, result.Iterations, "should complete in 1 iteration")

	// The output file should have been written with the raw JSON.
	data, readErr := os.ReadFile(outputPath)
	require.NoError(t, readErr)
	assert.Contains(t, string(data), "target_files", "output file should contain PrepOutput JSON")
}

// TestRunDirectPrep_MalformedOutputStillReturnsResult verifies that garbage
// content is still returned as the raw output (the caller is responsible for
// parsing PrepOutput JSON). RunDirectPrep itself only errors on transport or
// API errors, not on content shape.
//
// This matches the task spec: "RunDirectPrep returns *DirectToolAgentResult
// only; caller parses PrepOutput from the output file."
func TestRunDirectPrep_MalformedOutputStillReturnsResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := toolChatResponse{
			Choices: []toolChatChoice{
				{
					Message:      toolChatMsg{Role: "assistant", Content: "not valid json {{{"},
					FinishReason: "stop",
				},
			},
			Usage: struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
				TotalTokens      int `json:"total_tokens"`
			}{PromptTokens: 100, CompletionTokens: 10, TotalTokens: 110},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	workDir := t.TempDir()
	outputPath := filepath.Join(workDir, "task-prep.json")

	cfg := DirectPrepConfig{DirectToolAgentConfig: DefaultDirectToolAgentConfig()}
	cfg.Endpoint = server.URL
	cfg.WorkDir = workDir

	opts := PrepPromptOpts{
		TaskTitle:       "test task",
		TaskDescription: "test desc",
		EstimatedFiles:  []string{"main.go"},
		WorkDir:         workDir,
	}

	// RunDirectPrep should succeed at the transport level and return the raw
	// garbage. The orchestrator layer is responsible for detecting malformed
	// JSON and degrading gracefully.
	result, err := RunDirectPrep(cfg, opts, outputPath)
	require.NoError(t, err, "transport-level success should not error on garbage content")
	require.NotNil(t, result)

	// Output file should have been written with the raw (garbage) content.
	data, readErr := os.ReadFile(outputPath)
	require.NoError(t, readErr)
	assert.Equal(t, "not valid json {{{", string(data))

	// Caller parses the file; verify the orchestrator-layer contract holds:
	// unmarshaling the garbage fails.
	var parsed PrepOutputShape
	parseErr := json.Unmarshal(data, &parsed)
	assert.Error(t, parseErr, "malformed output should fail to parse as PrepOutput")
}

// TestRunDirectPrep_APIErrorReturnsError verifies that a 500 response from
// the mock SGLang endpoint surfaces as a Go error.
func TestRunDirectPrep_APIErrorReturnsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"upstream unavailable"}`))
	}))
	defer server.Close()

	workDir := t.TempDir()
	outputPath := filepath.Join(workDir, "task-prep.json")

	cfg := DirectPrepConfig{DirectToolAgentConfig: DefaultDirectToolAgentConfig()}
	cfg.Endpoint = server.URL
	cfg.WorkDir = workDir

	opts := PrepPromptOpts{
		TaskTitle:       "test task",
		TaskDescription: "test desc",
		EstimatedFiles:  []string{"main.go"},
		WorkDir:         workDir,
	}

	_, err := RunDirectPrep(cfg, opts, outputPath)
	require.Error(t, err, "API 500 should surface as an error")
}

// PrepOutputShape is a minimal local mirror of orchestrator.PrepOutput for
// the test-side parse check. We cannot import the orchestrator package here
// (it would create a cycle: orchestrator already imports agent). Keeping
// this shape local to the test file is intentional — direct_prep.go itself
// does not depend on any orchestrator-layer types.
type PrepOutputShape struct {
	TargetFiles []struct {
		Path string `json:"path"`
	} `json:"target_files"`
}
