package ctxmon

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// LlamaSlot represents a single slot from llama-server's GET /slots endpoint.
// Each slot tracks one concurrent inference context with its own KV cache.
type LlamaSlot struct {
	ID       int    `json:"id"`
	NCtx     int    `json:"n_ctx"`     // total context window size for this slot
	NPast    int    `json:"n_past"`    // tokens currently in the KV cache
	NPredict int    `json:"n_predict"` // max tokens to predict (-1 = unlimited)
	State    int    `json:"state"`     // 0=idle, 1=processing
	IsActive bool   `json:"is_processing"`
	TaskID   int    `json:"id_task"`
	DynModel string `json:"dynatemp_model,omitempty"`

	// Token counters for this slot's lifetime.
	NPromptTokensProcessed     int `json:"n_decoded,omitempty"`        // prompt tokens evaluated
	NTokensPredicted           int `json:"tokens_predicted,omitempty"` // generation tokens produced
	PromptTokensProcessedTotal int `json:"prompt_tokens_processed,omitempty"`
}

// LlamaSlotState constants.
const (
	LlamaSlotIdle       = 0
	LlamaSlotProcessing = 1
)

// LlamaServerMetrics holds parsed llama-server Prometheus metrics relevant
// to context and load monitoring, fetched from GET /metrics.
type LlamaServerMetrics struct {
	KVCacheUsageRatio    float64 // llamacpp:kv_cache_usage_ratio
	KVCacheTokens        int64   // llamacpp:kv_cache_tokens_count
	RequestsProcessing   int     // llamacpp:requests_processing
	RequestsPending      int     // llamacpp:requests_pending
	TokensPredictedTotal int64   // llamacpp:tokens_predicted_total
	TokensEvaluatedTotal int64   // llamacpp:prompt_tokens_total
	SlotsIdle            int     // llamacpp:slots_idle
	SlotsProcessing      int     // llamacpp:slots_processing
}

// ReadLlamaServerSlots fetches and parses llama-server's /slots endpoint.
// The endpoint should be the full URL, e.g. "http://localhost:8081/slots".
func ReadLlamaServerSlots(endpoint string) ([]LlamaSlot, error) {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(endpoint)
	if err != nil {
		return nil, fmt.Errorf("fetch llama-server slots: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch llama-server slots: status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read llama-server slots body: %w", err)
	}

	return ParseLlamaServerSlots(body)
}

// ParseLlamaServerSlots parses the JSON response from llama-server's /slots
// endpoint into a slice of LlamaSlot structs.
func ParseLlamaServerSlots(data []byte) ([]LlamaSlot, error) {
	var slots []LlamaSlot
	if err := json.Unmarshal(data, &slots); err != nil {
		return nil, fmt.Errorf("parse llama-server slots: %w", err)
	}
	return slots, nil
}

// LlamaServerSlotsUsage computes aggregate context usage from a set of
// llama-server slots. It returns the total n_ctx capacity, total n_past
// (tokens currently cached), and counts of active/idle slots.
//
// This provides a server-wide view of context pressure analogous to what
// vLLM metrics provide, but with per-slot granularity.
func LlamaServerSlotsUsage(slots []LlamaSlot) *LlamaServerMetrics {
	m := &LlamaServerMetrics{}
	var totalCtx, totalPast int64
	for _, s := range slots {
		totalCtx += int64(s.NCtx)
		totalPast += int64(s.NPast)
		if s.State == LlamaSlotProcessing || s.IsActive {
			m.SlotsProcessing++
		} else {
			m.SlotsIdle++
		}
	}

	m.KVCacheTokens = totalPast
	if totalCtx > 0 {
		m.KVCacheUsageRatio = float64(totalPast) / float64(totalCtx)
	}

	return m
}

