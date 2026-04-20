package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// happyEnvelope renders a canonical claude-CLI JSON envelope whose
// `result` field carries the plan JSON. Helper keeps tests readable.
func happyEnvelope(t *testing.T, plan map[string]any, tokensIn, tokensOut int) []byte {
	t.Helper()
	planBytes, err := json.Marshal(plan)
	require.NoError(t, err)
	env := map[string]any{
		"result": string(planBytes),
		"usage": map[string]int{
			"input_tokens":  tokensIn,
			"output_tokens": tokensOut,
		},
	}
	raw, err := json.Marshal(env)
	require.NoError(t, err)
	return raw
}

// TestClaudePlanGen_HappyPath is the canonical success path: the stub
// invoker returns a well-formed envelope, the generator parses it, and
// returns a planResult with the tokens + plan populated.
func TestClaudePlanGen_HappyPath(t *testing.T) {
	plan := map[string]any{
		"subtasks": []any{
			map[string]any{"title": "t", "phase": "test", "tests_for": []any{float64(1)}},
			map[string]any{"title": "i", "phase": "implementation"},
		},
	}
	envelope := happyEnvelope(t, plan, 1234, 567)
	invoker := func(ctx context.Context, prompt, model string) ([]byte, []byte, int, error) {
		assert.NotEmpty(t, prompt, "prompt must not be empty")
		assert.Equal(t, DefaultPlannerModel, model)
		return envelope, nil, 0, nil
	}
	gen := newClaudePlanGen(invoker, 30*time.Second, DefaultPlannerModel)

	res, err := gen(context.Background(), minimalValidRequest())
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, 1234, res.TokensIn)
	assert.Equal(t, 567, res.TokensOut)
	require.NoError(t, validatePlan(res.Plan))
}

// TestClaudePlanGen_NonZeroExitSurfacesAsError: CLI non-zero exit → the
// caller sees an error. The HTTP handler maps that to 500 (generator bug)
// since it's not a deadline and not an upstream message.
func TestClaudePlanGen_NonZeroExitSurfacesAsError(t *testing.T) {
	invoker := func(ctx context.Context, prompt, model string) ([]byte, []byte, int, error) {
		return nil, []byte("auth failed"), 1, errors.New("exit status 1")
	}
	gen := newClaudePlanGen(invoker, 30*time.Second, DefaultPlannerModel)

	_, err := gen(context.Background(), minimalValidRequest())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exit=1")
	assert.Contains(t, err.Error(), "auth failed")
}

// TestClaudePlanGen_TimeoutSurfacesAsDeadlineExceeded: when ctx expires
// during the subprocess, the generator returns context.DeadlineExceeded
// so writePlanError maps it to 504.
func TestClaudePlanGen_TimeoutSurfacesAsDeadlineExceeded(t *testing.T) {
	// Invoker ignores ctx and blocks until the outer timeout trips. We use
	// a tiny timeout and a slow stub to drive the deadline.
	invoker := func(ctx context.Context, prompt, model string) ([]byte, []byte, int, error) {
		// Block until ctx is done (simulating a stuck subprocess).
		<-ctx.Done()
		return nil, nil, 124, errors.New("signal: killed")
	}
	gen := newClaudePlanGen(invoker, 10*time.Millisecond, DefaultPlannerModel)

	_, err := gen(context.Background(), minimalValidRequest())
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

// TestClaudePlanGen_UpstreamErrorInEnvelope: the CLI reports is_error=true
// for an upstream Anthropic failure. The generator surfaces the error so
// writePlanError can map it to 502.
func TestClaudePlanGen_UpstreamErrorInEnvelope(t *testing.T) {
	env := map[string]any{
		"is_error": true,
		"error":    "anthropic returned 500",
		"usage":    map[string]int{"input_tokens": 100, "output_tokens": 0},
	}
	raw, _ := json.Marshal(env)
	invoker := func(ctx context.Context, prompt, model string) ([]byte, []byte, int, error) {
		return raw, nil, 0, nil
	}
	gen := newClaudePlanGen(invoker, 30*time.Second, DefaultPlannerModel)

	_, err := gen(context.Background(), minimalValidRequest())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "upstream")
}

// TestClaudePlanGen_MalformedEnvelope: the CLI returns garbage. The
// generator returns a parse error (maps to 500, since it's a CLI
// contract bug rather than an Anthropic problem).
func TestClaudePlanGen_MalformedEnvelope(t *testing.T) {
	invoker := func(ctx context.Context, prompt, model string) ([]byte, []byte, int, error) {
		return []byte("not json"), nil, 0, nil
	}
	gen := newClaudePlanGen(invoker, 30*time.Second, DefaultPlannerModel)

	_, err := gen(context.Background(), minimalValidRequest())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse claude output")
}

// TestExtractPlanFromText_StripsMarkdownFences verifies defensive parsing:
// when the model wraps its response in ```json ... ``` despite the
// prompt, the extractor still recovers the plan.
func TestExtractPlanFromText_StripsMarkdownFences(t *testing.T) {
	wrapped := "```json\n{\"subtasks\": [{\"title\":\"ok\"}]}\n```"
	plan, err := extractPlanFromText(wrapped)
	require.NoError(t, err)
	require.Contains(t, plan, "subtasks")
}

// TestExtractPlanFromText_FindsInnerObject: when prose leaks in around
// the JSON, the fallback path locates the outermost { ... } block.
func TestExtractPlanFromText_FindsInnerObject(t *testing.T) {
	wrapped := `Sure, here's your plan:
{"subtasks": [{"title":"ok"}]}
Let me know if you need changes.`
	plan, err := extractPlanFromText(wrapped)
	require.NoError(t, err)
	require.Contains(t, plan, "subtasks")
}

// TestRenderPlannerPrompt_IncludesTaskBody: the rendered prompt carries
// the full task/project/worktree context so the model has what it needs.
func TestRenderPlannerPrompt_IncludesTaskBody(t *testing.T) {
	req := minimalValidRequest()
	prompt, err := renderPlannerPrompt(req)
	require.NoError(t, err)
	assert.Contains(t, prompt, "subtasks")
	assert.Contains(t, prompt, req.WorktreePath)
}

// TestServer_UsesClaudeGenerator end-to-end: wire a stub invoker through
// NewServer and confirm the /plan handler returns the claude-produced
// plan verbatim. Guards against accidental stub usage in the handler
// wiring.
func TestServer_UsesClaudeGenerator(t *testing.T) {
	plan := map[string]any{
		"subtasks": []any{
			map[string]any{"title": "from claude", "phase": "test", "tests_for": []any{float64(1)}},
			map[string]any{"title": "impl", "phase": "implementation"},
		},
	}
	invoker := func(ctx context.Context, prompt, model string) ([]byte, []byte, int, error) {
		return happyEnvelope(t, plan, 99, 42), nil, 0, nil
	}
	srv := NewServer("", Deps{
		GeneratePlan: newClaudePlanGen(invoker, 30*time.Second, DefaultPlannerModel),
	}, nil)

	req := minimalValidRequest()
	body, _ := json.Marshal(req)

	// Drive a request through the handler.
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	resp, err := http.Post(ts.URL+"/plan", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, 200, resp.StatusCode)

	var got planResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	subtasks, ok := got.Plan["subtasks"].([]any)
	require.True(t, ok)
	require.Len(t, subtasks, 2)
	first, _ := subtasks[0].(map[string]any)
	assert.Equal(t, "from claude", first["title"], "server must return claude-generated plan, not stub")
}
