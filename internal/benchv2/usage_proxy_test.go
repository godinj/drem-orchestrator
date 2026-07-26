package benchv2

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const testProxyPublicBaseURL = "http://canvasbench-usage-proxy:8080/v1"
const testProxySourceState = "0123456789abcdef-dirty-fedcba9876543210"
const testProxyConfigSHA = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"

func TestUsageProxyMeasuresNonStreamingServerUsage(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		require.Equal(t, "Bearer upstream-secret", request.Header.Get("Authorization"))
		var payload map[string]any
		require.NoError(t, json.NewDecoder(request.Body).Decode(&payload))
		require.NotEqual(t, true, payload["stream"])
		require.Equal(t, "qwen-runtime", payload["model"])
		require.Equal(t, float64(42), payload["seed"])
		require.Equal(t, 0.2, payload["temperature"])
		require.Equal(t, 0.9, payload["top_p"])
		require.Equal(t, float64(20), payload["top_k"])
		require.Equal(t, float64(1024), payload["max_tokens"])
		kwargs, ok := payload["chat_template_kwargs"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, true, kwargs["preserve_thinking"])
		require.Equal(t, "retained", kwargs["other"])
		writeProxyJSON(writer, http.StatusOK, map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "done"}}},
			"usage":   map[string]int{"prompt_tokens": 11, "completion_tokens": 3, "total_tokens": 14},
		})
	}))
	defer upstream.Close()
	proxy, client := usageProxyFixture(t, upstream.URL+"/v1/chat/completions", "upstream-secret")
	session := startTestUsageSession(t, client)

	requestBody := `{"model":"wrong","seed":1,"temperature":1,"top_p":0.1,"top_k":1,"max_tokens":1,"chat_template_kwargs":{"preserve_thinking":false,"other":"retained"},"messages":[]}`
	response := postTestCompletion(t, proxy.URL, session.APIKey, requestBody)
	require.Equal(t, http.StatusOK, response.StatusCode)
	_ = response.Body.Close()
	response = postTestCompletion(t, proxy.URL, session.APIKey, requestBody)
	require.Equal(t, http.StatusOK, response.StatusCode)
	_ = response.Body.Close()
	usage, err := client.AttestServerUsage(context.Background(), session)
	require.NoError(t, err)
	require.Equal(t, ServerUsage{
		Source: ServerUsageSourceProxy, CorrelationID: session.CorrelationID,
		RequestsMeasured: 2, RequestsTotal: 2, PromptTokens: 22, CompletionTokens: 6, Complete: true,
	}, usage)
	_, err = client.AttestServerUsage(context.Background(), session)
	require.ErrorContains(t, err, "HTTP 409")
}

func TestUsageProxyForcesAndMeasuresStreamingUsage(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var payload map[string]any
		require.NoError(t, json.NewDecoder(request.Body).Decode(&payload))
		require.Equal(t, true, payload["stream"])
		options, ok := payload["stream_options"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, true, options["include_usage"])
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}],\"usage\":null}\n\n")
		_, _ = io.WriteString(writer, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":2}}\n\n")
		_, _ = io.WriteString(writer, "data: [DONE]\n\n")
	}))
	defer upstream.Close()
	proxy, client := usageProxyFixture(t, upstream.URL+"/v1/chat/completions", "")
	session := startTestUsageSession(t, client)
	response := postTestCompletion(t, proxy.URL, session.APIKey, `{"model":"qwen","stream":true,"stream_options":{"include_usage":false},"messages":[]}`)
	raw, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	_ = response.Body.Close()
	require.Contains(t, string(raw), "[DONE]")
	usage, err := client.AttestServerUsage(context.Background(), session)
	require.NoError(t, err)
	require.Equal(t, 7, usage.PromptTokens)
	require.Equal(t, 2, usage.CompletionTokens)
}

