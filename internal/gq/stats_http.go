package gq

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// StatsHandler serves the observability endpoints (/stats, /metrics, /healthz).
type StatsHandler struct {
	stats   *Stats
	sched   *Scheduler
	breaker *Breaker
	started time.Time
}

// NewStatsHandler creates a stats HTTP handler.
func NewStatsHandler(stats *Stats, sched *Scheduler, breaker *Breaker) *StatsHandler {
	return &StatsHandler{
		stats:   stats,
		sched:   sched,
		breaker: breaker,
		started: time.Now(),
	}
}

// BuildMetricsMux returns an http.Handler with /stats, /metrics, /healthz routes.
func (h *StatsHandler) BuildMetricsMux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/stats", h.handleStats)
	mux.HandleFunc("/metrics", h.handleMetrics)
	mux.HandleFunc("/healthz", h.handleHealthz)
	return mux
}

// StatsResponse matches the design doc section 7 schema.
type StatsResponse struct {
	TS       string         `json:"ts"`
	UptimeS  int64          `json:"uptime_s"`
	Slots    SlotsInfo      `json:"slots"`
	Queue    QueueInfo      `json:"queue"`
	WaitMs   WaitInfo       `json:"wait_ms"`
	Dispatch DispatchTotals `json:"dispatch"`
	Upstream BreakerStats   `json:"upstream"`
}

// SlotsInfo describes slot utilization.
type SlotsInfo struct {
	Capacity  int `json:"capacity"`
	InUse     int `json:"in_use"`
	Available int `json:"available"`
}

// QueueInfo describes queue state.
type QueueInfo struct {
	TotalDepth int                     `json:"total_depth"`
	ByPriority map[string]PriorityInfo `json:"by_priority"`
	ByCaller   []CallerInfo            `json:"by_caller"`
}

// PriorityInfo describes depth+callers for a priority level.
type PriorityInfo struct {
	Depth   int `json:"depth"`
	Callers int `json:"callers"`
}

// CallerInfo describes a single caller's queue presence.
type CallerInfo struct {
	Caller   string `json:"caller"`
	Priority string `json:"priority"`
	Depth    int    `json:"depth"`
}

// WaitInfo holds wait-time quantiles per priority.
type WaitInfo struct {
	WindowS int           `json:"window_s"`
	High    WaitQuantiles `json:"high"`
	Normal  WaitQuantiles `json:"normal"`
	Low     WaitQuantiles `json:"low"`
}

