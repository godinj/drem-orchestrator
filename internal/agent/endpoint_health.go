// endpoint_health.go implements a circuit breaker for the LLM endpoint.
// When the endpoint is unreachable, the circuit opens and dispatch is paused
// with exponential backoff. On recovery, the circuit closes and dispatch
// resumes immediately. All methods are safe for concurrent use.
package agent

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// EndpointHealthChecker implements a circuit breaker for LLM endpoint health.
//
//	state 0 = closed (healthy, dispatch allowed)
//	state 1 = open   (unhealthy, dispatch blocked until cooldown expires)
type EndpointHealthChecker struct {
	probeURL     string
	mu           sync.Mutex
	state        int // 0=closed, 1=open
	lastProbe    time.Time
	cooldown     time.Duration
	baseCooldown time.Duration
	maxCooldown  time.Duration
	consecutives int
	httpClient   *http.Client
	logger       *slog.Logger
}

// NewEndpointHealthChecker creates a health checker that probes /v1/models
// derived from the given chat completions endpoint.
func NewEndpointHealthChecker(endpoint string, logger *slog.Logger) *EndpointHealthChecker {
	probeURL := strings.TrimSuffix(endpoint, "/v1/chat/completions") + "/v1/models"
	return &EndpointHealthChecker{
		probeURL:     probeURL,
		baseCooldown: 10 * time.Second,
		maxCooldown:  5 * time.Minute,
		cooldown:     10 * time.Second,
		httpClient:   &http.Client{Timeout: 5 * time.Second},
		logger:       logger,
	}
}

// IsHealthy returns whether the LLM endpoint is reachable. When the circuit
// is open, it returns false without probing until the cooldown expires, then
// performs a half-open probe to check for recovery.
func (h *EndpointHealthChecker) IsHealthy() bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	now := time.Now()

	// Circuit closed — trust RecordSuccess; no probe needed.
	if h.state == 0 {
		return true
	}

	// Circuit open — check if cooldown expired.
	if now.Before(h.lastProbe.Add(h.cooldown)) {
		return false // still in cooldown
	}

	// Half-open: cooldown expired, attempt recovery probe.
	if h.probe(now) {
		h.state = 0
		h.consecutives = 0
		h.cooldown = h.baseCooldown
		h.logger.Info("endpoint health: circuit CLOSED (recovered)",
			"probe_url", h.probeURL)
		return true
	}

	// Still down — escalate cooldown.
	h.escalateCooldown()
	h.logger.Warn("endpoint health: still unhealthy after probe",
		"probe_url", h.probeURL, "next_cooldown", h.cooldown)
	return false
}

// RecordSuccess signals a successful LLM API call. Resets the circuit to
// closed if it was open.
func (h *EndpointHealthChecker) RecordSuccess() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.state == 1 {
		h.logger.Info("endpoint health: circuit CLOSED via RecordSuccess",
			"probe_url", h.probeURL)
	}
	h.state = 0
	h.consecutives = 0
	h.cooldown = h.baseCooldown
}

// RecordFailure signals a failed LLM API call. Opens the circuit and applies
// exponential backoff to the cooldown period.
func (h *EndpointHealthChecker) RecordFailure() {
	h.mu.Lock()
	defer h.mu.Unlock()
	wasOpen := h.state == 1
	h.state = 1
	h.lastProbe = time.Now()
	h.escalateCooldown()
	if !wasOpen {
		h.logger.Warn("endpoint health: circuit OPENED",
			"probe_url", h.probeURL, "cooldown", h.cooldown,
			"consecutive_failures", h.consecutives)
	}
}

// Status returns a human-readable status string for logging/TUI.
func (h *EndpointHealthChecker) Status() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.state == 0 {
		return "healthy"
	}
	remaining := time.Until(h.lastProbe.Add(h.cooldown))
	if remaining < 0 {
		remaining = 0
	}
	return fmt.Sprintf("unhealthy (retry in %s, %d consecutive failures)",
		remaining.Round(time.Second), h.consecutives)
}

// probe performs an HTTP GET to the probe URL. Caller must hold mu.
func (h *EndpointHealthChecker) probe(now time.Time) bool {
	h.lastProbe = now
	resp, err := h.httpClient.Get(h.probeURL)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

// escalateCooldown applies exponential backoff. Caller must hold mu.
func (h *EndpointHealthChecker) escalateCooldown() {
	h.consecutives++
	cd := h.baseCooldown
	for i := 1; i < h.consecutives; i++ {
		cd *= 2
		if cd > h.maxCooldown {
			cd = h.maxCooldown
			break
		}
	}
	h.cooldown = cd
}
