package csuite

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// DefaultPurgeRetention is the default age after which archived messages are
// eligible for purge. Callers may override this per-call via PurgeArchivedMessages.
const DefaultPurgeRetention = 7 * 24 * time.Hour

// PurgeBatchSize controls how many rows are deleted per batch to avoid long
// transactions and lock contention.
const PurgeBatchSize = 500

// PurgeResult reports what PurgeArchivedMessages did, enabling observability.
type PurgeResult struct {
	Deleted  int
	Elapsed  time.Duration
	CutoffAt time.Time
}

// AgentDashboardRow is a projection returned by the query service — one row
// per agent with their latest heartbeat and unread message count.
type AgentDashboardRow struct {
	Agent       CsuiteAgent
	UnreadCount int
	LatestInbox *time.Time // most recent inbound message CreatedAt, nil if none
}

// Store provides CRUD and query operations for csuite models.
// It wraps a *gorm.DB and is the single entry point for all csuite
// persistence. Methods are organised into agent CRUD, message CRUD with
// state-machine transitions, age-based purge, and dashboard queries.
type Store struct {
	db *gorm.DB
}

// NewStore creates a Store backed by the given database connection.
func NewStore(db *gorm.DB) *Store {
	return &Store{db: db}
}

// ---------------------------------------------------------------------------
// Agent CRUD
// ---------------------------------------------------------------------------

// CreateAgent persists a new CsuiteAgent. Returns an error if the name is
// empty or already taken.
func (s *Store) CreateAgent(agent *CsuiteAgent) error {
	if agent.Name == "" {
		return fmt.Errorf("agent name must not be empty")
	}
	if agent.ID == uuid.Nil {
		agent.ID = uuid.New()
	}
	if err := s.db.Create(agent).Error; err != nil {
		return fmt.Errorf("create agent %q: %w", agent.Name, err)
	}
	return nil
}

// GetAgentByName fetches an agent by its unique name.
// Returns gorm.ErrRecordNotFound when no match exists.
func (s *Store) GetAgentByName(name string) (*CsuiteAgent, error) {
	var agent CsuiteAgent
	if err := s.db.Where("name = ?", name).First(&agent).Error; err != nil {
		return nil, err
	}
	return &agent, nil
}

// ListAgents returns all agents ordered by name.
func (s *Store) ListAgents() ([]CsuiteAgent, error) {
	var agents []CsuiteAgent
	if err := s.db.Order("name").Find(&agents).Error; err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}
	return agents, nil
}

// UpdateAgent saves changes to an existing agent. The agent must already
// exist (non-nil ID). Returns gorm.ErrRecordNotFound if the ID is unknown.
func (s *Store) UpdateAgent(agent *CsuiteAgent) error {
	var existing CsuiteAgent
	if err := s.db.First(&existing, "id = ?", agent.ID).Error; err != nil {
		return err
	}
	if err := s.db.Save(agent).Error; err != nil {
		return fmt.Errorf("update agent %q: %w", agent.Name, err)
	}
	return nil
}

