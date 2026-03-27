package watcher

import "time"

const defaultSafetyInterval = 5 * time.Minute

// AgentTriggerer schedules an agent turn and reports the outcome.
// Both Deduplicator and LifecycleManager satisfy this interface.
type AgentTriggerer interface {
	TriggerAgent(agent string) TriggerResult
}

// SafetyTimer periodically calls TriggerAgent("mike") at a fixed interval.
// It is a safety net ensuring Mike processes operational state even if
// signal/event triggers miss something. Kyle is never triggered.
//
// Use NewSafetyTimer to create a SafetyTimer, then call Start to begin.
// Call Stop to halt the timer; Stop blocks until the background goroutine exits.
type SafetyTimer struct {
	interval  time.Duration
	triggerer AgentTriggerer
	stop      chan struct{}
	done      chan struct{}
}

// NewSafetyTimer creates a SafetyTimer that wakes "mike" at interval.
// If interval is 0, the default of 5 minutes is used.
func NewSafetyTimer(interval time.Duration, triggerer AgentTriggerer) *SafetyTimer {
	if interval == 0 {
		interval = defaultSafetyInterval
	}
	return &SafetyTimer{
		interval:  interval,
		triggerer: triggerer,
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
	}
}

// Start begins the periodic safety timer in the background. The first tick
// fires after one full interval has elapsed (not immediately). Start must
// not be called more than once.
func (s *SafetyTimer) Start() {
	go s.run()
}

// Stop halts the timer and waits for the background goroutine to exit.
func (s *SafetyTimer) Stop() {
	close(s.stop)
	<-s.done
}

// run is the background goroutine. It fires TriggerAgent("mike") on each tick
// until Stop is called.
func (s *SafetyTimer) run() {
	defer close(s.done)
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.triggerer.TriggerAgent("mike")
		case <-s.stop:
			return
		}
	}
}
