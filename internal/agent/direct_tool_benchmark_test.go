package agent

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func markerFailureTrajectoryMessages(source string) []toolChatMsg {
	messages := []toolChatMsg{{Role: "system", Content: "system"}, {Role: "user", Content: "write the marker regression"}}
	for index, result := range []string{
		"fixture declaration",
		"adjacent helper",
		source,
		"denied mutation-only read one",
		"denied mutation-only read two",
		"forced mutation prompt acknowledgement",
	} {
		id := fmt.Sprintf("trajectory-%d", index)
		messages = append(messages,
			toolChatMsg{Role: "assistant", ToolCalls: []toolCall{{
				ID: id, Type: "function",
				Function: toolCallFunction{Name: "read", Arguments: fmt.Sprintf("{\"path\":\"file-%d.cpp\"}", index)},
			}}},
			toolChatMsg{Role: "tool", Name: "read", ToolCallID: id, Content: result},
		)
	}
	return messages
}

func historyContainsFullResult(messages []toolChatMsg, result string) bool {
	for _, message := range messages {
		if message.Role == "tool" && message.Content == result {
			return true
		}
	}
	return false
}

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

func TestMarkerFailureTrajectoryRetainsWritableSourceForForcedMutation(t *testing.T) {
	source := "TARGET_SENTINEL\n" + strings.Repeat("existing marker test source\n", 80) + "END_SENTINEL\n"
	messages := markerFailureTrajectoryMessages(source)

	aggressive, aggressiveFolded := compactToolResultHistoryForConfig(
		append([]toolChatMsg(nil), messages...),
		DirectToolAgentConfig{ToolHistoryMode: ToolHistoryAggressive},
		20,
	)
	retained, retainedFolded := compactToolResultHistoryForConfig(
		append([]toolChatMsg(nil), messages...),
		DirectToolAgentConfig{
			ToolHistoryMode:                ToolHistoryRetainRecent,
			ToolHistoryKeepRecentExchanges: 4,
			ToolHistoryRetentionPct:        70,
		},
		20,
	)

	require.False(t, historyContainsFullResult(aggressive, source),
		"the historical live policy lost the writable source before recovery")
	require.True(t, historyContainsFullResult(retained, source),
		"the mutating-worker policy must retain the writable source through recovery")
	require.Greater(t, aggressiveFolded, retainedFolded)
}

