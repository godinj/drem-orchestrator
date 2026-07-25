package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBenchmarkReadAllowlistRejectsAdjacentFile(t *testing.T) {
	dir := t.TempDir()
	allowed := filepath.Join(dir, "allowed.txt")
	denied := filepath.Join(dir, "denied.txt")
	require.NoError(t, os.WriteFile(allowed, []byte("allowed"), 0o644))
	require.NoError(t, os.WriteFile(denied, []byte("denied"), 0o644))
	executor := &toolExecutor{workDir: dir, readableFiles: map[string]struct{}{allowed: {}}}
	result, err := executor.execRead(`{"path":"allowed.txt"}`)
	require.NoError(t, err)
	require.Contains(t, result, "allowed")
	_, err = executor.execRead(`{"path":"denied.txt"}`)
	require.ErrorContains(t, err, "read allowlist")
	_, err = executor.execGrep(`{"pattern":"denied"}`)
	require.ErrorContains(t, err, "read allowlist")
	_, err = executor.execGlob(`{"pattern":"*.txt"}`)
	require.ErrorContains(t, err, "read allowlist")
}

func TestBenchmarkWriteScopeRejectsOutOfScopeMutation(t *testing.T) {
	require.True(t, mutationTargetsOutsideScope("/work", "edit", `{"path":"other.cpp","old":"a","new":"b"}`, []string{"owned.cpp"}))
	require.False(t, mutationTargetsOutsideScope("/work", "edit", `{"path":"owned.cpp","old":"a","new":"b"}`, []string{"owned.cpp"}))
}

func TestRetainRecentHistoryDefersAggressiveFoldingBelowThreshold(t *testing.T) {
	messages := []toolChatMsg{{Role: "system", Content: "system"}, {Role: "user", Content: "task"}}
	for index := 0; index < 4; index++ {
		id := string(rune('a' + index))
		messages = append(messages,
			toolChatMsg{Role: "assistant", ToolCalls: []toolCall{{ID: id, Type: "function", Function: toolCallFunction{Name: "read", Arguments: `{"path":"file"}`}}}},
			toolChatMsg{Role: "tool", ToolCallID: id, Name: "read", Content: string(make([]byte, 500))})
	}
	aggressive, aggressiveBytes := compactToolResultHistoryForConfig(append([]toolChatMsg(nil), messages...), DirectToolAgentConfig{ToolHistoryMode: ToolHistoryAggressive}, 30)
	retained, retainedBytes := compactToolResultHistoryForConfig(append([]toolChatMsg(nil), messages...), DirectToolAgentConfig{ToolHistoryMode: ToolHistoryRetainRecent, ToolHistoryKeepRecentExchanges: 4, ToolHistoryRetentionPct: 70}, 30)
	require.Greater(t, aggressiveBytes, retainedBytes)
	require.Less(t, len(aggressive), len(retained))
	_, foldedAtThreshold := compactToolResultHistoryForConfig(append([]toolChatMsg(nil), messages...), DirectToolAgentConfig{ToolHistoryMode: ToolHistoryRetainRecent, ToolHistoryKeepRecentExchanges: 4, ToolHistoryRetentionPct: 70}, 70)
	require.Equal(t, aggressiveBytes, foldedAtThreshold)
}
