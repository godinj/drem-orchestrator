package watcher

import "log"

// FilteredEventBus abstracts the event bus operations needed by
// FilteredPrecheck. Implementations must return events as EventInfo
// slices. The eventbus.Bus adapter (busAdapter) in cmd/csuite-watcher
// satisfies this interface.
type FilteredEventBus interface {
	// UnackedDeliveries returns all unacked deliveries for the agent.
	UnackedDeliveries(agent string) ([]EventInfo, error)
	// UnackedDeliveriesByTypes returns unacked deliveries filtered by event type.
	UnackedDeliveriesByTypes(agent string, eventTypes []string) ([]EventInfo, error)
	// Ack acknowledges the given event IDs for the agent.
	Ack(agent string, eventIDs []string) error
}

// EventInfo is the minimal event reference needed by FilteredPrecheck.
type EventInfo struct {
	ID   string
	Type string
}

// FilteredPrecheck implements TurnPrecheck by checking the event bus for
// unacked deliveries filtered to only the event types relevant to each
// agent. Non-relevant events are auto-acked so they do not pile up.
//
// Agent-to-event-type mappings:
//
//	mike (COO):  task_status_changed
//	alex (CPO):  task_status_changed
//	seth (CTO):  task_status_changed
//	ross (HR):   task_status_changed
//	unknown:     all event types (safe fallback)
type FilteredPrecheck struct {
	inboxBaseDir string
	bus          FilteredEventBus
}

// AgentEventTypes maps agent names to the event types they care about.
// Agents not in this map receive all events (safe fallback).
var AgentEventTypes = map[string][]string{
	"mike": {"task_status_changed"},
	"alex": {"task_status_changed"},
	"seth": {"task_status_changed"},
	"ross": {"task_status_changed"},
}

// NewFilteredPrecheck creates a FilteredPrecheck that checks inbox messages
// and filtered event bus deliveries.
func NewFilteredPrecheck(inboxBaseDir string, bus FilteredEventBus) *FilteredPrecheck {
	return &FilteredPrecheck{
		inboxBaseDir: inboxBaseDir,
		bus:          bus,
	}
}

// HasWork returns true if the agent has unarchived inbox messages or unacked
// event bus deliveries of a relevant type. Non-relevant unacked events are
// auto-acked so they do not accumulate.
func (p *FilteredPrecheck) HasWork(agent string) bool {
	// Check inbox first (fast filesystem check).
	if HasInboxMessages(p.inboxBaseDir, agent) {
		return true
	}

	relevantTypes, known := AgentEventTypes[agent]
	if !known {
		// Unknown agent: fall back to checking all event types.
		allUnacked, err := p.bus.UnackedDeliveries(agent)
		if err != nil {
			log.Printf("precheck: event bus query for %s: %v", agent, err)
			return true // err on the side of running
		}
		return len(allUnacked) > 0
	}

	// Check for relevant unacked events.
	relevant, err := p.bus.UnackedDeliveriesByTypes(agent, relevantTypes)
	if err != nil {
		log.Printf("precheck: event bus filtered query for %s: %v", agent, err)
		return true // err on the side of running
	}

	// Auto-ack non-relevant events so they don't pile up.
	p.autoAckNonRelevant(agent, relevantTypes)

	return len(relevant) > 0
}

// autoAckNonRelevant acks all unacked deliveries for the agent that are NOT
// in the relevant event types list. This prevents non-relevant events from
// accumulating and triggering false positives on future checks.
func (p *FilteredPrecheck) autoAckNonRelevant(agent string, relevantTypes []string) {
	allUnacked, err := p.bus.UnackedDeliveries(agent)
	if err != nil {
		log.Printf("precheck: auto-ack query for %s: %v", agent, err)
		return
	}

	relevantSet := make(map[string]bool, len(relevantTypes))
	for _, t := range relevantTypes {
		relevantSet[t] = true
	}

	var toAck []string
	for _, ev := range allUnacked {
		if !relevantSet[ev.Type] {
			toAck = append(toAck, ev.ID)
		}
	}

	if len(toAck) > 0 {
		if err := p.bus.Ack(agent, toAck); err != nil {
			log.Printf("precheck: auto-ack non-relevant events for %s: %v", agent, err)
		}
	}
}
