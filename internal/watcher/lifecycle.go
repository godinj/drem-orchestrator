package watcher

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

const defaultTimeout = 10 * time.Minute

// LifecycleManager launches agent turns as claude subprocesses, waits for
// completion, parses JSON output for token counts, and records metrics.
//
// Configure ClaudeBin, WorkDir, and Timeout before calling RunTurn.
// ClaudeBin defaults to "claude"; Timeout defaults to 10 minutes.
type LifecycleManager struct {
	// ClaudeBin is the path to the claude CLI binary. Defaults to "claude".
	ClaudeBin string
	// WorkDir is the working directory for the subprocess. Empty uses the
	// current process working directory.
	WorkDir string
	// Timeout is the maximum duration a turn may run. Defaults to 10 minutes.
	Timeout time.Duration
	// MetricsStore is used to persist TurnResult after each turn.
	MetricsStore *MetricsStore
}

// NewLifecycleManager creates a LifecycleManager with default settings and
// the provided MetricsStore.
func NewLifecycleManager(store *MetricsStore) *LifecycleManager {
	return &LifecycleManager{
		ClaudeBin:    "claude",
		Timeout:      defaultTimeout,
		MetricsStore: store,
	}
}

// RunTurn launches a claude subprocess for the given agent using systemPrompt,
// waits for it to exit, parses the --output-format json response for token
// counts, records a TurnMetric, and returns a TurnResult.
//
// Returns ErrKyleException immediately — without launching any subprocess —
// if agent is "kyle".
func (m *LifecycleManager) RunTurn(agent string, systemPrompt string) (*TurnResult, error) {
	if agent == "kyle" {
		return nil, ErrKyleException
	}

	timeout := m.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}

	claudeBin := m.ClaudeBin
	if claudeBin == "" {
		claudeBin = "claude"
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, claudeBin,
		"-p", "Process your turn",
		"--system-prompt", systemPrompt,
		"--output-format", "json",
	)
	if m.WorkDir != "" {
		cmd.Dir = m.WorkDir
	}
	// Put the subprocess in its own process group so we can kill the entire
	// group (including grandchild processes like `sleep`) on timeout.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	startedAt := time.Now()
	runErr := cmd.Run()
	endedAt := time.Now()

	result := &TurnResult{
		Agent:     agent,
		StartedAt: startedAt,
		EndedAt:   endedAt,
		Duration:  endedAt.Sub(startedAt),
	}

	if runErr != nil {
		if ctx.Err() == context.DeadlineExceeded {
			result.ExitStatus = -1
			result.ErrorDetails = "turn killed: timeout exceeded"
		} else if exitErr, ok := runErr.(*exec.ExitError); ok {
			result.ExitStatus = exitErr.ExitCode()
			result.ErrorDetails = strings.TrimSpace(stderr.String())
		} else {
			result.ExitStatus = -1
			result.ErrorDetails = runErr.Error()
		}
	}

	// Parse JSON output for token counts; silently degrade on parse failure.
	var resp claudeResponse
	if jsonErr := json.Unmarshal(stdout.Bytes(), &resp); jsonErr == nil {
		result.InputTokens = resp.Usage.InputTokens
		result.OutputTokens = resp.Usage.OutputTokens
	}

	_ = m.MetricsStore.RecordTurn(result)

	return result, nil
}
