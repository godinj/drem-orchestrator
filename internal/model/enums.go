// Package model defines GORM models, enums, and custom JSON types for the
// Drem Orchestrator database layer.
package model

import "fmt"

// TaskStatus represents the lifecycle state of a task.
type TaskStatus string

const (
	StatusBacklog       TaskStatus = "backlog"
	StatusPlanning      TaskStatus = "planning"
	StatusPlanReview    TaskStatus = "plan_review"
	StatusInProgress    TaskStatus = "in_progress"
	StatusTestingReady  TaskStatus = "testing_ready"
	StatusManualTesting TaskStatus = "manual_testing"
	StatusMerging       TaskStatus = "merging"
	StatusPaused        TaskStatus = "paused"
	StatusDone          TaskStatus = "done"
	StatusFailed        TaskStatus = "failed"
)

// allTaskStatuses lists every valid TaskStatus value for parsing.
var allTaskStatuses = []TaskStatus{
	StatusBacklog,
	StatusPlanning,
	StatusPlanReview,
	StatusInProgress,
	StatusTestingReady,
	StatusManualTesting,
	StatusMerging,
	StatusPaused,
	StatusDone,
	StatusFailed,
}

// String returns the string representation of a TaskStatus.
func (s TaskStatus) String() string {
	return string(s)
}

// IsActionable returns true for statuses where the orchestrator can take
// automated action (scheduling agents, merging, etc.).
func (s TaskStatus) IsActionable() bool {
	switch s {
	case StatusBacklog, StatusPlanning, StatusInProgress, StatusMerging:
		return true
	default:
		return false
	}
}

// IsHumanGate returns true for statuses that require human approval before
// the task can proceed.
func (s TaskStatus) IsHumanGate() bool {
	switch s {
	case StatusPlanReview, StatusTestingReady, StatusManualTesting:
		return true
	default:
		return false
	}
}

// ParseTaskStatus converts a raw string to a TaskStatus, returning an error
// if the string does not match any known value.
func ParseTaskStatus(s string) (TaskStatus, error) {
	for _, ts := range allTaskStatuses {
		if string(ts) == s {
			return ts, nil
		}
	}
	return "", fmt.Errorf("unknown task status: %q", s)
}

// AgentType identifies the role of a Claude Code agent.
type AgentType string

const (
	AgentOrchestrator AgentType = "orchestrator"
	AgentPlanner      AgentType = "planner"
	AgentCoder        AgentType = "coder"
	AgentResearcher   AgentType = "researcher"
)

// allAgentTypes lists every valid AgentType value for parsing.
var allAgentTypes = []AgentType{
	AgentOrchestrator,
	AgentPlanner,
	AgentCoder,
	AgentResearcher,
}

// String returns the string representation of an AgentType.
func (t AgentType) String() string {
	return string(t)
}

// ParseAgentType converts a raw string to an AgentType, returning an error
// if the string does not match any known value.
func ParseAgentType(s string) (AgentType, error) {
	for _, at := range allAgentTypes {
		if string(at) == s {
			return at, nil
		}
	}
	return "", fmt.Errorf("unknown agent type: %q", s)
}

// AgentStatus represents the current operational state of an agent.
type AgentStatus string

const (
	AgentIdle    AgentStatus = "idle"
	AgentWorking AgentStatus = "working"
	AgentBlocked AgentStatus = "blocked"
	AgentDead    AgentStatus = "dead"
)

// allAgentStatuses lists every valid AgentStatus value for parsing.
var allAgentStatuses = []AgentStatus{
	AgentIdle,
	AgentWorking,
	AgentBlocked,
	AgentDead,
}

// String returns the string representation of an AgentStatus.
func (s AgentStatus) String() string {
	return string(s)
}

// ParseAgentStatus converts a raw string to an AgentStatus, returning an
// error if the string does not match any known value.
func ParseAgentStatus(s string) (AgentStatus, error) {
	for _, as := range allAgentStatuses {
		if string(as) == s {
			return as, nil
		}
	}
	return "", fmt.Errorf("unknown agent status: %q", s)
}
