package csuite_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/csuite"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

// ---------------------------------------------------------------------------
// Agent CRUD
// ---------------------------------------------------------------------------

func TestCreateAgent(t *testing.T) {
	store := testutil.NewTestStore(t)

	agent := &csuite.CsuiteAgent{
		Name:   "planner-01",
		Status: csuite.AgentMonOnline,
	}
	err := store.CreateAgent(agent)
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	if agent.ID == uuid.Nil {
		t.Error("expected agent ID to be set after create")
	}
	if agent.CreatedAt.IsZero() {
		t.Error("expected CreatedAt to be set")
	}
}

func TestCreateAgent_DuplicateName(t *testing.T) {
	store := testutil.NewTestStore(t)

	a1 := &csuite.CsuiteAgent{Name: "dup-agent", Status: csuite.AgentMonOffline}
	if err := store.CreateAgent(a1); err != nil {
		t.Fatalf("first create: %v", err)
	}

	a2 := &csuite.CsuiteAgent{Name: "dup-agent", Status: csuite.AgentMonOnline}
	err := store.CreateAgent(a2)
	if err == nil {
		t.Fatal("expected error for duplicate agent name, got nil")
	}
}

func TestCreateAgent_EmptyName(t *testing.T) {
	store := testutil.NewTestStore(t)

	err := store.CreateAgent(&csuite.CsuiteAgent{Name: "", Status: csuite.AgentMonOffline})
	if err == nil {
		t.Fatal("expected error for empty agent name, got nil")
	}
}

func TestGetAgentByName(t *testing.T) {
	store := testutil.NewTestStore(t)

	orig := &csuite.CsuiteAgent{Name: "coder-02", Status: csuite.AgentMonStale}
	if err := store.CreateAgent(orig); err != nil {
		t.Fatalf("setup: %v", err)
	}

	got, err := store.GetAgentByName("coder-02")
	if err != nil {
		t.Fatalf("GetAgentByName: %v", err)
	}
	if got.ID != orig.ID {
		t.Errorf("ID mismatch: got %v, want %v", got.ID, orig.ID)
	}
	if got.Status != csuite.AgentMonStale {
		t.Errorf("Status mismatch: got %v, want %v", got.Status, csuite.AgentMonStale)
	}
}

func TestGetAgentByName_NotFound(t *testing.T) {
	store := testutil.NewTestStore(t)

	_, err := store.GetAgentByName("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent agent, got nil")
	}
	if err != gorm.ErrRecordNotFound {
		t.Errorf("expected gorm.ErrRecordNotFound, got %v", err)
	}
}

func TestListAgents(t *testing.T) {
	store := testutil.NewTestStore(t)

	names := []string{"alpha", "beta", "gamma"}
	for _, n := range names {
		if err := store.CreateAgent(&csuite.CsuiteAgent{Name: n, Status: csuite.AgentMonOffline}); err != nil {
			t.Fatalf("setup %s: %v", n, err)
		}
	}

	agents, err := store.ListAgents()
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(agents) != 3 {
		t.Fatalf("expected 3 agents, got %d", len(agents))
	}
	// Should be ordered by name
	if agents[0].Name != "alpha" || agents[1].Name != "beta" || agents[2].Name != "gamma" {
		t.Errorf("unexpected order: %s, %s, %s", agents[0].Name, agents[1].Name, agents[2].Name)
	}
}

func TestListAgents_Empty(t *testing.T) {
	store := testutil.NewTestStore(t)

	agents, err := store.ListAgents()
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(agents) != 0 {
		t.Errorf("expected empty list, got %d", len(agents))
	}
}

func TestUpdateAgent(t *testing.T) {
	store := testutil.NewTestStore(t)

	agent := &csuite.CsuiteAgent{Name: "updatable", Status: csuite.AgentMonOffline}
	if err := store.CreateAgent(agent); err != nil {
		t.Fatalf("setup: %v", err)
	}

	now := time.Now()
	agent.Status = csuite.AgentMonOnline
	agent.HeartbeatAt = &now
	agent.ContextPercent = 42
	agent.CurrentActivity = "running tests"

	if err := store.UpdateAgent(agent); err != nil {
		t.Fatalf("UpdateAgent: %v", err)
	}

	got, err := store.GetAgentByName("updatable")
	if err != nil {
		t.Fatalf("re-fetch: %v", err)
	}
	if got.Status != csuite.AgentMonOnline {
		t.Errorf("Status not updated: got %v", got.Status)
	}
	if got.ContextPercent != 42 {
		t.Errorf("ContextPercent not updated: got %d", got.ContextPercent)
	}
	if got.CurrentActivity != "running tests" {
		t.Errorf("CurrentActivity not updated: got %q", got.CurrentActivity)
	}
}

