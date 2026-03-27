package watcher

import "context"

// EventDeliveryTrigger reads agent name lists from a channel and calls
// TriggerAgent for each agent, skipping "kyle" (Kyle exception).
// Each notification is processed fully before the next is consumed.
//
// Use NewEventDeliveryTrigger to construct and Run to start.
type EventDeliveryTrigger struct {
	notifications <-chan []string
	triggerer     AgentTriggerer
}

// NewEventDeliveryTrigger creates an EventDeliveryTrigger that reads agent
// name lists from notifications and wakes each agent via triggerer.
func NewEventDeliveryTrigger(notifications <-chan []string, triggerer AgentTriggerer) *EventDeliveryTrigger {
	return &EventDeliveryTrigger{
		notifications: notifications,
		triggerer:     triggerer,
	}
}

// Run reads from the notifications channel until ctx is cancelled.
// For each notification, it calls triggerer.TriggerAgent for every agent
// that is not "kyle". Notifications are processed sequentially: the next
// notification is not consumed until all agents in the current one have
// been triggered.
func (t *EventDeliveryTrigger) Run(ctx context.Context) {
	// stub — implementation pending
}
