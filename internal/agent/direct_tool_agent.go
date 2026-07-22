// direct_tool_agent.go implements a synchronous tool-calling agent that calls
// SGLang's OpenAI-compatible API directly. It extends the direct_classifier.go
// pattern to support agentic tool loops for coder, reviewer, and fixer roles.
//
// SGLang must be running with --tool-call-parser qwen25 to handle bidirectional
// conversion between OpenAI tool format and Qwen3-Coder's native tool call tokens.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"time"
)

// TraceEvent is a single entry in the optional agent trace log. One is written
// per iteration capturing the assistant reply and every tool call/result pair.
type TraceEvent struct {
	Iteration    int         `json:"iteration"`
	FinishReason string      `json:"finish_reason"`
	TokensIn     int         `json:"tokens_in"`
	TokensOut    int         `json:"tokens_out"`
	Assistant    string      `json:"assistant_content,omitempty"`
	ToolCalls    []TraceCall `json:"tool_calls,omitempty"`
}

// TraceCall is one tool invocation and its result as seen by the loop.
type TraceCall struct {
	Name   string `json:"name"`
	Args   string `json:"args"`
	Result string `json:"result"`
	Error  string `json:"error,omitempty"`
}

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

// DirectToolAgentConfig holds connection, generation, and execution parameters
// for the direct tool-calling agent.
type DirectToolAgentConfig struct {
	Endpoint      string        // Full URL for chat completions endpoint
	Model         string        // Model identifier sent in the API request
	MaxTokens     int           // Maximum tokens per response
	Temperature   float64       // Sampling temperature
	Timeout       time.Duration // HTTP request timeout per API call
	MaxIterations int           // Maximum tool-call loop iterations
	WorkDir       string        // Working directory; tools restricted to paths under this
	BashTimeout   time.Duration // Timeout for bash commands (default 30s)
	TraceWriter   io.Writer     // Optional: JSON-lines trace, one TraceEvent per iteration
	// GQCaller and GQPriority identify this request to a GQ admission proxy.
	// They are harmless when the endpoint is SGLang directly because unknown
	// HTTP headers are ignored.
	GQCaller   string
	GQPriority string
	// ChatTemplateKwargs is forwarded to SGLang as `chat_template_kwargs` in the
	// request body. Use for model-specific template flags (e.g. Gemma-4's
	// `enable_thinking: true`). Nil/empty ⇒ field omitted.
	ChatTemplateKwargs map[string]any

	// Context-window monitoring. ContextLimit is the model's input context
	// window size in tokens (e.g. 131072 for gemma4-26b). When > 0, the loop
	// inspects each response's prompt_tokens against ContextLimit and:
	//   - injects a one-shot system "wrap up" nudge once usage crosses
	//     ContextWarnPct (default 85);
	//   - hard-stops the loop with StopReason="context_limit" once usage
	//     crosses ContextStopPct (default 95) on any non-stop turn.
	// When ContextLimit is 0 the monitor is disabled and the result's
	// FinalContextPct stays 0.
	ContextLimit   int
	ContextWarnPct int
	ContextStopPct int

	// OnIteration is called after each successful API round-trip. Use it to
	// update heartbeat timestamps so the stale-agent reaper doesn't kill
	// long-running direct-tool agents. contextPct is the current context
	// window usage percentage (0 when ContextLimit is not configured).
	// Nil ⇒ no callback.
	OnIteration func(iteration, tokensIn, tokensOut, contextPct int)

	// OnToolCall is called after each tool execution with the tool name,
	// parsed arguments, and result length in bytes. Use it to persist
	// activity metrics (last tool, target file, phase) so the TUI can
	// display live agent activity. Nil ⇒ no callback.
	OnToolCall func(toolName string, args map[string]any, resultLen int)
}

