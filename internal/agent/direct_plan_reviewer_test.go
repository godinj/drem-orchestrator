package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Config defaults
// ---------------------------------------------------------------------------

func TestDefaultDirectPlanReviewerConfig(t *testing.T) {
	cfg := DefaultDirectPlanReviewerConfig()
	assert.Equal(t, "http://localhost:8081/v1/chat/completions", cfg.Endpoint)
	assert.Equal(t, "qwen3-coder-30b", cfg.Model)
	assert.Equal(t, 1024, cfg.MaxTokens)
	assert.InDelta(t, 0.1, cfg.Temperature, 1e-9)
	assert.Equal(t, 120*time.Second, cfg.Timeout)
	assert.Equal(t, "reviewer", cfg.GQCaller)
	assert.Equal(t, "high", cfg.GQPriority)
	assert.Equal(t, map[string]any{"enable_thinking": false}, cfg.ChatTemplateKwargs)
}

// ---------------------------------------------------------------------------
// User message construction
// ---------------------------------------------------------------------------

func TestBuildPlanReviewerUserMessage(t *testing.T) {
	title := "Add caching layer"
	desc := "Introduce a write-through cache in front of the metrics store."
	planJSON := `{"subtasks":[{"id":0,"title":"design","description":"x"}]}`

	msg := buildPlanReviewerUserMessage(title, desc, planJSON)

	assert.Contains(t, msg, "# Task")
	assert.Contains(t, msg, title)
	assert.Contains(t, msg, desc)
	assert.Contains(t, msg, "## Plan")
	// Plan should be code-fenced.
	assert.Contains(t, msg, "```json")
	assert.Contains(t, msg, planJSON)
	assert.Contains(t, msg, "```")
}

// ---------------------------------------------------------------------------
// System prompt content
// ---------------------------------------------------------------------------

func TestPlanReviewerSystemPrompt(t *testing.T) {
	// Verify all required keywords appear in the system prompt so the model
	// outputs the schema onReviewerCompleted expects.
	for _, needle := range []string{
		"review.json",
		"coverage",
		"recommendation",
		"approve",
		"revise",
		"reject",
		"phase` is exactly `implementation",
		"wiring-only `integration`",
		"zero-based subtask indices",
		"tests_for: [1]",
		"Any actionable issue requires",
		"Source-backed entrypoint chain",
		"production entrypoint",
		"exactly one semantic module boundary",
		"at most two files",
	} {
		assert.Contains(t, planReviewerSystemPrompt, needle,
			"system prompt missing keyword %q", needle)
	}
}

// ---------------------------------------------------------------------------
// RunDirectPlanReviewer — happy path
// ---------------------------------------------------------------------------

func TestRunDirectPlanReviewer_Success(t *testing.T) {
	reviewJSON := `{
		"coverage": "full",
		"uncovered_criteria": [],
		"file_overlap_risk": "low",
		"overlapping_pairs": [],
		"integration_gap": false,
		"tdd_assessment": {"test_coverage_adequate": true, "exceptions_justified": true, "issues": []},
		"issues": [],
		"recommendation": "approve"
	}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "reviewer", r.Header.Get("X-GQ-Caller"))
		assert.Equal(t, "high", r.Header.Get("X-GQ-Priority"))
		var request chatRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		assert.Equal(t, map[string]any{"enable_thinking": false}, request.ChatTemplateKwargs)
		resp := chatResponse{
			Choices: []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
				FinishReason string `json:"finish_reason"`
			}{
				{
					Message: struct {
						Content string `json:"content"`
					}{Content: reviewJSON},
					FinishReason: "stop",
				},
			},
			Usage: struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
				TotalTokens      int `json:"total_tokens"`
			}{PromptTokens: 1200, CompletionTokens: 220, TotalTokens: 1420},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := DefaultDirectPlanReviewerConfig()
	cfg.Endpoint = server.URL
	cfg.Timeout = 5 * time.Second

	dir := t.TempDir()
	taskID := uuid.New()
	result, err := RunDirectPlanReviewer(cfg, taskID, "Title", "Description", `{"subtasks":[]}`, dir)
	require.NoError(t, err)
	require.NotNil(t, result)

	expectedPath := filepath.Join(dir, "review.json")
	assert.Equal(t, expectedPath, result.OutputPath)
	assert.Equal(t, 1200, result.TokensIn)
	assert.Equal(t, 220, result.TokensOut)
	assert.Greater(t, result.Duration, time.Duration(0))
	assert.Equal(t, "stop", result.FinishReason)
	assert.Equal(t, len(reviewJSON), result.ContentBytes)

	// File written with expected content.
	data, err := os.ReadFile(expectedPath)
	require.NoError(t, err)
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(data, &parsed))
	assert.Equal(t, "approve", parsed["recommendation"])
	assert.Equal(t, "full", parsed["coverage"])
}

// ---------------------------------------------------------------------------
// RunDirectPlanReviewer — invalid JSON in response content
// ---------------------------------------------------------------------------

func TestRunDirectPlanReviewer_InvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := chatResponse{
			Choices: []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
				FinishReason string `json:"finish_reason"`
			}{
				{
					Message: struct {
						Content string `json:"content"`
					}{Content: "not actually JSON, just prose"},
					FinishReason: "stop",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := DefaultDirectPlanReviewerConfig()
	cfg.Endpoint = server.URL
	cfg.Timeout = 5 * time.Second

	dir := t.TempDir()
	result, err := RunDirectPlanReviewer(cfg, uuid.New(), "T", "D", "{}", dir)
	require.Error(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "invalid_json", DirectReviewFailureCode(err))
	assert.Contains(t, err.Error(), "not valid JSON")
}

// ---------------------------------------------------------------------------
// RunDirectPlanReviewer — empty content
// ---------------------------------------------------------------------------

func TestRunDirectPlanReviewer_EmptyResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := chatResponse{
			Choices: []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
				FinishReason string `json:"finish_reason"`
			}{
				{
					Message: struct {
						Content string `json:"content"`
					}{Content: ""},
					FinishReason: "length",
				},
			},
			Usage: struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
				TotalTokens      int `json:"total_tokens"`
			}{PromptTokens: 700, CompletionTokens: 1024, TotalTokens: 1724},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := DefaultDirectPlanReviewerConfig()
	cfg.Endpoint = server.URL
	cfg.Timeout = 5 * time.Second

	dir := t.TempDir()
	result, err := RunDirectPlanReviewer(cfg, uuid.New(), "T", "D", "{}", dir)
	require.Error(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 700, result.TokensIn)
	assert.Equal(t, 1024, result.TokensOut)
	assert.Equal(t, "length", result.FinishReason)
	assert.Equal(t, "empty_visible_completion", DirectReviewFailureCode(err))
	assert.True(t,
		strings.Contains(err.Error(), "empty") || strings.Contains(err.Error(), "no choices"),
		"expected empty/no-choices error, got: %v", err)
}

// ---------------------------------------------------------------------------
// RunDirectPlanReviewer — API HTTP error
// ---------------------------------------------------------------------------

func TestRunDirectPlanReviewer_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"backend exploded"}`))
	}))
	defer server.Close()

	cfg := DefaultDirectPlanReviewerConfig()
	cfg.Endpoint = server.URL
	cfg.Timeout = 5 * time.Second

	dir := t.TempDir()
	_, err := RunDirectPlanReviewer(cfg, uuid.New(), "T", "D", "{}", dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "status")
	assert.Contains(t, err.Error(), "500")
}

