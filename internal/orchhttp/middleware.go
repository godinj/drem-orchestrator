package orchhttp

import (
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
)

// Load-shedding middleware for Bug E (2026-04-21 retry storm). A single
// confused TUI client at 495 req/s pushed /tasks into 28 s latencies
// and 1.37 GiB of log churn; this package makes that class of pathology
// harmless by fast-failing overflow requests with 503 + Retry-After: 1
// rather than queueing them into unbounded latency.
//
// Two nested caps, both atomic-counter-based (no mutex), both
// stdlib-only — the plan explicitly forbids third-party rate-limit
// libraries:
//
//   1. maxInFlight wraps the entire mux with a global cap (default 64).
//   2. tasksInFlight wraps /projects/{name}/tasks with a tighter
//      per-endpoint cap (default 8), applied BEFORE the global one so
//      /tasks overflow does not consume the global budget.
//
// Both caps are env-tunable (DREM_ORCH_MAX_INFLIGHT and
// DREM_ORCH_TASKS_MAX_INFLIGHT); unset or "0" means default. Overflow
// paths do not increment the goroutine counter, so a saturated cap
// does not leak goroutines under sustained load.

const (
	defaultMaxInFlight      = 64
	defaultTasksMaxInFlight = 8

	envMaxInFlight      = "DREM_ORCH_MAX_INFLIGHT"
	envTasksMaxInFlight = "DREM_ORCH_TASKS_MAX_INFLIGHT"
)

// WithLoadShedding composes the global and per-/tasks concurrency caps
// in front of next, returning an http.Handler safe to mount as the top
// of the mux chain. It is a standalone export (not a Server method) so
// the middleware tests can exercise it without standing up a full
// Server; production uses it from Server.Routes().
func WithLoadShedding(next http.Handler) http.Handler {
	tasksCap := envCap(envTasksMaxInFlight, defaultTasksMaxInFlight)
	globalCap := envCap(envMaxInFlight, defaultMaxInFlight)
	return withGlobalCap(globalCap, withTasksCap(tasksCap, next))
}

// envCap reads an int from env, returning fallback when the var is
// unset, empty, "0", or unparseable. Negative values are clamped to
// fallback so a fat-fingered knob cannot silently disable shedding.
func envCap(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

// withGlobalCap returns a handler that admits at most n concurrent
// requests across the entire mux; overflow gets 503 + Retry-After: 1
// fast. The counter is atomic so there is no mutex on the hot path.
func withGlobalCap(n int, next http.Handler) http.Handler {
	var inflight atomic.Int32
	limit := int32(n)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if inflight.Add(1) > limit {
			inflight.Add(-1)
			shed(w)
			return
		}
		defer inflight.Add(-1)
		next.ServeHTTP(w, r)
	})
}

// withTasksCap returns a handler that admits at most n concurrent
// requests to /projects/{name}/tasks specifically. Non-/tasks paths
// flow through untouched. This sub-budget is the plan's W1.2 chokepoint
// — it stops /tasks from draining the global cap.
//
// The path matcher is intentionally lightweight: strings.HasPrefix
// against /projects/ and strings.HasSuffix against /tasks, skipping
// /tasks/{id}/* mutation routes. This avoids a second mux lookup and
// has no allocation cost.
func withTasksCap(n int, next http.Handler) http.Handler {
	var inflight atomic.Int32
	limit := int32(n)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isTasksListPath(r) {
			next.ServeHTTP(w, r)
			return
		}
		if inflight.Add(1) > limit {
			inflight.Add(-1)
			shed(w)
			return
		}
		defer inflight.Add(-1)
		next.ServeHTTP(w, r)
	})
}

// isTasksListPath reports whether r targets GET /projects/{name}/tasks
// (the expensive list endpoint). It deliberately excludes gate mutation
// routes like /projects/{name}/tasks/{id}/approve — those have their
// own natural rate limits (one per operator click) and should not share
// the /tasks list budget.
func isTasksListPath(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	p := r.URL.Path
	const prefix = "/projects/"
	if !strings.HasPrefix(p, prefix) {
		return false
	}
	// Look for /tasks at the end — allow optional trailing slash but
	// reject /tasks/anything-else.
	rest := strings.TrimPrefix(p, prefix)
	// rest should be "{name}/tasks" (optionally with trailing slash).
	slash := strings.IndexByte(rest, '/')
	if slash < 0 {
		return false
	}
	tail := rest[slash+1:]
	tail = strings.TrimSuffix(tail, "/")
	return tail == "tasks"
}

// shed writes the standard 503 + Retry-After: 1 + terse body for an
// overflow request. Callers invoke this after decrementing the atomic
// counter so the shed path is guaranteed not to leak budget.
func shed(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "1")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte("server overloaded — retry after 1s\n"))
}
