package memory

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

// createTestProject sets up a project with a task and agent for memory tests.
func createTestProject(t *testing.T, db *gorm.DB) (projectID, taskID, agentID uuid.UUID) {
	t.Helper()

	projectID = uuid.New()
	project := model.Project{
		ID:           projectID,
		Name:         "test-project",
		BareRepoPath: "/tmp/test.git",
	}
	if err := db.Create(&project).Error; err != nil {
		t.Fatalf("create project: %v", err)
	}

	taskID = uuid.New()
	task := model.Task{
		ID:          taskID,
		ProjectID:   projectID,
		Title:       "test task",
		Description: "a test task",
		Status:      model.StatusBacklog,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}

	agentID = uuid.New()
	agent := model.Agent{
		ID:        agentID,
		ProjectID: projectID,
		AgentType: model.AgentCoder,
		Name:      "test-agent",
		Status:    model.AgentIdle,
	}
	if err := db.Create(&agent).Error; err != nil {
		t.Fatalf("create agent: %v", err)
	}

	return projectID, taskID, agentID
}

func TestStoreMemory(t *testing.T) {
	t.Run("all fields populated", func(t *testing.T) {
		db := testutil.NewTestDB(t)
		mgr := NewManager(db)
		_, taskID, agentID := createTestProject(t, db)

		meta := map[string]any{"key": "value", "count": float64(42)}
		mem, err := mgr.StoreMemory(agentID, "decided to use JSON", "decision", &taskID, meta)
		if err != nil {
			t.Fatalf("StoreMemory: %v", err)
		}
		if mem.ID == uuid.Nil {
			t.Fatal("expected non-nil memory ID")
		}
		if mem.AgentID != agentID {
			t.Errorf("AgentID = %v, want %v", mem.AgentID, agentID)
		}
		if mem.Content != "decided to use JSON" {
			t.Errorf("Content = %q, want %q", mem.Content, "decided to use JSON")
		}
		if mem.MemoryType != "decision" {
			t.Errorf("MemoryType = %q, want %q", mem.MemoryType, "decision")
		}
		if mem.TaskID == nil || *mem.TaskID != taskID {
			t.Errorf("TaskID = %v, want %v", mem.TaskID, &taskID)
		}

		// Query back and verify persistence
		var fetched model.Memory
		if err := db.First(&fetched, "id = ?", mem.ID).Error; err != nil {
			t.Fatalf("query stored memory: %v", err)
		}
		if fetched.Content != mem.Content {
			t.Errorf("fetched Content = %q, want %q", fetched.Content, mem.Content)
		}
		if fetched.Metadata["key"] != "value" {
			t.Errorf("fetched Metadata[key] = %v, want %q", fetched.Metadata["key"], "value")
		}
	})

	t.Run("nil taskID", func(t *testing.T) {
		db := testutil.NewTestDB(t)
		mgr := NewManager(db)
		_, _, agentID := createTestProject(t, db)

		mem, err := mgr.StoreMemory(agentID, "a lesson learned", "lesson", nil, nil)
		if err != nil {
			t.Fatalf("StoreMemory with nil taskID: %v", err)
		}
		if mem.TaskID != nil {
			t.Errorf("TaskID = %v, want nil", mem.TaskID)
		}
	})

	t.Run("metadata JSON round-trip", func(t *testing.T) {
		db := testutil.NewTestDB(t)
		mgr := NewManager(db)
		_, taskID, agentID := createTestProject(t, db)

		meta := map[string]any{
			"files":   []any{"main.go", "util.go"},
			"nested":  map[string]any{"inner": "val"},
			"numeric": float64(3.14),
		}
		mem, err := mgr.StoreMemory(agentID, "complex metadata", "file_change", &taskID, meta)
		if err != nil {
			t.Fatalf("StoreMemory: %v", err)
		}

		var fetched model.Memory
		if err := db.First(&fetched, "id = ?", mem.ID).Error; err != nil {
			t.Fatalf("query: %v", err)
		}
		// Verify nested map survives round-trip
		nested, ok := fetched.Metadata["nested"].(map[string]any)
		if !ok {
			t.Fatalf("Metadata[nested] type = %T, want map[string]any", fetched.Metadata["nested"])
		}
		if nested["inner"] != "val" {
			t.Errorf("Metadata[nested][inner] = %v, want %q", nested["inner"], "val")
		}
	})
}

