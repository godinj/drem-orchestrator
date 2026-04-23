package orchestrator

import (
	"encoding/json"
	"time"

	"github.com/godinj/drem-orchestrator/internal/eventbus"
)

// csuiteAgents is the fixed set of C-Suite agents that receive event deliveries.
var csuiteAgents = []string{"kyle", "mike", "alex", "seth"}

// publishTaskTransition publishes a task status change event to the C-Suite
// event bus and creates delivery records for all known agents. Errors are
// logged but never returned — event bus emission must not disrupt task
// processing.
func (o *Orchestrator) publishTaskTransition(taskID, fromStatus, toStatus, details string) {
	if o.bus == nil {
		return
	}

	event := &eventbus.Event{
		Type:       "task_status_changed",
		Source:     "orchestrator",
		TaskID:     taskID,
		FromStatus: fromStatus,
		ToStatus:   toStatus,
		Details:    details,
		CreatedAt:  time.Now(),
	}

	if err := o.bus.Publish(event); err != nil {
		o.logger.Error("event bus: publish task transition",
			"task_id", taskID, "from", fromStatus, "to", toStatus, "error", err)
		return
	}

	o.deliverToAllAgents(event.ID, taskID)
}

// publishAgentStatus publishes an agent status change event to the C-Suite
// event bus. Used to notify C-Suite agents when orchestrator agents start
// working or die.
func (o *Orchestrator) publishAgentStatus(taskID, agentID, agentType, status string) {
	if o.bus == nil {
		return
	}

	detailsMap := map[string]string{
		"agent_id":   agentID,
		"agent_type": agentType,
	}
	detailsJSON, _ := json.Marshal(detailsMap)

	event := &eventbus.Event{
		Type:      "agent_status_changed",
		Source:    "orchestrator",
		TaskID:    taskID,
		ToStatus:  status,
		Details:   string(detailsJSON),
		CreatedAt: time.Now(),
	}

	if err := o.bus.Publish(event); err != nil {
		o.logger.Error("event bus: publish agent status",
			"task_id", taskID, "agent_id", agentID, "error", err)
		return
	}

	o.deliverToAllAgents(event.ID, taskID)
}

// deliverToAllAgents creates EventDelivery rows for every known C-Suite agent.
func (o *Orchestrator) deliverToAllAgents(eventID, taskID string) {
	for _, agent := range csuiteAgents {
		if err := o.bus.Deliver(eventID, agent); err != nil {
			o.logger.Error("event bus: deliver",
				"event_id", eventID, "task_id", taskID, "agent", agent, "error", err)
		}
	}
}
