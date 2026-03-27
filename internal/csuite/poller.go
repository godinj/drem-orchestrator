package csuite

import (
	"context"
	"fmt"
	"log"
	"time"
)

// DefaultPollInterval is the default interval between state snapshots.
const DefaultPollInterval = 5 * time.Second

// maxBackoffMultiplier caps exponential backoff at 32× the base interval.
const maxBackoffMultiplier = 32

// StateSnapshot contains a point-in-time view of pipeline health for
// dashboard rendering. Each field is populated by the Poller from its
// SnapshotSource and optional WatcherSource.
type StateSnapshot struct {
	AgentSummaries []AgentSummary
	InboxCounts    map[string]int
	WatcherData    *WatcherSnapshot // nil when watcher DB is unavailable
	Timestamp      time.Time
}

// SnapshotSource defines the data-access contract the Poller depends on.
// Defined at consumption site per architecture guidelines. In production
// this is satisfied by *DashboardQuery; in tests by a fake.
type SnapshotSource interface {
	AgentSummaries() ([]AgentSummary, error)
	UnreadMessageCounts() (map[string]int, error)
}

// Poller periodically queries the database via a SnapshotSource and emits
// StateSnapshots on a channel. It supports pause/resume and graceful
// shutdown via context cancellation.
type Poller struct {
	source        SnapshotSource
	watcherSource *WatcherSource // optional; nil means watcher data omitted
	interval      time.Duration
	snapC         chan StateSnapshot
	logger        *log.Logger
	pauseC        chan struct{}
	resumeC       chan struct{}
}

// NewPoller creates a Poller that queries source every interval and sends
// snapshots on the returned channel. Call Start to begin polling.
func NewPoller(source SnapshotSource, interval time.Duration, logger *log.Logger) *Poller {
	return &Poller{
		source:   source,
		interval: interval,
		snapC:    make(chan StateSnapshot, 1),
		logger:   logger,
		pauseC:   make(chan struct{}, 1),
		resumeC:  make(chan struct{}, 1),
	}
}

// Snapshots returns the read-only channel on which StateSnapshots are delivered.
func (p *Poller) Snapshots() <-chan StateSnapshot {
	return p.snapC
}

// Start begins the polling loop. It blocks until ctx is cancelled, at which
// point it closes the snapshot channel and returns.
func (p *Poller) Start(ctx context.Context) {
	defer close(p.snapC)

	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	paused := false
	failures := 0

	for {
		select {
		case <-ctx.Done():
			return
		case <-p.pauseC:
			paused = true
		case <-p.resumeC:
			paused = false
		case <-ticker.C:
			if paused {
				continue
			}
			snap, err := p.BuildSnapshot()
			if err != nil {
				failures++
				p.logger.Printf("snapshot error (attempt %d): %v", failures, err)
				ticker.Reset(p.backoff(failures))
				continue
			}
			if failures > 0 {
				failures = 0
				ticker.Reset(p.interval)
			}
			select {
			case p.snapC <- snap:
			default:
			}
		}
	}
}

// Pause stops snapshot emission until Resume is called. Safe to call
// multiple times (idempotent).
func (p *Poller) Pause() {
	select {
	case p.pauseC <- struct{}{}:
	default:
	}
}

// Resume restarts snapshot emission after a Pause. Safe to call without
// a preceding Pause (idempotent).
func (p *Poller) Resume() {
	select {
	case p.resumeC <- struct{}{}:
	default:
	}
}

// SetWatcherSource attaches a WatcherSource to the Poller. When set, each
// snapshot is enriched with turn metrics, token totals, and recent events
// from the watcher database. A nil source disables watcher enrichment.
func (p *Poller) SetWatcherSource(ws *WatcherSource) {
	p.watcherSource = ws
}

// BuildSnapshot assembles a StateSnapshot by querying the SnapshotSource
// and (if configured) the WatcherSource for turn metrics and events.
func (p *Poller) BuildSnapshot() (StateSnapshot, error) {
	agents, err := p.source.AgentSummaries()
	if err != nil {
		return StateSnapshot{}, fmt.Errorf("agent summaries: %w", err)
	}
	counts, err := p.source.UnreadMessageCounts()
	if err != nil {
		return StateSnapshot{}, fmt.Errorf("unread counts: %w", err)
	}

	snap := StateSnapshot{
		AgentSummaries: agents,
		InboxCounts:    counts,
		Timestamp:      time.Now(),
	}

	if p.watcherSource != nil {
		wSnap, wErr := p.watcherSource.Snapshot()
		if wErr != nil {
			p.logger.Printf("watcher snapshot error: %v", wErr)
		} else {
			snap.WatcherData = wSnap
		}
	}

	return snap, nil
}

// backoff returns an exponential backoff duration capped at
// maxBackoffMultiplier × the base interval.
func (p *Poller) backoff(failures int) time.Duration {
	mult := 1
	for i := 1; i < failures && mult < maxBackoffMultiplier; i++ {
		mult *= 2
	}
	return p.interval * time.Duration(mult)
}
