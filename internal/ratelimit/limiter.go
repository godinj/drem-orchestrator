package ratelimit

import (
	"sync"
	"time"
)

// Default rate-limit parameters for agent dispatch.
const (
	// DefaultMaxDispatches is the maximum number of agent dispatches allowed
	// within a single sliding window.
	DefaultMaxDispatches = 3

	// DefaultDispatchWindow is the sliding window duration over which
	// dispatches are counted.
	DefaultDispatchWindow = 60 * time.Second

	// DefaultJitterMax is the upper bound for random jitter added to
	// retry-after delays, preventing thundering-herd on window reset.
	DefaultJitterMax = 5 * time.Second
)

// Limiter implements a sliding window rate limiter for agent dispatch.
// It tracks dispatch timestamps and blocks new dispatches when the count
// within the sliding window reaches maxDispatches. Thread-safe via mutex.
//
// A zero-value Limiter blocks all dispatches; use New to create a usable one.
type Limiter struct {
	mu            sync.Mutex
	maxDispatches int
	window        time.Duration
	clock         func() time.Time
	jitter        func(max time.Duration) time.Duration
	recent        []time.Time
}

// Option configures a Limiter.
type Option func(*Limiter)

// WithClock injects a custom time source, primarily for testing.
func WithClock(fn func() time.Time) Option {
	return func(l *Limiter) {
		l.clock = fn
	}
}

// WithJitter injects a custom jitter function that returns a random duration
// up to the given maximum. Applied to the retry delay when dispatch is blocked.
func WithJitter(fn func(max time.Duration) time.Duration) Option {
	return func(l *Limiter) {
		l.jitter = fn
	}
}

// New creates a Limiter that allows at most maxDispatches within the given
// window duration. Panics if maxDispatches or window is zero.
func New(maxDispatches int, window time.Duration, opts ...Option) *Limiter {
	if maxDispatches <= 0 {
		panic("ratelimit: MaxDispatches must be positive")
	}
	if window <= 0 {
		panic("ratelimit: Window must be positive")
	}
	l := &Limiter{
		maxDispatches: maxDispatches,
		window:        window,
		clock:         time.Now,
		recent:        make([]time.Time, 0, maxDispatches),
	}
	for _, opt := range opts {
		opt(l)
	}
	return l
}

// Allow checks whether a dispatch is permitted. If allowed, it records the
// dispatch timestamp and returns (true, 0). If blocked, it returns
// (false, retryAfter) where retryAfter is the time until the oldest entry
// expires from the window, plus any configured jitter.
func (l *Limiter) Allow() (ok bool, retryAfter time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Zero-value Limiter blocks everything.
	if l.maxDispatches == 0 {
		return false, 0
	}

	now := l.clock()
	l.prune(now)

	if len(l.recent) < l.maxDispatches {
		l.recent = append(l.recent, now)
		return true, 0
	}

	// Window is full; compute delay until the oldest entry expires.
	oldest := l.recent[0]
	baseDelay := l.window - now.Sub(oldest)
	if baseDelay < 0 {
		baseDelay = 0
	}

	delay := baseDelay
	if l.jitter != nil {
		delay += l.jitter(baseDelay)
	}

	return false, delay
}

// Record notes that a dispatch occurred, adding the current timestamp to the
// sliding window. Call this after a successful spawn, not before.
func (l *Limiter) Record() {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.clock == nil {
		return
	}
	l.recent = append(l.recent, l.clock())
}

// prune removes entries that have slid outside the window. Must be called
// with l.mu held.
func (l *Limiter) prune(now time.Time) {
	cutoff := now.Add(-l.window)
	i := 0
	for i < len(l.recent) && l.recent[i].Before(cutoff) {
		i++
	}
	if i > 0 {
		copy(l.recent, l.recent[i:])
		l.recent = l.recent[:len(l.recent)-i]
	}
}
