package gq

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

const maxBodySize = 2 * 1024 * 1024 // 2 MB

// Handler is the HTTP ingress for the proxy. It classifies incoming requests,
// applies rate limiting and circuit breaker checks, enqueues items, and blocks
// until the dispatcher completes or the caller disconnects.
type Handler struct {
	sched   *Scheduler
	breaker *Breaker
	stats   *Stats
	cfg     *Config
	log     *slog.Logger

	// Per-caller rate limiters.
	rlMu       sync.Mutex
	rateLimits map[string]*rateLimiter
}

// NewHandler creates the proxy HTTP handler.
func NewHandler(sched *Scheduler, breaker *Breaker, stats *Stats, cfg *Config, log *slog.Logger) *Handler {
	return &Handler{
		sched:      sched,
		breaker:    breaker,
		stats:      stats,
		cfg:        cfg,
		log:        log,
		rateLimits: make(map[string]*rateLimiter),
	}
}

// ServeHTTP handles POST /v1/chat/completions requests and proxies
// GET /v1/models to the upstream for health-check compatibility.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Passthrough GET /v1/models to upstream so health checkers work.
	if r.Method == http.MethodGet && r.URL.Path == "/v1/models" {
		h.proxyModels(w, r)
		return
	}

	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}
	if r.URL.Path != "/v1/chat/completions" {
		writeError(w, http.StatusNotFound, "not_found", "use /v1/chat/completions")
		return
	}

	// Extract caller identity and priority.
	callerID := r.Header.Get("X-GQ-Caller")
	if callerID == "" {
		callerID = "unknown"
	}
	priorityStr := r.Header.Get("X-GQ-Priority")
	var priority Priority
	if priorityStr != "" {
		priority = ParsePriority(priorityStr)
	} else {
		priority = h.cfg.DefaultPriorityFor(callerID)
	}

	// Rate limit check.
	if !h.allowRate(callerID, priority) {
		writeError(w, http.StatusTooManyRequests, "rate_limited",
			fmt.Sprintf("caller %s exceeds rate limit", callerID))
		return
	}

	// Circuit breaker check.
	if !h.breaker.Allow() {
		bs := h.breaker.Stats()
		w.Header().Set("Retry-After", fmt.Sprintf("%d", int(bs.CooldownRemaining.Seconds())+1))
		writeError(w, http.StatusServiceUnavailable, "upstream_unavailable",
			"circuit breaker open")
		return
	}

	// Read body.
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodySize+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "read_error", err.Error())
		return
	}
	if len(body) > maxBodySize {
		writeError(w, http.StatusRequestEntityTooLarge, "body_too_large",
			fmt.Sprintf("max %d bytes", maxBodySize))
		return
	}

	// Parse stream flag from body.
	stream := parseStreamFlag(body)

	// Build queue item.
	deadline := time.Now().Add(h.cfg.LaneTimeout(priority))
	ctx, cancel := context.WithCancel(r.Context())
	item := NewQueueItem(callerID, priority, body, stream, deadline, ctx, cancel, w)

	h.log.Debug("enqueue",
		"id", item.ID[:8],
		"caller", callerID,
		"priority", priority,
		"est_tokens", item.EstPromptTokens,
		"stream", stream,
	)

	// Enqueue.
	if err := h.sched.Enqueue(item); err != nil {
		cancel()
		// Estimate wait for Retry-After.
		w.Header().Set("Retry-After", "5")
		writeError(w, http.StatusServiceUnavailable, "queue_full", err.Error())
		return
	}

	h.stats.RecordEnqueue(priority)

	// Block until dispatch completes or caller disconnects.
	select {
	case <-item.Done:
		// Dispatcher handled the response (wrote to w) or set an error.
		if item.Err != nil && item.StatusCode == 504 {
			// Queue timeout — dispatcher already closed Done.
			writeError(w, http.StatusGatewayTimeout, "queue_timeout", item.Err.Error())
		}
		// Otherwise the dispatcher already wrote the response.
	case <-r.Context().Done():
		// Caller disconnected. Cancel the item's context so the scheduler
		// or dispatcher can clean it up.
		cancel()
		h.stats.RecordCancel()
	}
}

// proxyModels forwards GET /v1/models to the upstream endpoint so that
// health checkers (e.g. the orchestrator's EndpointHealthChecker) get a
// valid response through gq.
func (h *Handler) proxyModels(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	upstream := h.cfg.Upstream + "/v1/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, upstream, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "proxy_error", err.Error())
		return
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// --- rate limiter ---

type rateLimiter struct {
	tokens    float64
	maxTokens float64
	lastCheck time.Time
}

func (h *Handler) allowRate(callerID string, p Priority) bool {
	rps := h.cfg.RateLimit.TempRPS
	if p <= Normal {
		rps = h.cfg.RateLimit.OrchRPS
	}
	if rps <= 0 {
		return true
	}

	h.rlMu.Lock()
	defer h.rlMu.Unlock()

	now := time.Now()
	rl, ok := h.rateLimits[callerID]
	if !ok {
		rl = &rateLimiter{
			tokens:    float64(rps),
			maxTokens: float64(rps),
			lastCheck: now,
		}
		h.rateLimits[callerID] = rl
	}

	// Refill tokens.
	elapsed := now.Sub(rl.lastCheck).Seconds()
	rl.tokens += elapsed * float64(rps)
	if rl.tokens > rl.maxTokens {
		rl.tokens = rl.maxTokens
	}
	rl.lastCheck = now

	// Prune stale limiters occasionally (every 100th call on this caller).
	if len(h.rateLimits) > 100 {
		h.pruneStale(now)
	}

	if rl.tokens < 1 {
		return false
	}
	rl.tokens--
	return true
}

func (h *Handler) pruneStale(now time.Time) {
	for k, rl := range h.rateLimits {
		if now.Sub(rl.lastCheck) > 5*time.Minute {
			delete(h.rateLimits, k)
		}
	}
}

// --- helpers ---

func parseStreamFlag(body []byte) bool {
	var partial struct {
		Stream bool `json:"stream"`
	}
	_ = json.Unmarshal(body, &partial)
	return partial.Stream
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"type":    code,
			"message": msg,
		},
	})
}
