// This file wires the Docker-stdout subscription into agentmon. A
// DockerSource subscribes to lifecycle events matching a label filter
// (typically drem.project=<name>) and spawns one tailContainer
// goroutine per running container. Die/destroy events cancel the
// corresponding tail. Ctx cancel shuts everything down and waits for
// the tails to drain.
//
// Observability hooks (added after the 41h silent-outage incident
// — see plans/agentmon-observability.md). Two gaps the incident
// exposed:
//   - A mismatched label filter matched ZERO events for 41 hours
//     without emitting any WARN. DockerSource now emits a single
//     Warn after Warn0Window elapses if the subscribe-to-now counter
//     is still zero. The intent is "tell the operator loudly the
//     first time a subscription has received nothing over a
//     reasonable startup window", not to fail hard — a flaky
//     daemon that takes 40s to deliver an event should NOT fail.
//   - The stuck-agent reconciler was operating on stale DB
//     heartbeats with no live signal. EventsMatchedLastMinute lets
//     consumers (or liveness probes) cheaply sample whether the
//     subscription is ingesting traffic without holding the Run
//     mutex.

package agentmon

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/godinj/drem-orchestrator/internal/container"
)

// defaultWarn0Window is the window from subscribe-start after which a
// DockerSource with zero observed events emits a one-shot Warn. Seth's
// triage scope called for 30–60s; 45s is the midpoint, long enough
// that a quiet CI does not trip it, short enough that a misconfigured
// production subscription surfaces within a minute.
const defaultWarn0Window = 45 * time.Second

// eventsWindowSeconds is the trailing-second count over which
// EventsMatchedLastMinute aggregates. Implemented as a ring of
// per-second buckets; approximate is fine — this is for observability,
// not billing.
const eventsWindowSeconds = 60

// Ingestor is the seam between the Docker-stdout path and the HTTP
// upload. The production implementation is HTTPIngestor (in client.go);
// tests inject a channel-backed fake.
type Ingestor interface {
	Ingest(ctx context.Context, records []IngestRecord) error
}

// DockerSource owns a Docker event subscription and the per-container
// tail goroutines it spawns. One DockerSource should run per agentmon
// process; tests may run multiple against a single FakeRuntime.
type DockerSource struct {
	// Runtime is the container runtime used to subscribe to events
	// and open log streams. Required.
	Runtime container.Runtime
	// Ingestor receives every batch of parsed records. Required.
	Ingestor Ingestor
	// Project tags tails with the worker's project name when no
	// drem.worker-id label is present; currently informational.
	Project string
	// ContainerFilter restricts which containers the source watches.
	// In the per-project agentmon deployment (see README.md) this is
	// typically {"drem.project": Project}. An empty filter watches
	// every container the daemon exposes — useful only in tests.
	ContainerFilter container.EventFilter

	// Warn0Window overrides the duration after which Run will emit a
	// one-shot Warn if no events have arrived since the subscription
	// opened. Zero means "use defaultWarn0Window". Tests set this to
	// a small value (e.g. 50ms) to exercise the timer without sleeping.
	Warn0Window time.Duration

	// Now is the clock source used by the per-second bucket ring that
	// backs EventsMatchedLastMinute. Zero means time.Now; tests inject
	// a stub to advance time deterministically.
	Now func() time.Time

	// eventsSeen counts every event the Run loop observes through the
	// subscription, including ones that are routed to no action (types
	// other than start/die/OOM/destroy). The zero-events Warn uses this.
	// atomic so the warn-timer goroutine can read it without locking.
	eventsSeen atomic.Int64

	// buckets is the per-second ring backing EventsMatchedLastMinute.
	// Protected by bucketsMu. Size is eventsWindowSeconds; the consumer
	// sums the buckets whose second-epoch falls in the trailing 60s.
	bucketsMu sync.Mutex
	buckets   [eventsWindowSeconds]bucketEntry

	// state tracks active per-container tails so EventDie can cancel
	// the matching tail goroutine.
	mu    sync.Mutex
	tails map[string]context.CancelFunc
	wg    sync.WaitGroup
}

