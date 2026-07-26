// direct_tool_agent.go implements a synchronous tool-calling agent that calls
// SGLang's OpenAI-compatible API directly. It extends the direct_classifier.go
// pattern to support agentic tool loops for coder, reviewer, and fixer roles.
//
// SGLang must be running with --tool-call-parser qwen25 to handle bidirectional
// conversion between OpenAI tool format and Qwen3-Coder's native tool call tokens.
package agent

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Core tool-call loop
// ---------------------------------------------------------------------------

// RunDirectToolAgent runs a tool-calling agent loop against the SGLang API.
// It sends the system prompt and user message, then iterates: if the model
// responds with tool calls, it executes them and feeds results back. The loop
// ends when the model returns finish_reason "stop" or max iterations are hit.
//
// The final text output is written to outputPath if provided.
func RunDirectToolAgent(cfg DirectToolAgentConfig, systemPrompt, userMessage string, tools []toolDefinition, outputPath string) (finalResult *DirectToolAgentResult, finalErr error) {
	start := time.Now()
	var peakRequestInput, resumedTurns, foldedBytes int
	readPaths := map[string]int{}
	deniedCalls := 0
	firstMutationIteration := 0
	var firstMutationMs int64
	defer func() {
		if finalResult != nil {
			finalResult.PeakRequestInput = peakRequestInput
			finalResult.ResumedTurns = resumedTurns
			finalResult.FoldedBytes = foldedBytes
			finalResult.DeniedCalls = deniedCalls
			finalResult.FirstMutationIteration = firstMutationIteration
			finalResult.FirstMutationMs = firstMutationMs
			for _, count := range readPaths {
				finalResult.UniqueReads++
				if count > 1 {
					finalResult.RedundantReads += count - 1
				}
			}
		}
	}()

	exec := &toolExecutor{
		workDir:       cfg.WorkDir,
		bashTimeout:   cfg.BashTimeout,
		scopedFiles:   make(map[string]struct{}, len(cfg.ScopedFiles)),
		readableFiles: make(map[string]struct{}, len(cfg.ReadableFiles)),
	}
	for _, path := range cfg.ReadableFiles {
		resolved, err := exec.resolvePath(path)
		if err == nil {
			exec.readableFiles[resolved] = struct{}{}
		}
	}
	for _, path := range cfg.ScopedFiles {
		resolved, err := exec.resolvePath(path)
		if err == nil {
			exec.scopedFiles[resolved] = struct{}{}
		}
	}

	messages := []toolChatMsg{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userMessage},
	}

	var totalTokensIn, totalTokensOut int
	startIteration := 0
	promptHash := directToolPromptHash(systemPrompt, userMessage)
	resumedJournal, journalErr := loadDirectToolJournal(cfg.JournalPath, promptHash)
	if journalErr != nil {
		return nil, journalErr
	}
	if cfg.RequireJournalResume && resumedJournal == nil {
		return nil, fmt.Errorf("required durable journal resume was unavailable or its immutable prompt fingerprint changed")
	}
	if resumedJournal != nil {
		messages = resumedJournal.Messages
		totalTokensIn = resumedJournal.TokensIn
		totalTokensOut = resumedJournal.TokensOut
		peakRequestInput = resumedJournal.PeakRequestInput
		startIteration = resumedJournal.NextIteration
		resumedTurns = startIteration
	}

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
	var semanticLoops semanticLoopDetector
	var failedMutationLoops failedMutationLoopDetector
	if resumedJournal != nil {
		failedMutationLoops.Restore(messages)
	}

	// File re-read tracker: counts how many times each resolved file path
	// has been read via the read tool. After maxFileReads, the tool result
	// includes a warning nudging the model to stop re-reading and act.
	fileReadCounts := map[string]int{}
	reconnaissanceBeforeMutation := 0
	blockedReconCallsBeforeMutation := 0
	blockedReconBatchesBeforeMutation := 0
	finalScopedReadGranted := false
	protectedWriteCallsBeforeMutation := 0
	protectedWriteBatchesBeforeMutation := 0
	scopeViolationCallsBeforeMutation := 0
	scopeViolationBatchesBeforeMutation := 0
	failedEditRecoveryPath := ""
	failedEditRecoveryAvailable := false
	failedEditRecoveryGranted := false
	mutationOnlyNudgeInjected := false
	totalToolCalls := 0
	mutationObserved := false
	if resumedJournal != nil {
		totalToolCalls = resumedJournal.TotalToolCalls
		mutationObserved = resumedJournal.MutationObserved
	}
	persistJournal := func(nextIteration int, completed bool, lastTurn *TraceEvent) error {
		return saveDirectToolJournal(cfg.JournalPath, directToolJournal{
			PromptHash:       promptHash,
			Messages:         messages,
			NextIteration:    nextIteration,
			TokensIn:         totalTokensIn,
			TokensOut:        totalTokensOut,
			PeakRequestInput: peakRequestInput,
			MutationObserved: mutationObserved,
			TotalToolCalls:   totalToolCalls,
			Completed:        completed,
			LastTurn:         lastTurn,
		})
	}

	maxIter := cfg.MaxIterations
	if maxIter <= 0 {
		maxIter = 20
	}

	for iteration := startIteration; iteration < maxIter; iteration++ {
		var folded int
		messages, folded = compactToolResultHistoryForConfig(messages, cfg, finalPct)
		foldedBytes += folded
		slog.Info("direct tool agent: calling API",
			"iteration", iteration,
			"messages", len(messages),
			"tokens_in_total", totalTokensIn,
			"tokens_out_total", totalTokensOut,
		)

		activeTools := tools
		if !mutationObserved {
			activeTools = preMutationTools(cfg, activeTools)
		}
		if failedEditRecoveryAvailable {
			activeTools = toolsByName([]string{"read", "edit"})
		} else if !mutationObserved && (hasNaturalStopCorrection(messages) || finalScopedReadGranted || blockedReconBatchesBeforeMutation > 0 || protectedWriteBatchesBeforeMutation > 0 || scopeViolationBatchesBeforeMutation > 0) {
			activeTools = mutationOnlyTools(cfg, tools)
		}
		resp, err := callToolAPI(cfg, messages, activeTools)
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
		if resp.Usage.PromptTokens > peakRequestInput {
			peakRequestInput = resp.Usage.PromptTokens
		}

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
			ElapsedMs:    time.Since(start).Milliseconds(),
		}

		// If the model is done, return the final content. A natural stop
		// always wins over the context monitor — if the model produced a
		// final answer on its own, there is no point in flagging
		// context_limit even if usage already crossed the stop threshold.
		if choice.FinishReason == "stop" || choice.FinishReason == "end_of_turn" {
			writeTrace(cfg.TraceWriter, traceEvent)
			if shouldCorrectUnmutatedNaturalStop(cfg, mutationObserved, totalTokensIn, choice.Message.Content, messages) {
				messages = append(messages, choice.Message, toolChatMsg{
					Role: "system", Content: naturalStopCorrectionPrompt,
				})
				if err := persistJournal(iteration+1, false, &traceEvent); err != nil {
					return nil, err
				}
				continue
			}
			if err := persistJournal(iteration+1, true, &traceEvent); err != nil {
				return nil, err
			}
			result := naturalStopResult(choice.Message.Content, totalTokensIn, totalTokensOut, iteration+1,
				time.Since(start), finalPct, mutationObserved, peakRequestInput, resumedTurns, foldedBytes)
			if !cfg.AllowReadOnlyCompletion && !mutationObserved && strings.HasPrefix(strings.TrimSpace(choice.Message.Content), "BLOCKED:") {
				result.StopReason = DirectToolStopReasonNoProgress
				return result, fmt.Errorf("agent reported typed blocker: %s", strings.TrimSpace(choice.Message.Content))
			}
			if !cfg.AllowReadOnlyCompletion && !mutationObserved && cfg.MaxInputTokensBeforeMutation > 0 && totalTokensIn >= cfg.MaxInputTokensBeforeMutation {
				result.StopReason = DirectToolStopReasonNoProgress
				reason := unmutatedNaturalStopReason(totalTokensIn, cfg.MaxInputTokensBeforeMutation, hasNaturalStopCorrection(messages))
				return result, fmt.Errorf("%s", reason)
			}
			finalOutput := result.Output

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

		// Decide whether this is the final paid response before executing its
		// tool calls, but stop only after that complete response batch. Stopping
		// here used to discard edits the model had already generated and billed.
		budgetReached := cfg.MaxCumulativeInputTokens > 0 && totalTokensIn >= cfg.MaxCumulativeInputTokens
		contextReached := cfg.ContextLimit > 0 && currentPct >= stopPct

		// If the model wants to call tools, execute them.
		if choice.FinishReason == "tool_calls" || len(choice.Message.ToolCalls) > 0 {
			for i := range choice.Message.ToolCalls {
				repaired, ok := repairMalformedScopedMutation(choice.Message.ToolCalls[i], cfg.ScopedFiles)
				if !ok {
					continue
				}
				slog.Warn("direct tool agent: repaired malformed scoped mutation call",
					"iteration", iteration,
					"original_tool", choice.Message.ToolCalls[i].Function.Name,
					"tool", repaired.Function.Name,
					"path", toolCallPath(repaired.Function.Arguments),
				)
				choice.Message.ToolCalls[i] = repaired
			}
			blockedReconThisBatch, finalScopedReadThisBatch := false, false
			protectedWriteThisBatch := false
			scopeViolationThisBatch := false
			failedEditRecoveryReadThisBatch := false
			failedMutationStopReason := ""
			// Per-turn cap: if the model emits more than maxToolCallsPerTurn
			// calls, only keep the first N and stub the rest. This prevents
			// degenerate generation (observed: model starts paginating a file
			// with 28 valid reads, then generation breaks down into 159
			// malformed calls, consuming 58k context tokens on garbage).
			callsToProcess := choice.Message.ToolCalls
			remainingCalls := cfg.MaxToolCalls - totalToolCalls
			if cfg.MaxToolCalls <= 0 {
				remainingCalls = len(callsToProcess)
			}
			callCap := maxToolCallsPerTurn
			if remainingCalls < callCap {
				callCap = remainingCalls
			}
			if callCap < 0 {
				callCap = 0
			}
			if len(callsToProcess) > callCap {
				slog.Warn("direct tool agent: capping tool calls per turn",
					"iteration", iteration,
					"total", len(callsToProcess),
					"cap", callCap,
				)
				callsToProcess = callsToProcess[:callCap]
				// Rewrite the assistant message to only contain the kept calls,
				// so capped calls don't pollute conversation history.
				cappedMsg := choice.Message
				cappedMsg.ToolCalls = callsToProcess
				messages = append(messages, cappedMsg)
			} else {
				// Append the assistant message with tool calls.
				messages = append(messages, choice.Message)
			}
			totalToolCalls += len(callsToProcess)

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
					"path", toolCallPath(tc.Function.Arguments),
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

				var result string
				var toolErr error
				reconnaissance := !mutationObserved && isReconnaissanceToolCall(tc.Function.Name, tc.Function.Arguments)
				unavailableBeforeMutation := !mutationObserved && !toolIsAvailable(activeTools, tc.Function.Name)
				recoveryRead := failedEditRecoveryAvailable && tc.Function.Name == "read" &&
					toolCallTargetsPath(tc.Function.Arguments, failedEditRecoveryPath)
				reconBudgetReached := !recoveryRead && !cfg.AllowReadOnlyCompletion && reconnaissance && cfg.MaxReadsBeforeMutation > 0 &&
					reconnaissanceBeforeMutation >= cfg.MaxReadsBeforeMutation
				inputBudgetReached := !recoveryRead && !cfg.AllowReadOnlyCompletion && reconnaissance && cfg.MaxInputTokensBeforeMutation > 0 &&
					totalTokensIn >= cfg.MaxInputTokensBeforeMutation
				finalScopedRead := !recoveryRead && !finalScopedReadGranted && tc.Function.Name == "read" &&
					(reconBudgetReached || inputBudgetReached) && toolCallTargetsAnyPath(cfg.WorkDir, tc.Function.Arguments, cfg.ScopedFiles)
				blockedRecon := (reconBudgetReached || inputBudgetReached) && !finalScopedRead
				protectedWrite := cfg.ProtectExistingFiles && tc.Function.Name == "write" && writeTargetsExistingFile(cfg.WorkDir, tc.Function.Arguments)
				outOfScopeMutation := mutationTargetsOutsideScope(cfg.WorkDir, tc.Function.Name, tc.Function.Arguments, cfg.ScopedFiles)
				if unavailableBeforeMutation {
					blockedReconCallsBeforeMutation++
					blockedReconThisBatch = true
					result = fmt.Sprintf("[HARNESS: Tool %q is unavailable before the first scoped mutation. Use structured read only for missing source detail, then edit/write an authorized path. Shell verification becomes available after a mutation checkpoint.]", tc.Function.Name)
				} else if outOfScopeMutation {
					scopeViolationCallsBeforeMutation++
					scopeViolationThisBatch = true
					result = fmt.Sprintf("[HARNESS: Mutation refused because %q is outside this worker's writable scope. Authorized paths: %s. Read-only inspection of adjacent files is allowed, but edit/write must stay within this exact list.]",
						toolCallPath(tc.Function.Arguments), strings.Join(cfg.ScopedFiles, ", "))
				} else if protectedWrite {
					protectedWriteCallsBeforeMutation++
					protectedWriteThisBatch = true
					result = "[HARNESS: Whole-file write refused because this path already exists. Preserve established coverage and use edit with an exact existing substring. The write tool remains available for genuinely new files.]"
				} else if blockedRecon {
					blockedReconCallsBeforeMutation++
					blockedReconThisBatch = true
					result = fmt.Sprintf("[HARNESS: Scoped reconnaissance stopped (used=%d limit=%d, input=%d/%d). "+
						"The pre-mutation discovery budget is exhausted. Use the declared files and planned interface contract; your next call must be edit or write.]",
						reconnaissanceBeforeMutation, cfg.MaxReadsBeforeMutation, totalTokensIn, cfg.MaxInputTokensBeforeMutation)
				} else {
					result, toolErr = exec.execute(tc.Function.Name, tc.Function.Arguments)
					if (tc.Function.Name == "edit" || tc.Function.Name == "write") && toolErr == nil {
						if !mutationObserved {
							firstMutationIteration = iteration + 1
							firstMutationMs = time.Since(start).Milliseconds()
						}
						mutationObserved = true
						failedEditRecoveryAvailable = false
						failedMutationLoops.Reset()
					}
					if tc.Function.Name == "edit" && toolErr != nil && !failedEditRecoveryGranted && editErrorIsRecoverable(toolErr) {
						failedEditRecoveryPath = toolCallPath(tc.Function.Arguments)
						failedEditRecoveryAvailable = failedEditRecoveryPath != ""
						failedEditRecoveryGranted = failedEditRecoveryAvailable
					}
				}
				if finalScopedRead {
					finalScopedReadGranted = true
					finalScopedReadThisBatch = true
					result += "\n\n[HARNESS] This was the single scoped final-look read. The next turn is edit-only; use this exact content to make the planned mutation."
				}
				if recoveryRead && !blockedRecon {
					failedEditRecoveryAvailable = false
					failedEditRecoveryReadThisBatch = true
				}
				if reconnaissance && !blockedRecon {
					reconnaissanceBeforeMutation++
				}
				traceCall := TraceCall{
					Name: tc.Function.Name,
					Args: tc.Function.Arguments,
				}
				if toolErr != nil {
					if strings.Contains(toolErr.Error(), "allowlist") {
						deniedCalls++
					}
					traceCall.Error = toolErr.Error()
					result = "ERROR: " + toolErr.Error()
					if count := failedMutationLoops.Observe(tc.Function.Name, tc.Function.Arguments, toolErr.Error()); count == 2 {
						result += "\n\n[HARNESS] This exact mutation has now failed twice with the same error, even across intervening reads. Change the mutation arguments; do not repeat this call."
					} else if count >= 3 {
						failedMutationStopReason = fmt.Sprintf("identical %s mutation failed %d times across interleaved calls: %s", tc.Function.Name, count, toolErr.Error())
						result += "\n\n[HARNESS] Stopping this no-progress cycle after the third identical failed mutation."
					}
				}

				// File re-read tracker: warn when the same file is read
				// too many times, nudging the model to use what it has.
				if tc.Function.Name == "read" {
					var readArgs struct {
						Path string `json:"path"`
					}
					if json.Unmarshal([]byte(tc.Function.Arguments), &readArgs) == nil && readArgs.Path != "" {
						readPaths[filepath.Clean(readArgs.Path)]++
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
				decision, semanticReason := semanticRecoveryNone, ""
				if !unavailableBeforeMutation && !blockedRecon && !protectedWrite && !outOfScopeMutation {
					decision, semanticReason = semanticLoops.Observe(curKey, preNoteResult)
				}
				if !mutationObserved && decision == semanticRecoveryRehydrate {
					result += "\n\n[HARNESS SEMANTIC RECOVERY: " + semanticReason + ". The exact task contract and latest scoped artifact remain authoritative. Do not repeat discovery. Your next response must mutate an authorized path or respond `BLOCKED: <concrete missing fact>`."
				} else if !mutationObserved && decision == semanticRecoveryStop {
					writeTrace(cfg.TraceWriter, traceEvent)
					return &DirectToolAgentResult{
						Output: choice.Message.Content, TokensIn: totalTokensIn, TokensOut: totalTokensOut,
						Iterations: iteration + 1, Duration: time.Since(start), FinalContextPct: finalPct,
						StopReason: DirectToolStopReasonNoProgress, PeakRequestInput: peakRequestInput, ResumedTurns: resumedTurns, FoldedBytes: foldedBytes,
					}, fmt.Errorf("semantic no-progress detector: %s", semanticReason)
				}
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
			if blockedReconThisBatch {
				blockedReconBatchesBeforeMutation++
			}
			if protectedWriteThisBatch {
				protectedWriteBatchesBeforeMutation++
			}
			if scopeViolationThisBatch {
				scopeViolationBatchesBeforeMutation++
			}
			if (finalScopedReadThisBatch || blockedReconThisBatch || protectedWriteThisBatch || scopeViolationThisBatch) && !mutationOnlyNudgeInjected {
				messages = append(messages, toolChatMsg{
					Role:    "system",
					Content: "[HARNESS] The next turn is mutation-only. Read, search, and shell tools are intentionally unavailable. Use edit for an existing file; write is available only when an authorized path is genuinely new.",
				})
				mutationOnlyNudgeInjected = true
			}
			if failedEditRecoveryAvailable {
				messages = append(messages, toolChatMsg{
					Role:    "system",
					Content: fmt.Sprintf("[HARNESS] Your surgical edit did not match. You have one recovery read of %s, followed by an edit-only turn. Read only that file and then retry with an exact unique substring.", failedEditRecoveryPath),
				})
			}
			if err := persistJournal(iteration+1, false, &traceEvent); err != nil {
				return nil, err
			}
			if failedMutationStopReason != "" {
				return &DirectToolAgentResult{
					Output: choice.Message.Content, TokensIn: totalTokensIn, TokensOut: totalTokensOut,
					Iterations: iteration + 1, Duration: time.Since(start), FinalContextPct: finalPct,
					StopReason: DirectToolStopReasonNoProgress, MutationObserved: mutationObserved,
					PeakRequestInput: peakRequestInput, ResumedTurns: resumedTurns, FoldedBytes: foldedBytes,
				}, fmt.Errorf("failed-mutation no-progress detector: %s", failedMutationStopReason)
			}
			// Count denied reconnaissance by assistant response, not by raw tool
			// call. Models commonly emit paired reads in a single response. A granted
			// final scoped read transitions the next request to mutation-only tools,
			// but is not itself a denial. After the first genuinely denied response,
			// return its tool result so the model gets one complete corrective turn;
			// a second denied response is genuine refusal to make progress.
			if !cfg.AllowReadOnlyCompletion && !mutationObserved && blockedReconBatchesBeforeMutation >= 2 {
				return &DirectToolAgentResult{
						Output: choice.Message.Content, TokensIn: totalTokensIn, TokensOut: totalTokensOut,
						Iterations: iteration + 1, Duration: time.Since(start), FinalContextPct: finalPct,
						StopReason: DirectToolStopReasonNoProgress,
					}, fmt.Errorf("scoped agent refused to mutate after %d reconnaissance calls, %d blocked calls, and %d blocked response batches",
						reconnaissanceBeforeMutation, blockedReconCallsBeforeMutation, blockedReconBatchesBeforeMutation)
			}
			if !cfg.AllowReadOnlyCompletion && !mutationObserved && protectedWriteBatchesBeforeMutation >= 2 {
				return &DirectToolAgentResult{
						Output: choice.Message.Content, TokensIn: totalTokensIn, TokensOut: totalTokensOut,
						Iterations: iteration + 1, Duration: time.Since(start), FinalContextPct: finalPct,
						StopReason: DirectToolStopReasonNoProgress,
					}, fmt.Errorf("scoped agent refused surgical editing after %d protected whole-file writes in %d response batches",
						protectedWriteCallsBeforeMutation, protectedWriteBatchesBeforeMutation)
			}
			if !cfg.AllowReadOnlyCompletion && !mutationObserved && scopeViolationBatchesBeforeMutation >= 2 {
				return &DirectToolAgentResult{
						Output: choice.Message.Content, TokensIn: totalTokensIn, TokensOut: totalTokensOut,
						Iterations: iteration + 1, Duration: time.Since(start), FinalContextPct: finalPct,
						StopReason: DirectToolStopReasonNoProgress,
					}, fmt.Errorf("scoped agent attempted %d out-of-scope mutations in %d response batches without an authorized mutation",
						scopeViolationCallsBeforeMutation, scopeViolationBatchesBeforeMutation)
			}

			if cfg.MaxToolCalls > 0 && totalToolCalls >= cfg.MaxToolCalls {
				result := &DirectToolAgentResult{
					Output: choice.Message.Content, TokensIn: totalTokensIn, TokensOut: totalTokensOut,
					Iterations: iteration + 1, Duration: time.Since(start), FinalContextPct: finalPct,
					StopReason: DirectToolStopReasonToolBudget, MutationObserved: mutationObserved,
				}
				if mutationObserved || cfg.AllowReadOnlyCompletion {
					return result, nil
				}
				return result, fmt.Errorf("tool-call budget reached without a mutation checkpoint: %d/%d", totalToolCalls, cfg.MaxToolCalls)
			}
			// If this response was the first blocked reconnaissance batch, return
			// its tool results to the model before enforcing the input ceiling. The
			// next response must mutate; a second blocked batch fails above.
			if !cfg.AllowReadOnlyCompletion && !mutationObserved && !finalScopedReadThisBatch && !blockedReconThisBatch && !protectedWriteThisBatch && !scopeViolationThisBatch && !failedEditRecoveryAvailable && !failedEditRecoveryReadThisBatch && cfg.MaxInputTokensBeforeMutation > 0 && totalTokensIn >= cfg.MaxInputTokensBeforeMutation {
				return &DirectToolAgentResult{
					Output: choice.Message.Content, TokensIn: totalTokensIn, TokensOut: totalTokensOut,
					Iterations: iteration + 1, Duration: time.Since(start), FinalContextPct: finalPct,
					StopReason: DirectToolStopReasonNoProgress,
				}, fmt.Errorf("pre-mutation input budget reached without a mutation checkpoint: %d/%d", totalTokensIn, cfg.MaxInputTokensBeforeMutation)
			}

			if budgetReached {
				result := &DirectToolAgentResult{
					Output: choice.Message.Content, TokensIn: totalTokensIn, TokensOut: totalTokensOut,
					Iterations: iteration + 1, Duration: time.Since(start), FinalContextPct: finalPct,
					StopReason: DirectToolStopReasonTokenBudget, MutationObserved: mutationObserved,
				}
				if mutationObserved || cfg.AllowReadOnlyCompletion {
					return result, nil
				}
				return result, fmt.Errorf("cumulative input token budget reached without a mutation checkpoint: %d/%d", totalTokensIn, cfg.MaxCumulativeInputTokens)
			}
			if contextReached {
				slog.Warn("direct tool agent: context stop threshold reached after checkpointing response",
					"iteration", iteration, "pct", currentPct, "limit", cfg.ContextLimit,
					"prompt_tokens", resp.Usage.PromptTokens)
				return &DirectToolAgentResult{
					Output: choice.Message.Content, TokensIn: totalTokensIn, TokensOut: totalTokensOut,
					Iterations: iteration + 1, Duration: time.Since(start), FinalContextPct: finalPct,
					StopReason: DirectToolStopReasonContextLimit, MutationObserved: mutationObserved,
				}, nil
			}

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

		if budgetReached {
			writeTrace(cfg.TraceWriter, traceEvent)
			result := &DirectToolAgentResult{
				Output: choice.Message.Content, TokensIn: totalTokensIn, TokensOut: totalTokensOut,
				Iterations: iteration + 1, Duration: time.Since(start), FinalContextPct: finalPct,
				StopReason: DirectToolStopReasonTokenBudget, MutationObserved: mutationObserved,
			}
			if mutationObserved || cfg.AllowReadOnlyCompletion {
				return result, nil
			}
			return result, fmt.Errorf("cumulative input token budget reached without a mutation checkpoint: %d/%d", totalTokensIn, cfg.MaxCumulativeInputTokens)
		}
		if contextReached {
			writeTrace(cfg.TraceWriter, traceEvent)
			return &DirectToolAgentResult{
				Output: choice.Message.Content, TokensIn: totalTokensIn, TokensOut: totalTokensOut,
				Iterations: iteration + 1, Duration: time.Since(start), FinalContextPct: finalPct,
				StopReason: DirectToolStopReasonContextLimit, MutationObserved: mutationObserved,
			}, nil
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
