package gq

import (
	"container/list"
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Lane is a per-(priority, callerID) FIFO queue with DRR credit tracking.
type Lane struct {
	Items   *list.List
	Deficit int
}

// Scheduler manages priority lanes, slot semaphore, and the DRR dispatch
// algorithm. Thread-safe — all public methods lock internally.
type Scheduler struct {
	mu sync.Mutex

	// lanes[priority] -> callerID -> Lane
	lanes [numPriorities]map[string]*Lane

	// rrIndex tracks round-robin cursor per priority level.
	rrIndex [numPriorities]int

	// rrKeys caches sorted caller keys per priority for stable RR iteration.
	// Rebuilt when callers change.
	rrKeys [numPriorities][]string

	// slots is a counting semaphore for upstream dispatch concurrency.
	slots chan struct{}

	// wake is signalled on enqueue or slot release to unpark the dispatcher.
	wake chan struct{}

	// totalDepth tracks aggregate queue depth across all lanes.
	totalDepth atomic.Int64

	cfg   *Config
	clock func() time.Time
}

// NewScheduler creates a scheduler with the given config.
func NewScheduler(cfg *Config) *Scheduler {
	s := &Scheduler{
		slots: make(chan struct{}, cfg.MaxSlots),
		wake:  make(chan struct{}, 1),
		cfg:   cfg,
		clock: time.Now,
	}
	for i := 0; i < numPriorities; i++ {
		s.lanes[i] = make(map[string]*Lane)
	}
	// Pre-fill slot semaphore (each token = one available slot).
	for i := 0; i < cfg.MaxSlots; i++ {
		s.slots <- struct{}{}
	}
	return s
}

// Enqueue adds an item to the appropriate lane. Returns an error if the
// queue is at max depth. Caller must not hold mu.
func (s *Scheduler) Enqueue(item *QueueItem) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if int(s.totalDepth.Load()) >= s.cfg.QueueMaxDepth {
		return fmt.Errorf("queue full (depth %d >= max %d)", s.totalDepth.Load(), s.cfg.QueueMaxDepth)
	}

	p := item.EffectivePriority
	callerID := item.CallerID
	lane, ok := s.lanes[p][callerID]
	if !ok {
		lane = &Lane{Items: list.New()}
		s.lanes[p][callerID] = lane
		s.rebuildKeys(p)
	}
	lane.Items.PushBack(item)
	s.totalDepth.Add(1)

	// Signal dispatcher.
	select {
	case s.wake <- struct{}{}:
	default:
	}
	return nil
}

