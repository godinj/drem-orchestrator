package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const directToolJournalVersion = 1

// directToolJournal is the durable boundary between paid model turns. It is
// deliberately a complete replay state rather than a prose summary: after a
// timeout or container loss the next worker continues from the last completed
// tool batch without asking the model to rediscover the same source.
type directToolJournal struct {
	Version          int           `json:"version"`
	PromptHash       string        `json:"prompt_hash"`
	Messages         []toolChatMsg `json:"messages"`
	NextIteration    int           `json:"next_iteration"`
	TokensIn         int           `json:"tokens_in"`
	TokensOut        int           `json:"tokens_out"`
	PeakRequestInput int           `json:"peak_request_input"`
	MutationObserved bool          `json:"mutation_observed"`
	TotalToolCalls   int           `json:"total_tool_calls"`
	Completed        bool          `json:"completed"`
	LastTurn         *TraceEvent   `json:"last_turn,omitempty"`
}

func directToolPromptHash(systemPrompt, userMessage string) string {
	sum := sha256.Sum256([]byte(systemPrompt + "\x00" + userMessage))
	return hex.EncodeToString(sum[:])
}

func loadDirectToolJournal(path, promptHash string) (*directToolJournal, error) {
	if path == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) || len(raw) == 0 {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read direct-tool journal: %w", err)
	}
	var journal directToolJournal
	if err := json.Unmarshal(raw, &journal); err != nil {
		return nil, nil // an empty pre-created mount is a fresh journal
	}
	if journal.Version != directToolJournalVersion || journal.PromptHash != promptHash || journal.Completed || len(journal.Messages) < 2 {
		return nil, nil
	}
	return &journal, nil
}

func saveDirectToolJournal(path string, journal directToolJournal) error {
	if path == "" {
		return nil
	}
	journal.Version = directToolJournalVersion
	raw, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		return fmt.Errorf("encode direct-tool journal: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create direct-tool journal directory: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("write direct-tool journal: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("publish direct-tool journal: %w", err)
	}
	return nil
}
