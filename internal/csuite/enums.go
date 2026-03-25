// Package csuite defines GORM models and enums for C-Suite agent monitoring.
package csuite

import "fmt"

// AgentMonStatus represents the operational state of a C-Suite monitored agent.
type AgentMonStatus string

const (
	AgentMonOnline  AgentMonStatus = "online"
	AgentMonOffline AgentMonStatus = "offline"
	AgentMonStale   AgentMonStatus = "stale"
)

var allAgentMonStatuses = []AgentMonStatus{
	AgentMonOnline,
	AgentMonOffline,
	AgentMonStale,
}

// String returns the string representation of an AgentMonStatus.
func (s AgentMonStatus) String() string {
	return string(s)
}

// ParseAgentMonStatus converts a raw string to an AgentMonStatus.
func ParseAgentMonStatus(s string) (AgentMonStatus, error) {
	for _, v := range allAgentMonStatuses {
		if string(v) == s {
			return v, nil
		}
	}
	return "", fmt.Errorf("invalid AgentMonStatus: %q", s)
}

// InboxPriority represents the priority level of a C-Suite inbox message.
type InboxPriority string

const (
	PriorityLow    InboxPriority = "low"
	PriorityNormal InboxPriority = "normal"
	PriorityHigh   InboxPriority = "high"
)

var allInboxPriorities = []InboxPriority{
	PriorityLow,
	PriorityNormal,
	PriorityHigh,
}

// String returns the string representation of an InboxPriority.
func (p InboxPriority) String() string {
	return string(p)
}

// ParseInboxPriority converts a raw string to an InboxPriority.
func ParseInboxPriority(s string) (InboxPriority, error) {
	for _, v := range allInboxPriorities {
		if string(v) == s {
			return v, nil
		}
	}
	return "", fmt.Errorf("invalid InboxPriority: %q", s)
}

// InboxMessageType represents the type of a C-Suite inbox message.
type InboxMessageType string

const (
	MessageTypeStatus  InboxMessageType = "status"
	MessageTypeRequest InboxMessageType = "request"
	MessageTypeAlert   InboxMessageType = "alert"
)

var allInboxMessageTypes = []InboxMessageType{
	MessageTypeStatus,
	MessageTypeRequest,
	MessageTypeAlert,
}

// String returns the string representation of an InboxMessageType.
func (m InboxMessageType) String() string {
	return string(m)
}

// ParseInboxMessageType converts a raw string to an InboxMessageType.
func ParseInboxMessageType(s string) (InboxMessageType, error) {
	for _, v := range allInboxMessageTypes {
		if string(v) == s {
			return v, nil
		}
	}
	return "", fmt.Errorf("invalid InboxMessageType: %q", s)
}