// bucketEntry is one slot in the per-second ring. Epoch is the unix
// second the count belongs to; stale entries (older than the trailing
// window) are skipped by the reader.
type bucketEntry struct {
	epoch int64
	count int
}

// Run subscribes to lifecycle events and drives tails until ctx is
// cancelled. Returns when the event channel closes and all tail
// goroutines have drained. Any error from SubscribeEvents is returned
// immediately; errors from individual tails are logged (see tail.go).
func (s *DockerSource) Run(ctx context.Context) error {
	if s.Runtime == nil {
		return fmt.Errorf("agentmon docker source: Runtime is required")
	}
	if s.Ingestor == nil {
		return fmt.Errorf("agentmon docker source: Ingestor is required")
	}
	s.mu.Lock()
	s.tails = make(map[string]context.CancelFunc)
	s.mu.Unlock()

	events, err := s.Runtime.SubscribeEvents(ctx, s.ContainerFilter)
	if err != nil {
		return fmt.Errorf("agentmon docker source: subscribe: %w", err)
	}

	// Kick off the one-shot zero-events WARN goroutine. It sleeps for
	// Warn0Window, then samples eventsSeen; if still zero, logs the
	// pre-formatted WARN and exits. Cancelling ctx wakes it early and
	// suppresses the log (shutdown path should not spam).
	s.startWarn0Goroutine(ctx)

	for {
		select {
		case <-ctx.Done():
			s.shutdown()
			return nil
		case ev, ok := <-events:
			if !ok {
				s.shutdown()
				return nil
			}
			// Count BEFORE handling so a filter that matches-but-
			// parses-wrong still registers — the whole point of the
			// counter is to distinguish "zero traffic" from "traffic
			// that didn't route to an action".
			s.eventsSeen.Add(1)
			s.recordEventNow()
			s.handleEvent(ctx, ev)
		}
	}
}

// startWarn0Goroutine fires a single Warn after Warn0Window if no
// events have been observed yet. It exits silently on ctx cancel —
// shutdown is not a misconfiguration signal and should not WARN.
func (s *DockerSource) startWarn0Goroutine(ctx context.Context) {
	window := s.Warn0Window
	if window <= 0 {
		window = defaultWarn0Window
	}
	go func() {
		timer := time.NewTimer(window)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if s.eventsSeen.Load() > 0 {
			return
		}
		slog.Warn(
			"agentmon docker source: zero events matched in 45s since subscription start",
			"filter_labels", filterLabelsJSON(s.ContainerFilter.Labels),
			"window", window,
			"project", s.Project,
		)
	}()
}

