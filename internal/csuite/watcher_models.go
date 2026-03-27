package csuite

import "time"

// AgentWatcherMetrics contains watcher-derived metrics for a single agent.
// These supplement the disk-based AgentSummary with turn history and token
// totals from the watcher's SQLite database.
type AgentWatcherMetrics struct {
	// Running indicates whether the agent has an active turn in progress
	// (a turn_metrics row with zero EndedAt). When the watcher is not
	// running, all agents show idle.
	Running bool

	// LastTurn holds a summary of the most recently completed turn.
	// Nil if the agent has never completed a turn.
	LastTurn *TurnSummary

	// TokensInTotal is the cumulative input tokens across all recorded turns.
	TokensInTotal int

	// TokensOutTotal is the cumulative output tokens across all recorded turns.
	TokensOutTotal int

	// TurnsToday is the number of turns completed since midnight (local time).
	TurnsToday int
}

// TurnSummary holds display-ready data for a single completed agent turn.
type TurnSummary struct {
	StartedAt       time.Time
	EndedAt         time.Time
	DurationMs      int
	TokensIn        int
	TokensOut       int
	EventsProcessed int
	ExitStatus      int
	ErrorDetails    string
}

// EventSummary holds display-ready data for a recent event bus entry.
type EventSummary struct {
	ID        string
	Type      string
	Source    string
	TaskID    string
	CreatedAt time.Time
}

// WatcherSnapshot captures all watcher-derived data at a point in time.
// It is produced by WatcherSource.Snapshot and included in StateSnapshot
// when the watcher database is available.
type WatcherSnapshot struct {
	// Available is true when the watcher database was successfully opened.
	Available bool

	// Metrics maps agent name to their watcher metrics.
	Metrics map[string]AgentWatcherMetrics

	// RecentEvents contains the most recent events from the event bus,
	// ordered newest first. Empty if the event bus DB is not accessible.
	RecentEvents []EventSummary

	// WatcherHeartbeat is the most recent watcher heartbeat timestamp.
	// Nil if no heartbeat was found.
	WatcherHeartbeat *time.Time
}
