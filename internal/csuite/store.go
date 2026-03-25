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
	return fmt.Errorf("not implemented")
}

// GetAgentByName fetches an agent by its unique name.
// Returns gorm.ErrRecordNotFound when no match exists.
func (s *Store) GetAgentByName(name string) (*CsuiteAgent, error) {
	return nil, fmt.Errorf("not implemented")
}

// ListAgents returns all agents ordered by name.
func (s *Store) ListAgents() ([]CsuiteAgent, error) {
	return nil, fmt.Errorf("not implemented")
}

// UpdateAgent saves changes to an existing agent. The agent must already
// exist (non-nil ID). Returns gorm.ErrRecordNotFound if the ID is unknown.
func (s *Store) UpdateAgent(agent *CsuiteAgent) error {
	return fmt.Errorf("not implemented")
}

// DeleteAgent removes an agent by ID.
// Returns gorm.ErrRecordNotFound if the ID is unknown.
func (s *Store) DeleteAgent(id uuid.UUID) error {
	return fmt.Errorf("not implemented")
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
	return fmt.Errorf("not implemented")
}

// GetMessagesByAgent returns all non-archived messages addressed to the named
// agent, ordered by creation time descending (newest first).
func (s *Store) GetMessagesByAgent(agentName string) ([]CsuiteInboxMessage, error) {
	return nil, fmt.Errorf("not implemented")
}

// ListUnreadMessages returns all non-archived messages across all agents,
// ordered by priority (high→normal→low) then creation time descending.
func (s *Store) ListUnreadMessages() ([]CsuiteInboxMessage, error) {
	return nil, fmt.Errorf("not implemented")
}

// ArchiveMessage transitions a message to the archived state. This is a
// one-way state transition — archived messages cannot be un-archived.
// Returns gorm.ErrRecordNotFound if the ID is unknown and an error if the
// message is already archived.
func (s *Store) ArchiveMessage(id uuid.UUID) error {
	return fmt.Errorf("not implemented")
}

// DeleteMessage removes a message by ID.
// Returns gorm.ErrRecordNotFound if the ID is unknown.
func (s *Store) DeleteMessage(id uuid.UUID) error {
	return fmt.Errorf("not implemented")
}

// ---------------------------------------------------------------------------
// Purge
// ---------------------------------------------------------------------------

// PurgeArchivedMessages deletes archived messages whose UpdatedAt is older
// than the given retention duration. Deletion happens in batches of
// PurgeBatchSize to limit transaction size. Returns a PurgeResult with
// observability data.
func (s *Store) PurgeArchivedMessages(retention time.Duration) (*PurgeResult, error) {
	return nil, fmt.Errorf("not implemented")
}

// ---------------------------------------------------------------------------
// Query service — dashboard aggregation
// ---------------------------------------------------------------------------

// AgentDashboard returns one AgentDashboardRow per agent, including unread
// message count and latest inbound message timestamp. This is a single query
// (or minimal query set) designed for dashboard rendering — callers should
// not need to issue separate CRUD calls to assemble the same data.
func (s *Store) AgentDashboard() ([]AgentDashboardRow, error) {
	return nil, fmt.Errorf("not implemented")
}

// UnreadCountByAgent returns a map of agent name → unread (non-archived)
// message count for all agents that have at least one unread message.
func (s *Store) UnreadCountByAgent() (map[string]int, error) {
	return nil, fmt.Errorf("not implemented")
}