// ParseLlamaServerMetrics parses llama-server Prometheus text-format metrics
// into a LlamaServerMetrics struct. It extracts kv_cache_usage_ratio,
// kv_cache_tokens_count, tokens_predicted_total, prompt_tokens_total,
// requests_processing, requests_pending, slots_idle, and slots_processing.
func ParseLlamaServerMetrics(text string) (*LlamaServerMetrics, error) {
	m := &LlamaServerMetrics{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Parse Prometheus text format: "metric_name{labels} value"
		// or "metric_name value" (no labels).
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}

		name := parts[0]
		// Strip labels: "llamacpp:kv_cache_usage_ratio{}" → "llamacpp:kv_cache_usage_ratio"
		if idx := strings.Index(name, "{"); idx >= 0 {
			name = name[:idx]
		}

		val, err := strconv.ParseFloat(parts[len(parts)-1], 64)
		if err != nil {
			continue
		}

		switch name {
		case "llamacpp:kv_cache_usage_ratio":
			m.KVCacheUsageRatio = val
		case "llamacpp:kv_cache_tokens_count":
			m.KVCacheTokens = int64(val)
		case "llamacpp:tokens_predicted_total":
			m.TokensPredictedTotal = int64(val)
		case "llamacpp:prompt_tokens_total":
			m.TokensEvaluatedTotal = int64(val)
		case "llamacpp:requests_processing":
			m.RequestsProcessing = int(val)
		case "llamacpp:requests_pending":
			m.RequestsPending = int(val)
		case "llamacpp:slots_idle":
			m.SlotsIdle = int(val)
		case "llamacpp:slots_processing":
			m.SlotsProcessing = int(val)
		}
	}
	return m, nil
}

// LlamaServerSlotToUsage converts a single llama-server slot's context state
// into a ctxmon Usage value. This is useful when mapping a specific slot to
// a specific agent for per-agent context monitoring.
//
// contextWindowSize overrides the slot's n_ctx if positive; pass 0 to use
// the slot's own n_ctx value.
func LlamaServerSlotToUsage(slot LlamaSlot, contextWindowSize int) *Usage {
	ctxSize := slot.NCtx
	if contextWindowSize > 0 {
		ctxSize = contextWindowSize
	}
	if ctxSize <= 0 {
		ctxSize = DefaultOpenCodeContextWindow
	}

	usedPct := 0
	if slot.NPast > 0 && ctxSize > 0 {
		usedPct = slot.NPast * 100 / ctxSize
		if usedPct > 100 {
			usedPct = 100
		}
	}

	return &Usage{
		TotalInputTokens:  slot.NPast,
		TotalOutputTokens: 0, // slot doesn't separate input/output in n_past
		ContextWindowSize: ctxSize,
		UsedPercent:       usedPct,
		RemainingPercent:  100 - usedPct,
		TotalCostUSD:      0, // local model, no API cost
		LastUpdated:       time.Now(),
	}
}

// ReadLlamaServerSlotsUsage fetches all slots from the llama-server /slots
// endpoint, finds the most-utilized active slot, and returns its context
// usage as a ctxmon Usage value. This is the primary reader for agents
// backed by llama-server.
//
// If contextWindowSize is 0, the slot's own n_ctx is used.
// Returns nil, nil if no active slots are found.
func ReadLlamaServerSlotsUsage(endpoint string, contextWindowSize int) (*Usage, error) {
	slots, err := ReadLlamaServerSlots(endpoint)
	if err != nil {
		return nil, err
	}

	if len(slots) == 0 {
		return nil, nil
	}

	// Find the active slot with the most context usage (n_past).
	// If no active slot, fall back to the slot with highest n_past overall.
	var best *LlamaSlot
	var bestActive *LlamaSlot
	for i := range slots {
		s := &slots[i]
		if best == nil || s.NPast > best.NPast {
			best = s
		}
		if (s.State == LlamaSlotProcessing || s.IsActive) &&
			(bestActive == nil || s.NPast > bestActive.NPast) {
			bestActive = s
		}
	}

	target := bestActive
	if target == nil {
		target = best
	}
	if target == nil {
		return nil, nil
	}

	return LlamaServerSlotToUsage(*target, contextWindowSize), nil
}
