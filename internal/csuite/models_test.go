package csuite_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/csuite"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

func TestCsuiteAgentRoundTrip(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &csuite.CsuiteAgent{}, &csuite.CsuiteInboxMessage{})

	t.Run("create and reload preserves fields", func(t *testing.T) {
		now := time.Now().Truncate(time.Second)
		ag := csuite.CsuiteAgent{
			ID:              uuid.New(),
			Name:            "planner-alpha",
			Status:          csuite.AgentMonOnline,
			HeartbeatAt:     &now,
			ContextPercent:  42,
			CurrentActivity: "planning task #7",
		}
		if err := db.Create(&ag).Error; err != nil {
			t.Fatalf("create csuite agent: %v", err)
		}

		var loaded csuite.CsuiteAgent
		if err := db.First(&loaded, "id = ?", ag.ID).Error; err != nil {
			t.Fatalf("reload csuite agent: %v", err)
		}

		if loaded.Name != "planner-alpha" {
			t.Errorf("Name = %q, want %q", loaded.Name, "planner-alpha")
		}
		if loaded.Status != csuite.AgentMonOnline {
			t.Errorf("Status = %q, want %q", loaded.Status, csuite.AgentMonOnline)
		}
		if loaded.ContextPercent != 42 {
			t.Errorf("ContextPercent = %d, want 42", loaded.ContextPercent)
		}
		if loaded.CurrentActivity != "planning task #7" {
			t.Errorf("CurrentActivity = %q, want %q", loaded.CurrentActivity, "planning task #7")
		}
		if loaded.HeartbeatAt == nil {
			t.Fatal("HeartbeatAt is nil, want non-nil")
		}
		if !loaded.HeartbeatAt.Truncate(time.Second).Equal(now) {
			t.Errorf("HeartbeatAt = %v, want %v", loaded.HeartbeatAt, now)
		}
	})

	t.Run("auto-generates UUID when nil", func(t *testing.T) {
		ag := csuite.CsuiteAgent{
			Name:   "coder-beta",
			Status: csuite.AgentMonOffline,
		}
		if err := db.Create(&ag).Error; err != nil {
			t.Fatalf("create csuite agent: %v", err)
		}
		// UUID callback should populate ID — stubs don't register this,
		// so the test verifies the model works with the UUID callback.
		if ag.ID == uuid.Nil {
			t.Error("expected auto-generated UUID, got uuid.Nil")
		}
	})

	t.Run("preserves explicit UUID", func(t *testing.T) {
		explicit := uuid.New()
		ag := csuite.CsuiteAgent{
			ID:     explicit,
			Name:   "reviewer-gamma",
			Status: csuite.AgentMonStale,
		}
		if err := db.Create(&ag).Error; err != nil {
			t.Fatalf("create csuite agent: %v", err)
		}
		if ag.ID != explicit {
			t.Errorf("ID = %v, want %v", ag.ID, explicit)
		}
	})
}

func TestCsuiteInboxMessageRoundTrip(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &csuite.CsuiteAgent{}, &csuite.CsuiteInboxMessage{})

	t.Run("create and reload preserves fields", func(t *testing.T) {
		msg := csuite.CsuiteInboxMessage{
			ID:        uuid.New(),
			FromAgent: "planner",
			ToAgent:   "coder",
			Subject:   "Task assignment",
			Body:      "Please implement the login feature",
			Priority:  csuite.PriorityHigh,
			Type:      csuite.MessageTypeRequest,
			Archived:  false,
		}
		if err := db.Create(&msg).Error; err != nil {
			t.Fatalf("create inbox message: %v", err)
		}

		var loaded csuite.CsuiteInboxMessage
		if err := db.First(&loaded, "id = ?", msg.ID).Error; err != nil {
			t.Fatalf("reload inbox message: %v", err)
		}

		if loaded.FromAgent != "planner" {
			t.Errorf("FromAgent = %q, want %q", loaded.FromAgent, "planner")
		}
		if loaded.ToAgent != "coder" {
			t.Errorf("ToAgent = %q, want %q", loaded.ToAgent, "coder")
		}
		if loaded.Subject != "Task assignment" {
			t.Errorf("Subject = %q, want %q", loaded.Subject, "Task assignment")
		}
		if loaded.Body != "Please implement the login feature" {
			t.Errorf("Body = %q, want %q", loaded.Body, "Please implement the login feature")
		}
		if loaded.Priority != csuite.PriorityHigh {
			t.Errorf("Priority = %q, want %q", loaded.Priority, csuite.PriorityHigh)
		}
		if loaded.Type != csuite.MessageTypeRequest {
			t.Errorf("Type = %q, want %q", loaded.Type, csuite.MessageTypeRequest)
		}
		if loaded.Archived != false {
			t.Errorf("Archived = %v, want false", loaded.Archived)
		}
	})

	t.Run("archived flag persists as true", func(t *testing.T) {
		msg := csuite.CsuiteInboxMessage{
			ID:        uuid.New(),
			FromAgent: "reviewer",
			ToAgent:   "coder",
			Subject:   "Old review",
			Priority:  csuite.PriorityLow,
			Type:      csuite.MessageTypeStatus,
			Archived:  true,
		}
		if err := db.Create(&msg).Error; err != nil {
			t.Fatalf("create archived message: %v", err)
		}

		var loaded csuite.CsuiteInboxMessage
		if err := db.First(&loaded, "id = ?", msg.ID).Error; err != nil {
			t.Fatalf("reload archived message: %v", err)
		}
		if loaded.Archived != true {
			t.Errorf("Archived = %v, want true", loaded.Archived)
		}
	})
}