func TestGetMemories(t *testing.T) {
	db := testutil.NewTestDB(t)
	mgr := NewManager(db)

	_, taskID, agentID := createTestProject(t, db)

	// Create a second agent under the same project
	agent2ID := uuid.New()
	agent2 := model.Agent{
		ID:        agent2ID,
		ProjectID: uuid.MustParse(func() string { var p model.Agent; db.First(&p, "id = ?", agentID); var a model.Agent; db.First(&a, "id = ?", agentID); return a.ProjectID.String() }()),
		AgentType: model.AgentReviewer,
		Name:      "test-agent-2",
		Status:    model.AgentIdle,
	}
	if err := db.Create(&agent2).Error; err != nil {
		t.Fatalf("create agent2: %v", err)
	}

	// Store memories for agent 1
	taskIDPtr := &taskID
	for _, mt := range []string{"decision", "blocker", "file_change"} {
		if _, err := mgr.StoreMemory(agentID, "agent1 "+mt, mt, taskIDPtr, nil); err != nil {
			t.Fatalf("store memory: %v", err)
		}
	}
	// Store a memory for agent 1 without task
	if _, err := mgr.StoreMemory(agentID, "agent1 lesson", "lesson", nil, nil); err != nil {
		t.Fatalf("store memory: %v", err)
	}

	// Store memories for agent 2
	if _, err := mgr.StoreMemory(agent2ID, "agent2 decision", "decision", taskIDPtr, nil); err != nil {
		t.Fatalf("store memory: %v", err)
	}

	tests := []struct {
		name       string
		agentID    *uuid.UUID
		taskID     *uuid.UUID
		memoryType string
		limit      int
		wantCount  int
	}{
		{
			name:      "filter by agentID only",
			agentID:   &agentID,
			wantCount: 4,
		},
		{
			name:      "filter by agentID and taskID",
			agentID:   &agentID,
			taskID:    &taskID,
			wantCount: 3,
		},
		{
			name:       "filter by memoryType",
			memoryType: "decision",
			wantCount:  2,
		},
		{
			name:      "limit parameter",
			agentID:   &agentID,
			limit:     2,
			wantCount: 2,
		},
		{
			name:       "no matches",
			memoryType: "nonexistent_type",
			wantCount:  0,
		},
		{
			name:      "multiple agents returns only requested",
			agentID:   &agent2ID,
			wantCount: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := mgr.GetMemories(tc.agentID, tc.taskID, tc.memoryType, tc.limit)
			if err != nil {
				t.Fatalf("GetMemories: %v", err)
			}
			if len(got) != tc.wantCount {
				t.Errorf("got %d memories, want %d", len(got), tc.wantCount)
			}
		})
	}
}