// AcquireSlot blocks until a dispatch slot is available or ctx is cancelled.
func (s *Scheduler) AcquireSlot(ctx context.Context) error {
	select {
	case <-s.slots:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ReleaseSlot returns a dispatch slot and signals the dispatcher only if
// there is queued work. This prevents a busy-loop when slots are available
// but the queue is empty (wake → acquire → PickNext nil → release → wake …).
func (s *Scheduler) ReleaseSlot() {
	s.slots <- struct{}{}
	// Only wake if there's work to do — Enqueue signals wake independently,
	// so items arriving after this check still wake the dispatcher.
	if s.totalDepth.Load() > 0 {
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
}

// Wake returns the wake channel for the dispatcher to select on.
func (s *Scheduler) Wake() <-chan struct{} {
	return s.wake
}

// PickNext selects the next item to dispatch using strict priority + DRR +
// aging + shortest-context tiebreak. Returns nil if no eligible item exists.
// Caller must not hold mu.
func (s *Scheduler) PickNext() *QueueItem {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.applyAging()

	for p := High; p < numPriorities; p++ {
		lanes := s.lanes[p]
		keys := s.rrKeys[p]
		if len(keys) == 0 {
			continue
		}

		// DRR pass: iterate callers round-robin at this priority.
		for tries := 0; tries < len(keys); tries++ {
			idx := s.rrIndex[p] % len(keys)
			s.rrIndex[p] = (s.rrIndex[p] + 1) % len(keys)
			callerID := keys[idx]

			lane, ok := lanes[callerID]
			if !ok || lane.Items.Len() == 0 {
				continue
			}

			// Prune cancelled/expired items from head.
			s.pruneHead(lane, p, callerID)
			if lane.Items.Len() == 0 {
				continue
			}

			lane.Deficit += s.cfg.QuantumTokens

			head := lane.Items.Front().Value.(*QueueItem)
			if head.EstPromptTokens <= lane.Deficit {
				winner := s.shortestOfFirstK(lane)
				lane.Deficit -= winner.EstPromptTokens
				s.totalDepth.Add(-1)

				// Cleanup empty lane.
				if lane.Items.Len() == 0 {
					delete(lanes, callerID)
					s.rebuildKeys(p)
				}
				return winner
			}
		}
	}
	return nil
}

// Depth returns the total number of queued items across all lanes.
func (s *Scheduler) Depth() int {
	return int(s.totalDepth.Load())
}

// SlotsInUse returns the number of slots currently held by dispatchers.
func (s *Scheduler) SlotsInUse() int {
	return s.cfg.MaxSlots - len(s.slots)
}

// SlotsAvailable returns the number of free dispatch slots.
func (s *Scheduler) SlotsAvailable() int {
	return len(s.slots)
}

// LaneStat describes the state of a single priority+caller lane.
type LaneStat struct {
	CallerID string
	Priority Priority
	Depth    int
}

// LaneStats returns per-lane depth information for the stats endpoint.
func (s *Scheduler) LaneStats() []LaneStat {
	s.mu.Lock()
	defer s.mu.Unlock()

	var stats []LaneStat
	for p := High; p < numPriorities; p++ {
		for callerID, lane := range s.lanes[p] {
			if lane.Items.Len() > 0 {
				stats = append(stats, LaneStat{
					CallerID: callerID,
					Priority: Priority(p),
					Depth:    lane.Items.Len(),
				})
			}
		}
	}
	return stats
}

// PriorityDepths returns (depth, callerCount) per priority level.
func (s *Scheduler) PriorityDepths() [numPriorities]struct{ Depth, Callers int } {
	s.mu.Lock()
	defer s.mu.Unlock()

	var result [numPriorities]struct{ Depth, Callers int }
	for p := High; p < numPriorities; p++ {
		for _, lane := range s.lanes[p] {
			d := lane.Items.Len()
			if d > 0 {
				result[p].Depth += d
				result[p].Callers++
			}
		}
	}
	return result
}

// RequeueAtHead re-inserts an item at the front of its lane after a 429.
// Used by the dispatcher for one-time requeue on upstream overload.
func (s *Scheduler) RequeueAtHead(item *QueueItem) {
	s.mu.Lock()
	defer s.mu.Unlock()

	p := item.EffectivePriority
	callerID := item.CallerID
	lane, ok := s.lanes[p][callerID]
	if !ok {
		lane = &Lane{Items: list.New()}
		s.lanes[p][callerID] = lane
		s.rebuildKeys(p)
	}
	lane.Items.PushFront(item)
	s.totalDepth.Add(1)

	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// --- internal helpers ---

// applyAging promotes head items that have waited past their aging threshold.
// Caller must hold mu.
func (s *Scheduler) applyAging() {
	now := s.clock()
	for p := Normal; p < numPriorities; p++ {
		for callerID, lane := range s.lanes[p] {
			if lane.Items.Len() == 0 {
				continue
			}
			head := lane.Items.Front().Value.(*QueueItem)
			threshold := s.cfg.AgingThreshold(head.EffectivePriority)
			if threshold > 0 && now.Sub(head.EnqueuedAt) >= threshold {
				// Promote head item to one priority class higher.
				lane.Items.Remove(lane.Items.Front())
				s.totalDepth.Add(-1)
				if lane.Items.Len() == 0 {
					delete(s.lanes[p], callerID)
					s.rebuildKeys(p)
				}

				promoted := Priority(p - 1)
				head.EffectivePriority = promoted
				targetLane, ok := s.lanes[promoted][head.CallerID]
				if !ok {
					targetLane = &Lane{Items: list.New()}
					s.lanes[promoted][head.CallerID] = targetLane
					s.rebuildKeys(promoted)
				}
				targetLane.Items.PushBack(head)
				s.totalDepth.Add(1)
			}
		}
	}
}

// pruneHead removes cancelled or expired items from the front of a lane.
// Caller must hold mu.
func (s *Scheduler) pruneHead(lane *Lane, p Priority, callerID string) {
	now := s.clock()
	for lane.Items.Len() > 0 {
		head := lane.Items.Front().Value.(*QueueItem)
		if head.Ctx.Err() != nil || now.After(head.Deadline) {
			lane.Items.Remove(lane.Items.Front())
			s.totalDepth.Add(-1)
			// Signal the caller that their request was dropped.
			if head.Ctx.Err() == nil {
				head.Err = fmt.Errorf("queue_timeout: waited %v in %s lane",
					now.Sub(head.EnqueuedAt).Round(time.Millisecond), p)
				head.StatusCode = 504
			}
			close(head.Done)
		} else {
			break
		}
	}
	if lane.Items.Len() == 0 {
		delete(s.lanes[p], callerID)
		s.rebuildKeys(p)
	}
}

// shortestOfFirstK scans the first K items in a lane and removes+returns
// the one with the smallest EstPromptTokens. Caller must hold mu.
func (s *Scheduler) shortestOfFirstK(lane *Lane) *QueueItem {
	k := s.cfg.TiebreakScanK
	if k <= 1 || lane.Items.Len() <= 1 {
		item := lane.Items.Front().Value.(*QueueItem)
		lane.Items.Remove(lane.Items.Front())
		return item
	}

	var (
		bestElem   *list.Element
		bestItem   *QueueItem
		bestTokens int
		scanned    int
	)

	for e := lane.Items.Front(); e != nil && scanned < k; e = e.Next() {
		item := e.Value.(*QueueItem)
		if bestElem == nil || item.EstPromptTokens < bestTokens {
			bestElem = e
			bestItem = item
			bestTokens = item.EstPromptTokens
		}
		scanned++
	}

	lane.Items.Remove(bestElem)
	return bestItem
}

// rebuildKeys rebuilds the sorted caller key list for a priority level.
// Caller must hold mu.
func (s *Scheduler) rebuildKeys(p Priority) {
	lanes := s.lanes[p]
	keys := make([]string, 0, len(lanes))
	for k := range lanes {
		keys = append(keys, k)
	}
	s.rrKeys[p] = keys
	if s.rrIndex[p] >= len(keys) {
		s.rrIndex[p] = 0
	}
}