func TestMarkerFailureTrajectoryReachesEditWithRetainedSource(t *testing.T) {
	requestCount := 0
	source := "TARGET_SENTINEL\n" + strings.Repeat("existing marker test source\n", 80) + "END_SENTINEL\n"
	forcedTurnSawSource := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		var req toolChatRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))

		resp := toolChatResponse{}
		switch requestCount {
		case 1:
			resp.Choices = []toolChatChoice{{Message: toolChatMsg{Role: "assistant", ToolCalls: []toolCall{{
				ID: "read-target-first", Type: "function",
				Function: toolCallFunction{Name: "read", Arguments: "{\"path\":\"tests/integration/test_jump_motions.cpp\"}"},
			}}}, FinishReason: "tool_calls"}}
		case 2:
			resp.Choices = []toolChatChoice{{Message: toolChatMsg{Role: "assistant", ToolCalls: []toolCall{{
				ID: "read-fixture", Type: "function",
				Function: toolCallFunction{Name: "read", Arguments: "{\"path\":\"tests/integration/helpers/VimTestFixture.h\"}"},
			}}}, FinishReason: "tool_calls"}}
		case 3:
			resp.Choices = []toolChatChoice{{Message: toolChatMsg{Role: "assistant", ToolCalls: []toolCall{{
				ID: "read-adjacent", Type: "function",
				Function: toolCallFunction{Name: "read", Arguments: "{\"path\":\"tests/integration/adjacent.txt\"}"},
			}}}, FinishReason: "tool_calls"}}
		case 4:
			resp.Choices = []toolChatChoice{{Message: toolChatMsg{Role: "assistant", ToolCalls: []toolCall{{
				ID: "read-target-final", Type: "function",
				Function: toolCallFunction{Name: "read", Arguments: "{\"path\":\"tests/integration/test_jump_motions.cpp\"}"},
			}}}, FinishReason: "tool_calls"}}
		case 5, 6:
			resp.Choices = []toolChatChoice{{Message: toolChatMsg{Role: "assistant", ToolCalls: []toolCall{{
				ID: fmt.Sprintf("hallucinated-read-%d", requestCount), Type: "function",
				Function: toolCallFunction{Name: "read", Arguments: "{\"path\":\"tests/integration/helpers/VimTestFixture.h\"}"},
			}}}, FinishReason: "tool_calls"}}
		case 7:
			for _, message := range req.Messages {
				if message.Role == "tool" &&
					strings.Contains(message.Content, "1|TARGET_SENTINEL") &&
					strings.Contains(message.Content, "82|END_SENTINEL") {
					forcedTurnSawSource = true
				}
			}
			require.True(t, forcedTurnSawSource, "forced mutation request lost the exact writable source")
			require.Len(t, req.Tools, 1)
			require.Equal(t, "edit", req.Tools[0].Function.Name)
			require.Contains(t, fmt.Sprint(req.Messages), mutationOnlyBlockedRecoveryPrompt)
			resp.Choices = []toolChatChoice{{Message: toolChatMsg{Role: "assistant", ToolCalls: []toolCall{{
				ID: "edit-target", Type: "function",
				Function: toolCallFunction{Name: "edit", Arguments: "{\"path\":\"tests/integration/test_jump_motions.cpp\",\"old\":\"TARGET_SENTINEL\\n\",\"new\":\"TARGET_SENTINEL_EDITED\\n\"}"},
			}}}, FinishReason: "tool_calls"}}
		default:
			resp.Choices = []toolChatChoice{{Message: toolChatMsg{Role: "assistant", Content: "done"}, FinishReason: "stop"}}
		}
		resp.Usage.PromptTokens = 2_000
		resp.Usage.CompletionTokens = 50
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	}))
	defer server.Close()

	workDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workDir, "tests/integration/helpers"), 0o755))
	target := filepath.Join(workDir, "tests/integration/test_jump_motions.cpp")
	require.NoError(t, os.WriteFile(target, []byte(source), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "tests/integration/helpers/VimTestFixture.h"), []byte("fixture"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "tests/integration/adjacent.txt"), []byte("adjacent"), 0o644))

	cfg := DefaultDirectToolAgentConfig().ForWorkload("coder", "test")
	cfg.Endpoint = server.URL
	cfg.WorkDir = workDir
	cfg.ScopedFiles = []string{"tests/integration/test_jump_motions.cpp"}
	cfg.ProtectExistingFiles = true
	cfg.MaxReadsBeforeMutation = 3
	cfg.MaxCumulativeInputTokens = 100_000
	cfg.MaxInputTokensBeforeMutation = 90_000
	cfg.MaxToolCalls = 20

	result, err := RunDirectToolAgent(cfg, "system", "write marker coverage",
		ToolsForRoleScope("coder", cfg.ScopedFiles), "")
	require.NoError(t, err)
	require.True(t, result.MutationObserved)
	require.Equal(t, 8, requestCount)
	require.True(t, forcedTurnSawSource)
	content, readErr := os.ReadFile(target)
	require.NoError(t, readErr)
	require.Contains(t, string(content), "TARGET_SENTINEL_EDITED")
}

// TestBenchmarkMarkerAnchorMismatchRecovery captures the failed Ryan marker
// trajectory at the harness boundary: reconnaissance is already exhausted
// when the first authorized edit uses a stale namespace anchor. The edit
// recovery contract must outrank the generic reconnaissance stop, retain the
// exact writable source, and admit the corrected one-file checkpoint.
func TestBenchmarkMarkerAnchorMismatchRecovery(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		var message toolChatMsg
		finishReason := "tool_calls"
		switch requestCount {
		case 1, 2, 3, 4:
			message = toolChatMsg{Role: "assistant", ToolCalls: []toolCall{{
				ID: fmt.Sprintf("marker-read-%d", requestCount), Type: "function",
				Function: toolCallFunction{Name: "read", Arguments: `{"path":"tests/integration/test_jump_motions.cpp"}`},
			}}}
		case 5:
			message = toolChatMsg{Role: "assistant", ToolCalls: []toolCall{{
				ID: "marker-stale-edit", Type: "function",
				Function: toolCallFunction{Name: "edit", Arguments: `{"path":"tests/integration/test_jump_motions.cpp","old":"} // namespace dc::vim::test","new":"marker coverage"}`},
			}}}
		case 6:
			message = toolChatMsg{Role: "assistant", ToolCalls: []toolCall{{
				ID: "marker-anchor-recovery-read", Type: "function",
				Function: toolCallFunction{Name: "read", Arguments: `{"path":"tests/integration/test_jump_motions.cpp"}`},
			}}}
		case 7:
			message = toolChatMsg{Role: "assistant", ToolCalls: []toolCall{{
				ID: "marker-corrected-edit", Type: "function",
				Function: toolCallFunction{Name: "edit", Arguments: `{"path":"tests/integration/test_jump_motions.cpp","old":"existing jump tests","new":"existing jump tests\nmarker coverage"}`},
			}}}
		default:
			message = toolChatMsg{Role: "assistant", Content: "done"}
			finishReason = "stop"
		}
		resp := toolChatResponse{Choices: []toolChatChoice{{Message: message, FinishReason: finishReason}}}
		resp.Usage.PromptTokens = 2_000
		resp.Usage.CompletionTokens = 50
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	}))
	defer server.Close()

	dir := t.TempDir()
	target := filepath.Join(dir, "tests/integration/test_jump_motions.cpp")
	require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o755))
	require.NoError(t, os.WriteFile(target, []byte("existing jump tests"), 0o644))
	cfg := DefaultDirectToolAgentConfig().ForWorkload("coder", "test")
	cfg.Endpoint = server.URL
	cfg.WorkDir = dir
	cfg.ScopedFiles = []string{"tests/integration/test_jump_motions.cpp"}
	cfg.RequiredMutationFiles = append([]string(nil), cfg.ScopedFiles...)
	cfg.ProtectExistingFiles = true
	cfg.MaxReadsBeforeMutation = 1
	cfg.MaxInputTokensBeforeMutation = 100_000
	cfg.MaxCumulativeInputTokens = 100_000
	cfg.MaxToolCalls = 20

	result, err := RunDirectToolAgent(cfg, "system", "write marker command coverage",
		ToolsForRoleScope("coder", cfg.ScopedFiles), "")
	require.NoError(t, err)
	require.True(t, result.MutationObserved)
	require.Equal(t, 8, requestCount)
	content, readErr := os.ReadFile(target)
	require.NoError(t, readErr)
	require.Equal(t, "existing jump tests\nmarker coverage", string(content))
}