func TestUpdateAgent_NotFound(t *testing.T) {
	store := testutil.NewTestStore(t)

	agent := &csuite.CsuiteAgent{ID: uuid.New(), Name: "ghost"}
	err := store.UpdateAgent(agent)
	if err == nil {
		t.Fatal("expected error updating nonexistent agent, got nil")
	}
}

func TestDeleteAgent(t *testing.T) {
	store := testutil.NewTestStore(t)

	agent := &csuite.CsuiteAgent{Name: "doomed", Status: csuite.AgentMonOffline}
	if err := store.CreateAgent(agent); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if err := store.DeleteAgent(agent.ID); err != nil {
		t.Fatalf("DeleteAgent: %v", err)
	}

	_, err := store.GetAgentByName("doomed")
	if err != gorm.ErrRecordNotFound {
		t.Errorf("expected not found after delete, got %v", err)
	}
}

func TestDeleteAgent_NotFound(t *testing.T) {
	store := testutil.NewTestStore(t)

	err := store.DeleteAgent(uuid.New())
	if err == nil {
		t.Fatal("expected error deleting nonexistent agent, got nil")
	}
}

// ---------------------------------------------------------------------------
// Message CRUD
// ---------------------------------------------------------------------------

func TestCreateMessage(t *testing.T) {
	store := testutil.NewTestStore(t)

	msg := &csuite.CsuiteInboxMessage{
		FromAgent: "planner",
		ToAgent:   "coder",
		Subject:   "plan approved",
		Body:      "Go ahead with implementation",
		Priority:  csuite.PriorityHigh,
		Type:      csuite.MessageTypeRequest,
	}
	if err := store.CreateMessage(msg); err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	if msg.ID == uuid.Nil {
		t.Error("expected message ID to be set")
	}
	if msg.Archived {
		t.Error("new message should not be archived")
	}
}

func TestCreateMessage_MissingFields(t *testing.T) {
	store := testutil.NewTestStore(t)

	cases := []struct {
		name string
		msg  csuite.CsuiteInboxMessage
	}{
		{"empty from", csuite.CsuiteInboxMessage{ToAgent: "a", Subject: "s"}},
		{"empty to", csuite.CsuiteInboxMessage{FromAgent: "a", Subject: "s"}},
		{"empty subject", csuite.CsuiteInboxMessage{FromAgent: "a", ToAgent: "b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := store.CreateMessage(&tc.msg)
			if err == nil {
				t.Errorf("expected error for %s, got nil", tc.name)
			}
		})
	}
}

func TestGetMessagesByAgent(t *testing.T) {
	store := testutil.NewTestStore(t)

	// Create messages to different agents
	for i, to := range []string{"coder", "coder", "reviewer"} {
		msg := &csuite.CsuiteInboxMessage{
			FromAgent: "planner",
			ToAgent:   to,
			Subject:   "msg-" + string(rune('A'+i)),
			Priority:  csuite.PriorityNormal,
			Type:      csuite.MessageTypeStatus,
		}
		if err := store.CreateMessage(msg); err != nil {
			t.Fatalf("setup msg %d: %v", i, err)
		}
	}

	msgs, err := store.GetMessagesByAgent("coder")
	if err != nil {
		t.Fatalf("GetMessagesByAgent: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages for coder, got %d", len(msgs))
	}

	// Should be newest first
	if msgs[0].CreatedAt.Before(msgs[1].CreatedAt) {
		t.Error("expected newest first ordering")
	}
}

