package watcher

import (
	"sync"
	"time"
)

type schedulerActionKind int

const (
	schedulerNoop schedulerActionKind = iota
	schedulerStartNow
	schedulerStartAfter
)

type schedulerAction struct {
	kind  schedulerActionKind
	delay time.Duration
}

type scheduledAgent struct {
	running bool
	queued  bool
	delayed bool
	lastEnd time.Time
}

// turnScheduler owns the in-process trigger state for each agent. It decides
// whether a trigger may start immediately, queue behind a running turn, wait for
// cooldown, or be refused. Callers own all side effects that execute a turn.
type turnScheduler struct {
	mu              sync.Mutex
	allowed         map[string]struct{}
	agents          map[string]*scheduledAgent
	cooldown        time.Duration
	now             func() time.Time
	duplicateResult TriggerResult
}

func newTurnScheduler(allowedAgents []string, cooldown time.Duration, duplicateResult TriggerResult) *turnScheduler {
	var allowed map[string]struct{}
	if allowedAgents != nil {
		allowed = make(map[string]struct{}, len(allowedAgents))
		for _, agent := range allowedAgents {
			allowed[agent] = struct{}{}
		}
	}
	return &turnScheduler{
		allowed:         allowed,
		agents:          make(map[string]*scheduledAgent),
		cooldown:        cooldown,
		now:             time.Now,
		duplicateResult: duplicateResult,
	}
}

func (s *turnScheduler) Trigger(agent string) (TriggerResult, schedulerAction) {
	if !s.isAllowed(agent) {
		return Refused, schedulerAction{}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	entry := s.agents[agent]
	if entry != nil && entry.running {
		if entry.delayed {
			return Cooldown, schedulerAction{}
		}
		if !entry.queued {
			entry.queued = true
			return Queued, schedulerAction{}
		}
		return s.duplicateResult, schedulerAction{}
	}

	if entry != nil {
		if remaining := s.remainingCooldown(entry); remaining > 0 {
			entry.running = true
			entry.delayed = true
			return Cooldown, schedulerAction{kind: schedulerStartAfter, delay: remaining}
		}
	}

	s.agents[agent] = &scheduledAgent{running: true}
	return Started, schedulerAction{kind: schedulerStartNow}
}

func (s *turnScheduler) Complete(agent string) schedulerAction {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry := s.agents[agent]
	if entry == nil {
		return schedulerAction{}
	}

	if entry.queued {
		entry.queued = false
		if s.cooldown > 0 {
			entry.lastEnd = s.now()
			entry.delayed = true
			return schedulerAction{kind: schedulerStartAfter, delay: s.cooldown}
		}
		return schedulerAction{kind: schedulerStartNow}
	}

	if s.cooldown > 0 {
		entry.running = false
		entry.lastEnd = s.now()
		return schedulerAction{}
	}

	delete(s.agents, agent)
	return schedulerAction{}
}

func (s *turnScheduler) DelayElapsed(agent string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry := s.agents[agent]
	if entry == nil || !entry.delayed {
		return false
	}
	entry.delayed = false
	return true
}

func (s *turnScheduler) Cancel(agent string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.agents, agent)
}

func (s *turnScheduler) isAllowed(agent string) bool {
	if s.allowed == nil {
		return agent != kyle
	}
	_, ok := s.allowed[agent]
	return ok
}

func (s *turnScheduler) remainingCooldown(entry *scheduledAgent) time.Duration {
	if s.cooldown == 0 || entry.lastEnd.IsZero() {
		return 0
	}
	remaining := s.cooldown - s.now().Sub(entry.lastEnd)
	if remaining <= 0 {
		return 0
	}
	return remaining
}
