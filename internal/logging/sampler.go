// Package logging provides a per-site log sampler that wraps
// error-rate-prone loop logs so overflow does not produce gigabyte
// drem.log tails. Bug E W4.1 (2026-04-21): a confused TUI client fired
// ~495 req/s at /tasks; each request produced one "orchhttp request
// status=500 duration_ms=~7000" log line, and drem.log grew to 1.37 GiB
// dominated by ~1406 repetitions of that single shape per 200 KB tail.
// The orch's job is to survive that client without becoming its own
// log DDoS attacker.
//
// Two admission modes are supported:
//
//   - EveryN(n) — admit once per n attempts per site. Use when the
//     rate ceiling is "per attempt" rather than "per wall-clock".
//   - EveryD(d) — admit at most once per duration d per site. Use
//     when wall-clock bursts are the problem (retry storms).
//
// Both compose naturally: call with one Option and forget. The sampler
// keys admissions per site-tag so a hot /tasks site never starves a
// colder /workers site. Counters are atomic; state lives in a sync.Map
// so adding a new site does not require holding a mutex on the hot
// path.
package logging

import (
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Sampler admits at most a configured fraction of Allow calls per site.
// It is zero-configuration via NewSampler and safe for concurrent use.
// The chosen admission policy (EveryN vs EveryD) is set at construction
// time and applied uniformly to every site; sites are discriminated by
// their string tag and track state independently.
type Sampler struct {
	policy policy
	clock  func() time.Time
	sites  sync.Map // map[string]*siteState
}

// siteState is the per-site admission counter. A single *siteState is
// allocated on first Allow for a site and reused thereafter. Both
// counter and lastEmit are atomics so the hot path stays lock-free.
// emitted starts at 0 and flips to 1 on the first admission; it lets
// windowPolicy distinguish "never fired" from "fired at clock-time 0",
// which matters in tests that inject a zero-origin clock.
type siteState struct {
	count    atomic.Int64
	emitted  atomic.Int32 // 0 == never fired, 1 == has fired at least once
	lastEmit atomic.Int64 // unix-nanos of most recent admitted call
}

// policy is the strategy shared by EveryN and EveryD.  Implementations
// decide, given a per-site state and a clock, whether the current call
// should be admitted.  Keeping this an interface means the Sampler does
// not have to branch on policy type on every Allow.
type policy interface {
	admit(s *siteState, now time.Time) bool
}

// Option configures a Sampler.  Callers pass exactly one policy option
// (EveryN or EveryD) plus any number of modifier options (like
// WithClock).  Passing no policy option is a programmer error and
// panics at construction time — silently admitting every call would
// defeat the point of the sampler.
type Option func(*Sampler)

// EveryN configures a count-based policy: admit exactly one call per n
// attempts per site. Calls where the per-site counter modulo n equals
// zero are admitted; all others are suppressed. n must be positive; a
// non-positive n panics at construction time because "admit nothing"
// and "admit everything" are not valid sampling modes.
func EveryN(n int) Option {
	if n <= 0 {
		panic("logging: EveryN requires n > 0")
	}
	return func(s *Sampler) {
		s.policy = &nthPolicy{n: int64(n)}
	}
}

// EveryD configures a time-window policy: admit at most one call per d
// per site. The first call after the window elapses is admitted; the
// window is reset on admission (not on every Allow) so a steady stream
// of suppressed calls cannot starve the window-reset check. d must be
// positive.
func EveryD(d time.Duration) Option {
	if d <= 0 {
		panic("logging: EveryD requires d > 0")
	}
	return func(s *Sampler) {
		s.policy = &windowPolicy{window: d}
	}
}

// WithClock overrides the default time source. Tests inject a fixed or
// controllable clock so window-based admission is deterministic; no
// production call site should pass this.
func WithClock(fn func() time.Time) Option {
	return func(s *Sampler) {
		s.clock = fn
	}
}

// NewSampler constructs a Sampler with the supplied options. A policy
// option (EveryN or EveryD) is required; omission panics. The returned
// Sampler has no goroutines and no background state — it is safe to
// allocate as a package-level var.
func NewSampler(opts ...Option) *Sampler {
	s := &Sampler{clock: time.Now}
	for _, opt := range opts {
		opt(s)
	}
	if s.policy == nil {
		panic("logging: NewSampler requires EveryN or EveryD option")
	}
	return s
}

// Allow reports whether a log call at the given site should be emitted
// under the sampler's policy. Callers should guard their slog call
// with: if s.Allow("tag") { slog.Info(...) }. Allow is cheap —
// atomic.Add and an int64 compare on the hot path — so gating every
// call site is fine even in tight loops.
func (s *Sampler) Allow(site string) bool {
	st := s.siteState(site)
	return s.policy.admit(st, s.clock())
}

// Printf is a convenience wrapper that applies Allow and, on admission,
// formats args via fmt.Sprintf and emits at the requested level via the
// supplied slog.Logger. It exists so call-sites that already build a
// formatted message do not have to duplicate the admission check.
// Suppressed calls return without allocating the formatted string.
func (s *Sampler) Printf(logger *slog.Logger, level slog.Level, site, format string, args ...any) {
	if !s.Allow(site) {
		return
	}
	logger.Log(nil, level, fmt.Sprintf(format, args...), "site", site)
}

// siteState returns the cached per-site state, allocating one on first
// use. sync.Map.LoadOrStore is the cheapest primitive for this shape —
// no mutex in the read-mostly path, a single allocation per distinct
// site in the write path.
func (s *Sampler) siteState(site string) *siteState {
	if v, ok := s.sites.Load(site); ok {
		return v.(*siteState)
	}
	st := &siteState{}
	actual, _ := s.sites.LoadOrStore(site, st)
	return actual.(*siteState)
}

// nthPolicy admits call numbers 1, n+1, 2n+1, …  Using (count-1) mod n
// == 0 makes the first call after site creation always admitted, which
// is the more useful default than waiting for n-1 suppressions before
// the first log line.
type nthPolicy struct{ n int64 }

func (p *nthPolicy) admit(s *siteState, _ time.Time) bool {
	// count is post-increment: the first call sees count==1.
	count := s.count.Add(1)
	return (count-1)%p.n == 0
}

// windowPolicy admits the first call after lastEmit + window. CAS is
// used on lastEmit so two racing goroutines cannot both succeed in
// crossing the same window boundary. Under contention the loser sees
// the winner's updated lastEmit and bails.
type windowPolicy struct{ window time.Duration }

func (p *windowPolicy) admit(s *siteState, now time.Time) bool {
	nowNs := now.UnixNano()
	for {
		prev := s.lastEmit.Load()
		fired := s.emitted.Load() == 1
		if fired && nowNs-prev < int64(p.window) {
			return false
		}
		if s.lastEmit.CompareAndSwap(prev, nowNs) {
			s.emitted.Store(1)
			return true
		}
	}
}
