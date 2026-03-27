package csuite

import (
	"fmt"
	"os"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// maxRunningTurnAge is the threshold after which a turn with zero EndedAt
// is considered stale (abandoned) rather than actively running.
const maxRunningTurnAge = 15 * time.Minute

// turnMetricRow mirrors the watcher.TurnMetric GORM model for read-only
// queries. Defined here to avoid importing the watcher package.
type turnMetricRow struct {
	Agent           string
	StartedAt       time.Time
	EndedAt         time.Time
	DurationMs      int
	TokensIn        int
	TokensOut       int
	InputTokens     int
	OutputTokens    int
	EventsProcessed int
	ExitStatus      int
	ErrorDetails    *string
}

func (turnMetricRow) TableName() string { return "turn_metrics" }

// heartbeatRow mirrors the watcher.Heartbeat model for read-only queries.
type heartbeatRow struct {
	Timestamp time.Time
}

func (heartbeatRow) TableName() string { return "heartbeats" }

// eventRow mirrors the eventbus.Event model for read-only queries.
type eventRow struct {
	ID        string    `gorm:"primaryKey"`
	EventType string    `gorm:"column:event_type"`
	Source    string    `gorm:"column:source"`
	TaskID    string    `gorm:"column:task_id"`
	CreatedAt time.Time `gorm:"column:created_at"`
}

func (eventRow) TableName() string { return "events" }

// WatcherSource reads watcher metrics from the watcher SQLite database
// and optionally event data from the event bus database. Both databases
// are opened in read-only mode. Missing databases are handled gracefully.
type WatcherSource struct {
	watcherDB  *gorm.DB
	eventBusDB *gorm.DB
}

// NewWatcherSource opens the watcher database (and optionally the event
// bus database) for read-only dashboard queries. If the watcher database
// does not exist, returns (nil, nil) -- the caller should treat a nil
// return as "watcher not available".
//
// The event bus database is best-effort: if missing, event data is
// simply omitted from snapshots.
func NewWatcherSource(watcherDBPath, eventBusDBPath string) (*WatcherSource, error) {
	if _, err := os.Stat(watcherDBPath); os.IsNotExist(err) {
		return nil, nil
	}

	wDB, err := openReadOnly(watcherDBPath)
	if err != nil {
		return nil, fmt.Errorf("open watcher DB: %w", err)
	}

	ws := &WatcherSource{watcherDB: wDB}

	if eventBusDBPath != "" {
		if _, statErr := os.Stat(eventBusDBPath); statErr == nil {
			if eDB, openErr := openReadOnly(eventBusDBPath); openErr == nil {
				ws.eventBusDB = eDB
			}
		}
	}

	return ws, nil
}

// NewWatcherSourceFromDB creates a WatcherSource from pre-opened database
// connections. This is primarily useful for testing where in-memory SQLite
// databases are used.
func NewWatcherSourceFromDB(watcherDB, eventBusDB *gorm.DB) *WatcherSource {
	return &WatcherSource{
		watcherDB:  watcherDB,
		eventBusDB: eventBusDB,
	}
}

// Snapshot produces a WatcherSnapshot with per-agent metrics, recent events,
// and the latest watcher heartbeat. All queries are read-only.
func (ws *WatcherSource) Snapshot() (*WatcherSnapshot, error) {
	snap := &WatcherSnapshot{
		Available: true,
		Metrics:   make(map[string]AgentWatcherMetrics),
	}

	if err := ws.loadTokenTotals(snap); err != nil {
		return nil, err
	}
	if err := ws.loadTurnsToday(snap); err != nil {
		return nil, err
	}
	ws.loadLastTurns(snap)
	ws.loadRunningStatus(snap)
	ws.loadHeartbeat(snap)
	ws.loadRecentEvents(snap)

	return snap, nil
}

// Close releases both database connections.
func (ws *WatcherSource) Close() error {
	var firstErr error
	if db, err := ws.watcherDB.DB(); err == nil {
		if cerr := db.Close(); cerr != nil && firstErr == nil {
			firstErr = cerr
		}
	}
	if ws.eventBusDB != nil {
		if db, err := ws.eventBusDB.DB(); err == nil {
			if cerr := db.Close(); cerr != nil && firstErr == nil {
				firstErr = cerr
			}
		}
	}
	return firstErr
}

// ---------------------------------------------------------------------------
// Internal query helpers
// ---------------------------------------------------------------------------

// loadTokenTotals populates cumulative token totals per agent.
func (ws *WatcherSource) loadTokenTotals(snap *WatcherSnapshot) error {
	type tokenSum struct {
		Agent    string
		TotalIn  int
		TotalOut int
	}
	var sums []tokenSum
	err := ws.watcherDB.Model(&turnMetricRow{}).
		Select("agent, SUM(COALESCE(tokens_in,0) + COALESCE(input_tokens,0)) as total_in, SUM(COALESCE(tokens_out,0) + COALESCE(output_tokens,0)) as total_out").
		Group("agent").
		Find(&sums).Error
	if err != nil {
		return fmt.Errorf("token sums: %w", err)
	}
	for _, s := range sums {
		m := snap.Metrics[s.Agent]
		m.TokensInTotal = s.TotalIn
		m.TokensOutTotal = s.TotalOut
		snap.Metrics[s.Agent] = m
	}
	return nil
}

// loadTurnsToday populates the number of turns completed since midnight.
func (ws *WatcherSource) loadTurnsToday(snap *WatcherSnapshot) error {
	todayStart := todayMidnight()
	type turnCount struct {
		Agent string
		Count int
	}
	var counts []turnCount
	err := ws.watcherDB.Model(&turnMetricRow{}).
		Select("agent, COUNT(*) as count").
		Where("ended_at >= ?", todayStart).
		Group("agent").
		Find(&counts).Error
	if err != nil {
		return fmt.Errorf("turns today: %w", err)
	}
	for _, c := range counts {
		m := snap.Metrics[c.Agent]
		m.TurnsToday = c.Count
		snap.Metrics[c.Agent] = m
	}
	return nil
}

// loadLastTurns populates the most recent completed turn per agent.
func (ws *WatcherSource) loadLastTurns(snap *WatcherSnapshot) {
	for _, agent := range ws.distinctAgents() {
		var row turnMetricRow
		err := ws.watcherDB.
			Where("agent = ? AND ended_at > ?", agent, time.Time{}).
			Order("ended_at DESC").
			First(&row).Error
		if err != nil {
			continue
		}
		m := snap.Metrics[agent]
		m.LastTurn = turnRowToSummary(&row)
		snap.Metrics[agent] = m
	}
}

// loadRunningStatus marks agents as running if they have a turn with zero
// EndedAt that started within maxRunningTurnAge.
func (ws *WatcherSource) loadRunningStatus(snap *WatcherSnapshot) {
	cutoff := time.Now().Add(-maxRunningTurnAge)
	var running []turnMetricRow
	err := ws.watcherDB.
		Where("ended_at = ? AND started_at > ?", time.Time{}, cutoff).
		Find(&running).Error
	if err != nil {
		return
	}
	for _, r := range running {
		m := snap.Metrics[r.Agent]
		m.Running = true
		snap.Metrics[r.Agent] = m
	}
}

// loadHeartbeat populates the most recent watcher heartbeat timestamp.
func (ws *WatcherSource) loadHeartbeat(snap *WatcherSnapshot) {
	var hb heartbeatRow
	if err := ws.watcherDB.Order("timestamp DESC").First(&hb).Error; err == nil {
		snap.WatcherHeartbeat = &hb.Timestamp
	}
}

// loadRecentEvents populates the 20 most recent events from the event bus.
func (ws *WatcherSource) loadRecentEvents(snap *WatcherSnapshot) {
	if ws.eventBusDB == nil {
		return
	}
	var events []eventRow
	err := ws.eventBusDB.Order("created_at DESC").Limit(20).Find(&events).Error
	if err != nil {
		return
	}
	snap.RecentEvents = make([]EventSummary, len(events))
	for i, e := range events {
		snap.RecentEvents[i] = EventSummary{
			ID:        e.ID,
			Type:      e.EventType,
			Source:    e.Source,
			TaskID:    e.TaskID,
			CreatedAt: e.CreatedAt,
		}
	}
}

// ---------------------------------------------------------------------------
// Utility functions
// ---------------------------------------------------------------------------

// distinctAgents returns the unique agent names from the turn_metrics table.
func (ws *WatcherSource) distinctAgents() []string {
	var agents []string
	ws.watcherDB.Model(&turnMetricRow{}).Distinct("agent").Pluck("agent", &agents)
	return agents
}

// turnRowToSummary converts a turnMetricRow to a TurnSummary, merging
// the dual token fields (TokensIn/InputTokens, TokensOut/OutputTokens).
func turnRowToSummary(row *turnMetricRow) *TurnSummary {
	ts := &TurnSummary{
		StartedAt:       row.StartedAt,
		EndedAt:         row.EndedAt,
		DurationMs:      row.DurationMs,
		TokensIn:        row.TokensIn + row.InputTokens,
		TokensOut:       row.TokensOut + row.OutputTokens,
		EventsProcessed: row.EventsProcessed,
		ExitStatus:      row.ExitStatus,
	}
	if row.ErrorDetails != nil {
		ts.ErrorDetails = *row.ErrorDetails
	}
	return ts
}

// todayMidnight returns today's date at 00:00:00 local time.
func todayMidnight() time.Time {
	now := time.Now()
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
}

// openReadOnly opens a SQLite database with WAL journal mode and silent
// logging. The busy timeout is set to 5 seconds for concurrent reads.
func openReadOnly(dbPath string) (*gorm.DB, error) {
	dsn := fmt.Sprintf("%s?_journal_mode=WAL&_busy_timeout=5000", dbPath)
	cfg := &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	}
	return gorm.Open(sqlite.Open(dsn), cfg)
}
