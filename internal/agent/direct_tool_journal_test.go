package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
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

func TestRunDirectToolAgentResumesNaturalStopCorrectiveTurnAsMutationOnly(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "existing.cpp"), []byte("original"), 0o644))
	journalPath := filepath.Join(t.TempDir(), "journal.json")
	var calls atomic.Int32
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			_ = json.NewEncoder(w).Encode(toolChatResponse{
				Choices: []toolChatChoice{{Message: toolChatMsg{Role: "assistant", Content: "compacted before mutation"}, FinishReason: "stop"}},
				Usage:   usageForTest(60, 5),
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
	cfg.MaxInputTokensBeforeMutation = 50
	cfg.MaxCumulativeInputTokens = 200
	cfg.ProtectExistingFiles = true
	cfg.ScopedFiles = []string{"existing.cpp"}
	result, err := RunDirectToolAgent(cfg, "system", "task", ToolsForRole("coder"), "")
	require.Error(t, err)
	require.Equal(t, DirectToolStopReasonTimeout, result.StopReason)
	first.Close()

	resumedCalls := 0
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resumedCalls++
		var req toolChatRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		resp := toolChatResponse{}
		if resumedCalls == 1 {
			names := make([]string, 0, len(req.Tools))
			for _, tool := range req.Tools {
				names = append(names, tool.Function.Name)
			}
			require.Equal(t, []string{"edit"}, names)
			conversation := ""
			for _, message := range req.Messages {
				conversation += message.Content
			}
			require.Contains(t, conversation, "single reserved corrective turn")
			resp.Choices = []toolChatChoice{{Message: toolChatMsg{Role: "assistant", ToolCalls: []toolCall{{
				ID: "resumed-edit", Type: "function",
				Function: toolCallFunction{Name: "edit", Arguments: `{"path":"existing.cpp","old":"original","new":"changed"}`},
			}}}, FinishReason: "tool_calls"}}
		} else {
			resp.Choices = []toolChatChoice{{Message: toolChatMsg{Role: "assistant", Content: "done"}, FinishReason: "stop"}}
		}
		resp.Usage = usageForTest(60, 5)
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	}))
	defer second.Close()
	cfg.Endpoint = second.URL
	cfg.Timeout = time.Second
	result, err = RunDirectToolAgent(cfg, "system", "task", ToolsForRole("coder"), "")
	require.NoError(t, err)
	require.Equal(t, 1, result.ResumedTurns)
	require.True(t, result.MutationObserved)
	require.Equal(t, 2, resumedCalls)
	content, readErr := os.ReadFile(filepath.Join(dir, "existing.cpp"))
	require.NoError(t, readErr)
	require.Equal(t, "changed", string(content))
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
