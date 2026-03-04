// Package supervisor provides a lightweight LLM-powered decision layer for
// the orchestrator. It calls `claude -p` synchronously at decision points
// (failure diagnosis, feedback integration, merge conflict analysis) and
// returns structured JSON evaluations.
package supervisor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Supervisor calls Claude in pipe mode for focused evaluations at
// orchestrator decision points.
type Supervisor struct {
	claudeBin string
	timeout   time.Duration
}

// New creates a Supervisor.
func New(claudeBin string, timeout time.Duration) *Supervisor {
	return &Supervisor{
		claudeBin: claudeBin,
		timeout:   timeout,
	}
}

// Evaluate runs `claude -p --dangerously-skip-permissions` with the given
// prompt on stdin and returns the raw text response.
func (s *Supervisor) Evaluate(ctx context.Context, prompt string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, s.claudeBin, "-p", "--dangerously-skip-permissions")
	cmd.Stdin = strings.NewReader(prompt)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("supervisor evaluate: timeout after %s", s.timeout)
		}
		return "", fmt.Errorf("supervisor evaluate: %w\nstderr: %s", err, stderr.String())
	}

	return strings.TrimSpace(stdout.String()), nil
}

// EvaluateJSON calls Evaluate, extracts the first JSON object from the
// response, and unmarshals it into target.
func (s *Supervisor) EvaluateJSON(ctx context.Context, prompt string, target any) error {
	raw, err := s.Evaluate(ctx, prompt)
	if err != nil {
		return err
	}

	jsonStr := extractJSON(raw)
	if jsonStr == "" {
		return fmt.Errorf("supervisor evaluate json: no JSON found in response: %s", truncateForPrompt(raw, 200))
	}

	if err := json.Unmarshal([]byte(jsonStr), target); err != nil {
		return fmt.Errorf("supervisor evaluate json: unmarshal: %w\nraw json: %s", err, truncateForPrompt(jsonStr, 500))
	}

	return nil
}

// extractJSON finds the first top-level JSON object or array in text,
// handling nested braces/brackets and string escapes.
func extractJSON(s string) string {
	// Find the first '{' or '['.
	startIdx := -1
	var openChar, closeChar byte
	for i := 0; i < len(s); i++ {
		if s[i] == '{' {
			startIdx = i
			openChar = '{'
			closeChar = '}'
			break
		}
		if s[i] == '[' {
			startIdx = i
			openChar = '['
			closeChar = ']'
			break
		}
	}
	if startIdx < 0 {
		return ""
	}

	depth := 0
	inString := false
	escaped := false

	for i := startIdx; i < len(s); i++ {
		c := s[i]

		if escaped {
			escaped = false
			continue
		}

		if c == '\\' && inString {
			escaped = true
			continue
		}

		if c == '"' {
			inString = !inString
			continue
		}

		if inString {
			continue
		}

		if c == openChar {
			depth++
		} else if c == closeChar {
			depth--
			if depth == 0 {
				return s[startIdx : i+1]
			}
		}
	}

	return ""
}

// truncateForPrompt truncates s to maxLen characters for error messages.
func truncateForPrompt(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
