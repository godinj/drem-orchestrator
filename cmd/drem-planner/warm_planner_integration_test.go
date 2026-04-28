package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordedClaudeInvocation struct {
	prompt string
	model  string
	count  int
}

func classifierBacklogPlanRequest() planRequest {
	taskID := uuid.NewString()
	return planRequest{
		TaskID: taskID,
		Task: map[string]any{
			"id":               taskID,
			"category":         "standard",
			"complexity_score": float64(6),
			"title":            "Write test: warm-planner invokes Claude provider and produces plan",
			"description":      "Create an integration test from the classifier backlog entry through the Claude-backed planner.",
			"target_files": []any{
				"cmd/drem-planner/warm_planner_integration_test.go",
				"cmd/drem-planner/claude.go",
			},
			"rationale": "Planner/provider integration touches the warm planner and Claude invocation boundary.",
		},
		Project: map[string]any{
			"name": "drem-orchestrator",
			"repo": "/home/drem/work",
		},
		WorktreePath: "/home/drem/work",
		TargetCoder:  targetCoder{Provider: "sglang-direct", Model: "gemma4-26b"},
		Effort:       "high",
	}
}

// TestWarmPlanner_ClassifierBacklogEntryInvokesClaudeProvider verifies the
// contract between the warm planner and its Claude-backed provider: a backlog
// entry shaped like classifier output is rendered into the provider prompt,
// Claude is invoked exactly once with the planner model, and its plan is
// surfaced as a well-formed planResult.
func TestWarmPlanner_ClassifierBacklogEntryInvokesClaudeProvider(t *testing.T) {
	req := classifierBacklogPlanRequest()
	wantPlan := map[string]any{
		"subtasks": []any{
			map[string]any{
				"title":       "Test Claude-backed warm planner",
				"description": "Add an integration test that exercises classifier backlog input through the Claude provider seam.",
				"agent_type":  "coder",
				"phase":       "test",
				"files":       []any{"cmd/drem-planner/warm_planner_integration_test.go"},
				"tests_for":   []any{float64(1)},
			},
			map[string]any{
				"title":        "Implement Claude-backed warm planner",
				"description":  "Wire the warm planner to invoke the Claude planning provider and return a validated plan.",
				"agent_type":   "coder",
				"phase":        "implementation",
				"files":        []any{"cmd/drem-planner/claude.go"},
				"dependencies": []any{float64(0)},
			},
		},
	}
	var invocation recordedClaudeInvocation
	gen := newClaudePlanGen(func(ctx context.Context, prompt, model string) ([]byte, []byte, int, error) {
		invocation.count++
		invocation.prompt = prompt
		invocation.model = model
		return happyEnvelope(t, wantPlan, 2210, 640), nil, 0, nil
	}, 30*time.Second, DefaultPlannerModel)

	got, err := gen(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.NoError(t, validatePlan(got.Plan))

	assert.Equal(t, 1, invocation.count)
	assert.Equal(t, DefaultPlannerModel, invocation.model)
	assert.Contains(t, invocation.prompt, req.TaskID)
	assert.Contains(t, invocation.prompt, "complexity_score")
	assert.Contains(t, invocation.prompt, "cmd/drem-planner/claude.go")
	assert.Contains(t, invocation.prompt, req.WorktreePath)
	assert.Equal(t, 2210, got.TokensIn)
	assert.Equal(t, 640, got.TokensOut)

	subtasks := got.Plan["subtasks"].([]any)
	require.Len(t, subtasks, 2)
	first := subtasks[0].(map[string]any)
	assert.Equal(t, "test", first["phase"])
	assert.Equal(t, "coder", first["agent_type"])
	assert.Equal(t, []any{float64(1)}, first["tests_for"])
}

// TestWarmPlanner_ClaudeProviderFailureReturnsNoPlan guards the error
// boundary: a failed Claude provider invocation should be reported as an
// error and must not produce a partial plan.
func TestWarmPlanner_ClaudeProviderFailureReturnsNoPlan(t *testing.T) {
	var invocation recordedClaudeInvocation
	gen := newClaudePlanGen(func(ctx context.Context, prompt, model string) ([]byte, []byte, int, error) {
		invocation.count++
		invocation.prompt = prompt
		invocation.model = model
		return nil, []byte("anthropic auth failed"), 1, errors.New("exit status 1")
	}, 30*time.Second, DefaultPlannerModel)

	got, err := gen(context.Background(), classifierBacklogPlanRequest())
	require.Error(t, err)
	assert.Nil(t, got)
	assert.Equal(t, 1, invocation.count)
	assert.Equal(t, DefaultPlannerModel, invocation.model)
	assert.True(t, strings.Contains(err.Error(), "exit=1"))
	assert.True(t, strings.Contains(err.Error(), "anthropic auth failed"))
}

// TestWarmPlanner_RejectsMalformedClaudePlan verifies the HTTP warm-planner
// path does not return a Claude response unless the produced plan satisfies
// the planner schema.
func TestWarmPlanner_RejectsMalformedClaudePlan(t *testing.T) {
	badPlan := map[string]any{"subtasks": []any{}}
	var invocation recordedClaudeInvocation
	srv := NewServer("", Deps{
		GeneratePlan: newClaudePlanGen(func(ctx context.Context, prompt, model string) ([]byte, []byte, int, error) {
			invocation.count++
			invocation.prompt = prompt
			invocation.model = model
			return happyEnvelope(t, badPlan, 80, 12), nil, 0, nil
		}, 30*time.Second, DefaultPlannerModel),
	}, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	body, err := json.Marshal(classifierBacklogPlanRequest())
	require.NoError(t, err)
	resp, err := http.Post(ts.URL+"/plan", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusConflict, resp.StatusCode)
	assert.Equal(t, 1, invocation.count)
	assert.Equal(t, DefaultPlannerModel, invocation.model)
}