func TestUsageProxyForcesSingleStrictPhaseTool(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var payload map[string]any
		require.NoError(t, json.NewDecoder(request.Body).Decode(&payload))
		choice := payload["tool_choice"].(map[string]any)
		require.Equal(t, "function", choice["type"])
		require.Equal(t, "complete_contract", choice["function"].(map[string]any)["name"])
		require.Equal(t, false, payload["parallel_tool_calls"])
		tools := payload["tools"].([]any)
		function := tools[0].(map[string]any)["function"].(map[string]any)
		require.Equal(t, true, function["strict"])
		kwargs := payload["chat_template_kwargs"].(map[string]any)
		require.Equal(t, false, kwargs["enable_thinking"])
		require.Equal(t, true, kwargs["preserve_thinking"])
		writeProxyJSON(writer, http.StatusOK, map[string]any{
			"choices": []any{}, "usage": map[string]int{"prompt_tokens": 3, "completion_tokens": 2},
		})
	}))
	defer upstream.Close()
	proxy, client := usageProxyFixture(t, upstream.URL+"/v1/chat/completions", "")
	trialRequest := testUsageTrialRequest()
	trialRequest.Task.PhaseContract = &PhaseContract{ToolName: "complete_contract"}
	trialRequest.Harness.PhaseContractMode = PiPhaseContractEnforcedV1
	session, err := client.StartServerUsage(context.Background(), trialRequest)
	require.NoError(t, err)
	response := postTestCompletion(t, proxy.URL, session.APIKey, `{
		"messages":[],
		"tools":[{"type":"function","function":{"name":"complete_contract","parameters":{"type":"object","properties":{},"required":[],"additionalProperties":false}}}]
	}`)
	require.Equal(t, http.StatusOK, response.StatusCode)
	_ = response.Body.Close()
	usage, err := client.AttestServerUsage(context.Background(), session)
	require.NoError(t, err)
	require.Equal(t, 3, usage.PromptTokens)
}

func TestUsageProxyRejectsMissingForcedPhaseTool(t *testing.T) {
	payload := map[string]any{"tools": []any{}}
	err := applyUsageProxyTrialPolicy(payload, UsageProxyTrialPolicy{
		ModelID: "qwen", Seed: 1, Temperature: 0.2, TopP: 0.9, TopK: 20,
		ContextWindow: 32768, MaxOutputTokens: 256, ForcedToolName: "complete_contract",
	})
	require.ErrorContains(t, err, "requires exactly one tool")
}

func TestUsageProxyAdminAuthAndCrossTrialIsolation(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, `{"choices":[],"usage":{"prompt_tokens":2,"completion_tokens":1}}`)
	}))
	defer upstream.Close()
	proxy, client := usageProxyFixture(t, upstream.URL+"/v1/chat/completions", "")

	unauthorized, err := http.NewRequest(http.MethodPost, proxy.URL+"/admin/v1/trials", nil)
	require.NoError(t, err)
	response, err := http.DefaultClient.Do(unauthorized)
	require.NoError(t, err)
	require.Equal(t, http.StatusUnauthorized, response.StatusCode)
	_ = response.Body.Close()

	first := startTestUsageSession(t, client)
	second := startTestUsageSession(t, client)
	require.NotEqual(t, first.CorrelationID, second.CorrelationID)
	require.NotEqual(t, first.APIKey, second.APIKey)
	require.GreaterOrEqual(t, len(first.APIKey), 40)

	response = postTestCompletion(t, proxy.URL, first.APIKey, `{"messages":[]}`)
	require.Equal(t, http.StatusOK, response.StatusCode)
	_ = response.Body.Close()
	firstUsage, err := client.AttestServerUsage(context.Background(), first)
	require.NoError(t, err)
	require.Equal(t, 1, firstUsage.RequestsTotal)

	response = postTestCompletion(t, proxy.URL, first.APIKey, `{"messages":[]}`)
	require.Equal(t, http.StatusUnauthorized, response.StatusCode, "consumed trial key must be frozen")
	_ = response.Body.Close()
	response = postTestCompletion(t, proxy.URL, second.APIKey, `{"messages":[]}`)
	require.Equal(t, http.StatusOK, response.StatusCode)
	_ = response.Body.Close()
	secondUsage, err := client.AttestServerUsage(context.Background(), second)
	require.NoError(t, err)
	require.Equal(t, 1, secondUsage.RequestsTotal, "first trial traffic must not enter the second ledger")
}

func TestUsageProxyRejectsInvalidTrialPolicyBeforeCredentialCreation(t *testing.T) {
	proxy, client := usageProxyFixture(t, "http://127.0.0.1:1/v1/chat/completions", "")
	server := proxy.Config.Handler.(*usageProxyServer)
	_, err := client.StartServerUsage(context.Background(), TrialRequest{})
	require.ErrorContains(t, err, "HTTP 400")
	server.mu.Lock()
	require.Empty(t, server.trials)
	server.mu.Unlock()
}

