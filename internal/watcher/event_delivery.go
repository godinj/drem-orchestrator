package watcher

import (
	"context"
	"sync"
	"time"
)

const defaultRecipientTriggerTimeout = 30 * time.Second

// EventDeliveryTrigger reads agent name lists from a channel and calls
// TriggerAgent for each agent, skipping "kyle" (Kyle exception).
// Each notification is processed fully before the next is consumed.
//
// Use NewEventDeliveryTrigger to construct and Run to start.
type EventDeliveryTrigger struct {
	notifications    <-chan []string
	triggerer        AgentTriggerer
	recipientTimeout time.Duration
}

// NewEventDeliveryTrigger creates an EventDeliveryTrigger that reads agent
// name lists from notifications and wakes each agent via triggerer.
func NewEventDeliveryTrigger(notifications <-chan []string, triggerer AgentTriggerer) *EventDeliveryTrigger {
	return &EventDeliveryTrigger{
		notifications:    notifications,
		triggerer:        triggerer,
		recipientTimeout: defaultRecipientTriggerTimeout,
	}
}

// SetRecipientTimeout overrides the maximum time Run waits for one recipient's
// TriggerAgent call before allowing later notification batches to proceed.
func (t *EventDeliveryTrigger) SetRecipientTimeout(timeout time.Duration) {
	t.recipientTimeout = timeout
}

// Run reads from the notifications channel until ctx is cancelled.
// For each notification, it calls triggerer.TriggerAgent for every agent
// that is not "kyle". Recipients in the same notification are fanned out
// concurrently so one slow trigger cannot delay the rest of the batch.
// The next notification is not consumed until all recipients complete or hit
// the per-recipient timeout.
func (t *EventDeliveryTrigger) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case agents, ok := <-t.notifications:
			if !ok {
				return
			}
			t.triggerAgents(agents)
		}
	}
}

func (t *EventDeliveryTrigger) triggerAgents(agents []string) {
	var wg sync.WaitGroup
	for _, agent := range agents {
		if agent == "kyle" {
			continue
		}
		agent := agent
		wg.Add(1)
		go func() {
			defer wg.Done()
			t.triggerAgentWithTimeout(agent)
		}()
	}
	wg.Wait()
}

func (t *EventDeliveryTrigger) triggerAgentWithTimeout(agent string) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		t.triggerer.TriggerAgent(agent)
	}()

	timeout := t.recipientTimeout
	if timeout <= 0 {
		timeout = defaultRecipientTriggerTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-done:
	case <-timer.C:
	}
}