// DefaultDirectToolAgentConfig returns a config targeting the local SGLang
// server with sensible defaults.
func DefaultDirectToolAgentConfig() DirectToolAgentConfig {
	return DirectToolAgentConfig{
		Endpoint:      "http://localhost:8081/v1/chat/completions",
		Model:         "qwen3-coder-30b",
		MaxTokens:     8192,
		Temperature:   0.1,
		Timeout:       120 * time.Second,
		MaxIterations: 20,
		BashTimeout:   30 * time.Second,
	}
}

// ---------------------------------------------------------------------------
// Result
// ---------------------------------------------------------------------------

// DirectToolAgentResult holds the outcome of a tool-agent run.
type DirectToolAgentResult struct {
	Output     string        // Final text output from the model
	OutputPath string        // Path to the written output file (if any)
	TokensIn   int           // Total prompt tokens across all iterations
	TokensOut  int           // Total completion tokens across all iterations
	Iterations int           // Number of loop iterations used
	Duration   time.Duration // Wall-clock time for the entire run
	// FinalContextPct is the most recent prompt_tokens / ContextLimit
	// expressed in 0..100. Stays 0 when ContextLimit is unset.
	FinalContextPct int
	// StopReason is set for structured non-natural stops such as
	// "context_limit", "max_iterations", or "no_progress".
	StopReason string
}

// Structured stop reasons emitted by DirectToolAgentResult.StopReason.
const (
	DirectToolStopReasonContextLimit  = "context_limit"
	DirectToolStopReasonMaxIterations = "max_iterations"
	DirectToolStopReasonNoProgress    = "no_progress"
	DirectToolStopReasonMaxTokens     = "max_tokens"
	DirectToolStopReasonTimeout       = "timeout"
)

// ---------------------------------------------------------------------------
// Tool schemas
// ---------------------------------------------------------------------------

