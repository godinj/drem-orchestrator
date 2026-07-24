package agent

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDirectToolAgentConfigForWorkload(t *testing.T) {
	cfg := DirectToolAgentConfig{
		MaxCumulativeInputTokens:                   60_000,
		TestMaxCumulativeInputTokens:               65_000,
		ImplementationMaxCumulativeInputTokens:     90_000,
		IntegrationMaxCumulativeInputTokens:        75_000,
		ReviewMaxCumulativeInputTokens:             30_000,
		MaxReadsBeforeMutation:                     4,
		TestMaxReadsBeforeMutation:                 8,
		ImplementationMaxReadsBeforeMutation:       6,
		IntegrationMaxReadsBeforeMutation:          6,
		MaxInputTokensBeforeMutation:               20_000,
		TestMaxInputTokensBeforeMutation:           18_000,
		ImplementationMaxInputTokensBeforeMutation: 30_000,
		IntegrationMaxInputTokensBeforeMutation:    24_000,
	}

	testCfg := cfg.ForWorkload("coder", "test")
	require.Equal(t, 65_000, testCfg.MaxCumulativeInputTokens)
	require.Equal(t, 8, testCfg.MaxReadsBeforeMutation)
	require.Equal(t, 18_000, testCfg.MaxInputTokensBeforeMutation)

	implCfg := cfg.ForWorkload("coder", "implementation")
	require.Equal(t, 90_000, implCfg.MaxCumulativeInputTokens)
	require.Equal(t, 6, implCfg.MaxReadsBeforeMutation)
	require.Equal(t, 30_000, implCfg.MaxInputTokensBeforeMutation)

	integrationCfg := cfg.ForWorkload("coder", "integration")
	require.Equal(t, 75_000, integrationCfg.MaxCumulativeInputTokens)
	require.Equal(t, 6, integrationCfg.MaxReadsBeforeMutation)
	require.Equal(t, 24_000, integrationCfg.MaxInputTokensBeforeMutation)
	require.True(t, integrationCfg.AllowReadOnlyCompletion)

	reviewCfg := cfg.ForWorkload("reviewer", "")
	require.Equal(t, 30_000, reviewCfg.MaxCumulativeInputTokens)
	require.Zero(t, reviewCfg.MaxReadsBeforeMutation)

	fixerCfg := cfg.ForWorkload("fixer", "")
	require.Equal(t, 90_000, fixerCfg.MaxCumulativeInputTokens)
	require.Equal(t, 6, fixerCfg.MaxReadsBeforeMutation)
}

func TestReconnaissanceClassifierCatchesBashDiscoveryBypass(t *testing.T) {
	for _, command := range []string{
		"ls tests/integration",
		"find src -name '*Transient*'",
		"rg -n Transient src",
		"grep -R Transient src",
		"git diff --stat",
	} {
		require.True(t, isReconnaissanceToolCall("bash", `{"cmd":`+strconv.Quote(command)+`}`), command)
	}
	require.False(t, isReconnaissanceToolCall("bash", `{"cmd":"scripts/dev test --filter Transient"}`))
	require.True(t, isReconnaissanceToolCall("read", `{"path":"a.go"}`))
}

func TestCompactToolResultHistoryKeepsOnlyRecentFullResults(t *testing.T) {
	large := strings.Repeat("result-data-", 100)
	messages := []toolChatMsg{{Role: "system", Content: "sys"}}
	for i := 0; i < 5; i++ {
		messages = append(messages, toolChatMsg{Role: "tool", Name: "read", ToolCallID: fmt.Sprintf("c%d", i), Content: large})
	}

	messages = compactToolResultHistory(messages)
	for i := 1; i <= 4; i++ {
		require.Contains(t, messages[i].Content, "result compacted")
		require.Less(t, len(messages[i].Content), len(large))
	}
	require.Equal(t, large, messages[5].Content)
}

