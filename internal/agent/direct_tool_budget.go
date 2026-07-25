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
	minimumMutationTurnHeadroom         = 20_000
	toolHistoryKeepRecent               = 1
	toolHistoryCompactedPrefix          = 240
	ToolArgumentsString                 = "string"
	ToolArgumentsObject                 = "object"
)

const (
	ToolHistoryAggressive   = "aggressive"
	ToolHistoryRetainRecent = "retain_recent"
)

// DirectToolAgentConfig holds connection, generation, and execution parameters
// for the direct tool-calling agent.
type DirectToolAgentConfig struct {
	Endpoint      string
	Model         string
	MaxTokens     int
	Temperature   float64
	TopP          float64
	TopK          int
	Seed          *int64
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
	// plus discovery-like bash. Read-only shell discovery is allowed within the
	// same budget because small models commonly use rg/ls despite the structured
	// tool guidance.
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
	// JournalPath persists the exact completed-turn replay state on a
	// host-backed mount. A replacement container resumes from it only when the
	// immutable prompt fingerprint matches.
	JournalPath          string
	RequireJournalResume bool
	GQCaller             string
	GQPriority           string

	ChatTemplateKwargs  map[string]any
	ToolArgumentsFormat string
	ContextLimit        int
	ContextWarnPct      int
	ContextStopPct      int
	// AllowReadOnlyCompletion is reserved for integration workers whose
	// assembled inputs may already be correct. They may inspect and run gates
	// without manufacturing a no-op source mutation.
	AllowReadOnlyCompletion bool
	// ProtectExistingFiles rejects whole-file write calls for paths that already
	// exist. Test-writing workers use this to preserve established coverage and
	// must make surgical edits; genuinely new test files may still be written.
	ProtectExistingFiles bool
	// ScopedFiles lets corrective mutation turns omit write when every
	// authorized path already exists.
	ScopedFiles []string
	// ReadableFiles is an optional exact read allowlist. When non-empty, the
	// structured read/search tools reject every repository path not named here.
	// DisableBash closes the shell escape hatch for benchmark runs so the read
	// contract is enforced by the tool layer rather than prompt text.
	ReadableFiles []string
	DisableBash   bool
	// ToolHistoryMode selects the historical-tool retention policy. The legacy
	// zero value is "aggressive". "retain_recent" keeps the configured number
	// of complete recent exchanges until live context reaches the threshold.
	ToolHistoryMode                string
	ToolHistoryKeepRecentExchanges int
	ToolHistoryRetentionPct        int
	OnIteration                    func(iteration, tokensIn, tokensOut, contextPct int)
	OnToolCall                     func(toolName string, args map[string]any, resultLen int)
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
	Output                 string
	OutputPath             string
	TokensIn               int
	TokensOut              int
	Iterations             int
	Duration               time.Duration
	FinalContextPct        int
	StopReason             string
	MutationObserved       bool
	PeakRequestInput       int
	ResumedTurns           int
	FoldedBytes            int
	UniqueReads            int
	RedundantReads         int
	DeniedCalls            int
	FirstMutationIteration int
	FirstMutationMs        int64
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
		cfg.AllowReadOnlyCompletion = true
	}
	return reserveMutationTurnHeadroom(cfg)
}

// reserveMutationTurnHeadroom prevents a pre-mutation reconnaissance response
// from consuming the entire cumulative replay budget. The next response still
// needs enough room to receive the harness refusal and emit an edit/write call.
// This is a runtime invariant so a stale or hand-edited project config cannot
// recreate the failure even if its phase overrides are internally inconsistent.
func reserveMutationTurnHeadroom(cfg DirectToolAgentConfig) DirectToolAgentConfig {
	if cfg.MaxCumulativeInputTokens <= minimumMutationTurnHeadroom || cfg.MaxInputTokensBeforeMutation <= 0 {
		return cfg
	}
	maxBeforeMutation := cfg.MaxCumulativeInputTokens - minimumMutationTurnHeadroom
	if cfg.MaxInputTokensBeforeMutation > maxBeforeMutation {
		cfg.MaxInputTokensBeforeMutation = maxBeforeMutation
	}
	return cfg
}

