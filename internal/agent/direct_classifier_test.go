package agent

import (
	"context"
	"encoding/json"
	"fmt"
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

// fakeSGLangServer returns a test server that responds with the given JSON
// payload in the chat completion content field. The handler records the
// last received request body so tests can assert on it.
type fakeSGLangServer struct {
	*httptest.Server
	lastRequestBody []byte
	lastHeader      http.Header
}

func newFakeSGLangServer(t *testing.T, contentJSON string, tokensIn, tokensOut int) *fakeSGLangServer {
	t.Helper()
	f := &fakeSGLangServer{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		f.lastRequestBody = body
		f.lastHeader = r.Header.Clone()

		resp := map[string]any{
			"choices": []map[string]any{
				{
					"message": map[string]any{
						"content": contentJSON,
					},
					"finish_reason": "stop",
				},
			},
			"usage": map[string]any{
				"prompt_tokens":     tokensIn,
				"completion_tokens": tokensOut,
				"total_tokens":      tokensIn + tokensOut,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	return f
}

// TestClassify_HappyPath exercises the extracted Classify entry point end-to-end:
// it calls a stub SGLang server, parses the response, and returns the
// classification JSON plus usage stats. This is the core contract the new
// drem-classifier binary will rely on.
func TestClassify_HappyPath(t *testing.T) {
	payload := `{"category":"quickfix","complexity_score":2,"title":"fix typo","description":"single-char typo","target_files":["README.md"],"rationale":"obvious"}`
	srv := newFakeSGLangServer(t, payload, 812, 48)
	defer srv.Close()

	cfg := DirectClassifierConfig{
		Endpoint:    srv.URL,
		Model:       "gemma4-26b",
		MaxTokens:   256,
		Temperature: 0.1,
		Timeout:     5 * time.Second,
		GQCaller:    "classifier",
		GQPriority:  "high",
	}

	result, err := Classify(context.Background(), cfg, ClassifyInput{
		TaskID:      uuid.New(),
		Title:       "Fix typo in README",
		Description: "Trailing whitespace in line 3",
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 812, result.TokensIn)
	assert.Equal(t, 48, result.TokensOut)
	assert.NotZero(t, result.Duration)
	assert.Equal(t, "classifier", srv.lastHeader.Get("X-GQ-Caller"))
	assert.Equal(t, "high", srv.lastHeader.Get("X-GQ-Priority"))

	// Result JSON must be parseable and preserve the classifier decision fields.
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(result.JSON, &parsed))
	assert.Equal(t, "quickfix", parsed["category"])
	assert.EqualValues(t, 2, parsed["complexity_score"])
}

// TestClassify_FiltersInternalContextKeys verifies that orchestrator-internal
// context keys are stripped from the prompt sent to SGLang so the model
// doesn't train on orchestration noise.
func TestClassify_FiltersInternalContextKeys(t *testing.T) {
	payload := `{"category":"standard","complexity_score":5,"title":"t","description":"d","target_files":[],"rationale":"r"}`
	srv := newFakeSGLangServer(t, payload, 100, 50)
	defer srv.Close()

	cfg := DirectClassifierConfig{
		Endpoint: srv.URL,
		Model:    "gemma4-26b",
		Timeout:  5 * time.Second,
	}

	_, err := Classify(context.Background(), cfg, ClassifyInput{
		TaskID: uuid.New(),
		Title:  "t",
		Context: map[string]any{
			"retry_count":  3,           // filtered
			"human_triage": true,        // filtered
			"relevant_ctx": "keep-this", // preserved
		},
	})
	require.NoError(t, err)

	// The recorded request body must contain the user-visible context key and
	// must NOT contain any internal bookkeeping keys.
	body := string(srv.lastRequestBody)
	assert.Contains(t, body, "relevant_ctx")
	assert.Contains(t, body, "keep-this")
	assert.NotContains(t, body, "retry_count")
	assert.NotContains(t, body, "human_triage")
}

// TestClassify_UpstreamError surfaces a non-200 upstream response as an
// error callers can inspect. The message includes the status code so
// failure modes are distinguishable at the orch layer.
func TestClassify_UpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("upstream unavailable"))
	}))
	defer srv.Close()

	cfg := DirectClassifierConfig{
		Endpoint: srv.URL,
		Model:    "gemma4-26b",
		Timeout:  5 * time.Second,
	}

	_, err := Classify(context.Background(), cfg, ClassifyInput{
		TaskID: uuid.New(),
		Title:  "t",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "502")
}

// TestClassify_InvalidJSON reports a parse error when the model returns
// non-JSON content. The classifier response_format pins json_object, but
// broken adapters or mock servers can still break this invariant.
func TestClassify_InvalidJSON(t *testing.T) {
	srv := newFakeSGLangServer(t, "not json {{{", 10, 5)
	defer srv.Close()

	cfg := DirectClassifierConfig{
		Endpoint: srv.URL,
		Model:    "gemma4-26b",
		Timeout:  5 * time.Second,
	}

	_, err := Classify(context.Background(), cfg, ClassifyInput{
		TaskID: uuid.New(),
		Title:  "t",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not valid JSON")
}

// TestClassify_ContextCancelled verifies the propagated context bounds the
// HTTP call; callers that cancel upstream expect Classify to return quickly.
func TestClassify_ContextCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Block long enough for the test-side cancel to win.
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // immediate

	cfg := DirectClassifierConfig{
		Endpoint: srv.URL,
		Model:    "gemma4-26b",
		Timeout:  5 * time.Second,
	}

	_, err := Classify(ctx, cfg, ClassifyInput{
		TaskID: uuid.New(),
		Title:  "t",
	})
	require.Error(t, err)
}

// TestRunDirectClassifier_WritesOutputFile verifies the legacy wrapper still
// writes classification-<taskID>.json so the inline orch rollback path keeps
// working.
func TestRunDirectClassifier_WritesOutputFile(t *testing.T) {
	payload := `{"category":"standard","complexity_score":7,"title":"refactor","description":"split module","target_files":["a.go"],"rationale":"done"}`
	srv := newFakeSGLangServer(t, payload, 200, 80)
	defer srv.Close()

	cfg := DirectClassifierConfig{
		Endpoint: srv.URL,
		Model:    "gemma4-26b",
		Timeout:  5 * time.Second,
	}

	outDir := t.TempDir()
	taskID := uuid.New()
	result, err := RunDirectClassifier(cfg, taskID, "refactor", "split module", nil, outDir)
	require.NoError(t, err)

	expected := filepath.Join(outDir, fmt.Sprintf("classification-%s.json", taskID))
	assert.Equal(t, expected, result.OutputPath)
	assert.Equal(t, 200, result.TokensIn)
	assert.Equal(t, 80, result.TokensOut)

	data, err := os.ReadFile(expected)
	require.NoError(t, err)
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(data, &parsed))
	assert.Equal(t, "standard", parsed["category"])
}
