package agent

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"time"
)

const naturalStopCorrectionPrompt = "[HARNESS] The prior response stopped without a mutation after reaching the pre-mutation ceiling. This is the single reserved corrective turn. Only mutation tools are available: edit an existing authorized file or write a genuinely new authorized file now. Do not continue reconnaissance or return another prose-only response."

func shouldCorrectUnmutatedNaturalStop(cfg DirectToolAgentConfig, mutationObserved bool, totalTokens int, content string, messages []toolChatMsg) bool {
	return !cfg.AllowReadOnlyCompletion && !mutationObserved && !hasNaturalStopCorrection(messages) &&
		cfg.MaxInputTokensBeforeMutation > 0 && totalTokens >= cfg.MaxInputTokensBeforeMutation &&
		!strings.HasPrefix(strings.TrimSpace(content), "BLOCKED:")
}

func hasNaturalStopCorrection(messages []toolChatMsg) bool {
	for _, message := range messages {
		if message.Role == "system" && message.Content == naturalStopCorrectionPrompt {
			return true
		}
	}
	return false
}

func unmutatedNaturalStopReason(totalTokens, limit int, correctionRequested bool) string {
	if correctionRequested {
		return "agent stopped without mutation after the reserved mutation-only corrective request"
	}
	return fmt.Sprintf("pre-mutation input budget reached without a mutation checkpoint: %d/%d", totalTokens, limit)
}

func naturalStopResult(output string, tokensIn, tokensOut, iterations int, duration time.Duration,
	finalContextPct int, mutationObserved bool, peakRequestInput, resumedTurns, foldedBytes int) *DirectToolAgentResult {
	return &DirectToolAgentResult{
		Output: output, TokensIn: tokensIn, TokensOut: tokensOut, Iterations: iterations,
		Duration: duration, FinalContextPct: finalContextPct, MutationObserved: mutationObserved,
		PeakRequestInput: peakRequestInput, ResumedTurns: resumedTurns, FoldedBytes: foldedBytes,
	}
}

type failedMutationLoopDetector struct {
	failures        map[string]int
	failuresByError map[string]int
}

func (d *failedMutationLoopDetector) Observe(toolName, arguments, failure string) int {
	if toolName != "edit" && toolName != "write" || failure == "" {
		return 0
	}
	if d.failures == nil {
		d.failures = make(map[string]int)
	}
	if d.failuresByError == nil {
		d.failuresByError = make(map[string]int)
	}
	key := toolName + "\x00" + arguments + "\x00" + failure
	errorKey := toolName + "\x00" + failure
	d.failures[key]++
	d.failuresByError[errorKey]++
	return max(d.failures[key], d.failuresByError[errorKey])
}

func (d *failedMutationLoopDetector) Reset() {
	clear(d.failures)
	clear(d.failuresByError)
}

// Restore reconstructs no-progress state from the durable conversation. This
// prevents a checkpoint continuation from buying three more copies of an
// already-repeated failed mutation merely because the worker process changed.
func (d *failedMutationLoopDetector) Restore(messages []toolChatMsg) {
	for _, message := range messages {
		if message.Role != "tool" || message.Name != "edit" && message.Name != "write" {
			continue
		}
		if strings.HasPrefix(message.Content, "edited ") || strings.HasPrefix(message.Content, "wrote ") {
			d.Reset()
			continue
		}
		if !strings.HasPrefix(message.Content, "ERROR: ") {
			continue
		}
		failure := strings.TrimPrefix(message.Content, "ERROR: ")
		if before, _, ok := strings.Cut(failure, "\n"); ok {
			failure = before
		}
		if d.failuresByError == nil {
			d.failuresByError = make(map[string]int)
		}
		d.failuresByError[message.Name+"\x00"+failure]++
	}
}

type semanticRecoveryDecision int

const (
	semanticRecoveryNone semanticRecoveryDecision = iota
	semanticRecoveryRehydrate
	semanticRecoveryStop
)

type semanticObservation struct {
	action string
	result [32]byte
}

// semanticLoopDetector catches more than byte-identical adjacent calls. It
// recognizes repeated observations reached through different reads and ABAB
// action cycles, grants exactly one artifact-rehydration turn, then requires a
// mutation or typed blocker instead of paying for a blind retry.
type semanticLoopDetector struct {
	observations []semanticObservation
	recoveries   int
}

func (d *semanticLoopDetector) Observe(action, result string) (semanticRecoveryDecision, string) {
	obs := semanticObservation{action: action, result: sha256.Sum256([]byte(result))}
	d.observations = append(d.observations, obs)
	if len(d.observations) > 6 {
		d.observations = d.observations[len(d.observations)-6:]
	}
	reason := ""
	if len(d.observations) >= 3 {
		last := d.observations[len(d.observations)-3:]
		if last[0].result == last[1].result && last[1].result == last[2].result {
			reason = "three tool calls produced the same observation"
		}
	}
	if reason == "" && len(d.observations) >= 4 {
		last := d.observations[len(d.observations)-4:]
		if last[0] == last[2] && last[1] == last[3] {
			reason = "tool actions entered an alternating ABAB cycle"
		}
	}
	if reason == "" {
		return semanticRecoveryNone, ""
	}
	if d.recoveries == 0 {
		d.recoveries++
		return semanticRecoveryRehydrate, reason
	}
	return semanticRecoveryStop, fmt.Sprintf("%s after the single semantic recovery", reason)
}