func TestGetMessagesByAgent_ExcludesArchived(t *testing.T) {
	store := testutil.NewTestStore(t)

	msg := &csuite.CsuiteInboxMessage{
		FromAgent: "a",
		ToAgent:   "b",
		Subject:   "will be archived",
		Priority:  csuite.PriorityNormal,
		Type:      csuite.MessageTypeStatus,
	}
	if err := store.CreateMessage(msg); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.ArchiveMessage(msg.ID); err != nil {
		t.Fatalf("archive: %v", err)
	}

	msgs, err := store.GetMessagesByAgent("b")
	if err != nil {
		t.Fatalf("GetMessagesByAgent: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("expected 0 messages after archiving, got %d", len(msgs))
	}
}

func TestListUnreadMessages(t *testing.T) {
	store := testutil.NewTestStore(t)

	priorities := []csuite.InboxPriority{
		csuite.PriorityLow,
		csuite.PriorityHigh,
		csuite.PriorityNormal,
	}
	for i, p := range priorities {
		msg := &csuite.CsuiteInboxMessage{
			FromAgent: "sender",
			ToAgent:   "receiver",
			Subject:   "msg-" + string(rune('A'+i)),
			Priority:  p,
			Type:      csuite.MessageTypeStatus,
		}
		if err := store.CreateMessage(msg); err != nil {
			t.Fatalf("setup msg %d: %v", i, err)
		}
	}

	msgs, err := store.ListUnreadMessages()
	if err != nil {
		t.Fatalf("ListUnreadMessages: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgs))
	}

	// Ordered: high, normal, low
	if msgs[0].Priority != csuite.PriorityHigh {
		t.Errorf("first message should be high priority, got %v", msgs[0].Priority)
	}
	if msgs[1].Priority != csuite.PriorityNormal {
		t.Errorf("second message should be normal priority, got %v", msgs[1].Priority)
	}
	if msgs[2].Priority != csuite.PriorityLow {
		t.Errorf("third message should be low priority, got %v", msgs[2].Priority)
	}
}

func TestListUnreadMessages_ExcludesArchived(t *testing.T) {
	store := testutil.NewTestStore(t)

	msg := &csuite.CsuiteInboxMessage{
		FromAgent: "a",
		ToAgent:   "b",
		Subject:   "archived msg",
		Priority:  csuite.PriorityNormal,
		Type:      csuite.MessageTypeStatus,
	}
	if err := store.CreateMessage(msg); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.ArchiveMessage(msg.ID); err != nil {
		t.Fatalf("archive: %v", err)
	}

	msgs, err := store.ListUnreadMessages()
	if err != nil {
		t.Fatalf("ListUnreadMessages: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("expected 0 unread after archive, got %d", len(msgs))
	}
}

// ---------------------------------------------------------------------------
// Message state-machine transitions
// ---------------------------------------------------------------------------

func TestArchiveMessage(t *testing.T) {
	store := testutil.NewTestStore(t)

	msg := &csuite.CsuiteInboxMessage{
		FromAgent: "a",
		ToAgent:   "b",
		Subject:   "to archive",
		Priority:  csuite.PriorityNormal,
		Type:      csuite.MessageTypeStatus,
	}
	if err := store.CreateMessage(msg); err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := store.ArchiveMessage(msg.ID); err != nil {
		t.Fatalf("ArchiveMessage: %v", err)
	}

	// Verify the message is now excluded from unread queries
	msgs, err := store.GetMessagesByAgent("b")
	if err != nil {
		t.Fatalf("GetMessagesByAgent: %v", err)
	}
	if len(msgs) != 0 {
		t.Error("archived message should not appear in GetMessagesByAgent")
	}
}

func TestArchiveMessage_AlreadyArchived(t *testing.T) {
	store := testutil.NewTestStore(t)

	msg := &csuite.CsuiteInboxMessage{
		FromAgent: "a",
		ToAgent:   "b",
		Subject:   "already done",
		Priority:  csuite.PriorityNormal,
		Type:      csuite.MessageTypeStatus,
	}
	if err := store.CreateMessage(msg); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.ArchiveMessage(msg.ID); err != nil {
		t.Fatalf("first archive: %v", err)
	}

	// Second archive should error — one-way transition
	err := store.ArchiveMessage(msg.ID)
	if err == nil {
		t.Fatal("expected error archiving already-archived message, got nil")
	}
}

func TestArchiveMessage_NotFound(t *testing.T) {
	store := testutil.NewTestStore(t)

	err := store.ArchiveMessage(uuid.New())
	if err == nil {
		t.Fatal("expected error archiving nonexistent message, got nil")
	}
}

func TestDeleteMessage(t *testing.T) {
	store := testutil.NewTestStore(t)

	msg := &csuite.CsuiteInboxMessage{
		FromAgent: "a",
		ToAgent:   "b",
		Subject:   "to delete",
		Priority:  csuite.PriorityNormal,
		Type:      csuite.MessageTypeStatus,
	}
	if err := store.CreateMessage(msg); err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := store.DeleteMessage(msg.ID); err != nil {
		t.Fatalf("DeleteMessage: %v", err)
	}

	msgs, err := store.GetMessagesByAgent("b")
	if err != nil {
		t.Fatalf("GetMessagesByAgent: %v", err)
	}
	if len(msgs) != 0 {
		t.Error("deleted message should not appear")
	}
}

func TestDeleteMessage_NotFound(t *testing.T) {
	store := testutil.NewTestStore(t)

	err := store.DeleteMessage(uuid.New())
	if err == nil {
		t.Fatal("expected error deleting nonexistent message, got nil")
	}
}

// ---------------------------------------------------------------------------
// Purge
// ---------------------------------------------------------------------------

func TestPurgeArchivedMessages(t *testing.T) {
	store := testutil.NewTestStore(t)
	db := testutil.NewTestDBWithModels(t,
		&csuite.CsuiteAgent{},
		&csuite.CsuiteInboxMessage{},
	)
	store = csuite.NewStore(db)

	// Create and archive two messages, then backdate one beyond retention
	old := &csuite.CsuiteInboxMessage{
		FromAgent: "a", ToAgent: "b", Subject: "old",
		Priority: csuite.PriorityNormal, Type: csuite.MessageTypeStatus,
	}
	recent := &csuite.CsuiteInboxMessage{
		FromAgent: "a", ToAgent: "b", Subject: "recent",
		Priority: csuite.PriorityNormal, Type: csuite.MessageTypeStatus,
	}
	for _, m := range []*csuite.CsuiteInboxMessage{old, recent} {
		if err := store.CreateMessage(m); err != nil {
			t.Fatalf("create: %v", err)
		}
		if err := store.ArchiveMessage(m.ID); err != nil {
			t.Fatalf("archive: %v", err)
		}
	}

	// Backdate the "old" message to 10 days ago via raw SQL
	tenDaysAgo := time.Now().Add(-10 * 24 * time.Hour)
	db.Model(&csuite.CsuiteInboxMessage{}).Where("id = ?", old.ID).
		Update("updated_at", tenDaysAgo)

	result, err := store.PurgeArchivedMessages(csuite.DefaultPurgeRetention)
	if err != nil {
		t.Fatalf("PurgeArchivedMessages: %v", err)
	}

	if result.Deleted != 1 {
		t.Errorf("expected 1 deleted, got %d", result.Deleted)
	}
	if result.Elapsed <= 0 {
		t.Error("expected positive elapsed duration")
	}
	if result.CutoffAt.IsZero() {
		t.Error("expected non-zero cutoff time")
	}
}

func TestPurgeArchivedMessages_SkipsNonArchived(t *testing.T) {
	store := testutil.NewTestStore(t)
	db := testutil.NewTestDBWithModels(t,
		&csuite.CsuiteAgent{},
		&csuite.CsuiteInboxMessage{},
	)
	store = csuite.NewStore(db)

	// Create a non-archived message and backdate it
	msg := &csuite.CsuiteInboxMessage{
		FromAgent: "a", ToAgent: "b", Subject: "not archived",
		Priority: csuite.PriorityNormal, Type: csuite.MessageTypeStatus,
	}
	if err := store.CreateMessage(msg); err != nil {
		t.Fatalf("create: %v", err)
	}

	tenDaysAgo := time.Now().Add(-10 * 24 * time.Hour)
	db.Model(&csuite.CsuiteInboxMessage{}).Where("id = ?", msg.ID).
		Update("updated_at", tenDaysAgo)

	result, err := store.PurgeArchivedMessages(csuite.DefaultPurgeRetention)
	if err != nil {
		t.Fatalf("PurgeArchivedMessages: %v", err)
	}
	if result.Deleted != 0 {
		t.Errorf("expected 0 deleted (non-archived), got %d", result.Deleted)
	}
}

func TestPurgeArchivedMessages_CustomRetention(t *testing.T) {
	store := testutil.NewTestStore(t)
	db := testutil.NewTestDBWithModels(t,
		&csuite.CsuiteAgent{},
		&csuite.CsuiteInboxMessage{},
	)
	store = csuite.NewStore(db)

	msg := &csuite.CsuiteInboxMessage{
		FromAgent: "a", ToAgent: "b", Subject: "short retention",
		Priority: csuite.PriorityNormal, Type: csuite.MessageTypeStatus,
	}
	if err := store.CreateMessage(msg); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.ArchiveMessage(msg.ID); err != nil {
		t.Fatalf("archive: %v", err)
	}

	// Backdate to 2 hours ago
	twoHoursAgo := time.Now().Add(-2 * time.Hour)
	db.Model(&csuite.CsuiteInboxMessage{}).Where("id = ?", msg.ID).
		Update("updated_at", twoHoursAgo)

	// Purge with 1-hour retention — should delete
	result, err := store.PurgeArchivedMessages(1 * time.Hour)
	if err != nil {
		t.Fatalf("PurgeArchivedMessages: %v", err)
	}
	if result.Deleted != 1 {
		t.Errorf("expected 1 deleted with 1h retention, got %d", result.Deleted)
	}
}

func TestPurgeArchivedMessages_NothingToPurge(t *testing.T) {
	store := testutil.NewTestStore(t)

	result, err := store.PurgeArchivedMessages(csuite.DefaultPurgeRetention)
	if err != nil {
		t.Fatalf("PurgeArchivedMessages: %v", err)
	}
	if result.Deleted != 0 {
		t.Errorf("expected 0 deleted on empty DB, got %d", result.Deleted)
	}
}

// ---------------------------------------------------------------------------
// Query service — dashboard aggregation
// ---------------------------------------------------------------------------

func TestAgentDashboard(t *testing.T) {
	db := testutil.NewTestDBWithModels(t,
		&csuite.CsuiteAgent{},
		&csuite.CsuiteInboxMessage{},
	)
	store := csuite.NewStore(db)

	// Create two agents
	a1 := &csuite.CsuiteAgent{Name: "agent-a", Status: csuite.AgentMonOnline}
	a2 := &csuite.CsuiteAgent{Name: "agent-b", Status: csuite.AgentMonOffline}
	for _, a := range []*csuite.CsuiteAgent{a1, a2} {
		if err := store.CreateAgent(a); err != nil {
			t.Fatalf("create agent: %v", err)
		}
	}

	// Send 2 unread messages to agent-a, 1 to agent-b, archive 1 of agent-a's
	msgs := []struct {
		to      string
		subject string
		archive bool
	}{
		{"agent-a", "msg1", false},
		{"agent-a", "msg2", true},
		{"agent-b", "msg3", false},
	}
	for _, m := range msgs {
		msg := &csuite.CsuiteInboxMessage{
			FromAgent: "sender", ToAgent: m.to, Subject: m.subject,
			Priority: csuite.PriorityNormal, Type: csuite.MessageTypeStatus,
		}
		if err := store.CreateMessage(msg); err != nil {
			t.Fatalf("create msg: %v", err)
		}
		if m.archive {
			if err := store.ArchiveMessage(msg.ID); err != nil {
				t.Fatalf("archive msg: %v", err)
			}
		}
	}

	rows, err := store.AgentDashboard()
	if err != nil {
		t.Fatalf("AgentDashboard: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}

	// Build a lookup by name
	byName := make(map[string]csuite.AgentDashboardRow)
	for _, r := range rows {
		byName[r.Agent.Name] = r
	}

	if byName["agent-a"].UnreadCount != 1 {
		t.Errorf("agent-a unread: got %d, want 1", byName["agent-a"].UnreadCount)
	}
	if byName["agent-b"].UnreadCount != 1 {
		t.Errorf("agent-b unread: got %d, want 1", byName["agent-b"].UnreadCount)
	}
}

func TestAgentDashboard_Empty(t *testing.T) {
	store := testutil.NewTestStore(t)

	rows, err := store.AgentDashboard()
	if err != nil {
		t.Fatalf("AgentDashboard: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("expected 0 rows on empty DB, got %d", len(rows))
	}
}

func TestUnreadCountByAgent(t *testing.T) {
	db := testutil.NewTestDBWithModels(t,
		&csuite.CsuiteAgent{},
		&csuite.CsuiteInboxMessage{},
	)
	store := csuite.NewStore(db)

	// 3 messages to coder (1 archived), 1 to reviewer
	for _, m := range []struct {
		to      string
		archive bool
	}{
		{"coder", false},
		{"coder", false},
		{"coder", true},
		{"reviewer", false},
	} {
		msg := &csuite.CsuiteInboxMessage{
			FromAgent: "planner", ToAgent: m.to, Subject: "x",
			Priority: csuite.PriorityNormal, Type: csuite.MessageTypeStatus,
		}
		if err := store.CreateMessage(msg); err != nil {
			t.Fatalf("create: %v", err)
		}
		if m.archive {
			if err := store.ArchiveMessage(msg.ID); err != nil {
				t.Fatalf("archive: %v", err)
			}
		}
	}

	counts, err := store.UnreadCountByAgent()
	if err != nil {
		t.Fatalf("UnreadCountByAgent: %v", err)
	}

	if counts["coder"] != 2 {
		t.Errorf("coder unread: got %d, want 2", counts["coder"])
	}
	if counts["reviewer"] != 1 {
		t.Errorf("reviewer unread: got %d, want 1", counts["reviewer"])
	}
}

func TestUnreadCountByAgent_Empty(t *testing.T) {
	store := testutil.NewTestStore(t)

	counts, err := store.UnreadCountByAgent()
	if err != nil {
		t.Fatalf("UnreadCountByAgent: %v", err)
	}
	if len(counts) != 0 {
		t.Errorf("expected empty map, got %d entries", len(counts))
	}
}
