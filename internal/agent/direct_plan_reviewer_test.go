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
	assert.Equal(t, 2048, cfg.MaxTokens)
	assert.InDelta(t, 0.1, cfg.Temperature, 1e-9)
	assert.Equal(t, 120*time.Second, cfg.Timeout)
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
	_, err := RunDirectPlanReviewer(cfg, uuid.New(), "T", "D", "{}", dir)
	require.Error(t, err)
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
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := DefaultDirectPlanReviewerConfig()
	cfg.Endpoint = server.URL
	cfg.Timeout = 5 * time.Second

	dir := t.TempDir()
	_, err := RunDirectPlanReviewer(cfg, uuid.New(), "T", "D", "{}", dir)
	require.Error(t, err)
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
