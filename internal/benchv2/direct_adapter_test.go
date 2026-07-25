package benchv2

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDirectAdapterMapsPreserveThinkingLiterally(t *testing.T) {
	var request map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
		require.NoError(t, json.NewDecoder(incoming.Body).Decode(&request))
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":1,"total_tokens":13}}`))
	}))
	defer server.Close()

	run, err := (DirectToolAdapter{Endpoint: server.URL}).Run(context.Background(), TrialRequest{
		Task: TaskSpec{
			ID: "request-wire", Role: "implementation", AllowedToolPolicies: []string{ToolPolicyStructured, ToolPolicySandboxed},
			Budget: Budget{MaxInputTokens: 1000, MaxOutputTokens: 32, MaxToolCalls: 2, MaxIterations: 1, TimeoutSeconds: 5},
		},
		Seed: 7, Temperature: .6, TopP: .95, TopK: 20, ContextWindow: 131072, PreserveThinking: true,
		Harness: HarnessConfig{HistoryMode: "retain_recent", ToolPolicy: ToolPolicyStructured, KeepRecentExchanges: 3, RetentionThresholdPC: 70},
		Runtime: RuntimeAttestation{ModelID: "qwen3.6-27b-code"},
	})
	require.NoError(t, err)
	require.Equal(t, "done", run.Output)
	kwargs, ok := request["chat_template_kwargs"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, true, kwargs["preserve_thinking"])
	require.NotContains(t, kwargs, "enable_thinking")
}

func TestDirectAdapterRejectsSandboxedShellPolicy(t *testing.T) {
	_, err := (DirectToolAdapter{}).Run(context.Background(), TrialRequest{Task: TaskSpec{AllowedToolPolicies: []string{ToolPolicyStructured, ToolPolicySandboxed}}, Harness: HarnessConfig{ToolPolicy: ToolPolicySandboxed}})
	require.ErrorContains(t, err, "structured_only")
}
