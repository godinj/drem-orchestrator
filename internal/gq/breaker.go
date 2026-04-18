package gq

import (
	"sync"
	"time"
)

// BreakerState represents the circuit breaker state.
type BreakerState int

const (
	BreakerClosed BreakerState = iota
	BreakerOpen
	BreakerHalfOpen
)

// String returns the lowercase name of the breaker state.
func (s BreakerState) String() string {
	switch s {
	case BreakerClosed:
		return "closed"
	case BreakerOpen:
		return "open"
	case BreakerHalfOpen:
		return "half_open"
	default:
		return "unknown"
	}
}

// Breaker implements an upstream circuit breaker with exponential backoff.
// Thread-safe — all public methods lock internally.
type Breaker struct {
	mu sync.Mutex

	state               BreakerState
	consecutiveFailures int
	lastFailureAt       time.Time
	openedAt            time.Time
	currentCooldown     time.Duration

	cfg   BreakerConfig
	clock func() time.Time
}

// NewBreaker creates a circuit breaker with the given config.
func NewBreaker(cfg BreakerConfig) *Breaker {
	return &Breaker{
		state:           BreakerClosed,
		currentCooldown: cfg.OpenCooldown.Duration,
		cfg:             cfg,
		clock:           time.Now,
	}
}

// Allow returns true if a request should be allowed through.
// In closed state: always allows.
// In open state: allows only after cooldown expires (transitions to half-open).
// In half-open state: allows one probe request.
func (b *Breaker) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case BreakerClosed:
		return true
	case BreakerOpen:
		if b.clock().Sub(b.openedAt) >= b.currentCooldown {
			b.state = BreakerHalfOpen
			return true
		}
		return false
	case BreakerHalfOpen:
		// Only one probe allowed — block until we get a result.
		return false
	default:
		return true
	}
}

// RecordSuccess records a successful upstream response. Resets the breaker
// to closed state.
func (b *Breaker) RecordSuccess() {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.consecutiveFailures = 0
	b.state = BreakerClosed
	b.currentCooldown = b.cfg.OpenCooldown.Duration
}

// RecordFailure records a failed upstream response. Opens the breaker
// after cfg.OpenAfterFailures consecutive failures.
func (b *Breaker) RecordFailure() {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.consecutiveFailures++
	b.lastFailureAt = b.clock()

	if b.state == BreakerHalfOpen {
		// Probe failed — reopen with doubled cooldown.
		b.state = BreakerOpen
		b.openedAt = b.clock()
		b.currentCooldown *= 2
		if b.currentCooldown > b.cfg.MaxCooldown.Duration {
			b.currentCooldown = b.cfg.MaxCooldown.Duration
		}
		return
	}

	if b.consecutiveFailures >= b.cfg.OpenAfterFailures {
		b.state = BreakerOpen
		b.openedAt = b.clock()
	}
}

// State returns the current breaker state.
func (b *Breaker) State() BreakerState {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

// BreakerStats holds breaker state for the stats endpoint.
type BreakerStats struct {
	State               string        `json:"breaker_state"`
	ConsecutiveFailures int           `json:"consecutive_failures"`
	LastFailureAt       *time.Time    `json:"last_failure_ts"`
	CooldownRemaining   time.Duration `json:"cooldown_remaining_s"`
}

// Stats returns the current breaker statistics.
func (b *Breaker) Stats() BreakerStats {
	b.mu.Lock()
	defer b.mu.Unlock()

	stats := BreakerStats{
		State:               b.state.String(),
		ConsecutiveFailures: b.consecutiveFailures,
	}
	if !b.lastFailureAt.IsZero() {
		t := b.lastFailureAt
		stats.LastFailureAt = &t
	}
	if b.state == BreakerOpen {
		elapsed := b.clock().Sub(b.openedAt)
		remaining := b.currentCooldown - elapsed
		if remaining < 0 {
			remaining = 0
		}
		stats.CooldownRemaining = remaining
	}
	return stats
}
