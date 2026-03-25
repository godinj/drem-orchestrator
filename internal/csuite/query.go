package csuite

import (
	"fmt"

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
	var agents []CsuiteAgent
	// COALESCE nil heartbeats to epoch so they sort last in DESC order.
	err := q.db.Order("COALESCE(heartbeat_at, '0001-01-01') DESC").
		Find(&agents).Error
	if err != nil {
		return nil, fmt.Errorf("agents with latest heartbeat: %w", err)
	}
	return agents, nil
}

// UnreadMessageCounts returns a map of agent name to unread (non-archived)
// message count for messages addressed to that agent.
func (q *DashboardQuery) UnreadMessageCounts() (map[string]int, error) {
	type countRow struct {
		ToAgent string
		Count   int
	}
	var rows []countRow
	err := q.db.Model(&CsuiteInboxMessage{}).
		Select("to_agent, COUNT(*) as count").
		Where("archived = ?", false).
		Group("to_agent").
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("unread message counts: %w", err)
	}
	result := make(map[string]int, len(rows))
	for _, r := range rows {
		result[r.ToAgent] = r.Count
	}
	return result, nil
}

// AgentSummaries returns all agents enriched with their unread message
// counts, ordered by most recent heartbeat first. This is a composite
// query combining AgentsWithLatestHeartbeat and UnreadMessageCounts.
func (q *DashboardQuery) AgentSummaries() ([]AgentSummary, error) {
	agents, err := q.AgentsWithLatestHeartbeat()
	if err != nil {
		return nil, fmt.Errorf("agent summaries: %w", err)
	}

	counts, err := q.UnreadMessageCounts()
	if err != nil {
		return nil, fmt.Errorf("agent summaries counts: %w", err)
	}

	summaries := make([]AgentSummary, len(agents))
	for i, a := range agents {
		summaries[i] = AgentSummary{
			CsuiteAgent: a,
			UnreadCount: counts[a.Name],
		}
	}
	return summaries, nil
}