func TestCompactToolResultHistoryConvertsOldMutationCallsToNonCallableSummaries(t *testing.T) {
	large := strings.Repeat("source-line\n", 1_000)
	messages := []toolChatMsg{{Role: "system", Content: "sys"}}
	originalArguments := make([]string, 0, 3)
	for i := 0; i < 3; i++ {
		args := fmt.Sprintf(`{"path":"src/file%d.cpp","content":%q}`, i, large)
		originalArguments = append(originalArguments, args)
		messages = append(messages,
			toolChatMsg{Role: "assistant", ToolCalls: []toolCall{{
				ID: fmt.Sprintf("c%d", i), Type: "function",
				Function: toolCallFunction{Name: "write", Arguments: args},
			}}},
			toolChatMsg{Role: "tool", Name: "write", ToolCallID: fmt.Sprintf("c%d", i), Content: "ok"},
		)
	}

	compacted, folded := compactToolResultHistoryWithStats(messages)
	require.Positive(t, folded)
	toolCallMessages := 0
	summaries := 0
	for _, message := range compacted {
		if len(message.ToolCalls) > 0 {
			toolCallMessages++
			require.Equal(t, originalArguments[2], message.ToolCalls[0].Function.Arguments)
		}
		if strings.HasPrefix(message.Content, "[HARNESS: prior tool exchange compacted]") {
			summaries++
			require.Empty(t, message.ToolCalls)
		}
	}
	require.Equal(t, 1, toolCallMessages)
	require.Equal(t, 2, summaries)
}

func TestCompactToolResultHistoryNeverKeepsCompactedReplayCallable(t *testing.T) {
	messages := []toolChatMsg{
		{Role: "assistant", ToolCalls: []toolCall{{ID: "read", Function: toolCallFunction{Name: "read", Arguments: `{"path":"src/file.cpp"}`}}}},
		{Role: "tool", Name: "read", ToolCallID: "read", Content: "full file"},
		{Role: "assistant", ToolCalls: []toolCall{{ID: "bad", Function: toolCallFunction{Name: "edit", Arguments: `{"_drem_compacted":"prior tool arguments","path":"src/file.cpp"}`}}}},
		{Role: "tool", Name: "edit", ToolCallID: "bad", Content: "ERROR: old string appears many times"},
	}
	compacted := compactToolResultHistory(messages)
	for _, message := range compacted {
		for _, call := range message.ToolCalls {
			require.NotContains(t, call.Function.Arguments, `_drem_compacted`)
		}
	}
	require.Len(t, compacted[0].ToolCalls, 1, "latest valid read remains replayable")
}

func TestBoundedRequestMaxTokensSeparatesLiveContextFromCumulativeBudget(t *testing.T) {
	cfg := DirectToolAgentConfig{MaxTokens: 8192, ContextLimit: 32768}
	messages := []toolChatMsg{
		{Role: "system", Content: strings.Repeat("s", 1200)},
		{Role: "user", Content: strings.Repeat("u", 100_000)},
	}
	got := boundedRequestMaxTokens(cfg, messages, nil)
	require.Positive(t, got)
	require.Less(t, got, cfg.MaxTokens)
	require.LessOrEqual(t, (101_200+3)/4+got+4096, cfg.ContextLimit)
}

func TestBoundedRequestMaxTokensLeavesHeadroomForObservedQwenTokenizerDelta(t *testing.T) {
	// The live Canvas failure had 25,903 server-counted input tokens while the
	// byte estimator undercounted by 1,025. Preserve at least 3,071 tokens of
	// headroom after that exact observed delta rather than riding the boundary.
	cfg := DirectToolAgentConfig{MaxTokens: 8192, ContextLimit: 32768}
	messages := []toolChatMsg{{Role: "user", Content: strings.Repeat("x", 99_500)}}
	got := boundedRequestMaxTokens(cfg, messages, nil)
	estimatedInput := (99_500 + 3) / 4
	actualInput := estimatedInput + 1025
	require.LessOrEqual(t, actualInput+got, cfg.ContextLimit-3071)
}

