// Package agent manages Claude Code agent lifecycles.
//
// direct_plan_reviewer.go implements a synchronous plan reviewer that calls
// SGLang's OpenAI-compatible API directly, bypassing the OpenCode subprocess.
// This cuts token usage from ~40K to a few thousand per review and eliminates
// tool-loop overhead — plan review is read-only and does not need tools.
package agent

import (
	"bytes"
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

// DirectPlanReviewerConfig holds connection and generation parameters for the
// direct SGLang plan reviewer.
type DirectPlanReviewerConfig struct {
	Endpoint    string        // Full URL for chat completions endpoint
	Model       string        // Model identifier sent in the API request
	MaxTokens   int           // Maximum tokens in the response
	Temperature float64       // Sampling temperature (low = deterministic)
	Timeout     time.Duration // HTTP request timeout
}

// DefaultDirectPlanReviewerConfig returns a config targeting the local SGLang
// server with sensible defaults for plan review.
func DefaultDirectPlanReviewerConfig() DirectPlanReviewerConfig {
	return DirectPlanReviewerConfig{
		Endpoint:    "http://localhost:8081/v1/chat/completions",
		Model:       "gemma4-26b",
		MaxTokens:   2048,
		Temperature: 0.1,
		Timeout:     120 * time.Second,
	}
}

// ---------------------------------------------------------------------------
// Result
// ---------------------------------------------------------------------------

// DirectPlanReviewerResult holds the outcome of a direct plan review call.
type DirectPlanReviewerResult struct {
	OutputPath string        // Path to the written review.json file
	TokensIn   int           // Prompt tokens consumed
	TokensOut  int           // Completion tokens generated
	Duration   time.Duration // Wall-clock time for the API call
}

// ---------------------------------------------------------------------------
// System prompt
// ---------------------------------------------------------------------------

// planReviewerSystemPrompt is the system prompt for the direct plan reviewer.
// It is condensed from prompt.planReviewerInstructions and pins the exact
// review.json schema that onReviewerCompleted parses.
const planReviewerSystemPrompt = `You are a plan reviewer agent for a software project orchestrator. A planner has produced a decomposition plan for a task. Your job is to evaluate the plan against the task's acceptance criteria and surface structural problems before any code is written.

## Review Criteria

Evaluate the plan for:

1. **Coverage**: Does every acceptance criterion from the task description have at least one subtask addressing it? List any uncovered criteria verbatim.
2. **File overlap risk**: Do subtasks share files? High overlap causes merge conflicts and serialized execution. Flag pairs of subtasks that touch the same files.
3. **Integration gap**: Is there a final integration subtask that wires the pieces together? A plan that produces isolated components with no wiring subtask has an integration gap.
4. **TDD structure**: Does every implementation subtask have a corresponding test subtask with ` + "`" + `tests_for` + "`" + `? Are test descriptions specific about the behavior they verify? Are any TDD exceptions justified (integration wiring / research are valid; "too hard to test" is not)?
5. **Depth**: Does each implementation subtask include ` + "`" + `module_boundaries` + "`" + ` and ` + "`" + `interface_shapes` + "`" + `? Are modules deep (rich internal logic, few exports) rather than shallow pass-through wrappers? Flag subtasks that lack depth metadata or define trivial wrappers.
6. **Decomposition quality**: Are subtasks sized appropriately (3-6 is typical)? Are dependencies between subtasks correct?

## Output Format

Respond with ONLY a JSON object matching this exact schema. No markdown fences, no prose, no commentary.

{
  "coverage": "full" | "partial" | "none",
  "uncovered_criteria": ["criterion text not addressed by any subtask"],
  "file_overlap_risk": "low" | "medium" | "high",
  "overlapping_pairs": [{"a": 0, "b": 2, "files": ["shared.go"]}],
  "integration_gap": true,
  "tdd_assessment": {
    "test_coverage_adequate": true,
    "exceptions_justified": true,
    "issues": ["Test subtask N only tests happy path, missing edge cases for..."]
  },
  "issues": ["issue description"],
  "recommendation": "approve" | "revise" | "reject"
}

## Rules

1. Respond with ONLY valid JSON. No markdown, no code fences, no commentary.
2. ` + "`" + `recommendation` + "`" + ` must be one of: approve, revise, reject.
3. Use ` + "`" + `revise` + "`" + ` when the plan is structurally salvageable with edits; use ` + "`" + `reject` + "`" + ` when it needs fundamental redesign.
4. Do not modify any code. Your only output is the review.json content described above.`

// ---------------------------------------------------------------------------
// User message construction
// ---------------------------------------------------------------------------

// buildPlanReviewerUserMessage constructs the user message from the task's
// title, description, and the plan JSON produced by the planner.
func buildPlanReviewerUserMessage(title, description, planJSON string) string {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "# Task\n\n**Title**: %s\n\n**Description**:\n%s\n\n", title, description)
	buf.WriteString("## Plan\n\n")
	buf.WriteString("```json\n")
	buf.WriteString(planJSON)
	if len(planJSON) == 0 || planJSON[len(planJSON)-1] != '\n' {
		buf.WriteByte('\n')
	}
	buf.WriteString("```\n")
	return buf.String()
}