func TestUsageProxyLiveAttestationRejectsEveryMismatchBeforeTrialCreation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*UsageProxyAttestation)
	}{
		{"source state", func(attestation *UsageProxyAttestation) { attestation.SourceState = "wrong-source" }},
		{"image", func(attestation *UsageProxyAttestation) {
			attestation.Image = "ghcr.io/godinj/wrong@sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
		}},
		{"config", func(attestation *UsageProxyAttestation) {
			attestation.ConfigSHA256 = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler, err := NewUsageProxyHandler(UsageProxyServerConfig{
				UpstreamChatCompletions: "http://127.0.0.1:1/v1/chat/completions",
				PublicBaseURL:           testProxyPublicBaseURL, AdminToken: "admin-secret",
				SourceState: testProxySourceState, Image: testUsageProxyImage, ConfigSHA256: testProxyConfigSHA,
			})
			require.NoError(t, err)
			proxyState := handler.(*usageProxyServer)
			proxy := httptest.NewServer(handler)
			defer proxy.Close()
			expected := testUsageProxyAttestation()
			test.mutate(&expected)
			client, err := NewUsageProxyClient(UsageProxyClientConfig{
				AdminURL: proxy.URL, PublicBaseURL: testProxyPublicBaseURL, AdminToken: "admin-secret",
				ExpectedAttestation: expected,
			})
			require.NoError(t, err)
			_, err = client.StartServerUsage(context.Background(), TrialRequest{})
			require.ErrorContains(t, err, "does not match the matrix")
			proxyState.mu.Lock()
			require.Empty(t, proxyState.trials, "attestation mismatch must precede trial credential creation")
			proxyState.mu.Unlock()
		})
	}
}

func TestUsageProxyLiveAttestationRequiresAdminAuthentication(t *testing.T) {
	proxy, _ := usageProxyFixture(t, "http://127.0.0.1:1/v1/chat/completions", "")
	response, err := http.Get(proxy.URL + "/admin/v1/attestation")
	require.NoError(t, err)
	require.Equal(t, http.StatusUnauthorized, response.StatusCode)
	_ = response.Body.Close()
}

func TestUsageProxyFailsClosedAcrossUpstreamRetry(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			writeProxyError(writer, http.StatusInternalServerError, "retry me")
			return
		}
		_, _ = io.WriteString(writer, `{"choices":[],"usage":{"prompt_tokens":5,"completion_tokens":1}}`)
	}))
	defer upstream.Close()
	proxy, client := usageProxyFixture(t, upstream.URL+"/v1/chat/completions", "")
	session := startTestUsageSession(t, client)
	response := postTestCompletion(t, proxy.URL, session.APIKey, `{"messages":[]}`)
	require.Equal(t, http.StatusInternalServerError, response.StatusCode)
	_ = response.Body.Close()
	response = postTestCompletion(t, proxy.URL, session.APIKey, `{"messages":[]}`)
	require.Equal(t, http.StatusOK, response.StatusCode)
	_ = response.Body.Close()
	_, err := client.AttestServerUsage(context.Background(), session)
	require.ErrorContains(t, err, "HTTP 422", "unmeasured failed attempts make the entire trial incomparable")
	_, err = client.AttestServerUsage(context.Background(), session)
	require.ErrorContains(t, err, "HTTP 409", "an incomplete ledger is still consume-once")
}

func TestUsageProxyAttestsPreInferenceClientRejection(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writeProxyError(writer, http.StatusBadRequest, "context length exceeded")
	}))
	defer upstream.Close()
	proxy, client := usageProxyFixture(t, upstream.URL+"/v1/chat/completions", "")
	session := startTestUsageSession(t, client)
	response := postTestCompletion(t, proxy.URL, session.APIKey, `{"messages":[]}`)
	require.Equal(t, http.StatusBadRequest, response.StatusCode)
	_ = response.Body.Close()
	usage, err := client.AttestServerUsage(context.Background(), session)
	require.NoError(t, err)
	require.True(t, usage.Complete)
	require.Equal(t, 0, usage.RequestsMeasured)
	require.Equal(t, 1, usage.RequestsRejected)
	require.Equal(t, 1, usage.RequestsTotal)
	require.Equal(t, []ServerUsageRejection{{HTTPStatus: http.StatusBadRequest, Count: 1}}, usage.Rejections)
	require.Zero(t, usage.PromptTokens)
	require.Zero(t, usage.CompletionTokens)
}