// TestRunDirectPlanReviewer_LiveCanary is opt-in because it uses the operator's
// loopback tunnel. Its fixture is Canvas-shaped but contains no checkout data.
func TestRunDirectPlanReviewer_LiveCanary(t *testing.T) {
	endpoint := os.Getenv("DREM_LIVE_REVIEWER_ENDPOINT")
	if endpoint == "" {
		t.Skip("set DREM_LIVE_REVIEWER_ENDPOINT to run the remote reviewer canary")
	}
	cfg := DefaultDirectPlanReviewerConfig()
	cfg.Endpoint = endpoint
	if model := os.Getenv("DREM_LIVE_REVIEWER_MODEL"); model != "" {
		cfg.Model = model
	}
	cfg.Timeout = 120 * time.Second
	description := `Compact verified Canvas contract: acceptance criteria cover stable interior transient splits, reverse/stretch mapping, one undo transaction, command registration, and focused tests. Scope includes the model API, action implementation, ActionCoordinator registration, keymap, manifests, and integration test.`
	plan := `{"subtasks":[
		{"phase":"test","description":"Add focused stable-interior transient, reverse/stretch mapping, locked/invalid input, multi-event transaction, and undo regressions.","files":["tests/integration/test_audio_transient_slicing.cpp"],"tests_for":[1]},
		{"phase":"implementation","description":"Implement bounded transient slicing and event geometry mapping.","files":["src/model/AudioClip.h","src/model/AudioClipTransientSlicing.cpp"],"dependencies":[0],"module_boundaries":[{"package":"model","description":"owns slicing math","exports":1}],"interface_contracts":[{"kind":"cpp_function","state":"planned","owner_file":"src/model/AudioClip.h","signature":"divideAtTransients(settings)"}]},
		{"phase":"test","description":"Verify the visible command is registered, routed, key-bound, and invokes the selected-event slicing transaction.","files":["tests/integration/test_audio_transient_slicing.cpp"],"tests_for":[3],"dependencies":[0]},
		{"phase":"implementation","description":"Implement the audio action callback against the planned model API.","files":["src/ui/ActionAudioProcesses.cpp"],"dependencies":[2],"module_boundaries":[{"package":"ui","description":"owns command callback","exports":1}],"interface_contracts":[{"kind":"registry_action","state":"planned","owner_file":"src/ui/ActionAudioProcesses.cpp","action_id":"clip.divide-transients"}]},
		{"phase":"integration","description":"Wire registerAllActions to the audio process registry, add the key route and manifests, then run native plus Computer Use verification through the production command entrypoint.","files":["src/model/AudioClip.h","src/model/AudioClipTransientSlicing.cpp","src/ui/ActionAudioProcesses.cpp","src/ui/ActionCoordinator.cpp","config/default_keymap.yaml","tests/integration/test_audio_transient_slicing.cpp","cmake/DremCanvasSources.cmake","tests/cmake/IntegrationSources.cmake"],"writable_files":["src/ui/ActionCoordinator.cpp","config/default_keymap.yaml","cmake/DremCanvasSources.cmake","tests/cmake/IntegrationSources.cmake"],"dependencies":[1,3]}
	]}`

	result, err := RunDirectPlanReviewer(cfg, uuid.New(), "Divide audio events at transients", description, plan, t.TempDir())
	require.NoError(t, err)
	require.Equal(t, "stop", result.FinishReason)
	require.Positive(t, result.TokensIn)
	require.Positive(t, result.TokensOut)
	data, err := os.ReadFile(result.OutputPath)
	require.NoError(t, err)
	var review map[string]any
	require.NoError(t, json.Unmarshal(data, &review))
	require.Equal(t, "approve", review["recommendation"], "review: %#v", review)
}
