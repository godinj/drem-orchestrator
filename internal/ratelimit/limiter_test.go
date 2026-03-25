package ratelimit

import (
	"sync"
	"testing"
	"time"
)

func TestAllow_UnderLimit(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }

	lim := New(3, time.Minute, WithClock(clock))

	for i := 0; i < 3; i++ {
		ok, delay := lim.Allow()
		if !ok {
			t.Fatalf("dispatch %d: expected allowed, got blocked", i+1)
		}
		if delay != 0 {
			t.Fatalf("dispatch %d: expected zero delay, got %v", i+1, delay)
		}
	}
}

func TestAllow_BlocksAtLimit(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }

	lim := New(3, time.Minute, WithClock(clock))

	// Exhaust the limit.
	for i := 0; i < 3; i++ {
		lim.Allow()
	}

	ok, _ := lim.Allow()
	if ok {
		t.Fatal("expected dispatch to be blocked after limit reached")
	}
}

func TestAllow_BlockedDispatchReturnsDelay(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }

	lim := New(3, time.Minute, WithClock(clock))

	for i := 0; i < 3; i++ {
		lim.Allow()
	}

	// Advance 20s so oldest entry is 20s old; window is 60s, so 40s remain.
	now = now.Add(20 * time.Second)

	ok, delay := lim.Allow()
	if ok {
		t.Fatal("expected dispatch to be blocked")
	}
	// Delay should be roughly 40s (time until oldest entry slides out).
	if delay < 39*time.Second || delay > 41*time.Second {
		t.Fatalf("expected delay ~40s, got %v", delay)
	}
}

func TestAllow_AllowsAfterWindowSlides(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }

	lim := New(3, time.Minute, WithClock(clock))

	for i := 0; i < 3; i++ {
		lim.Allow()
	}

	// Advance past the window so all entries expire.
	now = now.Add(61 * time.Second)

	ok, delay := lim.Allow()
	if !ok {
		t.Fatal("expected dispatch to be allowed after window slides")
	}
	if delay != 0 {
		t.Fatalf("expected zero delay, got %v", delay)
	}
}

func TestAllow_PartialWindowSlide(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }

	lim := New(2, time.Minute, WithClock(clock))

	// Dispatch at t=0.
	lim.Allow()

	// Dispatch at t=30s.
	now = now.Add(30 * time.Second)
	lim.Allow()

	// At t=30s both entries are in the window; limit reached.
	ok, _ := lim.Allow()
	if ok {
		t.Fatal("expected blocked at t=30s")
	}

	// At t=61s the first entry (t=0) has slid out; only second (t=30s) remains.
	now = now.Add(31 * time.Second)

	ok, delay := lim.Allow()
	if !ok {
		t.Fatal("expected allowed after oldest entry slid out")
	}
	if delay != 0 {
		t.Fatalf("expected zero delay, got %v", delay)
	}
}

func TestAllow_JitterAddsDelay(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }

	// maxJitter returns a fixed jitter for deterministic testing.
	fixedJitter := 5 * time.Second
	jitterFn := func(max time.Duration) time.Duration { return fixedJitter }

	lim := New(1, time.Minute, WithClock(clock), WithJitter(jitterFn))

	lim.Allow()

	ok, delay := lim.Allow()
	if ok {
		t.Fatal("expected blocked")
	}
	// Delay = time until oldest slides out + jitter.
	// Oldest is at t=0, window=60s, now=0s, so base delay = 60s.
	// Total = 60s + 5s = 65s.
	expected := 60*time.Second + fixedJitter
	if delay != expected {
		t.Fatalf("expected delay %v (including jitter), got %v", expected, delay)
	}
}

func TestAllow_ConcurrentAccess(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	clock := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}

	const limit = 5
	lim := New(limit, time.Minute, WithClock(clock))

	var wg sync.WaitGroup
	allowed := make(chan bool, 100)

	// Launch 20 goroutines all trying to dispatch.
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, _ := lim.Allow()
			allowed <- ok
		}()
	}

	wg.Wait()
	close(allowed)

	count := 0
	for ok := range allowed {
		if ok {
			count++
		}
	}

	if count != limit {
		t.Fatalf("expected exactly %d allowed dispatches, got %d", limit, count)
	}
}

func TestZeroValue_BlocksAll(t *testing.T) {
	// A zero-value Limiter (no max, no window) should block everything
	// rather than silently allowing unbounded dispatch.
	var lim Limiter

	ok, _ := lim.Allow()
	if ok {
		t.Fatal("zero-value Limiter should block all dispatches")
	}
}

func TestNew_PanicsOnZeroMax(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for zero MaxDispatches")
		}
	}()
	New(0, time.Minute)
}

func TestNew_PanicsOnZeroWindow(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for zero Window")
		}
	}()
	New(3, 0)
}