func TestUsageProxyPreservesMeasuredUsageBeforeClientRejection(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			_, _ = io.WriteString(writer, `{"choices":[],"usage":{"prompt_tokens":17,"completion_tokens":4}}`)
			return
		}
		writeProxyError(writer, http.StatusBadRequest, "context length exceeded")
	}))
	defer upstream.Close()
	proxy, client := usageProxyFixture(t, upstream.URL+"/v1/chat/completions", "")
	session := startTestUsageSession(t, client)
	first := postTestCompletion(t, proxy.URL, session.APIKey, `{"messages":[]}`)
	require.Equal(t, http.StatusOK, first.StatusCode)
	_ = first.Body.Close()
	second := postTestCompletion(t, proxy.URL, session.APIKey, `{"messages":[]}`)
	require.Equal(t, http.StatusBadRequest, second.StatusCode)
	_ = second.Body.Close()
	usage, err := client.AttestServerUsage(context.Background(), session)
	require.NoError(t, err)
	require.Equal(t, 1, usage.RequestsMeasured)
	require.Equal(t, 1, usage.RequestsRejected)
	require.Equal(t, 2, usage.RequestsTotal)
	require.Equal(t, 17, usage.PromptTokens)
	require.Equal(t, 4, usage.CompletionTokens)
}

func TestUsageProxyAggregatesOnlyAttributableRequestRejections(t *testing.T) {
	statuses := []int{http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity, http.StatusRequestEntityTooLarge}
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		status := statuses[int(calls.Add(1))-1]
		writeProxyError(writer, status, "request validation failed")
	}))
	defer upstream.Close()
	proxy, client := usageProxyFixture(t, upstream.URL+"/v1/chat/completions", "")
	session := startTestUsageSession(t, client)
	for _, status := range statuses {
		response := postTestCompletion(t, proxy.URL, session.APIKey, `{"messages":[]}`)
		require.Equal(t, status, response.StatusCode)
		_ = response.Body.Close()
	}
	usage, err := client.AttestServerUsage(context.Background(), session)
	require.NoError(t, err)
	require.Equal(t, 3, usage.RequestsRejected)
	require.Equal(t, []ServerUsageRejection{
		{HTTPStatus: http.StatusRequestEntityTooLarge, Count: 2},
		{HTTPStatus: http.StatusUnprocessableEntity, Count: 1},
	}, usage.Rejections)
}

func TestUsageProxyDoesNotAttributeInfrastructureOrConfigurationFailures(t *testing.T) {
	for _, status := range []int{
		http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusRequestTimeout,
		http.StatusConflict, http.StatusTooEarly, http.StatusTooManyRequests, http.StatusInternalServerError,
	} {
		t.Run(fmt.Sprintf("HTTP_%d", status), func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writeProxyError(writer, status, "not attributable")
			}))
			defer upstream.Close()
			proxy, client := usageProxyFixture(t, upstream.URL+"/v1/chat/completions", "")
			session := startTestUsageSession(t, client)
			response := postTestCompletion(t, proxy.URL, session.APIKey, `{"messages":[]}`)
			require.Equal(t, status, response.StatusCode)
			_ = response.Body.Close()
			_, err := client.AttestServerUsage(context.Background(), session)
			require.ErrorContains(t, err, "HTTP 422")
		})
	}
}

func TestValidateServerUsageRejectsMalformedRejectionAccounting(t *testing.T) {
	valid := ServerUsage{
		Source: ServerUsageSourceProxy, CorrelationID: "trial", RequestsRejected: 1, RequestsTotal: 1,
		Rejections: []ServerUsageRejection{{HTTPStatus: http.StatusBadRequest, Count: 1}}, Complete: true,
	}
	require.NoError(t, validateServerUsage(valid, "trial"))
	for _, mutate := range []func(*ServerUsage){
		func(usage *ServerUsage) { usage.Rejections[0].Count = 2 },
		func(usage *ServerUsage) { usage.Rejections[0].Count = 0 },
		func(usage *ServerUsage) { usage.Rejections[0].HTTPStatus = http.StatusTooManyRequests },
		func(usage *ServerUsage) { usage.Rejections = append(usage.Rejections, usage.Rejections[0]) },
	} {
		candidate := valid
		candidate.Rejections = append([]ServerUsageRejection(nil), valid.Rejections...)
		mutate(&candidate)
		require.Error(t, validateServerUsage(candidate, "trial"))
	}
}

