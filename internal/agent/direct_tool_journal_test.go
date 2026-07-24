package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDirectToolJournalRejectsDifferentPromptAndCompletedRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.json")
	journal := directToolJournal{
		PromptHash:    "one",
		Messages:      []toolChatMsg{{Role: "system", Content: "s"}, {Role: "user", Content: "u"}},
		NextIteration: 2,
	}
	require.NoError(t, saveDirectToolJournal(path, journal))
	loaded, err := loadDirectToolJournal(path, "two")
	require.NoError(t, err)
	require.Nil(t, loaded)

	journal.Completed = true
	require.NoError(t, saveDirectToolJournal(path, journal))
	loaded, err = loadDirectToolJournal(path, "one")
	require.NoError(t, err)
	require.Nil(t, loaded)
}

func TestRunDirectToolAgentResumesAfterTimeoutWithoutRepeatingMutationTurn(t *testing.T) {
	dir := t.TempDir()
	journalPath := filepath.Join(t.TempDir(), "journal.json")
	var calls atomic.Int32
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			_ = json.NewEncoder(w).Encode(toolChatResponse{
				Choices: []toolChatChoice{{Message: toolChatMsg{Role: "assistant", ToolCalls: []toolCall{{
					ID: "write-1", Type: "function", Function: toolCallFunction{Name: "write", Arguments: `{"path":"checkpoint.txt","content":"preserved"}`},
				}}}, FinishReason: "tool_calls"}},
				Usage: usageForTest(100, 10),
			})
			return
		}
		time.Sleep(100 * time.Millisecond)
	}))
	cfg := DefaultDirectToolAgentConfig()
	cfg.Endpoint = first.URL
	cfg.WorkDir = dir
	cfg.Timeout = 20 * time.Millisecond
	cfg.JournalPath = journalPath
	result, err := RunDirectToolAgent(cfg, "system", "task", ToolsForRole("coder"), "")
	require.Error(t, err)
	require.Equal(t, DirectToolStopReasonTimeout, result.StopReason)
	first.Close()

	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req toolChatRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		require.GreaterOrEqual(t, len(req.Messages), 4)
		require.Equal(t, "tool", req.Messages[len(req.Messages)-1].Role)
		_ = json.NewEncoder(w).Encode(toolChatResponse{
			Choices: []toolChatChoice{{Message: toolChatMsg{Role: "assistant", Content: "done"}, FinishReason: "stop"}},
			Usage:   usageForTest(120, 5),
		})
	}))
	defer second.Close()
	cfg.Endpoint = second.URL
	cfg.Timeout = time.Second
	result, err = RunDirectToolAgent(cfg, "system", "task", ToolsForRole("coder"), "")
	require.NoError(t, err)
	require.Equal(t, 1, result.ResumedTurns)
	require.True(t, result.MutationObserved)
	require.Equal(t, 220, result.TokensIn)
}

func TestRunDirectToolAgentRequiredResumeFailsClosedWithoutMatchingJournal(t *testing.T) {
	cfg := DefaultDirectToolAgentConfig()
	cfg.WorkDir = t.TempDir()
	cfg.JournalPath = filepath.Join(t.TempDir(), "journal.json")
	cfg.RequireJournalResume = true

	result, err := RunDirectToolAgent(cfg, "system", "task", ToolsForRole("coder"), "")
	require.Error(t, err)
	require.Nil(t, result)
	require.Contains(t, err.Error(), "required durable journal resume")
}

func usageForTest(in, out int) struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
} {
	return struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	}{PromptTokens: in, CompletionTokens: out, TotalTokens: in + out}
}
