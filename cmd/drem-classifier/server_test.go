package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/internal/agent"
)

// stubClassify returns a Deps.Classify function that always returns the given
// result and error. It records the most recent ClassifyInput so tests can
// assert on what was forwarded from the HTTP body.
type stubClassify struct {
	result *agent.ClassifyResult
	err    error
	last   agent.ClassifyInput
}

func (s *stubClassify) fn() func(context.Context, agent.DirectClassifierConfig, agent.ClassifyInput) (*agent.ClassifyResult, error) {
	return func(_ context.Context, _ agent.DirectClassifierConfig, in agent.ClassifyInput) (*agent.ClassifyResult, error) {
		s.last = in
		return s.result, s.err
	}
}

// newTestServer builds a Server wired to the given stub and spins up an
// httptest.Server so tests exercise the full routing/middleware stack.
func newTestServer(t *testing.T, stub *stubClassify, token string) *httptest.Server {
	t.Helper()
	srv := NewServer(agent.DefaultDirectClassifierConfig(), token, Deps{Classify: stub.fn()}, nil)
	return httptest.NewServer(srv.Handler())
}

// TestClassify_200_HappyPath is the canonical success path: a valid body, a
// happy classifier stub, and a structured classifyResponse returned to orch.
func TestClassify_200_HappyPath(t *testing.T) {
	stub := &stubClassify{
		result: &agent.ClassifyResult{
			JSON: []byte(`{
  "category": "quickfix",
  "complexity_score": 2,
  "title": "Fix typo",
  "description": "Single-char fix",
  "target_files": ["README.md"],
  "rationale": "obvious"
}`),
			TokensIn:  812,
			TokensOut: 48,
			Duration:  947 * time.Millisecond,
		},
	}
	ts := newTestServer(t, stub, "")
	defer ts.Close()

	taskID := uuid.New().String()
	body, _ := json.Marshal(classifyRequest{
		TaskID:      taskID,
		Title:       "Fix typo",
		Description: "single-char typo in README",
	})

	resp, err := http.Post(ts.URL+"/classify", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	var got classifyResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	assert.Equal(t, taskID, got.TaskID)
	assert.Equal(t, "quickfix", got.Category)
	assert.Equal(t, 2, got.ComplexityScore)
	assert.Equal(t, []string{"README.md"}, got.TargetFiles)
	assert.Equal(t, 812, got.TokensIn)
	assert.Equal(t, 48, got.TokensOut)
	assert.Equal(t, 947, got.DurationMS)
	assert.False(t, got.NeedsClarification)

	// ClassifyInput forwarded correctly.
	assert.Equal(t, "Fix typo", stub.last.Title)
	assert.Equal(t, "single-char typo in README", stub.last.Description)
	assert.Equal(t, taskID, stub.last.TaskID.String())
}

// TestClassify_200_NeedsClarification verifies the clarification pathway is
// surfaced verbatim — orch uses it to stall the task for human input.
func TestClassify_200_NeedsClarification(t *testing.T) {
	stub := &stubClassify{
		result: &agent.ClassifyResult{
			JSON: []byte(`{
  "needs_clarification": true,
  "questions": ["Which module?", "Target branch?"]
}`),
			TokensIn:  200,
			TokensOut: 20,
		},
	}
	ts := newTestServer(t, stub, "")
	defer ts.Close()

	body, _ := json.Marshal(classifyRequest{
		TaskID: uuid.New().String(),
		Title:  "Something vague",
	})
	resp, err := http.Post(ts.URL+"/classify", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	var got classifyResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	assert.True(t, got.NeedsClarification)
	assert.Equal(t, []string{"Which module?", "Target branch?"}, got.Questions)
}

// TestClassify_400_BadJSON rejects malformed request bodies up front so
// orch sees a clean 400 rather than a stack trace in logs.
func TestClassify_400_BadJSON(t *testing.T) {
	stub := &stubClassify{}
	ts := newTestServer(t, stub, "")
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/classify", "application/json", bytes.NewReader([]byte("not json")))
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "decode body")
}

// TestClassify_400_MissingFields catches required-field omissions without
// round-tripping to the LLM.
func TestClassify_400_MissingFields(t *testing.T) {
	cases := []struct {
		name    string
		body    classifyRequest
		wantMsg string
	}{
		{name: "missing task_id", body: classifyRequest{Title: "t"}, wantMsg: "task_id is required"},
		{name: "missing title", body: classifyRequest{TaskID: uuid.New().String()}, wantMsg: "title is required"},
		{name: "bad uuid", body: classifyRequest{TaskID: "nope", Title: "t"}, wantMsg: "not a valid uuid"},
	}

	stub := &stubClassify{}
	ts := newTestServer(t, stub, "")
	defer ts.Close()

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, _ := json.Marshal(tc.body)
			resp, err := http.Post(ts.URL+"/classify", "application/json", bytes.NewReader(b))
			require.NoError(t, err)
			defer resp.Body.Close()
			require.Equal(t, http.StatusBadRequest, resp.StatusCode)
			body, _ := io.ReadAll(resp.Body)
			assert.Contains(t, string(body), tc.wantMsg)
		})
	}
}