func TestUsageProxyRefusesToFreezeInFlightLedger(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		_, _ = io.WriteString(writer, `{"choices":[],"usage":{"prompt_tokens":3,"completion_tokens":1}}`)
	}))
	defer upstream.Close()
	proxy, client := usageProxyFixture(t, upstream.URL+"/v1/chat/completions", "")
	session := startTestUsageSession(t, client)
	responseDone := make(chan *http.Response, 1)
	go func() {
		request, _ := http.NewRequest(http.MethodPost, proxy.URL+"/v1/chat/completions", bytes.NewBufferString(`{"messages":[]}`))
		request.Header.Set("Authorization", "Bearer "+session.APIKey)
		response, _ := http.DefaultClient.Do(request)
		responseDone <- response
	}()
	<-started
	usageDone := make(chan ServerUsage, 1)
	usageErr := make(chan error, 1)
	go func() {
		usage, err := client.AttestServerUsage(context.Background(), session)
		usageDone <- usage
		usageErr <- err
	}()
	require.Eventually(t, func() bool {
		server := proxy.Config.Handler.(*usageProxyServer)
		server.mu.Lock()
		defer server.mu.Unlock()
		return server.trials[session.CorrelationID].Frozen
	}, time.Second, 10*time.Millisecond)
	frozenResponse := postTestCompletion(t, proxy.URL, session.APIKey, `{"messages":[]}`)
	require.Equal(t, http.StatusUnauthorized, frozenResponse.StatusCode)
	_ = frozenResponse.Body.Close()
	close(release)
	response := <-responseDone
	require.NotNil(t, response)
	_ = response.Body.Close()
	usage := <-usageDone
	require.NoError(t, <-usageErr)
	require.Equal(t, 1, usage.RequestsMeasured)
}

func TestUsageProxyDrainsUsageAfterHarnessDisconnect(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		close(started)
		<-release
		_, _ = io.WriteString(writer, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":13,\"completion_tokens\":5}}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()
	proxy, client := usageProxyFixture(t, upstream.URL+"/v1/chat/completions", "")
	session := startTestUsageSession(t, client)
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, proxy.URL+"/v1/chat/completions", bytes.NewBufferString(`{"stream":true,"messages":[]}`))
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer "+session.APIKey)
	requestDone := make(chan error, 1)
	go func() {
		response, err := http.DefaultClient.Do(request)
		if response != nil {
			_ = response.Body.Close()
		}
		requestDone <- err
	}()
	<-started
	cancelRequest()
	require.Error(t, <-requestDone)
	close(release)
	attestCtx, cancelAttest := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelAttest()
	usage, err := client.AttestServerUsage(attestCtx, session)
	require.NoError(t, err)
	require.Equal(t, 13, usage.PromptTokens)
	require.Equal(t, 5, usage.CompletionTokens)
}

func TestUsageProxyRejectsMissingAndDuplicateUsage(t *testing.T) {
	tests := []struct {
		name, response string
	}{
		{"missing", "data: {\"choices\":[]}\n\ndata: [DONE]\n\n"},
		{"duplicate", "data: {\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1}}\n\ndata: {\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1}}\n\ndata: [DONE]\n\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(writer, test.response)
			}))
			defer upstream.Close()
			proxy, client := usageProxyFixture(t, upstream.URL+"/v1/chat/completions", "")
			session := startTestUsageSession(t, client)
			response := postTestCompletion(t, proxy.URL, session.APIKey, `{"stream":true,"messages":[]}`)
			_ = response.Body.Close()
			_, err := client.AttestServerUsage(context.Background(), session)
			require.ErrorContains(t, err, "HTTP 422")
		})
	}
}

func TestReadPrivateTokenFileRequiresOwnerOnlyRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "admin.token")
	require.NoError(t, os.WriteFile(path, []byte("secret\n"), 0o600))
	token, err := ReadPrivateTokenFile(path)
	require.NoError(t, err)
	require.Equal(t, "secret", token)
	require.NoError(t, os.Chmod(path, 0o644))
	_, err = ReadPrivateTokenFile(path)
	require.ErrorContains(t, err, "only by its owner")
}