func positiveOverride(value, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

// DeriveWorkloadBudget raises static role defaults to the minimum viable
// budget implied by the actual prompt and scope. MaxCumulativeInputTokens is
// cumulative replay cost, not the model context window: a six-turn worker can
// legitimately consume far more than one 65k request. Operator values remain
// floors, while the formula guarantees one mutation turn of reserve.
func DeriveWorkloadBudget(cfg DirectToolAgentConfig, role, phase string, promptBytes, scopeFiles int) DirectToolAgentConfig {
	if promptBytes < 0 {
		promptBytes = 0
	}
	if scopeFiles < 0 {
		scopeFiles = 0
	}
	expectedTurns := 8
	switch phase {
	case "implementation":
		expectedTurns = 10
	case "integration", "test":
		expectedTurns = 8
	}
	if role == "reviewer" {
		expectedTurns = 2
	}
	return DeriveExpectedTurnsBudget(cfg, role, promptBytes, scopeFiles, expectedTurns)
}

// DeriveExpectedTurnsBudget applies the same prompt/scope formula using an
// execution-manifest turn count. Atomic workers own several ordered plan
// stages, so treating them as one ordinary implementation phase starves the
// very lane that was selected to avoid cross-worker inference and handoffs.
func DeriveExpectedTurnsBudget(cfg DirectToolAgentConfig, role string, promptBytes, scopeFiles, expectedTurns int) DirectToolAgentConfig {
	if promptBytes < 0 {
		promptBytes = 0
	}
	if scopeFiles < 0 {
		scopeFiles = 0
	}
	if expectedTurns <= 0 {
		expectedTurns = 1
	}
	promptTokens := (promptBytes + 2) / 3
	derived := promptTokens*2 + expectedTurns*6_000 + scopeFiles*1_500
	if role != "reviewer" {
		derived += minimumMutationTurnHeadroom
	}
	if derived > cfg.MaxCumulativeInputTokens {
		cfg.MaxCumulativeInputTokens = derived
	}
	if expectedTurns+2 > cfg.MaxIterations {
		cfg.MaxIterations = expectedTurns + 2
	}
	if role != "reviewer" {
		minimumPreMutation := promptTokens + 12_000
		if minimumPreMutation > cfg.MaxInputTokensBeforeMutation {
			cfg.MaxInputTokensBeforeMutation = minimumPreMutation
		}
		if expectedTurns*2 > cfg.MaxToolCalls {
			cfg.MaxToolCalls = expectedTurns * 2
		}
	}
	return reserveMutationTurnHeadroom(cfg)
}

// ExtendBudgetForJournalResume turns run-wide ceilings into continuation
// ceilings. Journal counters remain cumulative for telemetry, so a resumed
// process must add a fresh, formula-derived segment to the already-consumed
// totals or it will terminate immediately after loading the checkpoint.
func ExtendBudgetForJournalResume(cfg DirectToolAgentConfig, consumedInput, completedIterations, consumedToolCalls int) DirectToolAgentConfig {
	if consumedInput > 0 {
		cfg.MaxCumulativeInputTokens += consumedInput
	}
	if completedIterations > 0 {
		cfg.MaxIterations += completedIterations
	}
	if consumedToolCalls > 0 {
		cfg.MaxToolCalls += consumedToolCalls
	}
	return cfg
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
	compacted, _ := compactToolResultHistoryWithStats(messages)
	return compacted
}

func compactToolResultHistoryWithStats(messages []toolChatMsg) ([]toolChatMsg, int) {
	return compactToolResultHistoryPolicy(messages, toolHistoryKeepRecent)
}

func compactToolResultHistoryForConfig(messages []toolChatMsg, cfg DirectToolAgentConfig, liveContextPct int) ([]toolChatMsg, int) {
	if cfg.ToolHistoryMode != ToolHistoryRetainRecent {
		return compactToolResultHistoryPolicy(messages, toolHistoryKeepRecent)
	}
	threshold := cfg.ToolHistoryRetentionPct
	if threshold <= 0 {
		threshold = 70
	}
	keep := cfg.ToolHistoryKeepRecentExchanges
	if keep < 3 {
		keep = 3
	}
	if liveContextPct >= threshold {
		keep = toolHistoryKeepRecent
	}
	return compactToolResultHistoryPolicy(messages, keep)
}

func compactToolResultHistoryPolicy(messages []toolChatMsg, keepRecent int) ([]toolChatMsg, int) {
	messages, exchangeFolded := compactHistoricalToolExchangesKeeping(messages, keepRecent)
	remainingResults := keepRecent
	foldedBytes := exchangeFolded
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != "tool" {
			continue
		}
		if remainingResults > 0 {
			remainingResults--
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
		foldedBytes += len(original) - len(messages[i].Content)
	}
	return messages, foldedBytes
}

// compactHistoricalToolExchanges converts old assistant tool calls and their
// paired tool results into ordinary system summaries. Keeping compacted calls
// structurally executable taught Qwen to copy `_drem_compacted` arguments as
// its next mutation. Only the latest valid exchange remains as tool protocol;
// summaries preserve path, outcome prefix, byte count, and digest without
// becoming callable examples.
func compactHistoricalToolExchanges(messages []toolChatMsg) ([]toolChatMsg, int) {
	return compactHistoricalToolExchangesKeeping(messages, 1)
}

func compactHistoricalToolExchangesKeeping(messages []toolChatMsg, keepRecent int) ([]toolChatMsg, int) {
	if keepRecent < 1 {
		keepRecent = 1
	}
	keepAssistant := make(map[int]struct{}, keepRecent)
	remaining := keepRecent
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != "assistant" || len(messages[i].ToolCalls) == 0 {
			continue
		}
		valid := true
		for _, call := range messages[i].ToolCalls {
			if strings.Contains(call.Function.Arguments, `"_drem_compacted"`) {
				valid = false
				break
			}
		}
		if valid {
			keepAssistant[i] = struct{}{}
			remaining--
			if remaining == 0 {
				break
			}
		}
	}

	resultByID := make(map[string]toolChatMsg)
	resultIndexByID := make(map[string]int)
	for i, message := range messages {
		if message.Role == "tool" && message.ToolCallID != "" {
			resultByID[message.ToolCallID] = message
			resultIndexByID[message.ToolCallID] = i
		}
	}
	skip := make(map[int]struct{})
	foldedBytes := 0
	compacted := make([]toolChatMsg, 0, len(messages))
	seenHarness := make(map[string]struct{})
	for i, message := range messages {
		if _, omitted := skip[i]; omitted {
			continue
		}
		if message.Role == "system" && strings.HasPrefix(message.Content, "[HARNESS]") {
			if _, duplicate := seenHarness[message.Content]; duplicate {
				foldedBytes += len(message.Content)
				continue
			}
			seenHarness[message.Content] = struct{}{}
		}
		_, retained := keepAssistant[i]
		if message.Role != "assistant" || len(message.ToolCalls) == 0 || retained {
			compacted = append(compacted, message)
			continue
		}

		var summary strings.Builder
		summary.WriteString("[HARNESS: prior tool exchange compacted]")
		if content := strings.TrimSpace(message.Content); content != "" {
			summary.WriteString(" assistant=")
			summary.WriteString(truncateForStub(content, 160))
		}
		for _, call := range message.ToolCalls {
			arguments := call.Function.Arguments
			argumentDigest := sha256.Sum256([]byte(arguments))
			fmt.Fprintf(&summary, "\n- %s path=%q args_bytes=%d args_sha256=%x",
				call.Function.Name, toolCallPath(arguments), len(arguments), argumentDigest)
			foldedBytes += len(arguments)
			if toolResult, ok := resultByID[call.ID]; ok {
				resultDigest := sha256.Sum256([]byte(toolResult.Content))
				fmt.Fprintf(&summary, " result_bytes=%d result_sha256=%x result=%q",
					len(toolResult.Content), resultDigest, truncateForStub(toolResult.Content, 180))
				if resultIndex, exists := resultIndexByID[call.ID]; exists {
					skip[resultIndex] = struct{}{}
					foldedBytes += len(toolResult.Content)
				}
			}
		}
		compacted = append(compacted, toolChatMsg{Role: "system", Content: summary.String()})
	}
	return compacted, foldedBytes
}
