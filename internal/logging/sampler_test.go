package logging

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestSamplerEveryNEmitsAtMostOnePerN asserts that a sampler configured
// with EveryN(5) admits exactly one log call for every 5 attempts — the
// canonical "1 in N" behaviour Bug E W4.1 requires. The incident log
// shape (1406 repeated /tasks 500 lines per 200 KB of tail) is exactly
// what this prevents.
func TestSamplerEveryNEmitsAtMostOnePerN(t *testing.T) {
	s := NewSampler(EveryN(5))
	var emitted atomic.Int64
	for i := 0; i < 25; i++ {
		if s.Allow("site") {
			emitted.Add(1)
		}
	}
	// Every 5th call admits (call numbers 1, 6, 11, 16, 21): 5 total.
	if got := emitted.Load(); got != 5 {
		t.Fatalf("EveryN(5) over 25 attempts: got %d emissions, want 5", got)
	}
}

// TestSamplerEveryDSuppressesWithinWindow asserts that a time-window
// sampler admits at most one log call per window across rapid repeated
// calls, matching the "at most once per M seconds" half of the plan's
// sampling contract.
func TestSamplerEveryDSuppressesWithinWindow(t *testing.T) {
	now := time.Unix(0, 0)
	clock := func() time.Time { return now }
	s := NewSampler(EveryD(100*time.Millisecond), WithClock(clock))

	if !s.Allow("site") {
		t.Fatal("first call should be admitted")
	}
	// Rapid-fire within the window — all should be suppressed.
	for i := 0; i < 10; i++ {
		if s.Allow("site") {
			t.Fatalf("call %d within window should have been suppressed", i)
		}
	}
	// Advance past the window — next call should be admitted.
	now = now.Add(200 * time.Millisecond)
	if !s.Allow("site") {
		t.Fatal("call after window should be admitted")
	}
}

// TestSamplerPerSiteIndependence proves that distinct site tags do not
// share counters. A busy /tasks log site should not deprive a different
// /workers log site of its first-call admission.
func TestSamplerPerSiteIndependence(t *testing.T) {
	s := NewSampler(EveryN(10))
	// Burn 9 calls on site A — still under its budget, no admission yet
	// for the 10th either unless this fires exactly on the boundary.
	for i := 0; i < 9; i++ {
		s.Allow("site-a")
	}
	// Site B is untouched — its first call must be admitted regardless
	// of site A's counter.
	if !s.Allow("site-b") {
		t.Fatal("site-b's first call must be admitted independent of site-a")
	}
}

// TestSamplerIsGoroutineSafe fires many goroutines at a shared sampler
// and asserts the admitted count stays within the theoretical max. The
// counter is atomic, so no mutex is required on the hot path.
func TestSamplerIsGoroutineSafe(t *testing.T) {
	s := NewSampler(EveryN(100))
	var admitted atomic.Int64
	var wg sync.WaitGroup
	const workers = 20
	const callsPer = 500
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < callsPer; j++ {
				if s.Allow("site") {
					admitted.Add(1)
				}
			}
		}()
	}
	wg.Wait()
	// Total calls = 10_000. EveryN(100) admits one per 100 attempts, so
	// the exact admitted count is 100 under perfectly sequential
	// execution. Under concurrency the count stays at 100 because
	// atomic.Add is linearisable — every attempt increments once, so
	// "call number divisible by 100" fires exactly 100 times.
	const total = workers * callsPer
	const want = total / 100
	if got := admitted.Load(); got != want {
		t.Fatalf("concurrent admitted count = %d, want %d (total %d / 100)", got, want, total)
	}
}

// TestPrintfEmitsSampled applies a Sampler to slog output, fires many
// calls at a single site, and asserts that the slog handler sees exactly
// the expected number of records. This is the wiring test for the
// Printf-style helper the orchhttp handlers will use.
func TestPrintfEmitsSampled(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	s := NewSampler(EveryN(4))

	for i := 0; i < 12; i++ {
		s.Printf(logger, slog.LevelInfo, "hot-site", "iter=%d", i)
	}
	// EveryN(4) over 12 calls admits on calls 1, 5, 9 — 3 emissions.
	lines := strings.Count(buf.String(), "hot-site")
	if lines != 3 {
		t.Fatalf("EveryN(4) over 12 Printf calls: got %d log lines, want 3\nbuffer:\n%s", lines, buf.String())
	}
}