// builtinTools defines the complete set of available tool definitions.
// Descriptions are deliberately detailed to steer models (especially smaller
// ones like Gemma-4 26B) toward the structured tools instead of routing
// everything through bash. Bash is restricted to build/test/run commands.
var builtinTools = map[string]toolDefinition{
	"read": {
		Type: "function",
		Function: toolFunction{
			Name:        "read",
			Description: "Read file contents with line numbers. ALWAYS use this instead of cat/head/tail/sed -n. Returns formatted output with line numbers for precise editing. Supports offset and limit for large files.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"File path to read"},"offset":{"type":"integer","description":"Line number to start from (0-based)"},"limit":{"type":"integer","description":"Max lines to return (default 200)"}},"required":["path"]}`),
		},
	},
	"edit": {
		Type: "function",
		Function: toolFunction{
			Name:        "edit",
			Description: "Replace exact text in a file. Use for surgical changes — provide the exact existing text and its replacement. The old text must appear exactly once in the file. Safer than write for modifications to existing files.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"File path"},"old":{"type":"string","description":"Exact existing text to find (must be unique in file)"},"new":{"type":"string","description":"Replacement text"}},"required":["path","old","new"]}`),
		},
	},
	"write": {
		Type: "function",
		Function: toolFunction{
			Name:        "write",
			Description: "Write content to a file (creates or overwrites). ALWAYS use this instead of bash heredocs (cat <<EOF), echo >, or tee. Handles all special characters safely — no shell escaping issues with backticks, quotes, or dollar signs. Creates parent directories automatically.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"File path"},"content":{"type":"string","description":"Complete file content to write"}},"required":["path","content"]}`),
		},
	},
	"bash": {
		Type: "function",
		Function: toolFunction{
			Name:        "bash",
			Description: "Run a shell command for building, testing, and running programs (go test, go vet, make, etc.). Do NOT use bash for file operations — use the dedicated tools instead: read (not cat/head/tail), write (not cat <<EOF or echo >), edit (not sed -i), grep (not grep/rg), glob (not find/ls).",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"cmd":{"type":"string","description":"Shell command to execute. Must not be used for file reading (use read), file writing (use write), file editing (use edit), or searching (use grep/glob)."}},"required":["cmd"]}`),
		},
	},
	"grep": {
		Type: "function",
		Function: toolFunction{
			Name:        "grep",
			Description: "Search file contents with regex. ALWAYS use this instead of bash grep/rg. Returns matched lines with file paths and line numbers. Supports glob filtering to narrow search scope.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string","description":"Regex pattern to search for"},"path":{"type":"string","description":"Directory or file to search (default: working directory)"},"glob":{"type":"string","description":"File glob filter (e.g. *.go)"}},"required":["pattern"]}`),
		},
	},
	"glob": {
		Type: "function",
		Function: toolFunction{
			Name:        "glob",
			Description: "Find files by glob pattern. ALWAYS use this instead of find or ls for locating files. Returns matching file paths.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string","description":"Glob pattern (e.g. **/*.go)"},"path":{"type":"string","description":"Base directory (default: working directory)"}},"required":["pattern"]}`),
		},
	},
}

// roleTools maps agent roles to their permitted tool names.
var roleTools = map[string][]string{
	"coder":    {"read", "edit", "write", "bash", "grep", "glob"},
	"fixer":    {"read", "edit", "write", "bash", "grep", "glob"},
	"reviewer": {"read", "bash", "grep", "glob"},
	"prep":     {"read", "grep", "glob"},
}

// ToolsForRole returns the tool definitions permitted for the given agent role.
// Unknown roles get read-only tools (read, grep, glob).
func ToolsForRole(role string) []toolDefinition {
	names, ok := roleTools[role]
	if !ok {
		names = []string{"read", "grep", "glob"}
	}
	tools := make([]toolDefinition, 0, len(names))
	for _, name := range names {
		if td, exists := builtinTools[name]; exists {
			tools = append(tools, td)
		}
	}
	return tools
}

// ---------------------------------------------------------------------------
// Core tool-call loop
// ---------------------------------------------------------------------------

// RunDirectToolAgent runs a tool-calling agent loop against the SGLang API.
// It sends the system prompt and user message, then iterates: if the model
// responds with tool calls, it executes them and feeds results back. The loop
// ends when the model returns finish_reason "stop" or max iterations are hit.
//
// The final text output is written to outputPath if provided.
func RunDirectToolAgent(cfg DirectToolAgentConfig, systemPrompt, userMessage string, tools []toolDefinition, outputPath string) (*DirectToolAgentResult, error) {
	start := time.Now()

	exec := &toolExecutor{
		workDir:     cfg.WorkDir,
		bashTimeout: cfg.BashTimeout,
	}

	messages := []toolChatMsg{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userMessage},
	}

	var totalTokensIn, totalTokensOut int

	// Context-window monitor state. finalPct mirrors the most recent
	// prompt_tokens / ContextLimit and is propagated into every result return.
	// warnInjected is set the first time the warn nudge is appended so that
	// later iterations don't re-inject (avoids spamming the conversation).
	var finalPct int
	var warnInjected bool
	warnPct := cfg.ContextWarnPct
	if warnPct <= 0 {
		warnPct = 85
	}
	stopPct := cfg.ContextStopPct
	if stopPct <= 0 {
		stopPct = 95
	}

	// Loop detector state: remember the signature of the last tool call
	// (name+args) AND its result so we can recognise "no forward progress"
	// patterns and nudge the model toward a different approach. We consider
	// a call "stuck" when it repeats with identical args AND produces
	// identical output as the immediately preceding call — this catches both
	// failing-edit loops (result is the same error each time) and
	// successful-but-useless grep/ls loops (result is the same empty or
	// same-match text each time).
	var prevToolKey, prevToolOut string
	var consecutiveRepeats int

	// File re-read tracker: counts how many times each resolved file path
	// has been read via the read tool. After maxFileReads, the tool result
	// includes a warning nudging the model to stop re-reading and act.
	fileReadCounts := map[string]int{}

	maxIter := cfg.MaxIterations
	if maxIter <= 0 {
		maxIter = 20
	}

	for iteration := 0; iteration < maxIter; iteration++ {
		slog.Info("direct tool agent: calling API",
			"iteration", iteration,
			"messages", len(messages),
			"tokens_in_total", totalTokensIn,
			"tokens_out_total", totalTokensOut,
		)

		resp, err := callToolAPI(cfg, messages, tools)
		if err != nil {
			stopReason := ""
			if isTimeoutError(err) {
				stopReason = DirectToolStopReasonTimeout
			}
			return &DirectToolAgentResult{
				TokensIn:        totalTokensIn,
				TokensOut:       totalTokensOut,
				Iterations:      iteration,
				Duration:        time.Since(start),
				FinalContextPct: finalPct,
				StopReason:      stopReason,
			}, fmt.Errorf("API call failed at iteration %d: %w", iteration, err)
		}

		totalTokensIn += resp.Usage.PromptTokens
		totalTokensOut += resp.Usage.CompletionTokens

		// Context monitor: derive current usage from the most recent
		// prompt_tokens. PromptTokens reflects the size of the inbound
		// message stack, which is what matters for "approaching the model's
		// context limit" — totalTokensIn (cumulative across iterations) is
		// for cost accounting, not context.
		var currentPct int
		if cfg.ContextLimit > 0 && resp.Usage.PromptTokens > 0 {
			currentPct = (resp.Usage.PromptTokens * 100) / cfg.ContextLimit
			finalPct = currentPct
		}

		// Heartbeat callback: keeps the stale-agent reaper from killing
		// long-running direct-tool agents that aren't in the runner's
		// running map. Also lets callers persist incremental token counts
		// and live context percentage.
		if cfg.OnIteration != nil {
			cfg.OnIteration(iteration, totalTokensIn, totalTokensOut, currentPct)
		}

		if len(resp.Choices) == 0 {
			return &DirectToolAgentResult{
				TokensIn:        totalTokensIn,
				TokensOut:       totalTokensOut,
				Iterations:      iteration + 1,
				Duration:        time.Since(start),
				FinalContextPct: finalPct,
			}, fmt.Errorf("no choices in response at iteration %d", iteration)
		}

		choice := resp.Choices[0]

		traceEvent := TraceEvent{
			Iteration:    iteration,
			FinishReason: choice.FinishReason,
			TokensIn:     resp.Usage.PromptTokens,
			TokensOut:    resp.Usage.CompletionTokens,
			Assistant:    choice.Message.Content,
		}

		// If the model is done, return the final content. A natural stop
		// always wins over the context monitor — if the model produced a
		// final answer on its own, there is no point in flagging
		// context_limit even if usage already crossed the stop threshold.
		if choice.FinishReason == "stop" || choice.FinishReason == "end_of_turn" {
			writeTrace(cfg.TraceWriter, traceEvent)
			finalOutput := choice.Message.Content
			result := &DirectToolAgentResult{
				Output:          finalOutput,
				TokensIn:        totalTokensIn,
				TokensOut:       totalTokensOut,
				Iterations:      iteration + 1,
				Duration:        time.Since(start),
				FinalContextPct: finalPct,
			}

			// Write output file if requested.
			if outputPath != "" && finalOutput != "" {
				if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
					return result, fmt.Errorf("create output dir: %w", err)
				}
				if err := os.WriteFile(outputPath, []byte(finalOutput), 0o644); err != nil {
					return result, fmt.Errorf("write output file: %w", err)
				}
				result.OutputPath = outputPath
			}

			slog.Info("direct tool agent: completed",
				"iterations", iteration+1,
				"tokens_in", totalTokensIn,
				"tokens_out", totalTokensOut,
				"duration", time.Since(start).Round(time.Millisecond),
			)
			return result, nil
		}

		// Context monitor: hard stop. The natural-stop branch above already
		// returned, so reaching this point means the model wants to keep
		// going (tool calls, length truncation, etc.). If usage has crossed
		// the stop threshold, halt now to prevent runaway token spend.
		// Returns nil error (this is a planned safety stop, not a failure).
		if cfg.ContextLimit > 0 && currentPct >= stopPct {
			writeTrace(cfg.TraceWriter, traceEvent)
			slog.Warn("direct tool agent: context stop threshold reached",
				"iteration", iteration,
				"pct", currentPct,
				"limit", cfg.ContextLimit,
				"prompt_tokens", resp.Usage.PromptTokens,
			)
			return &DirectToolAgentResult{
				Output:          choice.Message.Content,
				TokensIn:        totalTokensIn,
				TokensOut:       totalTokensOut,
				Iterations:      iteration + 1,
				Duration:        time.Since(start),
				FinalContextPct: finalPct,
				StopReason:      DirectToolStopReasonContextLimit,
			}, nil
		}

		// If the model wants to call tools, execute them.
		if choice.FinishReason == "tool_calls" || len(choice.Message.ToolCalls) > 0 {
			// Per-turn cap: if the model emits more than maxToolCallsPerTurn
			// calls, only keep the first N and stub the rest. This prevents
			// degenerate generation (observed: model starts paginating a file
			// with 28 valid reads, then generation breaks down into 159
			// malformed calls, consuming 58k context tokens on garbage).
			callsToProcess := choice.Message.ToolCalls
			var cappedCalls []toolCall
			if len(callsToProcess) > maxToolCallsPerTurn {
				slog.Warn("direct tool agent: capping tool calls per turn",
					"iteration", iteration,
					"total", len(callsToProcess),
					"cap", maxToolCallsPerTurn,
				)
				cappedCalls = callsToProcess[maxToolCallsPerTurn:]
				callsToProcess = callsToProcess[:maxToolCallsPerTurn]
				// Rewrite the assistant message to only contain the kept calls,
				// so capped calls don't pollute conversation history.
				cappedMsg := choice.Message
				cappedMsg.ToolCalls = callsToProcess
				messages = append(messages, cappedMsg)
			} else {
				// Append the assistant message with tool calls.
				messages = append(messages, choice.Message)
			}

			// Stub the capped calls with a redirect message. Each tool_call_id
			// still gets a response so the OpenAI protocol is satisfied.
			for _, tc := range cappedCalls {
				stub := fmt.Sprintf("[HARNESS: Too many tool calls in one turn (%d). Only the first %d were executed. "+
					"Break your work across multiple turns — call a few tools, review the results, then continue.]",
					len(choice.Message.ToolCalls), maxToolCallsPerTurn)
				messages = append(messages, toolChatMsg{
					Role:       "tool",
					Content:    stub,
					ToolCallID: tc.ID,
					Name:       tc.Function.Name,
				})
			}

			// Intra-iteration dedup: if the model emits multiple tool calls
			// with identical name+args in a single turn, execute only the first
			// and stub the rest. Observed in bench: a single turn contained 60
			// identical `ls -R` calls. Each stub still preserves tool_call_id
			// so the OpenAI protocol (one result per call) is satisfied.
			seenThisIter := map[string]string{}

			for _, tc := range callsToProcess {
				slog.Info("direct tool agent: executing tool",
					"iteration", iteration,
					"tool", tc.Function.Name,
					"call_id", tc.ID,
				)

				curKey := tc.Function.Name + "\x00" + tc.Function.Arguments

				// If an identical call already ran this turn, stub the rest.
				if firstResult, dup := seenThisIter[curKey]; dup {
					slog.Warn("direct tool agent: duplicate tool call in same turn (stubbed)",
						"iteration", iteration,
						"tool", tc.Function.Name,
					)
					stub := "[HARNESS: duplicate tool call in the same turn — executed once above, re-using result.\n" +
						"First result was:\n" + truncateForStub(firstResult, 400) +
						"\n\nDo not emit multiple identical tool calls in one turn. If you need to re-check, do it on the next turn after considering the first result.]"
					messages = append(messages, toolChatMsg{
						Role:       "tool",
						Content:    stub,
						ToolCallID: tc.ID,
						Name:       tc.Function.Name,
					})
					traceEvent.ToolCalls = append(traceEvent.ToolCalls, TraceCall{
						Name:   tc.Function.Name,
						Args:   tc.Function.Arguments,
						Result: stub,
					})
					continue
				}

				result, toolErr := exec.execute(tc.Function.Name, tc.Function.Arguments)
				traceCall := TraceCall{
					Name: tc.Function.Name,
					Args: tc.Function.Arguments,
				}
				if toolErr != nil {
					traceCall.Error = toolErr.Error()
					result = "ERROR: " + toolErr.Error()
				}

				// File re-read tracker: warn when the same file is read
				// too many times, nudging the model to use what it has.
				if tc.Function.Name == "read" {
					var readArgs struct {
						Path string `json:"path"`
					}
					if json.Unmarshal([]byte(tc.Function.Arguments), &readArgs) == nil && readArgs.Path != "" {
						resolved, _ := filepath.Abs(filepath.Join(cfg.WorkDir, readArgs.Path))
						fileReadCounts[resolved]++
						if fileReadCounts[resolved] > maxFileReads {
							result += fmt.Sprintf("\n\n[HARNESS NOTE: You have read this file %d times. "+
								"Its contents have not changed. Stop re-reading and take action — "+
								"call write or edit to make your changes.]", fileReadCounts[resolved])
						}
					}
				}

				// Loop detector: if this call has identical name+args AND
				// produces identical output to the immediately preceding call,
				// the model is making no forward progress. Inject a nudge.
				// We store the pre-note result in prevToolOut so the comparison
				// fires every consecutive repeat, not just every other one.
				preNoteResult := result
				if curKey == prevToolKey && result == prevToolOut {
					consecutiveRepeats++
					slog.Warn("direct tool agent: loop detector triggered",
						"iteration", iteration,
						"tool", tc.Function.Name,
						"repeats", consecutiveRepeats,
					)
					if consecutiveRepeats >= 5 {
						writeTrace(cfg.TraceWriter, traceEvent)
						return &DirectToolAgentResult{
							Output:          choice.Message.Content,
							TokensIn:        totalTokensIn,
							TokensOut:       totalTokensOut,
							Iterations:      iteration + 1,
							Duration:        time.Since(start),
							FinalContextPct: finalPct,
							StopReason:      DirectToolStopReasonNoProgress,
						}, fmt.Errorf("agent made no progress after %d repeated %s tool calls", consecutiveRepeats+1, tc.Function.Name)
					}
					if consecutiveRepeats >= 3 {
						result += "\n\n[HARNESS NOTE: You have repeated this EXACT call " +
							fmt.Sprintf("%d", consecutiveRepeats+1) + " times with identical results. " +
							"The task may already be complete. If so, respond with DONE. " +
							"Otherwise you MUST try a completely different approach NOW.]"
					} else {
						result += "\n\n[HARNESS NOTE: This is the 2nd consecutive identical call producing identical output. You are making no forward progress. Try a different approach:\n" +
							" - if searching, broaden or narrow the query, or try a different tool (e.g. glob vs grep, or read a known file directly);\n" +
							" - if editing and the tool reports an error, re-read the file to confirm its current contents and check whitespace/indentation in your 'old' string, or use 'write' for a full-file replace;\n" +
							" - if the information you need is not findable, proceed with a reasonable assumption, document it in a code comment, and continue.]"
					}
				} else {
					consecutiveRepeats = 0
				}
				prevToolKey = curKey
				prevToolOut = preNoteResult
				seenThisIter[curKey] = result
				traceCall.Result = result
				traceEvent.ToolCalls = append(traceEvent.ToolCalls, traceCall)

				// Fire OnToolCall callback for activity tracking.
				if cfg.OnToolCall != nil {
					var parsedArgs map[string]any
					_ = json.Unmarshal([]byte(tc.Function.Arguments), &parsedArgs)
					cfg.OnToolCall(tc.Function.Name, parsedArgs, len(result))
				}

				// Append the tool result message.
				messages = append(messages, toolChatMsg{
					Role:       "tool",
					Content:    result,
					ToolCallID: tc.ID,
					Name:       tc.Function.Name,
				})
			}
			writeTrace(cfg.TraceWriter, traceEvent)

			// Context monitor: warn nudge. Inject a one-shot system message
			// asking the model to wrap up. Only fires once per run so the
			// conversation doesn't accumulate redundant nudges.
			if cfg.ContextLimit > 0 && currentPct >= warnPct && !warnInjected {
				slog.Warn("direct tool agent: context warn threshold crossed",
					"iteration", iteration,
					"pct", currentPct,
					"limit", cfg.ContextLimit,
					"prompt_tokens", resp.Usage.PromptTokens,
				)
				messages = append(messages, toolChatMsg{
					Role: "system",
					Content: fmt.Sprintf(
						"[HARNESS] Context window at %d%% (%d/%d tokens). Wrap up: "+
							"finish the current step, do not start any new investigations, "+
							"and respond with your final answer on the next turn.",
						currentPct, resp.Usage.PromptTokens, cfg.ContextLimit,
					),
				})
				warnInjected = true
			}
			continue
		}

		// Length-truncated response: the model hit max_tokens before completing
		// its turn. SGLang in this case may return an empty message (no content,
		// no tool_calls) because a partial tool-call cannot be returned as
		// structured JSON. Do NOT silently treat this as a successful stop —
		// surface as an error so callers (drembench, orchestrator) can retry or
		// classify distinctly.
		if choice.FinishReason == "length" {
			slog.Warn("direct tool agent: response truncated at max_tokens",
				"iteration", iteration,
				"max_tokens", cfg.MaxTokens,
				"tokens_out", resp.Usage.CompletionTokens,
				"had_content", choice.Message.Content != "",
				"had_tool_calls", len(choice.Message.ToolCalls) > 0,
			)
			writeTrace(cfg.TraceWriter, traceEvent)
			return &DirectToolAgentResult{
					Output:          choice.Message.Content,
					TokensIn:        totalTokensIn,
					TokensOut:       totalTokensOut,
					Iterations:      iteration + 1,
					Duration:        time.Since(start),
					FinalContextPct: finalPct,
					StopReason:      DirectToolStopReasonMaxTokens,
				}, fmt.Errorf("response truncated at max_tokens=%d (iteration %d); increase MaxTokens or reduce tool-call payload size",
					cfg.MaxTokens, iteration)
		}

		// Unexpected finish reason — treat any content as final output.
		slog.Warn("direct tool agent: unexpected finish_reason",
			"finish_reason", choice.FinishReason,
			"iteration", iteration,
		)
		writeTrace(cfg.TraceWriter, traceEvent)
		return &DirectToolAgentResult{
			Output:          choice.Message.Content,
			TokensIn:        totalTokensIn,
			TokensOut:       totalTokensOut,
			Iterations:      iteration + 1,
			Duration:        time.Since(start),
			FinalContextPct: finalPct,
		}, nil
	}

	return &DirectToolAgentResult{
		TokensIn:        totalTokensIn,
		TokensOut:       totalTokensOut,
		Iterations:      maxIter,
		Duration:        time.Since(start),
		FinalContextPct: finalPct,
		StopReason:      DirectToolStopReasonMaxIterations,
	}, fmt.Errorf("exceeded max iterations (%d)", maxIter)
}

func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	if os.IsTimeout(err) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// truncateForStub clamps a result string to n characters so dedup stubs do
// not blow the context when the original result was large. Used when a
// duplicate tool call is elided — the second+ result just points back at the
// first and does not need the full body repeated.
func truncateForStub(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n[...truncated]"
}

// writeTrace emits one JSON-line TraceEvent if the trace writer is configured.
// Failures are logged but never surface — tracing is strictly opt-in diagnostic.
func writeTrace(w io.Writer, ev TraceEvent) {
	if w == nil {
		return
	}
	data, err := json.Marshal(ev)
	if err != nil {
		slog.Warn("direct tool agent: trace marshal failed", "err", err)
		return
	}
	if _, err := w.Write(append(data, '\n')); err != nil {
		slog.Warn("direct tool agent: trace write failed", "err", err)
	}
}
