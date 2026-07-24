package agent

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	defaultMaxToolCalls                 = 12
	defaultMaxInputTokensBeforeMutation = 20_000
	toolHistoryKeepRecent               = 2
	toolHistoryCompactedPrefix          = 240
	ToolArgumentsString                 = "string"
	ToolArgumentsObject                 = "object"
)

// DirectToolAgentConfig holds connection, generation, and execution parameters
// for the direct tool-calling agent.
type DirectToolAgentConfig struct {
	Endpoint      string
	Model         string
	MaxTokens     int
	Temperature   float64
	Timeout       time.Duration
	MaxIterations int

	// MaxCumulativeInputTokens caps total prompt tokens billed across the
	// replayed tool loop. ContextLimit instead bounds one request's context.
	MaxCumulativeInputTokens               int
	TestMaxCumulativeInputTokens           int
	ImplementationMaxCumulativeInputTokens int
	IntegrationMaxCumulativeInputTokens    int
	ReviewMaxCumulativeInputTokens         int

	// MaxReadsBeforeMutation is retained as the configuration name for
	// compatibility, but bounds all reconnaissance: structured reads/searches
	// plus discovery-like bash. Scoped workers reject all bash before mutation.
	MaxReadsBeforeMutation               int
	TestMaxReadsBeforeMutation           int
	ImplementationMaxReadsBeforeMutation int
	IntegrationMaxReadsBeforeMutation    int

	// MaxToolCalls is a hard run-wide tool-call ceiling. The pre-mutation input
	// limits stop workers that spend inference without producing a checkpoint.
	MaxToolCalls                               int
	MaxInputTokensBeforeMutation               int
	TestMaxInputTokensBeforeMutation           int
	ImplementationMaxInputTokensBeforeMutation int
	IntegrationMaxInputTokensBeforeMutation    int

	WorkDir     string
	BashTimeout time.Duration
	TraceWriter io.Writer
	GQCaller    string
	GQPriority  string

	ChatTemplateKwargs  map[string]any
	ToolArgumentsFormat string
	ContextLimit        int
	ContextWarnPct      int
	ContextStopPct      int
	OnIteration         func(iteration, tokensIn, tokensOut, contextPct int)
	OnToolCall          func(toolName string, args map[string]any, resultLen int)
}

func DefaultDirectToolAgentConfig() DirectToolAgentConfig {
	return DirectToolAgentConfig{
		Endpoint:                 "http://localhost:8081/v1/chat/completions",
		Model:                    "qwen3-coder-30b",
		MaxTokens:                8192,
		Temperature:              0.1,
		Timeout:                  120 * time.Second,
		MaxIterations:            20,
		MaxCumulativeInputTokens: 60_000,
		BashTimeout:              30 * time.Second,
		ToolArgumentsFormat:      ToolArgumentsString,
	}
}

type DirectToolAgentResult struct {
	Output           string
	OutputPath       string
	TokensIn         int
	TokensOut        int
	Iterations       int
	Duration         time.Duration
	FinalContextPct  int
	StopReason       string
	MutationObserved bool
}

const (
	DirectToolStopReasonContextLimit  = "context_limit"
	DirectToolStopReasonMaxIterations = "max_iterations"
	DirectToolStopReasonNoProgress    = "no_progress"
	DirectToolStopReasonMaxTokens     = "max_tokens"
	DirectToolStopReasonTimeout       = "timeout"
	DirectToolStopReasonTokenBudget   = "token_budget"
	DirectToolStopReasonToolBudget    = "tool_budget"
)

