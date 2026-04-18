package gq

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Stats tracks rolling-window metrics for the proxy.
type Stats struct {
	mu sync.Mutex

	// Per-priority rolling wait-time windows.
	waitWindows [numPriorities]*rollingWindow

	// Aggregate counters (atomic for lock-free reads).
	completedTotal atomic.Int64
	failedTotal    atomic.Int64
	cancelledTotal atomic.Int64
	timeoutTotal   atomic.Int64
	enqueuedTotal  [numPriorities]atomic.Int64

	// Token counters.
	tokensInTotal  atomic.Int64
	tokensOutTotal atomic.Int64

	// Dispatch duration tracking.
	avgUpstreamMs atomic.Int64
	dispatchCount atomic.Int64

	windowSize time.Duration
	clock      func() time.Time
}

// NewStats creates a Stats tracker with the given window duration.
func NewStats(windowSize time.Duration) *Stats {
	s := &Stats{
		windowSize: windowSize,
		clock:      time.Now,
	}
	for i := 0; i < numPriorities; i++ {
		s.waitWindows[i] = newRollingWindow(windowSize)
	}
	return s
}

// RecordEnqueue increments the enqueue counter for a priority.
func (s *Stats) RecordEnqueue(p Priority) {
	if p >= 0 && int(p) < numPriorities {
		s.enqueuedTotal[p].Add(1)
	}
}

// RecordWait records a queue wait duration for a priority level.
func (s *Stats) RecordWait(p Priority, d time.Duration) {
	if p >= 0 && int(p) < numPriorities {
		s.mu.Lock()
		s.waitWindows[p].add(s.clock(), d)
		s.mu.Unlock()
	}
}

// RecordDispatch records a completed dispatch.
func (s *Stats) RecordDispatch(ok bool, upstreamMs, tokensIn, tokensOut int64) {
	if ok {
		s.completedTotal.Add(1)
	} else {
		s.failedTotal.Add(1)
	}
	s.tokensInTotal.Add(tokensIn)
	s.tokensOutTotal.Add(tokensOut)

	// Running average of upstream latency.
	count := s.dispatchCount.Add(1)
	if count == 1 {
		s.avgUpstreamMs.Store(upstreamMs)
	} else {
		// Exponential moving average (α ≈ 0.1).
		old := s.avgUpstreamMs.Load()
		updated := old + (upstreamMs-old)/10
		s.avgUpstreamMs.Store(updated)
	}
}

// RecordCancel increments the cancellation counter.
func (s *Stats) RecordCancel() {
	s.cancelledTotal.Add(1)
}

// RecordTimeout increments the queue timeout counter.
func (s *Stats) RecordTimeout() {
	s.timeoutTotal.Add(1)
}

// WaitQuantiles returns p50/p90/p99 wait times for a priority within the window.
type WaitQuantiles struct {
	P50   int64 `json:"p50"`
	P90   int64 `json:"p90"`
	P99   int64 `json:"p99"`
	Count int   `json:"count"`
}

// GetWaitQuantiles computes wait-time quantiles for a priority.
func (s *Stats) GetWaitQuantiles(p Priority) WaitQuantiles {
	s.mu.Lock()
	defer s.mu.Unlock()

	if p < 0 || int(p) >= numPriorities {
		return WaitQuantiles{}
	}

	values := s.waitWindows[p].values(s.clock())
	if len(values) == 0 {
		return WaitQuantiles{}
	}

	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	return WaitQuantiles{
		P50:   values[percentileIndex(len(values), 50)],
		P90:   values[percentileIndex(len(values), 90)],
		P99:   values[percentileIndex(len(values), 99)],
		Count: len(values),
	}
}

// DispatchTotals returns aggregate dispatch counters.
type DispatchTotals struct {
	Completed     int64 `json:"completed_total"`
	Failed        int64 `json:"failed_total"`
	Cancelled     int64 `json:"cancelled_total"`
	QueueTimeout  int64 `json:"queue_timeout_total"`
	AvgUpstreamMs int64 `json:"avg_upstream_ms"`
	TokensIn      int64 `json:"tokens_in_total"`
	TokensOut     int64 `json:"tokens_out_total"`
}

// GetDispatchTotals returns aggregate dispatch statistics.
func (s *Stats) GetDispatchTotals() DispatchTotals {
	return DispatchTotals{
		Completed:     s.completedTotal.Load(),
		Failed:        s.failedTotal.Load(),
		Cancelled:     s.cancelledTotal.Load(),
		QueueTimeout:  s.timeoutTotal.Load(),
		AvgUpstreamMs: s.avgUpstreamMs.Load(),
		TokensIn:      s.tokensInTotal.Load(),
		TokensOut:     s.tokensOutTotal.Load(),
	}
}

// --- rolling window ---

type windowEntry struct {
	ts    time.Time
	value int64 // wait time in milliseconds
}

type rollingWindow struct {
	entries []windowEntry
	size    time.Duration
}

func newRollingWindow(size time.Duration) *rollingWindow {
	return &rollingWindow{size: size}
}

func (w *rollingWindow) add(now time.Time, d time.Duration) {
	w.entries = append(w.entries, windowEntry{ts: now, value: d.Milliseconds()})
}

func (w *rollingWindow) values(now time.Time) []int64 {
	cutoff := now.Add(-w.size)
	// Compact: remove entries older than window.
	start := 0
	for start < len(w.entries) && w.entries[start].ts.Before(cutoff) {
		start++
	}
	if start > 0 {
		w.entries = w.entries[start:]
	}

	result := make([]int64, len(w.entries))
	for i, e := range w.entries {
		result[i] = e.value
	}
	return result
}

func percentileIndex(n, pct int) int {
	idx := (n*pct+99)/100 - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= n {
		idx = n - 1
	}
	return idx
}
