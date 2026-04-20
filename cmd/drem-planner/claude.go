// Claude CLI subprocess integration for the drem-planner server. Split out
// of server.go so the handler + validation logic stays readable and so
// tests can stub exec.Command without touching the HTTP surface.
//
// Per plans/warm-planner-pivot.md §2 and §9 question 2, the planner
// execs `claude` as a subprocess per /plan request with flags:
//
//	claude -p --output-format json --model claude-opus-4-6 \
//	       [--permission-mode dangerously-skip-permissions]
//
// The prompt is piped on stdin; plan JSON is extracted from the CLI's
// stdout. We intentionally don't shell-escape anything — exec.Cmd
// argv-encodes natively.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

// DefaultPlannerModel is the Anthropic model the planner invokes by
// default. Matches internal/capability/capability.go and all earlier
// planner work so a single upgrade ripples everywhere.
const DefaultPlannerModel = "claude-opus-4-6"

// claudeInvoker is the test seam for subprocess execution. The production
// implementation (runRealClaude below) shells out; unit tests wire a stub
// that returns pre-canned stdout/stderr/exitCode.
type claudeInvoker func(ctx context.Context, prompt string, model string) (stdout, stderr []byte, exitCode int, err error)

// runRealClaude is the production claudeInvoker. It launches `claude` with
// the flag set §2 documents, feeds the prompt on stdin, and returns the
// captured stdout/stderr plus exit code.
//
// The caller (newClaudePlanGen) imposes a timeout via ctx; runRealClaude
// itself never hangs — the ctx cancel propagates through exec.CommandContext.
func runRealClaude(ctx context.Context, prompt string, model string) ([]byte, []byte, int, error) {
	if model == "" {
		model = DefaultPlannerModel
	}
	// Flag set captured here so it's easy to sync against the CLI's help
	// output when a new version drops. Tested CLI version: 1.x-beta series
	// (npm @anthropic-ai/claude-code, 2026-04-20).
	//
	// -p / --print         non-interactive mode, exit after first response
	// --output-format json single JSON envelope on stdout
	// --model <id>         pin Anthropic model
	// --dangerously-skip-permissions  bypass per-tool approval prompts
	//                                 (no human is at the terminal)
	args := []string{
		"-p",
		"--output-format", "json",
		"--model", model,
		"--dangerously-skip-permissions",
	}
	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Stdin = strings.NewReader(prompt)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	exitCode := 0
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}
	return stdout.Bytes(), stderr.Bytes(), exitCode, runErr
}

// newClaudePlanGen returns a Deps.GeneratePlan wired to the given invoker
// with the given timeout. The production invoker is runRealClaude; tests
// supply a stub that short-circuits the subprocess.
func newClaudePlanGen(invoker claudeInvoker, timeout time.Duration, model string) func(context.Context, planRequest) (*planResult, error) {
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	if model == "" {
		model = DefaultPlannerModel
	}
	return func(ctx context.Context, req planRequest) (*planResult, error) {
		subCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		prompt, err := renderPlannerPrompt(req)
		if err != nil {
			return nil, fmt.Errorf("render prompt: %w", err)
		}
		start := time.Now()
		stdout, stderr, exitCode, runErr := invoker(subCtx, prompt, model)
		duration := time.Since(start)

		if runErr != nil {
			// Distinguish timeout-by-ctx from other process errors so the
			// HTTP handler can surface 504 vs 500 correctly. context.Cause
			// handles CommandContext's wrapped cancel.
			if subCtx.Err() == context.DeadlineExceeded {
				return nil, context.DeadlineExceeded
			}
			return nil, fmt.Errorf("claude subprocess (exit=%d): %w; stderr=%q", exitCode, runErr, truncateStderr(stderr))
		}
		if exitCode != 0 {
			return nil, fmt.Errorf("claude subprocess exited %d; stderr=%q", exitCode, truncateStderr(stderr))
		}

		// The CLI emits a JSON envelope on stdout; extract the plan and
		// the token counts. The envelope shape is stable across
		// claude-code versions we've tested: {"result": "<text>", "usage":
		// {"input_tokens": N, "output_tokens": N}, ...}. "result" carries
		// the assistant's response, which should be a JSON-mode plan
		// object. We parse defensively — a malformed envelope is an
		// upstream bug, not a planner bug.
		plan, tokensIn, tokensOut, parseErr := parseClaudeEnvelope(stdout)
		if parseErr != nil {
			return nil, fmt.Errorf("parse claude output: %w; raw=%q", parseErr, truncateStderr(stdout))
		}
		return &planResult{
			Plan:      plan,
			TokensIn:  tokensIn,
			TokensOut: tokensOut,
			Duration:  duration,
		}, nil
	}
}

// claudeEnvelope is the subset of the CLI's JSON output the planner cares
// about. Extra fields are ignored; the CLI evolves its telemetry freely.
type claudeEnvelope struct {
	Result  string          `json:"result"`
	Usage   claudeUsage     `json:"usage"`
	Error   string          `json:"error"`
	IsError bool            `json:"is_error"`
	Raw     json.RawMessage `json:"-"`
}

type claudeUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// parseClaudeEnvelope extracts the plan object from the CLI's stdout. The
// CLI's result field is a bare string carrying the model's text response,
// which (when prompted for JSON mode) is a JSON-encoded plan. We parse
// twice: once to unwrap the envelope, again to decode the plan.
//
// When the envelope reports is_error=true, surface that as a distinct
// error class so the caller maps it to 502 (upstream failure).
func parseClaudeEnvelope(stdout []byte) (map[string]any, int, int, error) {
	var env claudeEnvelope
	if err := json.Unmarshal(stdout, &env); err != nil {
		return nil, 0, 0, fmt.Errorf("envelope json: %w", err)
	}
	if env.IsError {
		return nil, env.Usage.InputTokens, env.Usage.OutputTokens, fmt.Errorf("upstream claude CLI reported error: %s", env.Error)
	}
	plan, err := extractPlanFromText(env.Result)
	if err != nil {
		return nil, env.Usage.InputTokens, env.Usage.OutputTokens, err
	}
	return plan, env.Usage.InputTokens, env.Usage.OutputTokens, nil
}

// extractPlanFromText finds the plan JSON object inside a free-form text
// response. The prompt we send asks the model to emit exactly one JSON
// object, but defensive parsing handles the case where the model wraps
// it in markdown fences or adds prose before/after.
func extractPlanFromText(text string) (map[string]any, error) {
	trimmed := strings.TrimSpace(text)
	// Strip a common markdown code fence if present.
	if strings.HasPrefix(trimmed, "```") {
		if idx := strings.Index(trimmed, "\n"); idx >= 0 {
			trimmed = trimmed[idx+1:]
		}
		if end := strings.LastIndex(trimmed, "```"); end >= 0 {
			trimmed = trimmed[:end]
		}
		trimmed = strings.TrimSpace(trimmed)
	}
	// First try a direct decode.
	var plan map[string]any
	if err := json.Unmarshal([]byte(trimmed), &plan); err == nil {
		return plan, nil
	}
	// Fallback: find the outermost {...} block.
	start := strings.Index(trimmed, "{")
	end := strings.LastIndex(trimmed, "}")
	if start < 0 || end < 0 || end <= start {
		return nil, fmt.Errorf("no JSON object found in claude result")
	}
	if err := json.Unmarshal([]byte(trimmed[start:end+1]), &plan); err != nil {
		return nil, fmt.Errorf("plan json: %w", err)
	}
	return plan, nil
}

// truncateStderr bounds the stderr captured in error messages so a
// runaway CLI can't fill the orch log.
func truncateStderr(b []byte) string {
	const max = 1024
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "...<truncated>"
}

// renderPlannerPrompt composes the text prompt the claude CLI is invoked
// with. The request body already carries full task + project context;
// converting it to a prompt here keeps orch out of prompt-assembly
// concerns and means the planner owns its own conversation shape.
//
// The rendered prompt asks the model to emit exactly one JSON plan per
// the schema in plans/warm-planner-pivot.md §5 response shape.
func renderPlannerPrompt(req planRequest) (string, error) {
	// Marshal the full request into the prompt as a JSON block rather
	// than re-stringifying each field — preserves everything orch forwards
	// and lets prompt-engineering changes land here without server.go churn.
	rawReq, err := json.MarshalIndent(map[string]any{
		"task":          req.Task,
		"project":       req.Project,
		"worktree_path": req.WorktreePath,
		"comments":      req.Comments,
		"target_coder":  req.TargetCoder,
		"effort":        req.Effort,
	}, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	var b strings.Builder
	b.WriteString("You are a software engineering planner. Given the following task, produce a plan.json that decomposes the work into TDD-paired subtasks.\n\n")
	b.WriteString("The plan MUST be a single JSON object with this shape:\n")
	b.WriteString("  {\"subtasks\": [ {\"title\":..., \"description\":..., \"agent_type\":\"coder\", \"phase\":\"test\"|\"implementation\", \"files\":[...], \"tests_for\":[indices], \"dependencies\":[indices]} ] }\n\n")
	b.WriteString("Constraints:\n")
	b.WriteString("- subtasks must be non-empty.\n")
	b.WriteString("- every tests_for / dependencies index must be within the subtasks slice.\n")
	b.WriteString("- every implementation subtask should be paired with exactly one test subtask via tests_for.\n\n")
	b.WriteString("Emit EXACTLY one JSON object on stdout. No markdown fences, no prose before or after.\n\n")
	b.WriteString("### Task context\n")
	b.Write(rawReq)
	b.WriteString("\n")
	return b.String(), nil
}

// readAllBounded is a helper that would return io.ReadAll up to n bytes;
// kept in the file so future logging paths can reuse it without pulling
// io.LimitReader inline everywhere.
func readAllBounded(r io.Reader, n int64) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, n))
}

// claudeVersionProbe shells out `claude --version` with a short timeout.
// /healthz uses this to catch the case where the container booted but the
// CLI is broken (npm install failed, binary removed, etc.). A running
// planner without a working claude would silently fail every /plan call
// at runtime; this probe surfaces it to the caller up front.
func claudeVersionProbe(ctx context.Context, timeout time.Duration) error {
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, "claude", "--version")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("claude --version failed: %w", err)
	}
	return nil
}