// TestBenchmarkMarkerPartialCheckpointRequiresContinuation captures the real
// marker-command failure trajectory: one owned file changed, a second owned
// file's surgical edit missed, the recovery read succeeded, and the cumulative
// budget ended the turn. The checkpoint is useful, but must never be reported
// as a completed worker result.
func TestBenchmarkMarkerPartialCheckpointRequiresContinuation(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		var calls []toolCall
		switch requestCount {
		case 1:
			calls = []toolCall{{ID: "header", Type: "function", Function: toolCallFunction{
				Name: "edit", Arguments: `{"path":"EditorAdapter.h","old":"lane handlers","new":"marker declaration\nlane handlers"}`}}}
		case 2:
			calls = []toolCall{{ID: "handler-miss", Type: "function", Function: toolCallFunction{
				Name: "edit", Arguments: `{"path":"EditorAdapterActionHandlers.inc","old":"imagined end comment","new":"marker handler"}`}}}
		default:
			calls = []toolCall{{ID: "handler-recovery-read", Type: "function", Function: toolCallFunction{
				Name: "read", Arguments: `{"path":"EditorAdapterActionHandlers.inc"}`}}}
		}
		resp := toolChatResponse{Choices: []toolChatChoice{{Message: toolChatMsg{Role: "assistant", ToolCalls: calls}, FinishReason: "tool_calls"}}}
		resp.Usage.PromptTokens = 30_000
		resp.Usage.CompletionTokens = 300
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	}))
	defer server.Close()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "EditorAdapter.h"), []byte("lane handlers"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "EditorAdapterActionHandlers.inc"), []byte("actual handler tail"), 0o644))
	cfg := DefaultDirectToolAgentConfig().ForWorkload("coder", "implementation")
	cfg.Endpoint = server.URL
	cfg.WorkDir = dir
	cfg.ScopedFiles = []string{"EditorAdapter.h", "EditorAdapterActionHandlers.inc"}
	cfg.RequiredMutationFiles = append([]string(nil), cfg.ScopedFiles...)
	cfg.ProtectExistingFiles = true
	cfg.MaxCumulativeInputTokens = 90_000
	cfg.MaxToolCalls = 12

	result, err := RunDirectToolAgent(cfg, "system", "implement marker declaration and handler",
		ToolsForRoleScope("coder", cfg.ScopedFiles), "")
	require.ErrorContains(t, err, "unresolved failed mutation repairs")
	require.Equal(t, DirectToolStopReasonTokenBudget, result.StopReason)
	require.Equal(t, []string{"EditorAdapterActionHandlers.inc"}, result.PendingMutationRepairs)
	require.Equal(t, []string{"EditorAdapterActionHandlers.inc"}, result.MissingRequiredMutations)
	require.True(t, result.MutationObserved)
	require.Equal(t, 3, requestCount)
}

func BenchmarkCompactMarkerFailureTrajectory(b *testing.B) {
	source := "TARGET_SENTINEL\n" + strings.Repeat("existing marker test source\n", 80)
	messages := markerFailureTrajectoryMessages(source)
	configs := map[string]DirectToolAgentConfig{
		"aggressive":    {ToolHistoryMode: ToolHistoryAggressive},
		"retain_recent": {ToolHistoryMode: ToolHistoryRetainRecent, ToolHistoryKeepRecentExchanges: 4, ToolHistoryRetentionPct: 70},
	}
	for name, cfg := range configs {
		b.Run(name, func(b *testing.B) {
			for iteration := 0; iteration < b.N; iteration++ {
				compactToolResultHistoryForConfig(append([]toolChatMsg(nil), messages...), cfg, 20)
			}
		})
	}
}