// ---------------------------------------------------------------------------
// Core implementation
// ---------------------------------------------------------------------------

// RunDirectPlanReviewer calls the SGLang API directly to review a plan and
// writes the review JSON to outputDir/review.json. The output filename is
// the exact path onReviewerCompleted reads from:
//
//	filepath.Join(ag.WorktreePath, "review.json")
//
// The function reuses chatRequest/chatMessage/chatResponse/chatRespFormat
// types defined in direct_classifier.go — do not redefine them here.
func RunDirectPlanReviewer(cfg DirectPlanReviewerConfig, taskID uuid.UUID, title, description, planJSON, outputDir string) (*DirectPlanReviewerResult, error) {
	start := time.Now()

	userMsg := buildPlanReviewerUserMessage(title, description, planJSON)

	reqBody := chatRequest{
		Model: cfg.Model,
		Messages: []chatMessage{
			{Role: "system", Content: planReviewerSystemPrompt},
			{Role: "user", Content: userMsg},
		},
		MaxTokens:      cfg.MaxTokens,
		Temperature:    cfg.Temperature,
		ResponseFormat: &chatRespFormat{Type: "json_object"},
	}

	reqJSON, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("direct plan reviewer: marshal request: %w", err)
	}

	slog.Info("direct plan reviewer: calling SGLang API",
		"task_id", taskID,
		"endpoint", cfg.Endpoint,
		"model", cfg.Model,
		"prompt_len", len(userMsg),
	)

	httpReq, err := http.NewRequest(http.MethodPost, cfg.Endpoint, bytes.NewReader(reqJSON))
	if err != nil {
		return nil, fmt.Errorf("direct plan reviewer: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: cfg.Timeout}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("direct plan reviewer: API call failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("direct plan reviewer: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("direct plan reviewer: API returned status %d: %s", resp.StatusCode, truncateBody(body, 500))
	}

	var chatResp chatResponse
	if err := json.Unmarshal(body, &chatResp); err != nil {
		return nil, fmt.Errorf("direct plan reviewer: parse response: %w", err)
	}

	if len(chatResp.Choices) == 0 {
		return nil, fmt.Errorf("direct plan reviewer: no choices in response")
	}

	content := chatResp.Choices[0].Message.Content
	if content == "" {
		return nil, fmt.Errorf("direct plan reviewer: empty response content (finish_reason: %s)",
			chatResp.Choices[0].FinishReason)
	}

	// Validate that the response is valid JSON before writing.
	var jsonCheck json.RawMessage
	if err := json.Unmarshal([]byte(content), &jsonCheck); err != nil {
		return nil, fmt.Errorf("direct plan reviewer: response is not valid JSON: %w\nraw: %s",
			err, truncateBody([]byte(content), 500))
	}

	// Pretty-print the JSON for consistency with agent-produced files.
	var prettyJSON bytes.Buffer
	if err := json.Indent(&prettyJSON, []byte(content), "", "  "); err != nil {
		prettyJSON.Reset()
		prettyJSON.WriteString(content)
	}

	// Write review.json at the exact path onReviewerCompleted expects.
	outputPath := filepath.Join(outputDir, "review.json")
	if err := os.WriteFile(outputPath, prettyJSON.Bytes(), 0o644); err != nil {
		return nil, fmt.Errorf("direct plan reviewer: write output: %w", err)
	}

	duration := time.Since(start)

	slog.Info("direct plan reviewer: completed",
		"task_id", taskID,
		"output_path", outputPath,
		"duration", duration.Round(time.Millisecond),
		"tokens_in", chatResp.Usage.PromptTokens,
		"tokens_out", chatResp.Usage.CompletionTokens,
		"finish_reason", chatResp.Choices[0].FinishReason,
	)

	return &DirectPlanReviewerResult{
		OutputPath: outputPath,
		TokensIn:   chatResp.Usage.PromptTokens,
		TokensOut:  chatResp.Usage.CompletionTokens,
		Duration:   duration,
	}, nil
}