// ForWorkload returns a runtime copy with role/phase-specific inference and
// reconnaissance budgets applied. Zero-valued overrides fall back to the
// generic limits, preserving compatibility with existing project configs.
func (cfg DirectToolAgentConfig) ForWorkload(role, phase string) DirectToolAgentConfig {
	if cfg.MaxToolCalls <= 0 {
		cfg.MaxToolCalls = defaultMaxToolCalls
	}
	if cfg.MaxInputTokensBeforeMutation <= 0 {
		cfg.MaxInputTokensBeforeMutation = defaultMaxInputTokensBeforeMutation
	}
	switch role {
	case "reviewer":
		if cfg.ReviewMaxCumulativeInputTokens > 0 {
			cfg.MaxCumulativeInputTokens = cfg.ReviewMaxCumulativeInputTokens
		}
		cfg.MaxReadsBeforeMutation = 0
		cfg.MaxInputTokensBeforeMutation = 0
		return cfg
	case "fixer":
		if phase == "" {
			phase = "implementation"
		}
	}

	switch phase {
	case "test":
		cfg.MaxCumulativeInputTokens = positiveOverride(cfg.TestMaxCumulativeInputTokens, cfg.MaxCumulativeInputTokens)
		cfg.MaxReadsBeforeMutation = positiveOverride(cfg.TestMaxReadsBeforeMutation, cfg.MaxReadsBeforeMutation)
		cfg.MaxInputTokensBeforeMutation = positiveOverride(cfg.TestMaxInputTokensBeforeMutation, cfg.MaxInputTokensBeforeMutation)
	case "implementation":
		cfg.MaxCumulativeInputTokens = positiveOverride(cfg.ImplementationMaxCumulativeInputTokens, cfg.MaxCumulativeInputTokens)
		cfg.MaxReadsBeforeMutation = positiveOverride(cfg.ImplementationMaxReadsBeforeMutation, cfg.MaxReadsBeforeMutation)
		cfg.MaxInputTokensBeforeMutation = positiveOverride(cfg.ImplementationMaxInputTokensBeforeMutation, cfg.MaxInputTokensBeforeMutation)
	case "integration":
		cfg.MaxCumulativeInputTokens = positiveOverride(cfg.IntegrationMaxCumulativeInputTokens, cfg.MaxCumulativeInputTokens)
		cfg.MaxReadsBeforeMutation = positiveOverride(cfg.IntegrationMaxReadsBeforeMutation, cfg.MaxReadsBeforeMutation)
		cfg.MaxInputTokensBeforeMutation = positiveOverride(cfg.IntegrationMaxInputTokensBeforeMutation, cfg.MaxInputTokensBeforeMutation)
	}
	return cfg
}

func positiveOverride(value, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

func isReconnaissanceToolCall(name, argsJSON string) bool {
	switch name {
	case "read", "grep", "glob":
		return true
	case "bash":
		var args struct {
			Cmd string `json:"cmd"`
		}
		if json.Unmarshal([]byte(argsJSON), &args) != nil {
			return true
		}
		return bashLooksLikeDiscovery(args.Cmd)
	default:
		return false
	}
}

func bashLooksLikeDiscovery(command string) bool {
	lower := strings.ToLower(strings.TrimSpace(command))
	for _, marker := range []string{
		"ls", "find", "grep", "rg", "cat", "head", "tail", "pwd", "tree",
		"git status", "git diff", "git show", "git log", "sed -n", "wc -l",
	} {
		if lower == marker || strings.HasPrefix(lower, marker+" ") ||
			strings.Contains(lower, "&& "+marker+" ") || strings.Contains(lower, "; "+marker+" ") ||
			strings.Contains(lower, "| "+marker+" ") {
			return true
		}
	}
	return false
}

func compactToolResultHistory(messages []toolChatMsg) []toolChatMsg {
	remaining := toolHistoryKeepRecent
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != "tool" {
			continue
		}
		if remaining > 0 {
			remaining--
			continue
		}
		if len(messages[i].Content) <= toolHistoryCompactedPrefix {
			continue
		}
		if strings.HasPrefix(messages[i].Content, "[HARNESS: prior ") {
			continue
		}
		original := messages[i].Content
		digest := sha256.Sum256([]byte(original))
		prefix := strings.TrimSpace(original[:toolHistoryCompactedPrefix])
		messages[i].Content = fmt.Sprintf("[HARNESS: prior %s result compacted; bytes=%d sha256=%x]\n%s", messages[i].Name, len(original), digest, prefix)
	}
	return messages
}
