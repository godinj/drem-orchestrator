package agent

import (
	"fmt"
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

	compactToolResultHistory(messages)
	for i := 1; i <= 3; i++ {
		require.Contains(t, messages[i].Content, "result compacted")
		require.Less(t, len(messages[i].Content), len(large))
	}
	require.Equal(t, large, messages[4].Content)
	require.Equal(t, large, messages[5].Content)
}

func TestDirectToolAgentConfigForWorkloadFallsBackToGenericLimits(t *testing.T) {
	cfg := DirectToolAgentConfig{MaxCumulativeInputTokens: 60_000, MaxReadsBeforeMutation: 4, MaxInputTokensBeforeMutation: 25_000}
	got := cfg.ForWorkload("coder", "test")
	require.Equal(t, 60_000, got.MaxCumulativeInputTokens)
	require.Equal(t, 4, got.MaxReadsBeforeMutation)
	require.Equal(t, 25_000, got.MaxInputTokensBeforeMutation)
}
