package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubPlanGen records the most recent request and returns the configured
// result/error. Tests wire this through Deps.GeneratePlan to exercise the
// handler path without claude installed.
type stubPlanGen struct {
	result *planResult
	err    error
	last   planRequest
}

func (s *stubPlanGen) fn() func(context.Context, planRequest) (*planResult, error) {
	return func(_ context.Context, req planRequest) (*planResult, error) {
		s.last = req
		return s.result, s.err
	}
}

// newTestServer spins up an httptest.Server wired to the given stub + token.
func newTestServer(t *testing.T, stub *stubPlanGen, token string) *httptest.Server {
	t.Helper()
	srv := NewServer(token, Deps{GeneratePlan: stub.fn()}, nil)
	return httptest.NewServer(srv.Handler())
}

// minimalValidPlan is the canonical test plan — two subtasks, tests_for
// pairing, passes validatePlan.
func minimalValidPlan() map[string]any {
	return map[string]any{
		"subtasks": []any{
			map[string]any{
				"title":       "test add",
				"description": "write test",
				"agent_type":  "coder",
				"phase":       "test",
				"tests_for":   []any{float64(1)},
				"files":       []any{"foo_test.go"},
			},
			map[string]any{
				"title":       "impl add",
				"description": "implement",
				"agent_type":  "coder",
				"phase":       "implementation",
				"files":       []any{"foo.go"},
			},
		},
	}
}

// minimalValidRequest returns a request payload that passes all handler-
// side validation so tests can focus on the downstream pathway.
func minimalValidRequest() planRequest {
	return planRequest{
		TaskID:       uuid.New().String(),
		Task:         map[string]any{"id": "t", "title": "something"},
		Project:      map[string]any{"id": "p"},
		WorktreePath: "/work/feature/example",
		Effort:       "high",
		TargetCoder:  targetCoder{Provider: "sglang-direct", Model: "gemma4-26b"},
	}
}

// TestPlan_200_HappyPath: the canonical success path — valid body,
// returning stub plan, structured planResponse back.
func TestPlan_200_HappyPath(t *testing.T) {
	stub := &stubPlanGen{
		result: &planResult{
			Plan:      minimalValidPlan(),
			TokensIn:  4300,
			TokensOut: 1200,
			Duration:  7 * time.Second,
		},
	}
	ts := newTestServer(t, stub, "")
	defer ts.Close()

	req := minimalValidRequest()
	body, _ := json.Marshal(req)
	resp, err := http.Post(ts.URL+"/plan", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	var got planResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	assert.Equal(t, req.TaskID, got.TaskID)
	assert.NotNil(t, got.Plan)
	assert.Contains(t, got.Plan, "subtasks")
	assert.Equal(t, 4300, got.TokensIn)
	assert.Equal(t, 1200, got.TokensOut)
	assert.Equal(t, 7000, got.DurationMS)

	// Forwarded request fields reach the stub.
	assert.Equal(t, req.TaskID, stub.last.TaskID)
	assert.Equal(t, req.WorktreePath, stub.last.WorktreePath)
}

// TestPlan_400_BadJSON rejects malformed bodies up front.
func TestPlan_400_BadJSON(t *testing.T) {
	stub := &stubPlanGen{}
	ts := newTestServer(t, stub, "")
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/plan", "application/json", bytes.NewReader([]byte("not json")))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "decode body")
}

// TestPlan_400_MissingFields catches required-field omissions before the
// plan generator runs.
func TestPlan_400_MissingFields(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*planRequest)
		wantMsg string
	}{
		{
			name:    "missing task_id",
			mutate:  func(r *planRequest) { r.TaskID = "" },
			wantMsg: "task_id is required",
		},
		{
			name:    "missing worktree_path",
			mutate:  func(r *planRequest) { r.WorktreePath = "" },
			wantMsg: "worktree_path is required",
		},
		{
			name:    "relative worktree_path",
			mutate:  func(r *planRequest) { r.WorktreePath = "relative/path" },
			wantMsg: "must be absolute",
		},
		{
			name:    "path escape",
			mutate:  func(r *planRequest) { r.WorktreePath = "/work/../etc/shadow" },
			wantMsg: "must be absolute",
		},
	}

	stub := &stubPlanGen{}
	ts := newTestServer(t, stub, "")
	defer ts.Close()

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := minimalValidRequest()
			tc.mutate(&req)
			body, _ := json.Marshal(req)
			resp, err := http.Post(ts.URL+"/plan", "application/json", bytes.NewReader(body))
			require.NoError(t, err)
			defer resp.Body.Close()
			require.Equal(t, http.StatusBadRequest, resp.StatusCode)
			b, _ := io.ReadAll(resp.Body)
			assert.Contains(t, string(b), tc.wantMsg)
		})
	}
}

// TestPlan_401_AuthEnforced: bearer token checks are enforced when the
// server has a token configured.
func TestPlan_401_AuthEnforced(t *testing.T) {
	stub := &stubPlanGen{result: &planResult{Plan: minimalValidPlan()}}
	ts := newTestServer(t, stub, "shh-secret")
	defer ts.Close()

	body, _ := json.Marshal(minimalValidRequest())

	t.Run("missing token", func(t *testing.T) {
		resp, err := http.Post(ts.URL+"/plan", "application/json", bytes.NewReader(body))
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})

	t.Run("wrong token", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/plan", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer wrong")
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})

	t.Run("correct token passes", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/plan", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer shh-secret")
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
	})
}