func TestGetProjectMemories(t *testing.T) {
	db := testutil.NewTestDB(t)
	mgr := NewManager(db)

	projectID, taskID, agentID := createTestProject(t, db)

	// Create second task and agent under same project
	task2ID := uuid.New()
	task2 := model.Task{
		ID:          task2ID,
		ProjectID:   projectID,
		Title:       "second task",
		Description: "another task",
		Status:      model.StatusBacklog,
	}
	if err := db.Create(&task2).Error; err != nil {
		t.Fatalf("create task2: %v", err)
	}

	agent2ID := uuid.New()
	agent2 := model.Agent{
		ID:        agent2ID,
		ProjectID: projectID,
		AgentType: model.AgentReviewer,
		Name:      "agent-2",
		Status:    model.AgentIdle,
	}
	if err := db.Create(&agent2).Error; err != nil {
		t.Fatalf("create agent2: %v", err)
	}

	// Store memories for both agents
	tid1 := &taskID
	tid2 := &task2ID
	if _, err := mgr.StoreMemory(agentID, "decision 1", "decision", tid1, nil); err != nil {
		t.Fatalf("store: %v", err)
	}
	if _, err := mgr.StoreMemory(agentID, "blocker 1", "blocker", tid1, nil); err != nil {
		t.Fatalf("store: %v", err)
	}
	if _, err := mgr.StoreMemory(agent2ID, "decision 2", "decision", tid2, nil); err != nil {
		t.Fatalf("store: %v", err)
	}
	if _, err := mgr.StoreMemory(agent2ID, "lesson 1", "lesson", tid2, nil); err != nil {
		t.Fatalf("store: %v", err)
	}

	t.Run("returns memories across all tasks in project", func(t *testing.T) {
		got, err := mgr.GetProjectMemories(projectID, nil, 0)
		if err != nil {
			t.Fatalf("GetProjectMemories: %v", err)
		}
		if len(got) != 4 {
			t.Errorf("got %d memories, want 4", len(got))
		}
	})

	t.Run("filter by memoryType slice", func(t *testing.T) {
		got, err := mgr.GetProjectMemories(projectID, []string{"decision"}, 0)
		if err != nil {
			t.Fatalf("GetProjectMemories: %v", err)
		}
		if len(got) != 2 {
			t.Errorf("got %d memories, want 2", len(got))
		}
		for _, m := range got {
			if m.MemoryType != "decision" {
				t.Errorf("MemoryType = %q, want %q", m.MemoryType, "decision")
			}
		}
	})

	t.Run("limit parameter respected", func(t *testing.T) {
		got, err := mgr.GetProjectMemories(projectID, nil, 2)
		if err != nil {
			t.Fatalf("GetProjectMemories: %v", err)
		}
		if len(got) != 2 {
			t.Errorf("got %d memories, want 2", len(got))
		}
	})

	t.Run("empty project returns empty slice", func(t *testing.T) {
		emptyProjectID := uuid.New()
		emptyProject := model.Project{
			ID:           emptyProjectID,
			Name:         "empty-project",
			BareRepoPath: "/tmp/empty.git",
		}
		if err := db.Create(&emptyProject).Error; err != nil {
			t.Fatalf("create project: %v", err)
		}

		got, err := mgr.GetProjectMemories(emptyProjectID, nil, 0)
		if err != nil {
			t.Fatalf("GetProjectMemories: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("got %d memories, want 0", len(got))
		}
	})
}

func TestCompactAgentMemory(t *testing.T) {
	t.Run("compacts multiple memories into summary", func(t *testing.T) {
		db := testutil.NewTestDB(t)
		mgr := NewManager(db)
		_, taskID, agentID := createTestProject(t, db)
		tid := &taskID

		// Store 5+ memories of various types
		for _, item := range []struct {
			content, memType string
		}{
			{"use gRPC for service comms", "decision"},
			{"refactored auth module", "file_change"},
			{"always validate inputs", "lesson"},
			{"waiting for API key", "blocker"},
			{"implemented login flow", "completion"},
			{"switched to protobuf", "decision"},
		} {
			if _, err := mgr.StoreMemory(agentID, item.content, item.memType, tid, nil); err != nil {
				t.Fatalf("store memory: %v", err)
			}
		}

		summary, err := mgr.CompactAgentMemory(agentID)
		if err != nil {
			t.Fatalf("CompactAgentMemory: %v", err)
		}
		if summary == "" {
			t.Fatal("expected non-empty summary")
		}

		// Verify summary contains expected sections
		if !strings.Contains(summary, "## Decisions") {
			t.Error("summary missing '## Decisions' section")
		}
		if !strings.Contains(summary, "## File Changes") {
			t.Error("summary missing '## File Changes' section")
		}
		if !strings.Contains(summary, "## Blockers") {
			t.Error("summary missing '## Blockers' section")
		}
		if !strings.Contains(summary, "## Completed") {
			t.Error("summary missing '## Completed' section")
		}
		if !strings.Contains(summary, "use gRPC for service comms") {
			t.Error("summary missing decision content")
		}

		// Verify agent's MemorySummary was updated
		var agent model.Agent
		if err := db.First(&agent, "id = ?", agentID).Error; err != nil {
			t.Fatalf("query agent: %v", err)
		}
		if agent.MemorySummary != summary {
			t.Error("agent MemorySummary not updated")
		}

		// Verify old memories were archived
		var archivedCount int64
		db.Model(&model.Memory{}).
			Where("agent_id = ?", agentID).
			Where("memory_type LIKE ?", "archived_%").
			Count(&archivedCount)
		if archivedCount != 6 {
			t.Errorf("archived count = %d, want 6", archivedCount)
		}
	})

	t.Run("idempotent compact does not duplicate", func(t *testing.T) {
		db := testutil.NewTestDB(t)
		mgr := NewManager(db)
		_, taskID, agentID := createTestProject(t, db)
		tid := &taskID

		if _, err := mgr.StoreMemory(agentID, "a decision", "decision", tid, nil); err != nil {
			t.Fatalf("store: %v", err)
		}

		summary1, err := mgr.CompactAgentMemory(agentID)
		if err != nil {
			t.Fatalf("first compact: %v", err)
		}

		// Second compact with no new (non-archived) memories
		summary2, err := mgr.CompactAgentMemory(agentID)
		if err != nil {
			t.Fatalf("second compact: %v", err)
		}
		if summary2 != "" {
			t.Errorf("second compact should return empty string, got %q", summary2)
		}

		// Agent should still have first summary
		var agent model.Agent
		if err := db.First(&agent, "id = ?", agentID).Error; err != nil {
			t.Fatalf("query agent: %v", err)
		}
		if agent.MemorySummary != summary1 {
			t.Error("agent MemorySummary should still contain first summary")
		}
	})

	t.Run("agent with no memories returns empty", func(t *testing.T) {
		db := testutil.NewTestDB(t)
		mgr := NewManager(db)
		_, _, agentID := createTestProject(t, db)

		summary, err := mgr.CompactAgentMemory(agentID)
		if err != nil {
			t.Fatalf("CompactAgentMemory: %v", err)
		}
		if summary != "" {
			t.Errorf("expected empty summary, got %q", summary)
		}
	})
}

func TestBuildAgentContext(t *testing.T) {
	t.Run("agent with memories returns formatted context", func(t *testing.T) {
		db := testutil.NewTestDB(t)
		mgr := NewManager(db)
		_, taskID, agentID := createTestProject(t, db)
		tid := &taskID

		// Set agent memory summary
		db.Model(&model.Agent{}).Where("id = ?", agentID).Update("memory_summary", "## Decisions\n- use JSON")

		// Store some task memories
		if _, err := mgr.StoreMemory(agentID, "implemented parser", "file_change", tid, nil); err != nil {
			t.Fatalf("store: %v", err)
		}
		if _, err := mgr.StoreMemory(agentID, "tests pass", "completion", tid, nil); err != nil {
			t.Fatalf("store: %v", err)
		}

		ctx, err := mgr.BuildAgentContext(agentID, taskID, 8000)
		if err != nil {
			t.Fatalf("BuildAgentContext: %v", err)
		}
		if ctx == "" {
			t.Fatal("expected non-empty context")
		}
		if !strings.Contains(ctx, "# Agent Memory Summary") {
			t.Error("context missing '# Agent Memory Summary'")
		}
		if !strings.Contains(ctx, "# Recent Task Memories") {
			t.Error("context missing '# Recent Task Memories'")
		}
		if !strings.Contains(ctx, "implemented parser") {
			t.Error("context missing task memory content")
		}
	})

	t.Run("token budget truncation", func(t *testing.T) {
		db := testutil.NewTestDB(t)
		mgr := NewManager(db)
		_, taskID, agentID := createTestProject(t, db)

		// Set a long memory summary
		longSummary := strings.Repeat("x", 10000)
		db.Model(&model.Agent{}).Where("id = ?", agentID).Update("memory_summary", longSummary)

		// maxTokens=10 => maxChars=40
		ctx, err := mgr.BuildAgentContext(agentID, taskID, 10)
		if err != nil {
			t.Fatalf("BuildAgentContext: %v", err)
		}
		if len(ctx) > 40 {
			t.Errorf("context length = %d, want <= 40", len(ctx))
		}
	})

	t.Run("no memories returns empty string", func(t *testing.T) {
		db := testutil.NewTestDB(t)
		mgr := NewManager(db)
		_, taskID, agentID := createTestProject(t, db)

		ctx, err := mgr.BuildAgentContext(agentID, taskID, 0)
		if err != nil {
			t.Fatalf("BuildAgentContext: %v", err)
		}
		if ctx != "" {
			t.Errorf("expected empty context, got %q", ctx)
		}
	})

	t.Run("includes project-wide context from other agents", func(t *testing.T) {
		db := testutil.NewTestDB(t)
		mgr := NewManager(db)
		projectID, taskID, agentID := createTestProject(t, db)

		// Create a second agent in the same project
		agent2ID := uuid.New()
		agent2 := model.Agent{
			ID:        agent2ID,
			ProjectID: projectID,
			AgentType: model.AgentReviewer,
			Name:      "agent-2",
			Status:    model.AgentIdle,
		}
		if err := db.Create(&agent2).Error; err != nil {
			t.Fatalf("create agent2: %v", err)
		}

		// Store a decision memory for agent2
		tid := &taskID
		if _, err := mgr.StoreMemory(agent2ID, "use structured logging", "decision", tid, nil); err != nil {
			t.Fatalf("store: %v", err)
		}

		ctx, err := mgr.BuildAgentContext(agentID, taskID, 8000)
		if err != nil {
			t.Fatalf("BuildAgentContext: %v", err)
		}
		if !strings.Contains(ctx, "# Project-Wide Context") {
			t.Error("context missing '# Project-Wide Context'")
		}
		if !strings.Contains(ctx, "use structured logging") {
			t.Error("context missing project-wide decision")
		}
	})
}

func TestExtractMemoriesFromOutput(t *testing.T) {
	tests := []struct {
		name      string
		output    string
		wantTypes []string
		wantMin   int
	}{
		{
			name:      "decision line",
			output:    "I decided to use a worker pool for concurrency",
			wantTypes: []string{"decision"},
			wantMin:   1,
		},
		{
			name:      "blocker line",
			output:    "We are blocked by the missing API credentials",
			wantTypes: []string{"blocker"},
			wantMin:   1,
		},
		{
			name:      "file change reference",
			output:    "I created file main.go with the entry point",
			wantTypes: []string{"file_change"},
			wantMin:   1,
		},
		{
			name:      "completion marker",
			output:    "I completed the authentication module implementation",
			wantTypes: []string{"completion"},
			wantMin:   1,
		},
		{
			name:      "multiple pattern matches",
			output:    "I decided to use REST.\nI created server.go with handlers.\nI completed the API layer.",
			wantTypes: []string{"decision", "file_change", "completion"},
			wantMin:   3,
		},
		{
			name:      "no matches",
			output:    "This is just a normal line with no special patterns.",
			wantTypes: nil,
			wantMin:   0,
		},
		{
			name:      "empty output",
			output:    "",
			wantTypes: nil,
			wantMin:   0,
		},
		{
			name:      "chose pattern for decision",
			output:    "We chose the adapter pattern for extensibility",
			wantTypes: []string{"decision"},
			wantMin:   1,
		},
		{
			name:      "approach pattern for decision",
			output:    "approach: use dependency injection throughout",
			wantTypes: []string{"decision"},
			wantMin:   1,
		},
		{
			name:      "need pattern for blocker",
			output:    "We need the database schema finalized first",
			wantTypes: []string{"blocker"},
			wantMin:   1,
		},
		{
			name:      "waiting for pattern for blocker",
			output:    "We are waiting for the CI pipeline fix",
			wantTypes: []string{"blocker"},
			wantMin:   1,
		},
		{
			name:      "modified file pattern",
			output:    "I modified config.yaml to add the new endpoint",
			wantTypes: []string{"file_change"},
			wantMin:   1,
		},
		{
			name:      "finished pattern for completion",
			output:    "finished the refactoring of the database layer",
			wantTypes: []string{"completion"},
			wantMin:   1,
		},
		{
			name:      "done pattern for completion",
			output:    "done: implemented all unit tests for the parser",
			wantTypes: []string{"completion"},
			wantMin:   1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := testutil.NewTestDB(t)
			mgr := NewManager(db)
			_, taskID, agentID := createTestProject(t, db)

			got, err := mgr.ExtractMemoriesFromOutput(agentID, taskID, tc.output)
			if err != nil {
				t.Fatalf("ExtractMemoriesFromOutput: %v", err)
			}
			if len(got) < tc.wantMin {
				t.Errorf("got %d memories, want at least %d", len(got), tc.wantMin)
			}

			// Verify expected memory types are present
			gotTypes := make(map[string]bool)
			for _, m := range got {
				gotTypes[m.MemoryType] = true
			}
			for _, wt := range tc.wantTypes {
				if !gotTypes[wt] {
					t.Errorf("missing expected memory type %q; got types: %v", wt, gotTypes)
				}
			}

			// Verify memories were persisted
			if tc.wantMin > 0 {
				var count int64
				db.Model(&model.Memory{}).Where("agent_id = ?", agentID).Count(&count)
				if int(count) < tc.wantMin {
					t.Errorf("persisted count = %d, want at least %d", count, tc.wantMin)
				}
			}
		})
	}

	t.Run("deduplicates identical matches", func(t *testing.T) {
		db := testutil.NewTestDB(t)
		mgr := NewManager(db)
		_, taskID, agentID := createTestProject(t, db)

		// Same line repeated should only produce one memory
		output := "I decided to use goroutines\nI decided to use goroutines"
		got, err := mgr.ExtractMemoriesFromOutput(agentID, taskID, output)
		if err != nil {
			t.Fatalf("ExtractMemoriesFromOutput: %v", err)
		}
		if len(got) != 1 {
			t.Errorf("got %d memories after dedup, want 1", len(got))
		}
	})
}