func usageProxyFixture(t *testing.T, upstreamURL, upstreamKey string) (*httptest.Server, *UsageProxyClient) {
	t.Helper()
	handler, err := NewUsageProxyHandler(UsageProxyServerConfig{
		UpstreamChatCompletions: upstreamURL, UpstreamAPIKey: upstreamKey,
		PublicBaseURL: testProxyPublicBaseURL, AdminToken: "admin-secret",
		SourceState: testProxySourceState, Image: testUsageProxyImage, ConfigSHA256: testProxyConfigSHA,
	})
	require.NoError(t, err)
	proxy := httptest.NewServer(handler)
	t.Cleanup(proxy.Close)
	client, err := NewUsageProxyClient(UsageProxyClientConfig{
		AdminURL: proxy.URL, PublicBaseURL: testProxyPublicBaseURL, AdminToken: "admin-secret",
		ExpectedAttestation: testUsageProxyAttestation(),
	})
	require.NoError(t, err)
	return proxy, client
}

func startTestUsageSession(t *testing.T, client *UsageProxyClient) UsageSession {
	t.Helper()
	session, err := client.StartServerUsage(context.Background(), testUsageTrialRequest())
	require.NoError(t, err)
	return session
}

func testUsageTrialRequest() TrialRequest {
	return TrialRequest{
		Task: TaskSpec{Budget: Budget{MaxOutputTokens: 1024}}, Runtime: RuntimeAttestation{ModelID: "qwen-runtime"},
		Seed: 42, Temperature: 0.2, TopP: 0.9, TopK: 20, ContextWindow: 32768, PreserveThinking: true,
	}
}

func postTestCompletion(t *testing.T, proxyURL, apiKey, body string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, proxyURL+"/v1/chat/completions", bytes.NewBufferString(body))
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer "+apiKey)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	return response
}

func TestUsageProxyClientRejectsPublicBaseURLMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/admin/v1/attestation" {
			writeProxyJSON(writer, http.StatusOK, testUsageProxyAttestation())
			return
		}
		_, _ = fmt.Fprintln(writer, `{"correlation_id":"id","base_url":"http://wrong/v1","api_key":"key"}`)
	}))
	defer server.Close()
	client, err := NewUsageProxyClient(UsageProxyClientConfig{
		AdminURL: server.URL, PublicBaseURL: testProxyPublicBaseURL, AdminToken: "admin",
		ExpectedAttestation: testUsageProxyAttestation(),
	})
	require.NoError(t, err)
	_, err = client.StartServerUsage(context.Background(), TrialRequest{})
	require.ErrorContains(t, err, "invalid trial credential")
}

func TestUsageProxyNeverForwardsTrialCredentialUpstream(t *testing.T) {
	var upstreamAuthorization string
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		upstreamAuthorization = request.Header.Get("Authorization")
		_, _ = io.WriteString(writer, `{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	defer upstream.Close()
	proxy, client := usageProxyFixture(t, upstream.URL+"/v1/chat/completions", "upstream-only")
	session := startTestUsageSession(t, client)
	response := postTestCompletion(t, proxy.URL, session.APIKey, `{"messages":[]}`)
	_ = response.Body.Close()
	_, err := client.AttestServerUsage(context.Background(), session)
	require.NoError(t, err)
	require.Equal(t, "Bearer upstream-only", upstreamAuthorization)
	require.NotContains(t, upstreamAuthorization, session.APIKey)
}

func TestUsageProxyErrorResponseDoesNotLeakSecrets(t *testing.T) {
	handler, err := NewUsageProxyHandler(UsageProxyServerConfig{
		UpstreamChatCompletions: "http://127.0.0.1:1/v1/chat/completions",
		PublicBaseURL:           testProxyPublicBaseURL, AdminToken: "admin-secret",
		SourceState: testProxySourceState, Image: testUsageProxyImage, ConfigSHA256: testProxyConfigSHA,
	})
	require.NoError(t, err)
	proxy := httptest.NewServer(handler)
	defer proxy.Close()
	response := postTestCompletion(t, proxy.URL, "not-a-trial-key", `{}`)
	raw, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	_ = response.Body.Close()
	require.Equal(t, http.StatusUnauthorized, response.StatusCode)
	require.NotContains(t, strings.ToLower(string(raw)), "admin-secret")
}

func testUsageProxyAttestation() UsageProxyAttestation {
	return UsageProxyAttestation{SourceState: testProxySourceState, Image: testUsageProxyImage, ConfigSHA256: testProxyConfigSHA}
}