func TestCallToolAPIRetriesProviderReportedContextRejectionWithoutInference(t *testing.T) {
	requestMaxTokens := make([]int, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request toolChatRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		requestMaxTokens = append(requestMaxTokens, request.MaxTokens)
		if len(requestMaxTokens) == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"This model's maximum context length is 32768 tokens. However, you requested 7694 output tokens and your prompt contains at least 25075 input tokens, for a total of at least 32769 tokens."}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(toolChatResponse{Choices: []toolChatChoice{{
			Message: toolChatMsg{Role: "assistant", Content: "done"}, FinishReason: "stop",
		}}})
	}))
	defer server.Close()

	cfg := DirectToolAgentConfig{Endpoint: server.URL, Model: "qwen", MaxTokens: 8192, ContextLimit: 32768}
	response, err := callToolAPI(cfg, []toolChatMsg{{Role: "user", Content: "work"}}, nil)
	require.NoError(t, err)
	require.Equal(t, "done", response.Choices[0].Message.Content)
	require.Len(t, requestMaxTokens, 2)
	require.Equal(t, requestMaxTokens[0]/2, requestMaxTokens[1])
	require.Less(t, requestMaxTokens[1], requestMaxTokens[0])
}

func TestDirectToolAgentConfigForWorkloadFallsBackToGenericLimits(t *testing.T) {
	cfg := DirectToolAgentConfig{MaxCumulativeInputTokens: 60_000, MaxReadsBeforeMutation: 4, MaxInputTokensBeforeMutation: 25_000}
	got := cfg.ForWorkload("coder", "test")
	require.Equal(t, 60_000, got.MaxCumulativeInputTokens)
	require.Equal(t, 4, got.MaxReadsBeforeMutation)
	require.Equal(t, 25_000, got.MaxInputTokensBeforeMutation)
}

func TestDirectToolAgentConfigForWorkloadReservesOneMutationTurn(t *testing.T) {
	cfg := DirectToolAgentConfig{
		MaxCumulativeInputTokens:         60_000,
		TestMaxCumulativeInputTokens:     65_000,
		MaxInputTokensBeforeMutation:     20_000,
		TestMaxInputTokensBeforeMutation: 55_000,
	}

	got := cfg.ForWorkload("coder", "test")
	require.Equal(t, 65_000, got.MaxCumulativeInputTokens)
	require.Equal(t, 45_000, got.MaxInputTokensBeforeMutation)
}

func TestDeriveWorkloadBudgetUsesPromptAndScopeInsteadOfFixedPhaseCeiling(t *testing.T) {
	cfg := DirectToolAgentConfig{
		MaxCumulativeInputTokens:     65_000,
		MaxInputTokensBeforeMutation: 20_000,
		MaxIterations:                6,
		MaxToolCalls:                 12,
	}
	got := DeriveWorkloadBudget(cfg, "coder", "test", 45_000, 2)
	require.Greater(t, got.MaxCumulativeInputTokens, 65_000)
	require.GreaterOrEqual(t, got.MaxIterations, 10)
	require.GreaterOrEqual(t, got.MaxToolCalls, 16)
	require.LessOrEqual(t, got.MaxInputTokensBeforeMutation, got.MaxCumulativeInputTokens-minimumMutationTurnHeadroom)
}

func TestDeriveExpectedTurnsBudgetAggregatesAtomicManifestStages(t *testing.T) {
	cfg := DirectToolAgentConfig{
		MaxCumulativeInputTokens: 90_000,
		MaxIterations:            12,
		MaxToolCalls:             20,
	}
	got := DeriveExpectedTurnsBudget(cfg, "coder", 45_000, 8, 44)
	require.GreaterOrEqual(t, got.MaxCumulativeInputTokens, 326_000)
	require.GreaterOrEqual(t, got.MaxIterations, 46)
	require.GreaterOrEqual(t, got.MaxToolCalls, 88)
}

func TestExtendBudgetForJournalResumeAddsFreshSegmentToConsumedCounters(t *testing.T) {
	cfg := DirectToolAgentConfig{MaxCumulativeInputTokens: 300_000, MaxIterations: 46, MaxToolCalls: 88}
	got := ExtendBudgetForJournalResume(cfg, 158_531, 8, 11)
	require.Equal(t, 458_531, got.MaxCumulativeInputTokens)
	require.Equal(t, 54, got.MaxIterations)
	require.Equal(t, 99, got.MaxToolCalls)
}
