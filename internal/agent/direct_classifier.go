// Package agent manages Claude Code agent lifecycles.
//
// direct_classifier.go implements a synchronous classifier that calls SGLang's
// OpenAI-compatible API directly, bypassing the OpenCode subprocess. This cuts
// token usage from ~42K to ~1.2-3.3K per classification.
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
	"path/filepath"
	"time"

	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

// DirectClassifierConfig holds connection and generation parameters for the
// direct SGLang classifier.
type DirectClassifierConfig struct {
	Endpoint    string        // Full URL for chat completions endpoint
	Model       string        // Model identifier sent in the API request
	MaxTokens   int           // Maximum tokens in the response
	Temperature float64       // Sampling temperature (low = deterministic)
	Timeout     time.Duration // HTTP request timeout
}

// DefaultDirectClassifierConfig returns a config targeting the local SGLang
// server with sensible defaults.
func DefaultDirectClassifierConfig() DirectClassifierConfig {
	return DirectClassifierConfig{
		Endpoint:    "http://localhost:8081/v1/chat/completions",
		Model:       "qwen3-coder-30b",
		MaxTokens:   1024,
		Temperature: 0.1,
		Timeout:     60 * time.Second,
	}
}

// ---------------------------------------------------------------------------
// Result
// ---------------------------------------------------------------------------

// ClassifyInput is the reusable, file-system-free input to Classify. Callers
// that want the legacy "write classification-<taskID>.json" behavior use
// RunDirectClassifier, which wraps Classify.
type ClassifyInput struct {
	TaskID      uuid.UUID      // Identifies the task being classified
	Title       string         // Task title
	Description string         // Task description
	Context     map[string]any // Task context; internal bookkeeping keys are filtered out
}

// ClassifyResult is the outcome of a direct classification call. It carries
// the raw LLM JSON payload alongside telemetry so upstream callers (legacy
// file-writing or HTTP-serving) can do their own persistence.
type ClassifyResult struct {
	JSON      []byte        // Parsed+validated classification JSON (pretty-printed)
	TokensIn  int           // Prompt tokens consumed
	TokensOut int           // Completion tokens generated
	Duration  time.Duration // Wall-clock time for the API call
}

// DirectClassifierResult holds the outcome of a direct classification call.
// It is the legacy shape returned by RunDirectClassifier; new callers should
// prefer ClassifyResult via Classify.
type DirectClassifierResult struct {
	OutputPath string        // Path to the written classification JSON file
	TokensIn   int           // Prompt tokens consumed
	TokensOut  int           // Completion tokens generated
	Duration   time.Duration // Wall-clock time for the API call
}

// ---------------------------------------------------------------------------
// System prompt
// ---------------------------------------------------------------------------

// classifierSystemPrompt is the system prompt for the direct classifier. It
// contains classification criteria, output format, and the complexity scale.
// This replaces the full prompt.Generate() output (~42K tokens) with a focused
// prompt (~800 tokens).
const classifierSystemPrompt = `You are a task classifier for a software project orchestrator. Your job is to classify incoming tasks by category and complexity based on their title and description.

## Classification Categories

- **quickfix** (complexity 1-3): Single-file change, clear fix, no architectural impact. Examples: fix a typo, update a constant, add a nil check, rename a variable.
- **standard** (complexity 4-10): Multi-file change, design decisions needed, or broad impact. Examples: new feature, refactor across packages, change a data model, add a new agent type.

## Complexity Scale (1-10)

1-2: Trivial -- one function, one file, obvious fix
3-4: Simple -- one package, clear pattern to follow
5-6: Moderate -- 2-4 files across 1-2 packages, some design decisions
7-8: Complex -- multiple packages, interface changes, test updates needed
9-10: Major -- architectural change, new subsystem, cross-cutting concern

## Output Format

Respond with ONLY a JSON object. No markdown fences, no explanation, no commentary.

Use this schema:

{
  "category": "quickfix" or "standard",
  "complexity_score": 1-10,
  "title": "Refined task title based on analysis",
  "description": "Enriched description with implementation specifics",
  "target_files": ["path/to/file1.go", "path/to/file2.go"],
  "rationale": "Evidence-based explanation for the classification"
}

If the task is too ambiguous to classify, output:

{
  "needs_clarification": true,
  "questions": ["Specific question 1", "Specific question 2"]
}

## Rules

1. Respond with ONLY valid JSON. No markdown, no code fences, no commentary.
2. The "target_files" field should list files you expect need modification based on the task description. Infer from package names, function names, and file naming conventions mentioned in the task.
3. Keep the "description" field concise but more specific than the original.
4. The "rationale" field should explain WHY you chose the category and complexity score.`