// DeleteAgent removes an agent by ID.
// Returns gorm.ErrRecordNotFound if the ID is unknown.
func (s *Store) DeleteAgent(id uuid.UUID) error {
	result := s.db.Delete(&CsuiteAgent{}, "id = ?", id)
	if result.Error != nil {
		return fmt.Errorf("delete agent: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// ---------------------------------------------------------------------------
// Message CRUD with state-machine transitions
// ---------------------------------------------------------------------------

// CreateMessage persists a new inbox message. FromAgent, ToAgent, and Subject
// must be non-empty.
func (s *Store) CreateMessage(msg *CsuiteInboxMessage) error {
	if msg.FromAgent == "" || msg.ToAgent == "" || msg.Subject == "" {
		return fmt.Errorf("from_agent, to_agent, and subject must not be empty")
	}
	if msg.ID == uuid.Nil {
		msg.ID = uuid.New()
	}
	msg.Archived = false
	if err := s.db.Create(msg).Error; err != nil {
		return fmt.Errorf("create message: %w", err)
	}
	return nil
}

// GetMessagesByAgent returns all non-archived messages addressed to the named
// agent, ordered by creation time descending (newest first).
func (s *Store) GetMessagesByAgent(agentName string) ([]CsuiteInboxMessage, error) {
	var msgs []CsuiteInboxMessage
	err := s.db.Where("to_agent = ? AND archived = ?", agentName, false).
		Order("created_at DESC").
		Find(&msgs).Error
	if err != nil {
		return nil, fmt.Errorf("get messages by agent %q: %w", agentName, err)
	}
	return msgs, nil
}

// ListUnreadMessages returns all non-archived messages across all agents,
// ordered by priority (high->normal->low) then creation time descending.
func (s *Store) ListUnreadMessages() ([]CsuiteInboxMessage, error) {
	var msgs []CsuiteInboxMessage
	// SQLite CASE expression to map priority strings to sort order.
	priorityCase := `CASE priority
		WHEN 'high' THEN 0
		WHEN 'normal' THEN 1
		WHEN 'low' THEN 2
		ELSE 3
	END`
	err := s.db.Where("archived = ?", false).
		Order(priorityCase).
		Order("created_at DESC").
		Find(&msgs).Error
	if err != nil {
		return nil, fmt.Errorf("list unread messages: %w", err)
	}
	return msgs, nil
}

// ArchiveMessage transitions a message to the archived state. This is a
// one-way state transition — archived messages cannot be un-archived.
// Returns gorm.ErrRecordNotFound if the ID is unknown and an error if the
// message is already archived.
func (s *Store) ArchiveMessage(id uuid.UUID) error {
	var msg CsuiteInboxMessage
	if err := s.db.First(&msg, "id = ?", id).Error; err != nil {
		return err
	}
	if msg.Archived {
		return fmt.Errorf("message %s is already archived", id)
	}
	if err := s.db.Model(&msg).Update("archived", true).Error; err != nil {
		return fmt.Errorf("archive message %s: %w", id, err)
	}
	return nil
}

// DeleteMessage removes a message by ID.
// Returns gorm.ErrRecordNotFound if the ID is unknown.
func (s *Store) DeleteMessage(id uuid.UUID) error {
	result := s.db.Delete(&CsuiteInboxMessage{}, "id = ?", id)
	if result.Error != nil {
		return fmt.Errorf("delete message: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// ---------------------------------------------------------------------------
// Purge
// ---------------------------------------------------------------------------

// PurgeArchivedMessages deletes archived messages whose UpdatedAt is older
// than the given retention duration. Deletion happens in batches of
// PurgeBatchSize to limit transaction size. Returns a PurgeResult with
// observability data.
func (s *Store) PurgeArchivedMessages(retention time.Duration) (*PurgeResult, error) {
	start := time.Now()
	cutoff := start.Add(-retention)

	var totalDeleted int
	for {
		result := s.db.Where("archived = ? AND updated_at < ?", true, cutoff).
			Limit(PurgeBatchSize).
			Delete(&CsuiteInboxMessage{})
		if result.Error != nil {
			return nil, fmt.Errorf("purge archived messages: %w", result.Error)
		}
		totalDeleted += int(result.RowsAffected)
		if result.RowsAffected < PurgeBatchSize {
			break
		}
	}

	return &PurgeResult{
		Deleted:  totalDeleted,
		Elapsed:  time.Since(start),
		CutoffAt: cutoff,
	}, nil
}

// ---------------------------------------------------------------------------
// Query service — message queries
// ---------------------------------------------------------------------------

// GetMessagesBetween returns messages exchanged between agent1 and agent2,
// ordered by created_at DESC, id DESC (newest first with deterministic tie
// break). If beforeID is non-zero, only messages older than that full sort key
// are returned. limit caps the result set; pass 0 to use the default (50).
func (s *Store) GetMessagesBetween(agent1, agent2 string, limit int, beforeID uuid.UUID) ([]CsuiteInboxMessage, error) {
	const defaultLimit = 50
	if limit <= 0 {
		limit = defaultLimit
	}

	q := s.db.Where(
		"(from_agent = ? AND to_agent = ?) OR (from_agent = ? AND to_agent = ?)",
		agent1, agent2, agent2, agent1,
	).Order("created_at DESC, id DESC").Limit(limit)

	if beforeID != uuid.Nil {
		var cursor CsuiteInboxMessage
		if err := s.db.First(&cursor, "id = ?", beforeID).Error; err != nil {
			return nil, fmt.Errorf("get cursor message %s: %w", beforeID, err)
		}
		q = q.Where("created_at < ? OR (created_at = ? AND id < ?)", cursor.CreatedAt, cursor.CreatedAt, cursor.ID)
	}

	var msgs []CsuiteInboxMessage
	if err := q.Find(&msgs).Error; err != nil {
		return nil, fmt.Errorf("get messages between %q and %q: %w", agent1, agent2, err)
	}
	return msgs, nil
}

// GetMessageCountByAgent returns the total number of messages (sent or
// received, including archived) for the named agent.
func (s *Store) GetMessageCountByAgent(scopedTo string) (int, error) {
	var count int64
	err := s.db.Model(&CsuiteInboxMessage{}).
		Where("from_agent = ? OR to_agent = ?", scopedTo, scopedTo).
		Count(&count).Error
	if err != nil {
		return 0, fmt.Errorf("message count for agent %q: %w", scopedTo, err)
	}
	return int(count), nil
}

// ---------------------------------------------------------------------------
// Query service — dashboard aggregation
// ---------------------------------------------------------------------------

// AgentDashboard returns one AgentDashboardRow per agent, including unread
// message count and latest inbound message timestamp. This is a single query
// (or minimal query set) designed for dashboard rendering — callers should
// not need to issue separate CRUD calls to assemble the same data.
func (s *Store) AgentDashboard() ([]AgentDashboardRow, error) {
	agents, err := s.ListAgents()
	if err != nil {
		return nil, fmt.Errorf("agent dashboard: %w", err)
	}

	counts, err := s.UnreadCountByAgent()
	if err != nil {
		return nil, fmt.Errorf("agent dashboard unread counts: %w", err)
	}

	// Fetch latest inbox time per agent in a single query.
	type latestRow struct {
		ToAgent  string
		LatestAt string
	}
	var latestRows []latestRow
	err = s.db.Model(&CsuiteInboxMessage{}).
		Select("to_agent, MAX(created_at) as latest_at").
		Group("to_agent").
		Find(&latestRows).Error
	if err != nil {
		return nil, fmt.Errorf("agent dashboard latest inbox: %w", err)
	}
	latestMap := make(map[string]time.Time, len(latestRows))
	for _, r := range latestRows {
		if t, parseErr := time.Parse("2006-01-02 15:04:05.999999999-07:00", r.LatestAt); parseErr == nil {
			latestMap[r.ToAgent] = t
		} else if t, parseErr = time.Parse(time.RFC3339Nano, r.LatestAt); parseErr == nil {
			latestMap[r.ToAgent] = t
		}
	}

	rows := make([]AgentDashboardRow, len(agents))
	for i, a := range agents {
		row := AgentDashboardRow{
			Agent:       a,
			UnreadCount: counts[a.Name],
		}
		if t, ok := latestMap[a.Name]; ok {
			ts := t
			row.LatestInbox = &ts
		}
		rows[i] = row
	}
	return rows, nil
}

// UnreadCountByAgent returns a map of agent name -> unread (non-archived)
// message count for all agents that have at least one unread message.
func (s *Store) UnreadCountByAgent() (map[string]int, error) {
	type countRow struct {
		ToAgent string
		Count   int
	}
	var rows []countRow
	err := s.db.Model(&CsuiteInboxMessage{}).
		Select("to_agent, COUNT(*) as count").
		Where("archived = ?", false).
		Group("to_agent").
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("unread count by agent: %w", err)
	}
	result := make(map[string]int, len(rows))
	for _, r := range rows {
		result[r.ToAgent] = r.Count
	}
	return result, nil
}