func (h *StatsHandler) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}

	depths := h.sched.PriorityDepths()
	lanes := h.sched.LaneStats()

	callers := make([]CallerInfo, len(lanes))
	for i, ls := range lanes {
		callers[i] = CallerInfo{
			Caller:   ls.CallerID,
			Priority: ls.Priority.String(),
			Depth:    ls.Depth,
		}
	}

	resp := StatsResponse{
		TS:      time.Now().UTC().Format(time.RFC3339),
		UptimeS: int64(time.Since(h.started).Seconds()),
		Slots: SlotsInfo{
			Capacity:  h.sched.cfg.MaxSlots,
			InUse:     h.sched.SlotsInUse(),
			Available: h.sched.SlotsAvailable(),
		},
		Queue: QueueInfo{
			TotalDepth: h.sched.Depth(),
			ByPriority: map[string]PriorityInfo{
				"high":   {Depth: depths[High].Depth, Callers: depths[High].Callers},
				"normal": {Depth: depths[Normal].Depth, Callers: depths[Normal].Callers},
				"low":    {Depth: depths[Low].Depth, Callers: depths[Low].Callers},
			},
			ByCaller: callers,
		},
		WaitMs: WaitInfo{
			WindowS: int(h.stats.windowSize.Seconds()),
			High:    h.stats.GetWaitQuantiles(High),
			Normal:  h.stats.GetWaitQuantiles(Normal),
			Low:     h.stats.GetWaitQuantiles(Low),
		},
		Dispatch: h.stats.GetDispatchTotals(),
		Upstream: h.breaker.Stats(),
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (h *StatsHandler) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}

	depths := h.sched.PriorityDepths()
	dispatch := h.stats.GetDispatchTotals()
	bs := h.breaker.Stats()

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

	// Slots.
	fmt.Fprintf(w, "# HELP gq_slots_in_use Number of dispatch slots currently held.\n")
	fmt.Fprintf(w, "# TYPE gq_slots_in_use gauge\n")
	fmt.Fprintf(w, "gq_slots_in_use %d\n", h.sched.SlotsInUse())

	fmt.Fprintf(w, "# HELP gq_slots_capacity Total dispatch slot capacity.\n")
	fmt.Fprintf(w, "# TYPE gq_slots_capacity gauge\n")
	fmt.Fprintf(w, "gq_slots_capacity %d\n", h.sched.cfg.MaxSlots)

	// Queue depth.
	fmt.Fprintf(w, "# HELP gq_queue_depth Number of items in queue by priority.\n")
	fmt.Fprintf(w, "# TYPE gq_queue_depth gauge\n")
	for p := High; p < numPriorities; p++ {
		fmt.Fprintf(w, "gq_queue_depth{priority=%q} %d\n", p.String(), depths[p].Depth)
	}

	// Per-caller depth.
	lanes := h.sched.LaneStats()
	fmt.Fprintf(w, "# HELP gq_queue_depth_by_caller Number of items per caller.\n")
	fmt.Fprintf(w, "# TYPE gq_queue_depth_by_caller gauge\n")
	for _, ls := range lanes {
		fmt.Fprintf(w, "gq_queue_depth_by_caller{caller=%q,priority=%q} %d\n",
			ls.CallerID, ls.Priority.String(), ls.Depth)
	}

	// Wait time quantiles.
	fmt.Fprintf(w, "# HELP gq_wait_ms Queue wait time in milliseconds.\n")
	fmt.Fprintf(w, "# TYPE gq_wait_ms summary\n")
	for p := High; p < numPriorities; p++ {
		q := h.stats.GetWaitQuantiles(p)
		fmt.Fprintf(w, "gq_wait_ms{priority=%q,quantile=\"0.5\"} %d\n", p.String(), q.P50)
		fmt.Fprintf(w, "gq_wait_ms{priority=%q,quantile=\"0.9\"} %d\n", p.String(), q.P90)
		fmt.Fprintf(w, "gq_wait_ms{priority=%q,quantile=\"0.99\"} %d\n", p.String(), q.P99)
		fmt.Fprintf(w, "gq_wait_ms_count{priority=%q} %d\n", p.String(), q.Count)
	}

	// Dispatch totals.
	fmt.Fprintf(w, "# HELP gq_dispatch_total Total dispatched requests.\n")
	fmt.Fprintf(w, "# TYPE gq_dispatch_total counter\n")
	fmt.Fprintf(w, "gq_dispatch_total{result=\"completed\"} %d\n", dispatch.Completed)
	fmt.Fprintf(w, "gq_dispatch_total{result=\"failed\"} %d\n", dispatch.Failed)
	fmt.Fprintf(w, "gq_dispatch_total{result=\"cancelled\"} %d\n", dispatch.Cancelled)
	fmt.Fprintf(w, "gq_dispatch_total{result=\"queue_timeout\"} %d\n", dispatch.QueueTimeout)

	// Breaker.
	fmt.Fprintf(w, "# HELP gq_upstream_breaker_state Circuit breaker state (0=closed,1=open,2=half_open).\n")
	fmt.Fprintf(w, "# TYPE gq_upstream_breaker_state gauge\n")
	bstate := 0
	switch bs.State {
	case "open":
		bstate = 1
	case "half_open":
		bstate = 2
	}
	fmt.Fprintf(w, "gq_upstream_breaker_state %d\n", bstate)
}

func (h *StatsHandler) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if h.breaker.State() == BreakerOpen {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "unhealthy", "reason": "breaker_open"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