// ---------------------------------------------------------------------------
// User message construction
// ---------------------------------------------------------------------------

// internalContextKeys are task context keys that should be filtered out of
// the user message because they are internal orchestrator bookkeeping.
var internalContextKeys = map[string]bool{
	"retry_count":             true,
	"schedule":                true,
	"baseline_tests_checked":  true,
	"human_triage":            true,
	"classifier_error":        true,
	"empty_work":              true,
	"prompt_adjustment":       true,
	"clarification_context":   true,
	"clarification_session":   true,
	"clarification_questions": true,
	"scores":                  true,
	"plan_validation":         true,
	"constraint_violations":   true,
	"paused_from":             true,
	"diagnostic_required":     true,
	"test_rejection_count":    true,
}

// buildClassifierUserMessage constructs the user message from the task title,
// description, and filtered context.
func buildClassifierUserMessage(title, description string, ctx map[string]any) string {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "# Task to Classify\n\n**Title**: %s\n\n**Description**:\n%s", title, description)

	if len(ctx) > 0 {
		hasRelevant := false
		for k := range ctx {
			if !internalContextKeys[k] {
				hasRelevant = true
				break
			}
		}
		if hasRelevant {
			buf.WriteString("\n\n## Additional Context\n")
			for k, v := range ctx {
				if internalContextKeys[k] {
					continue
				}
				fmt.Fprintf(&buf, "\n- **%s**: %v", k, v)
			}
		}
	}

	return buf.String()
}

// ---------------------------------------------------------------------------
// OpenAI-compatible API types
// ---------------------------------------------------------------------------