// TestClassify_401_MissingToken / InvalidToken — both when an auth token is
// configured on the server.
func TestClassify_401_AuthEnforced(t *testing.T) {
	stub := &stubClassify{
		result: &agent.ClassifyResult{JSON: []byte(`{"category":"quickfix","complexity_score":1,"title":"t","description":"d","target_files":[],"rationale":"r"}`)},
	}
	ts := newTestServer(t, stub, "shhh-very-secret")
	defer ts.Close()

	reqBody, _ := json.Marshal(classifyRequest{TaskID: uuid.New().String(), Title: "t"})

	t.Run("missing token", func(t *testing.T) {
		resp, err := http.Post(ts.URL+"/classify", "application/json", bytes.NewReader(reqBody))
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})

	t.Run("wrong token", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/classify", bytes.NewReader(reqBody))
		req.Header.Set("Authorization", "Bearer wrong")
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})

	t.Run("correct token passes", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/classify", bytes.NewReader(reqBody))
		req.Header.Set("Authorization", "Bearer shhh-very-secret")
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
	})
}

// TestClassify_502_UpstreamFail exercises the error-mapping path: when the
// classifier's own HTTP call to sglang/gq fails, the server returns 502 so
// orch can distinguish that from a classifier bug.
func TestClassify_502_UpstreamFail(t *testing.T) {
	stub := &stubClassify{
		err: errors.New("direct classifier: API call failed: connection refused"),
	}
	ts := newTestServer(t, stub, "")
	defer ts.Close()

	body, _ := json.Marshal(classifyRequest{TaskID: uuid.New().String(), Title: "t"})
	resp, err := http.Post(ts.URL+"/classify", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusBadGateway, resp.StatusCode)
}

// TestClassify_504_Timeout maps context.DeadlineExceeded to 504 so orch's
// retry backoff can treat timeouts differently from hard upstream failures.
func TestClassify_504_Timeout(t *testing.T) {
	stub := &stubClassify{
		err: fmt.Errorf("direct classifier: API call failed: %w", context.DeadlineExceeded),
	}
	ts := newTestServer(t, stub, "")
	defer ts.Close()

	body, _ := json.Marshal(classifyRequest{TaskID: uuid.New().String(), Title: "t"})
	resp, err := http.Post(ts.URL+"/classify", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusGatewayTimeout, resp.StatusCode)
}

// TestClassify_500_ClassifierBug: a non-upstream, non-timeout error maps to
// 500 so the orch retry loop resurfaces it.
func TestClassify_500_ClassifierBug(t *testing.T) {
	stub := &stubClassify{
		err: errors.New("direct classifier: marshal request: some internal bug"),
	}
	ts := newTestServer(t, stub, "")
	defer ts.Close()

	body, _ := json.Marshal(classifyRequest{TaskID: uuid.New().String(), Title: "t"})
	resp, err := http.Post(ts.URL+"/classify", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusInternalServerError, resp.StatusCode)
}

// TestHealthz_UpstreamReachable returns 200 with {ok:true} when the probe
// hook succeeds.
func TestHealthz_UpstreamReachable(t *testing.T) {
	srv := NewServer(agent.DefaultDirectClassifierConfig(), "", Deps{
		Classify:      (&stubClassify{}).fn(),
		ProbeUpstream: func(context.Context) error { return nil },
	}, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/healthz")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), `"ok":true`)
	assert.Contains(t, string(body), `"upstream":"reachable"`)
}

// TestHealthz_UpstreamUnreachable returns 503 when the probe fails; orch
// uses this to decide whether to keep dispatching.
func TestHealthz_UpstreamUnreachable(t *testing.T) {
	srv := NewServer(agent.DefaultDirectClassifierConfig(), "", Deps{
		Classify:      (&stubClassify{}).fn(),
		ProbeUpstream: func(context.Context) error { return errors.New("gq unreachable") },
	}, nil)
	// Force an immediate refresh on every call so the test doesn't cache
	// a stale healthy state from earlier runs.
	srv.probeEvery = 0

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/healthz")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
}

// TestHealthz_NoProbe treats the binary as healthy when ProbeUpstream is
// nil (tests, very early startup). This is the documented default in
// server.go so /healthz never returns a red state purely because of a
// wiring gap.
func TestHealthz_NoProbe(t *testing.T) {
	srv := NewServer(agent.DefaultDirectClassifierConfig(), "", Deps{
		Classify: (&stubClassify{}).fn(),
	}, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/healthz")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

// TestMetrics_ExposesExpvar verifies that /metrics returns the expvar-shaped
// JSON and that the classifier counters are registered. Prometheus can scrape
// it via its JSON exporter or a relabeling step.
func TestMetrics_ExposesExpvar(t *testing.T) {
	srv := NewServer(agent.DefaultDirectClassifierConfig(), "", Deps{
		Classify: (&stubClassify{
			result: &agent.ClassifyResult{
				JSON:      []byte(`{"category":"quickfix","complexity_score":1,"title":"t","description":"d","target_files":[],"rationale":"r"}`),
				TokensIn:  10,
				TokensOut: 5,
			},
		}).fn(),
	}, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// First drive some traffic so the counters are non-zero.
	body, _ := json.Marshal(classifyRequest{TaskID: uuid.New().String(), Title: "t"})
	resp, err := http.Post(ts.URL+"/classify", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	_ = resp.Body.Close()

	resp, err = http.Get(ts.URL + "/metrics")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	raw, _ := io.ReadAll(resp.Body)
	s := string(raw)
	assert.True(t, strings.Contains(s, "drem_classifier_requests_ok"),
		"metrics response missing drem_classifier_requests_ok counter: %s", s)
	assert.True(t, strings.Contains(s, "drem_classifier_llm_tokens_in_total"))
}
