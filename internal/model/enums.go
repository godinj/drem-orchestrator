// Package model defines GORM models, enums, and custom JSON types for the
// Drem Orchestrator database layer.
package model

import "fmt"

// TaskStatus represents the lifecycle state of a task.
type TaskStatus string

const (
	StatusClassifying        TaskStatus = "classifying"
	StatusBacklog            TaskStatus = "backlog"
	StatusPlanning           TaskStatus = "planning"
	StatusNeedsClarification TaskStatus = "needs_clarification"
	StatusPlanReview         TaskStatus = "plan_review"
	StatusTestWriting        TaskStatus = "test_writing"
	StatusTestReview         TaskStatus = "test_review"
	StatusInProgress         TaskStatus = "in_progress"
	StatusTestingReady       TaskStatus = "testing_ready"
	StatusMerging            TaskStatus = "merging"
	StatusPaused             TaskStatus = "paused"
	StatusDone               TaskStatus = "done"
	StatusFailed             TaskStatus = "failed"
	StatusRejected           TaskStatus = "rejected"
)

// allTaskStatuses lists every valid TaskStatus value for parsing.
var allTaskStatuses = []TaskStatus{
	StatusClassifying,
	StatusBacklog,
	StatusPlanning,
	StatusNeedsClarification,
	StatusPlanReview,
	StatusTestWriting,
	StatusTestReview,
	StatusInProgress,
	StatusTestingReady,
	StatusMerging,
	StatusPaused,
	StatusDone,
	StatusFailed,
	StatusRejected,
}

// String returns the string representation of a TaskStatus.
func (s TaskStatus) String() string {
	return string(s)
}

// IsActionable returns true for statuses where the orchestrator can take
// automated action (scheduling agents, merging, etc.).
func (s TaskStatus) IsActionable() bool {
	switch s {
	case StatusClassifying, StatusBacklog, StatusPlanning, StatusInProgress, StatusTestWriting, StatusMerging:
		return true
	default:
		return false
	}
}

// IsHumanGate returns true for statuses that require human approval before
// the task can proceed.
func (s TaskStatus) IsHumanGate() bool {
	switch s {
	case StatusPlanReview, StatusTestReview, StatusTestingReady, StatusNeedsClarification:
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

// TaskCategory distinguishes the lifecycle pipeline a task follows.
type TaskCategory string

const (
	CategoryStandard TaskCategory = "standard"
	CategoryQuickFix TaskCategory = "quickfix"
)

// allTaskCategories lists every valid TaskCategory value for parsing.
var allTaskCategories = []TaskCategory{CategoryStandard, CategoryQuickFix}

// String returns the string representation of a TaskCategory.
func (c TaskCategory) String() string {
	return string(c)
}

// ParseTaskCategory converts a raw string to a TaskCategory, returning an error
// if the string does not match any known value.
func ParseTaskCategory(s string) (TaskCategory, error) {
	for _, tc := range allTaskCategories {
		if string(tc) == s {
			return tc, nil
		}
	}
	return "", fmt.Errorf("unknown task category: %q", s)
}

// IsQuickFix returns true if the category is quickfix.
func (c TaskCategory) IsQuickFix() bool {
	return c == CategoryQuickFix
}

// AgentType identifies the role of a Claude Code agent.
type AgentType string

const (
	AgentOrchestrator AgentType = "orchestrator"
	AgentPlanner      AgentType = "planner"
	AgentCoder        AgentType = "coder"
	AgentResearcher   AgentType = "researcher"
	AgentReviewer     AgentType = "reviewer"
	AgentFixer        AgentType = "fixer"
	AgentClassifier   AgentType = "classifier"
	AgentPrep         AgentType = "prep"
)

// allAgentTypes lists every valid AgentType value for parsing.
var allAgentTypes = []AgentType{
	AgentOrchestrator,
	AgentPlanner,
	AgentCoder,
	AgentResearcher,
	AgentReviewer,
	AgentFixer,
	AgentClassifier,
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

// String returns the string representation of an AgentStatus.
func (s AgentStatus) String() string {
	return string(s)
}

// ExitReason constants for agent completion outcomes.
const (
	ExitReasonSuccess      = "success"       // Agent completed successfully
	ExitReasonContextLimit = "context_limit" // Context window limit reached
	ExitReasonError        = "error"         // Agent encountered an error
	ExitReasonKilled       = "killed"        // Agent was forcibly terminated
	ExitReasonTimeout      = "timeout"       // Agent exceeded timeout
	ExitReasonDefault      = "unknown"       // Default when exit info is nil
)
