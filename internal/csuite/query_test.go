package csuite_test

import (
	"testing"
	"time"

	"github.com/godinj/drem-orchestrator/internal/csuite"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

// --- AgentsWithLatestHeartbeat ---

func TestAgentsWithLatestHeartbeat_OrdersByMostRecentFirst(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &csuite.CsuiteAgent{}, &csuite.CsuiteInboxMessage{})
	q := csuite.NewDashboardQuery(db)

	now := time.Now().Truncate(time.Second)
	old := now.Add(-1 * time.Hour)
	recent := now.Add(-5 * time.Minute)

	ag1 := testutil.CreateCsuiteAgent(t, db, "agent-old", csuite.AgentMonOnline)
	db.Model(&ag1).Update("heartbeat_at", old)

	ag2 := testutil.CreateCsuiteAgent(t, db, "agent-recent", csuite.AgentMonOnline)
	db.Model(&ag2).Update("heartbeat_at", recent)

	ag3 := testutil.CreateCsuiteAgent(t, db, "agent-now", csuite.AgentMonOnline)
	db.Model(&ag3).Update("heartbeat_at", now)

	agents, err := q.AgentsWithLatestHeartbeat()
	if err != nil {
		t.Fatalf("AgentsWithLatestHeartbeat: %v", err)
	}

	if len(agents) != 3 {
		t.Fatalf("got %d agents, want 3", len(agents))
	}
	if agents[0].Name != "agent-now" {
		t.Errorf("agents[0].Name = %q, want %q", agents[0].Name, "agent-now")
	}
	if agents[1].Name != "agent-recent" {
		t.Errorf("agents[1].Name = %q, want %q", agents[1].Name, "agent-recent")
	}
	if agents[2].Name != "agent-old" {
		t.Errorf("agents[2].Name = %q, want %q", agents[2].Name, "agent-old")
	}
}

func TestAgentsWithLatestHeartbeat_NilHeartbeatsSortLast(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &csuite.CsuiteAgent{}, &csuite.CsuiteInboxMessage{})
	q := csuite.NewDashboardQuery(db)

	now := time.Now().Truncate(time.Second)

	// Agent with heartbeat.
	ag1 := testutil.CreateCsuiteAgent(t, db, "agent-with-hb", csuite.AgentMonOnline)
	db.Model(&ag1).Update("heartbeat_at", now)

	// Agent without heartbeat (nil).
	testutil.CreateCsuiteAgent(t, db, "agent-no-hb", csuite.AgentMonOffline)

	agents, err := q.AgentsWithLatestHeartbeat()
	if err != nil {
		t.Fatalf("AgentsWithLatestHeartbeat: %v", err)
	}

	if len(agents) != 2 {
		t.Fatalf("got %d agents, want 2", len(agents))
	}
	if agents[0].Name != "agent-with-hb" {
		t.Errorf("agents[0].Name = %q, want %q (heartbeat should sort first)", agents[0].Name, "agent-with-hb")
	}
	if agents[1].Name != "agent-no-hb" {
		t.Errorf("agents[1].Name = %q, want %q (nil heartbeat should sort last)", agents[1].Name, "agent-no-hb")
	}
}

func TestAgentsWithLatestHeartbeat_EmptyTable(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &csuite.CsuiteAgent{}, &csuite.CsuiteInboxMessage{})
	q := csuite.NewDashboardQuery(db)

	agents, err := q.AgentsWithLatestHeartbeat()
	if err != nil {
		t.Fatalf("AgentsWithLatestHeartbeat: %v", err)
	}
	if len(agents) != 0 {
		t.Errorf("got %d agents, want 0 for empty table", len(agents))
	}
}