type chatRequest struct {
	Model          string          `json:"model"`
	Messages       []chatMessage   `json:"messages"`
	MaxTokens      int             `json:"max_tokens"`
	Temperature    float64         `json:"temperature"`
	ResponseFormat *chatRespFormat `json:"response_format,omitempty"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRespFormat struct {
	Type string `json:"type"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// ---------------------------------------------------------------------------
// Core implementation
// ---------------------------------------------------------------------------

// Classify calls the SGLang-compatible /v1/chat/completions endpoint and
// returns the parsed classification JSON plus token-usage telemetry. Unlike
// RunDirectClassifier it does not touch the filesystem — callers that want
// the legacy "classification-<taskID>.json" artifact must write it themselves
// (or use the RunDirectClassifier wrapper). This split lets the warm
// drem-classifier container reuse the same prompt / parse / validate logic
// that the inline orch path uses.
//
// The ctx parameter is propagated to the HTTP request so callers can bound
// the call with a deadline independent of cfg.Timeout.
func Classify(ctx context.Context, cfg DirectClassifierConfig, in ClassifyInput) (*ClassifyResult, error) {
	start := time.Now()

	userMsg := buildClassifierUserMessage(in.Title, in.Description, in.Context)

	reqBody := chatRequest{
		Model: cfg.Model,
		Messages: []chatMessage{
			{Role: "system", Content: classifierSystemPrompt},
			{Role: "user", Content: userMsg},
		},
		MaxTokens:      cfg.MaxTokens,
		Temperature:    cfg.Temperature,
		ResponseFormat: &chatRespFormat{Type: "json_object"},
	}

	reqJSON, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("direct classifier: marshal request: %w", err)
	}

	slog.Info("direct classifier: calling SGLang API",
		"task_id", in.TaskID,
		"endpoint", cfg.Endpoint,
		"model", cfg.Model,
		"prompt_len", len(userMsg),
	)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.Endpoint, bytes.NewReader(reqJSON))
	if err != nil {
		return nil, fmt.Errorf("direct classifier: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: cfg.Timeout}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("direct classifier: API call failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("direct classifier: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("direct classifier: API returned status %d: %s", resp.StatusCode, truncateBody(body, 500))
	}

	var chatResp chatResponse
	if err := json.Unmarshal(body, &chatResp); err != nil {
		return nil, fmt.Errorf("direct classifier: parse response: %w", err)
	}

	if len(chatResp.Choices) == 0 {
		return nil, fmt.Errorf("direct classifier: no choices in response")
	}

	content := chatResp.Choices[0].Message.Content
	if content == "" {
		return nil, fmt.Errorf("direct classifier: empty response content (finish_reason: %s)",
			chatResp.Choices[0].FinishReason)
	}

	// Validate that the response is valid JSON before handing it to callers.
	var jsonCheck json.RawMessage
	if err := json.Unmarshal([]byte(content), &jsonCheck); err != nil {
		return nil, fmt.Errorf("direct classifier: response is not valid JSON: %w\nraw: %s",
			err, truncateBody([]byte(content), 500))
	}

	// Pretty-print the JSON for consistency with agent-produced files.
	var prettyJSON bytes.Buffer
	if err := json.Indent(&prettyJSON, []byte(content), "", "  "); err != nil {
		prettyJSON.Reset()
		prettyJSON.WriteString(content)
	}

	duration := time.Since(start)

	slog.Info("direct classifier: completed",
		"task_id", in.TaskID,
		"duration", duration.Round(time.Millisecond),
		"tokens_in", chatResp.Usage.PromptTokens,
		"tokens_out", chatResp.Usage.CompletionTokens,
		"finish_reason", chatResp.Choices[0].FinishReason,
	)

	return &ClassifyResult{
		JSON:      prettyJSON.Bytes(),
		TokensIn:  chatResp.Usage.PromptTokens,
		TokensOut: chatResp.Usage.CompletionTokens,
		Duration:  duration,
	}, nil
}

// RunDirectClassifier calls the SGLang API directly to classify a task and
// writes the classification JSON to outputDir/classification-<taskID>.json.
// The output file path matches what onClassifierCompleted reads from:
//
//	filepath.Join(ag.WorktreePath, fmt.Sprintf("classification-%s.json", task.ID))
//
// This is a thin wrapper around Classify that preserves the file-based
// handoff the inline orch path relies on. New integrations should use
// Classify directly and handle persistence themselves.
func RunDirectClassifier(cfg DirectClassifierConfig, taskID uuid.UUID, title, description string, taskContext map[string]any, outputDir string) (*DirectClassifierResult, error) {
	result, err := Classify(context.Background(), cfg, ClassifyInput{
		TaskID:      taskID,
		Title:       title,
		Description: description,
		Context:     taskContext,
	})
	if err != nil {
		return nil, err
	}

	// Write classification output file at the exact path onClassifierCompleted expects.
	outputPath := filepath.Join(outputDir, fmt.Sprintf("classification-%s.json", taskID))
	if err := os.WriteFile(outputPath, result.JSON, 0o644); err != nil {
		return nil, fmt.Errorf("direct classifier: write output: %w", err)
	}

	slog.Info("direct classifier: wrote classification file",
		"task_id", taskID,
		"output_path", outputPath,
	)

	return &DirectClassifierResult{
		OutputPath: outputPath,
		TokensIn:   result.TokensIn,
		TokensOut:  result.TokensOut,
		Duration:   result.Duration,
	}, nil
}

// truncateBody returns at most maxLen bytes from b as a string.
func truncateBody(b []byte, maxLen int) string {
	if len(b) <= maxLen {
		return string(b)
	}
	return string(b[:maxLen])
}
