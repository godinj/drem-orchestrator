package csuite

import (
	"gorm.io/gorm"
)

// AgentSummary enriches a CsuiteAgent with its unread message count.
type AgentSummary struct {
	CsuiteAgent
	UnreadCount int
}

// DashboardQuery provides bulk read operations for dashboard rendering.
// It aggregates data across CsuiteAgent and CsuiteInboxMessage tables
// to serve the C-Suite TUI with pre-joined, display-ready data.
type DashboardQuery struct {
	db *gorm.DB
}

// NewDashboardQuery creates a DashboardQuery backed by the given database.
func NewDashboardQuery(db *gorm.DB) *DashboardQuery {
	return &DashboardQuery{db: db}
}

// AgentsWithLatestHeartbeat returns all agents ordered by most recent
// heartbeat first. Agents with nil heartbeats sort last.
func (q *DashboardQuery) AgentsWithLatestHeartbeat() ([]CsuiteAgent, error) {
	return nil, nil
}

// UnreadMessageCounts returns a map of agent name to unread (non-archived)
// message count for messages addressed to that agent.
func (q *DashboardQuery) UnreadMessageCounts() (map[string]int, error) {
	return nil, nil
}

// AgentSummaries returns all agents enriched with their unread message
// counts, ordered by most recent heartbeat first. This is a composite
// query combining AgentsWithLatestHeartbeat and UnreadMessageCounts.
func (q *DashboardQuery) AgentSummaries() ([]AgentSummary, error) {
	return nil, nil
}