func TestAgentsWithLatestHeartbeat_PreservesAllFields(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &csuite.CsuiteAgent{}, &csuite.CsuiteInboxMessage{})
	q := csuite.NewDashboardQuery(db)

	now := time.Now().Truncate(time.Second)
	ag := testutil.CreateCsuiteAgent(t, db, "field-check", csuite.AgentMonStale)
	db.Model(&ag).Updates(map[string]any{
		"heartbeat_at":    now,
		"context_percent": 75,
		"current_activity": "testing fields",
	})

	agents, err := q.AgentsWithLatestHeartbeat()
	if err != nil {
		t.Fatalf("AgentsWithLatestHeartbeat: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("got %d agents, want 1", len(agents))
	}

	got := agents[0]
	if got.Status != csuite.AgentMonStale {
		t.Errorf("Status = %q, want %q", got.Status, csuite.AgentMonStale)
	}
	if got.ContextPercent != 75 {
		t.Errorf("ContextPercent = %d, want 75", got.ContextPercent)
	}
	if got.CurrentActivity != "testing fields" {
		t.Errorf("CurrentActivity = %q, want %q", got.CurrentActivity, "testing fields")
	}
}

// --- UnreadMessageCounts ---

func TestUnreadMessageCounts_CountsNonArchivedPerAgent(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &csuite.CsuiteAgent{}, &csuite.CsuiteInboxMessage{})
	q := csuite.NewDashboardQuery(db)

	// 2 unread messages to "coder", 1 to "planner".
	testutil.CreateCsuiteInboxMessage(t, db, "planner", "coder", "msg1", csuite.PriorityNormal, csuite.MessageTypeStatus)
	testutil.CreateCsuiteInboxMessage(t, db, "reviewer", "coder", "msg2", csuite.PriorityHigh, csuite.MessageTypeAlert)
	testutil.CreateCsuiteInboxMessage(t, db, "coder", "planner", "msg3", csuite.PriorityNormal, csuite.MessageTypeRequest)

	counts, err := q.UnreadMessageCounts()
	if err != nil {
		t.Fatalf("UnreadMessageCounts: %v", err)
	}

	if counts["coder"] != 2 {
		t.Errorf("coder unread = %d, want 2", counts["coder"])
	}
	if counts["planner"] != 1 {
		t.Errorf("planner unread = %d, want 1", counts["planner"])
	}
}

func TestUnreadMessageCounts_ExcludesArchivedMessages(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &csuite.CsuiteAgent{}, &csuite.CsuiteInboxMessage{})
	q := csuite.NewDashboardQuery(db)

	// 1 unread + 1 archived to "coder".
	testutil.CreateCsuiteInboxMessage(t, db, "planner", "coder", "unread", csuite.PriorityNormal, csuite.MessageTypeStatus)
	archived := testutil.CreateCsuiteInboxMessage(t, db, "planner", "coder", "archived", csuite.PriorityNormal, csuite.MessageTypeStatus)
	db.Model(&archived).Update("archived", true)

	counts, err := q.UnreadMessageCounts()
	if err != nil {
		t.Fatalf("UnreadMessageCounts: %v", err)
	}

	if counts["coder"] != 1 {
		t.Errorf("coder unread = %d, want 1 (archived should be excluded)", counts["coder"])
	}
}

func TestUnreadMessageCounts_EmptyTable(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &csuite.CsuiteAgent{}, &csuite.CsuiteInboxMessage{})
	q := csuite.NewDashboardQuery(db)

	counts, err := q.UnreadMessageCounts()
	if err != nil {
		t.Fatalf("UnreadMessageCounts: %v", err)
	}

	if len(counts) != 0 {
		t.Errorf("got %d entries, want 0 for empty table", len(counts))
	}
}

func TestUnreadMessageCounts_AllArchivedReturnsEmpty(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &csuite.CsuiteAgent{}, &csuite.CsuiteInboxMessage{})
	q := csuite.NewDashboardQuery(db)

	msg := testutil.CreateCsuiteInboxMessage(t, db, "a", "b", "s", csuite.PriorityNormal, csuite.MessageTypeStatus)
	db.Model(&msg).Update("archived", true)

	counts, err := q.UnreadMessageCounts()
	if err != nil {
		t.Fatalf("UnreadMessageCounts: %v", err)
	}

	if counts["b"] != 0 {
		t.Errorf("b unread = %d, want 0 (all archived)", counts["b"])
	}
}

// --- AgentSummaries ---