// TestPlan_409_InvalidPlanShape: the plan generator returned a plan that
// fails validatePlan — surface 409 so orch distinguishes validation
// failures from generator bugs (500) or upstream failures (502).
func TestPlan_409_InvalidPlanShape(t *testing.T) {
	stub := &stubPlanGen{
		result: &planResult{Plan: map[string]any{"subtasks": []any{}}}, // empty
	}
	ts := newTestServer(t, stub, "")
	defer ts.Close()

	body, _ := json.Marshal(minimalValidRequest())
	resp, err := http.Post(ts.URL+"/plan", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusConflict, resp.StatusCode)
}

// TestPlan_502_UpstreamFailure: a plan-generator error whose message
// mentions upstream/anthropic maps to 502.
func TestPlan_502_UpstreamFailure(t *testing.T) {
	stub := &stubPlanGen{
		err: errors.New("claude subprocess: upstream returned 500"),
	}
	ts := newTestServer(t, stub, "")
	defer ts.Close()

	body, _ := json.Marshal(minimalValidRequest())
	resp, err := http.Post(ts.URL+"/plan", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadGateway, resp.StatusCode)
}

// TestPlan_504_Timeout: a context.DeadlineExceeded maps to 504.
func TestPlan_504_Timeout(t *testing.T) {
	stub := &stubPlanGen{
		err: context.DeadlineExceeded,
	}
	ts := newTestServer(t, stub, "")
	defer ts.Close()

	body, _ := json.Marshal(minimalValidRequest())
	resp, err := http.Post(ts.URL+"/plan", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusGatewayTimeout, resp.StatusCode)
}

// TestPlan_500_GeneratorBug: a generic plan-generator error maps to 500.
func TestPlan_500_GeneratorBug(t *testing.T) {
	stub := &stubPlanGen{
		err: errors.New("planner: internal bug"),
	}
	ts := newTestServer(t, stub, "")
	defer ts.Close()

	body, _ := json.Marshal(minimalValidRequest())
	resp, err := http.Post(ts.URL+"/plan", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusInternalServerError, resp.StatusCode)
}

// TestHealthz_CredentialsPresent: /healthz returns 200 when the probe
// reports the credentials file readable.
func TestHealthz_CredentialsPresent(t *testing.T) {
	dir := t.TempDir()
	credsPath := filepath.Join(dir, ".credentials.json")
	require.NoError(t, os.WriteFile(credsPath, []byte("{}"), 0o600))

	srv := NewServer("", Deps{
		GeneratePlan: stubGeneratePlan,
		ProbeHealth:  func(ctx context.Context) error { return credentialsProbe(ctx, credsPath) },
	}, nil)
	srv.setHealthProbeTTL(0) // refresh every call
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/healthz")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

// TestHealthz_CredentialsMissing: /healthz returns 503 when the probe
// fails — gates orch's dispatch so missing creds fail fast rather than
// hanging on an Anthropic 401.
func TestHealthz_CredentialsMissing(t *testing.T) {
	srv := NewServer("", Deps{
		GeneratePlan: stubGeneratePlan,
		ProbeHealth:  func(ctx context.Context) error { return credentialsProbe(ctx, "/nonexistent/.credentials.json") },
	}, nil)
	srv.setHealthProbeTTL(0)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/healthz")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
}

// TestValidatePlan rejects every malformed shape plans/warm-planner-pivot.md §6
// enumerates.
func TestValidatePlan(t *testing.T) {
	cases := []struct {
		name    string
		plan    map[string]any
		wantErr string
	}{
		{
			name:    "missing subtasks",
			plan:    map[string]any{},
			wantErr: "missing subtasks",
		},
		{
			name:    "subtasks not array",
			plan:    map[string]any{"subtasks": "nope"},
			wantErr: "not an array",
		},
		{
			name:    "empty subtasks",
			plan:    map[string]any{"subtasks": []any{}},
			wantErr: "empty",
		},
		{
			name: "tests_for out of range",
			plan: map[string]any{
				"subtasks": []any{
					map[string]any{"title": "a", "tests_for": []any{float64(99)}},
					map[string]any{"title": "b"},
				},
			},
			wantErr: "out of range",
		},
		{
			name: "dependencies non-integer",
			plan: map[string]any{
				"subtasks": []any{
					map[string]any{"title": "a"},
					map[string]any{"title": "b", "dependencies": []any{"not a number"}},
				},
			},
			wantErr: "non-integer",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validatePlan(tc.plan)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// TestStubGeneratePlan verifies the commit-2 stub emits a plan that
// passes validatePlan — important because main.go defaults to it until
// the real claude invocation lands in commit 3.
func TestStubGeneratePlan(t *testing.T) {
	res, err := stubGeneratePlan(context.Background(), minimalValidRequest())
	require.NoError(t, err)
	require.NotNil(t, res)
	require.NoError(t, validatePlan(res.Plan))
}