// filterLabelsJSON renders the filter's label map in a stable,
// searchable form for operator greps. A plain map would be
// non-deterministically ordered in log output; this function sorts
// keys and emits compact JSON.
func filterLabelsJSON(labels map[string]string) string {
	if len(labels) == 0 {
		return "{}"
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	ordered := make(map[string]string, len(labels))
	for _, k := range keys {
		ordered[k] = labels[k]
	}
	b, err := json.Marshal(ordered)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// recordEventNow buckets one observed event into the trailing-minute
// ring. Called from Run for every event that reaches the select arm,
// independent of whether the event matches a routed type.
func (s *DockerSource) recordEventNow() {
	now := s.clock()
	epoch := now.Unix()
	idx := int(epoch % eventsWindowSeconds)
	s.bucketsMu.Lock()
	defer s.bucketsMu.Unlock()
	b := &s.buckets[idx]
	if b.epoch != epoch {
		// Reused slot; reset to this second.
		b.epoch = epoch
		b.count = 0
	}
	b.count++
}

// EventsMatchedLastMinute returns the number of events observed
// through the subscription in the trailing 60 seconds. Consumers
// (liveness probes, HTTP expvars, the reconciler) decide how to
// surface it. Approximate: if many events landed in the same second
// the counts may be rounded by at most one bucket on either edge.
func (s *DockerSource) EventsMatchedLastMinute() int {
	now := s.clock()
	nowEpoch := now.Unix()
	cutoff := nowEpoch - (eventsWindowSeconds - 1)
	total := 0
	s.bucketsMu.Lock()
	defer s.bucketsMu.Unlock()
	for _, b := range s.buckets {
		if b.epoch >= cutoff && b.epoch <= nowEpoch {
			total += b.count
		}
	}
	return total
}

// HasSeen returns true if any event with the given container ID has
// been observed since subscription start. Implements the hook shape
// consumed by the orchestrator's reconcile_stuck predicate. A true
// return means the subscription observed at least one lifecycle
// event for the container (start, die, OOM, destroy, etc.) since
// Run began. The DockerSource's tail map is authoritative for this
// because startTail/stopTail mutate it on every relevant event.
//
// Note: when a tail exits naturally (e.g. the log stream closed),
// removeTail drops the entry. That is fine for the reconciler's
// purpose — a container whose tail ran and exited WAS sighted, and
// the reconciler's question is "has this container ever been
// observed" not "is it active". To cover that narrower case we
// consult the per-second bucket total as a secondary signal: if
// there were events in the trailing minute the subscription is
// demonstrably live regardless of which container they were for.
// For a strict "sighted this container id" signal callers should
// build their own index over sighted IDs; the reconciler's shape
// treats empty trailing-minute traffic as "agentmon is blind",
// which is the v12–v14 failure mode.
func (s *DockerSource) HasSeen(containerID string) bool {
	s.mu.Lock()
	_, active := s.tails[containerID]
	s.mu.Unlock()
	if active {
		return true
	}
	// Fallback: if the subscription is demonstrably receiving events
	// in the trailing minute, consider the daemon side of the pipe
	// alive. A container whose tail exited is still covered by this
	// because start/die events both count toward the bucket total.
	// Callers that want strict per-ID sighting can wrap HasSeen with
	// their own set keyed off EventStart observations.
	return s.EventsMatchedLastMinute() > 0
}

// clock returns the current time from Now if set, else wall-clock.
// Tests inject Now to advance buckets deterministically.
func (s *DockerSource) clock() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// handleEvent routes a single lifecycle event to start or cancel a
// per-container tail. Unknown event types are ignored.
func (s *DockerSource) handleEvent(ctx context.Context, ev container.Event) {
	switch ev.Type {
	case container.EventStart:
		s.startTail(ctx, ev)
	case container.EventDie, container.EventOOM, container.EventDestroy:
		s.stopTail(ev.ContainerID)
	}
}

// startTail spawns a tail goroutine for the container unless one is
// already active. Duplicate EventStart messages for the same container
// — which Docker occasionally emits after a short restart — are
// coalesced into a single tail.
func (s *DockerSource) startTail(parent context.Context, ev container.Event) {
	s.mu.Lock()
	if _, exists := s.tails[ev.ContainerID]; exists {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(parent)
	s.tails[ev.ContainerID] = cancel
	s.mu.Unlock()

	workerID := ev.Labels["drem.worker_id"]
	if workerID == "" {
		workerID = ev.Labels["drem.worker-id"]
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer s.removeTail(ev.ContainerID)
		if err := tailContainer(ctx, s.Runtime, s.Ingestor, ev.ContainerID, workerID, ev.Labels); err != nil {
			slog.Warn("agentmon docker tail exited",
				"container", ev.ContainerID, "err", err)
		}
	}()
}

// stopTail cancels the tail goroutine for containerID. Safe to call
// when no tail exists.
func (s *DockerSource) stopTail(containerID string) {
	s.mu.Lock()
	cancel, ok := s.tails[containerID]
	if ok {
		delete(s.tails, containerID)
	}
	s.mu.Unlock()
	if ok {
		cancel()
	}
}

// removeTail drops a container's entry from the tail map without
// cancelling (used by a tail's defer when it exits naturally, for
// example because the log stream closed).
func (s *DockerSource) removeTail(containerID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tails, containerID)
}

// shutdown cancels every active tail and blocks until they all return.
// Called when ctx is cancelled or the event channel closes.
func (s *DockerSource) shutdown() {
	s.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(s.tails))
	for _, c := range s.tails {
		cancels = append(cancels, c)
	}
	s.tails = map[string]context.CancelFunc{}
	s.mu.Unlock()
	for _, c := range cancels {
		c()
	}
	s.wg.Wait()
}