func TestCsuiteAgentNameUniqueness(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &csuite.CsuiteAgent{}, &csuite.CsuiteInboxMessage{})

	ag1 := csuite.CsuiteAgent{
		ID:     uuid.New(),
		Name:   "unique-agent",
		Status: csuite.AgentMonOnline,
	}
	if err := db.Create(&ag1).Error; err != nil {
		t.Fatalf("create first agent: %v", err)
	}

	ag2 := csuite.CsuiteAgent{
		ID:     uuid.New(),
		Name:   "unique-agent", // duplicate name
		Status: csuite.AgentMonOffline,
	}
	err := db.Create(&ag2).Error
	if err == nil {
		t.Error("expected uniqueness violation for duplicate Name, got nil")
	}
}

func TestCsuiteModelCoexistence(t *testing.T) {
	// Verify csuite models can coexist with core models in the same database.
	db := testutil.NewTestDBWithModels(t, &csuite.CsuiteAgent{}, &csuite.CsuiteInboxMessage{})

	// Create a core model entity.
	proj := testutil.CreateProject(t, db, "coexist-project", "/tmp/bare", "master")
	if proj.Name != "coexist-project" {
		t.Fatalf("project name = %q, want %q", proj.Name, "coexist-project")
	}

	// Create a csuite agent using the factory.
	ag := testutil.CreateCsuiteAgent(t, db, "coexist-agent", csuite.AgentMonOnline)
	if ag.Name != "coexist-agent" {
		t.Fatalf("csuite agent name = %q, want %q", ag.Name, "coexist-agent")
	}

	// Create a csuite inbox message using the factory.
	msg := testutil.CreateCsuiteInboxMessage(t, db, "from-agent", "to-agent", "hello", csuite.PriorityNormal, csuite.MessageTypeStatus)
	if msg.Subject != "hello" {
		t.Fatalf("inbox message subject = %q, want %q", msg.Subject, "hello")
	}

	// Verify all three exist independently.
	var agentCount, msgCount int64
	db.Model(&csuite.CsuiteAgent{}).Count(&agentCount)
	db.Model(&csuite.CsuiteInboxMessage{}).Count(&msgCount)

	if agentCount != 1 {
		t.Errorf("csuite agent count = %d, want 1", agentCount)
	}
	if msgCount != 1 {
		t.Errorf("inbox message count = %d, want 1", msgCount)
	}
}

func TestCsuiteFactoryFunctions(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &csuite.CsuiteAgent{}, &csuite.CsuiteInboxMessage{})

	t.Run("CreateCsuiteAgent", func(t *testing.T) {
		ag := testutil.CreateCsuiteAgent(t, db, "factory-agent", csuite.AgentMonOnline)
		if ag.ID == uuid.Nil {
			t.Error("expected non-nil UUID")
		}
		if ag.Name != "factory-agent" {
			t.Errorf("Name = %q, want %q", ag.Name, "factory-agent")
		}
		if ag.Status != csuite.AgentMonOnline {
			t.Errorf("Status = %q, want %q", ag.Status, csuite.AgentMonOnline)
		}
	})

	t.Run("CreateCsuiteInboxMessage", func(t *testing.T) {
		msg := testutil.CreateCsuiteInboxMessage(t, db, "sender", "receiver", "test subject", csuite.PriorityHigh, csuite.MessageTypeAlert)
		if msg.ID == uuid.Nil {
			t.Error("expected non-nil UUID")
		}
		if msg.FromAgent != "sender" {
			t.Errorf("FromAgent = %q, want %q", msg.FromAgent, "sender")
		}
		if msg.ToAgent != "receiver" {
			t.Errorf("ToAgent = %q, want %q", msg.ToAgent, "receiver")
		}
		if msg.Priority != csuite.PriorityHigh {
			t.Errorf("Priority = %q, want %q", msg.Priority, csuite.PriorityHigh)
		}
		if msg.Type != csuite.MessageTypeAlert {
			t.Errorf("Type = %q, want %q", msg.Type, csuite.MessageTypeAlert)
		}
	})
}