func TestAgentSummaries_EnrichesAgentsWithUnreadCounts(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &csuite.CsuiteAgent{}, &csuite.CsuiteInboxMessage{})
	q := csuite.NewDashboardQuery(db)

	now := time.Now().Truncate(time.Second)

	ag1 := testutil.CreateCsuiteAgent(t, db, "coder", csuite.AgentMonOnline)
	db.Model(&ag1).Update("heartbeat_at", now)

	ag2 := testutil.CreateCsuiteAgent(t, db, "planner", csuite.AgentMonOnline)
	db.Model(&ag2).Update("heartbeat_at", now.Add(-10*time.Minute))

	// 2 unread to coder, 0 to planner.
	testutil.CreateCsuiteInboxMessage(t, db, "planner", "coder", "msg1", csuite.PriorityNormal, csuite.MessageTypeStatus)
	testutil.CreateCsuiteInboxMessage(t, db, "reviewer", "coder", "msg2", csuite.PriorityHigh, csuite.MessageTypeAlert)

	summaries, err := q.AgentSummaries()
	if err != nil {
		t.Fatalf("AgentSummaries: %v", err)
	}

	if len(summaries) != 2 {
		t.Fatalf("got %d summaries, want 2", len(summaries))
	}

	// Should be ordered by heartbeat: coder (now) before planner (10min ago).
	if summaries[0].Name != "coder" {
		t.Errorf("summaries[0].Name = %q, want %q", summaries[0].Name, "coder")
	}
	if summaries[0].UnreadCount != 2 {
		t.Errorf("summaries[0].UnreadCount = %d, want 2", summaries[0].UnreadCount)
	}
	if summaries[1].Name != "planner" {
		t.Errorf("summaries[1].Name = %q, want %q", summaries[1].Name, "planner")
	}
	if summaries[1].UnreadCount != 0 {
		t.Errorf("summaries[1].UnreadCount = %d, want 0", summaries[1].UnreadCount)
	}
}

func TestAgentSummaries_EmptyDatabase(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &csuite.CsuiteAgent{}, &csuite.CsuiteInboxMessage{})
	q := csuite.NewDashboardQuery(db)

	summaries, err := q.AgentSummaries()
	if err != nil {
		t.Fatalf("AgentSummaries: %v", err)
	}
	if len(summaries) != 0 {
		t.Errorf("got %d summaries, want 0", len(summaries))
	}
}

func TestAgentSummaries_AgentWithNoMessages(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &csuite.CsuiteAgent{}, &csuite.CsuiteInboxMessage{})
	q := csuite.NewDashboardQuery(db)

	now := time.Now().Truncate(time.Second)
	ag := testutil.CreateCsuiteAgent(t, db, "lonely-agent", csuite.AgentMonOnline)
	db.Model(&ag).Update("heartbeat_at", now)

	summaries, err := q.AgentSummaries()
	if err != nil {
		t.Fatalf("AgentSummaries: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("got %d summaries, want 1", len(summaries))
	}
	if summaries[0].UnreadCount != 0 {
		t.Errorf("UnreadCount = %d, want 0 for agent with no messages", summaries[0].UnreadCount)
	}
}

func TestAgentSummaries_ExcludesArchivedFromCounts(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &csuite.CsuiteAgent{}, &csuite.CsuiteInboxMessage{})
	q := csuite.NewDashboardQuery(db)

	now := time.Now().Truncate(time.Second)
	ag := testutil.CreateCsuiteAgent(t, db, "coder", csuite.AgentMonOnline)
	db.Model(&ag).Update("heartbeat_at", now)

	testutil.CreateCsuiteInboxMessage(t, db, "planner", "coder", "unread", csuite.PriorityNormal, csuite.MessageTypeStatus)
	archived := testutil.CreateCsuiteInboxMessage(t, db, "planner", "coder", "archived", csuite.PriorityNormal, csuite.MessageTypeStatus)
	db.Model(&archived).Update("archived", true)

	summaries, err := q.AgentSummaries()
	if err != nil {
		t.Fatalf("AgentSummaries: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("got %d summaries, want 1", len(summaries))
	}
	if summaries[0].UnreadCount != 1 {
		t.Errorf("UnreadCount = %d, want 1 (archived excluded)", summaries[0].UnreadCount)
	}
}
