package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

// TestSpawnAgentInWorktreeWithMultipleTypes tests spawning multiple agent types
// in worktrees with different ModelID and Effort configurations.
func TestSpawnAgentInWorktreeWithMultipleTypes(t *testing.T) {
	db := testutil.NewTestDB(t)

	// Create a fake Claude binary.
	binDir := t.TempDir()
	claudeBin := writeFakeClaudeBin(t, binDir, 0)

	// Create a project and task.
	project := testutil.CreateProject(t, db, "test-project", "/tmp/test.git", "master")
	task := testutil.CreateTask(t, db, project.ID, "Multi-agent task", model.StatusInProgress)

	// Define agentConfigs that returns different ModelID and Effort for each agent type.
	agentConfigs := func(at model.AgentType) model.AgentCLIConfig {
		switch at {
		case model.AgentPlanner:
			return model.AgentCLIConfig{
				Model:  "claude-opus",
				Effort: "high",
			}
		case model.AgentCoder:
			return model.AgentCLIConfig{
				Model:  "claude-sonnet",
				Effort: "medium",
			}
		case model.AgentResearcher:
			return model.AgentCLIConfig{
				Model:  "claude-haiku",
				Effort: "low",
			}
		default:
			return model.AgentCLIConfig{
				Effort: "medium",
			}
		}
	}

	// Create a Runner with the custom agentConfigs.
	runner := &Runner{
		db:              db,
		startProcess:    StartAgentProcess,
		tmuxSessionName: "test-session",
		worktree:        nil, // Not needed for SpawnAgentInWorktree
		claudeBin:       claudeBin,
		maxConcurrent:   4,
		agentConfigs:    agentConfigs,
		running:         make(map[uuid.UUID]*RunningAgent),
		completions:     make(chan Completion, 4),
		semaphore:       make(chan struct{}, 4),
	}

	// Test cases for different agent types.
	testCases := []struct {
		name           string
		agentType      model.AgentType
		expectedModel  string
		expectedEffort string
	}{
		{
			name:           "planner with opus and high effort",
			agentType:      model.AgentPlanner,
			expectedModel:  "claude-opus",
			expectedEffort: "high",
		},
		{
			name:           "coder with sonnet and medium effort",
			agentType:      model.AgentCoder,
			expectedModel:  "claude-sonnet",
			expectedEffort: "medium",
		},
		{
			name:           "researcher with haiku and low effort",
			agentType:      model.AgentResearcher,
			expectedModel:  "claude-haiku",
			expectedEffort: "low",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			prompt := fmt.Sprintf("You are a %s agent. Perform the task.", tc.agentType)
			worktreeDir := t.TempDir()

			// Spawn the agent in an existing worktree.
			agent, err := runner.SpawnAgentInWorktree(&task, worktreeDir, tc.agentType, prompt)
			if err != nil {
				t.Fatalf("SpawnAgentInWorktree: %v", err)
			}

			// Verify the returned agent has correct ModelID and Effort.
			if agent.ModelID != tc.expectedModel {
				t.Errorf("agent.ModelID = %q, want %q", agent.ModelID, tc.expectedModel)
			}
			if agent.Effort != tc.expectedEffort {
				t.Errorf("agent.Effort = %q, want %q", agent.Effort, tc.expectedEffort)
			}

			// Verify the agent status is WORKING.
			if agent.Status != model.AgentWorking {
				t.Errorf("agent.Status = %q, want %q", agent.Status, model.AgentWorking)
			}

			// Verify the agent type is correct.
			if agent.AgentType != tc.agentType {
				t.Errorf("agent.AgentType = %q, want %q", agent.AgentType, tc.agentType)
			}

			// Verify worktree path is set correctly.
			if agent.WorktreePath != worktreeDir {
				t.Errorf("agent.WorktreePath = %q, want %q", agent.WorktreePath, worktreeDir)
			}

			// Verify session name is set.
			if agent.TmuxSession == "" {
				t.Error("agent.TmuxSession is empty")
			}

			// Verify task link.
			if agent.CurrentTaskID == nil || *agent.CurrentTaskID != task.ID {
				t.Errorf("agent.CurrentTaskID = %v, want %v", agent.CurrentTaskID, &task.ID)
			}

			// Verify heartbeat is recent.
			if agent.HeartbeatAt == nil {
				t.Error("agent.HeartbeatAt is nil")
			} else if time.Since(*agent.HeartbeatAt) > 5*time.Second {
				t.Error("agent.HeartbeatAt is stale")
			}

			// Query the agent from the database and verify ModelID and Effort are persisted.
			var dbAgent model.Agent
			if err := db.First(&dbAgent, "id = ?", agent.ID).Error; err != nil {
				t.Fatalf("query agent from db: %v", err)
			}

			if dbAgent.ModelID != tc.expectedModel {
				t.Errorf("db agent.ModelID = %q, want %q", dbAgent.ModelID, tc.expectedModel)
			}
			if dbAgent.Effort != tc.expectedEffort {
				t.Errorf("db agent.Effort = %q, want %q", dbAgent.Effort, tc.expectedEffort)
			}

			// Verify the prompt file was written correctly.
			promptPath := filepath.Join(agent.WorktreePath, ".claude", "agent-prompt.md")
			promptContent, err := os.ReadFile(promptPath)
			if err != nil {
				t.Fatalf("read prompt file: %v", err)
			}
			if string(promptContent) != prompt {
				t.Errorf("prompt content = %q, want %q", string(promptContent), prompt)
			}

			// Verify settings.json was written.
			settingsPath := filepath.Join(agent.WorktreePath, ".claude", "settings.json")
			if _, err := os.Stat(settingsPath); os.IsNotExist(err) {
				t.Error("settings.json not found")
			}

			// Clean up: stop the agent to release resources.
			if err := runner.StopAgent(agent.ID); err != nil {
				t.Logf("StopAgent: %v (cleanup error)", err)
			}
		})
	}
}

// TestSpawnAgentInWorktreePopulatesModelIDAndEffort verifies ModelID and Effort
// are populated when spawning an agent in an existing worktree.
func TestSpawnAgentInWorktreePopulatesModelIDAndEffort(t *testing.T) {
	db := testutil.NewTestDB(t)

	// Create a fake Claude binary.
	binDir := t.TempDir()
	claudeBin := writeFakeClaudeBin(t, binDir, 0)

	// Create a project and task.
	project := testutil.CreateProject(t, db, "test-project", "/tmp/test.git", "master")
	task := testutil.CreateTask(t, db, project.ID, "Code review task", model.StatusInProgress)

	// Define agentConfigs with specific model and effort for reviewer.
	agentConfigs := func(at model.AgentType) model.AgentCLIConfig {
		switch at {
		case model.AgentReviewer:
			return model.AgentCLIConfig{
				Model:  "claude-opus",
				Effort: "high",
			}
		case model.AgentFixer:
			return model.AgentCLIConfig{
				Model:  "claude-sonnet",
				Effort: "medium",
			}
		default:
			return model.AgentCLIConfig{
				Effort: "medium",
			}
		}
	}

	// Create a Runner with the custom agentConfigs.
	runner := &Runner{
		db:              db,
		startProcess:    StartAgentProcess,
		tmuxSessionName: "test-session",
		worktree:        nil, // Not needed for SpawnAgentInWorktree
		claudeBin:       claudeBin,
		maxConcurrent:   4,
		agentConfigs:    agentConfigs,
		running:         make(map[uuid.UUID]*RunningAgent),
		completions:     make(chan Completion, 4),
		semaphore:       make(chan struct{}, 4),
	}

	// Use a temporary directory as the existing worktree.
	existingWorktree := t.TempDir()

	prompt := "Review the code and provide feedback."

	// Spawn a reviewer agent in the existing worktree.
	agent, err := runner.SpawnAgentInWorktree(&task, existingWorktree, model.AgentReviewer, prompt)
	if err != nil {
		t.Fatalf("SpawnAgentInWorktree: %v", err)
	}

	// Verify ModelID and Effort are populated from config.
	if agent.ModelID != "claude-opus" {
		t.Errorf("agent.ModelID = %q, want %q", agent.ModelID, "claude-opus")
	}
	if agent.Effort != "high" {
		t.Errorf("agent.Effort = %q, want %q", agent.Effort, "high")
	}

	// Verify worktree path is set correctly.
	if agent.WorktreePath != existingWorktree {
		t.Errorf("agent.WorktreePath = %q, want %q", agent.WorktreePath, existingWorktree)
	}

	// Verify the agent is in the database with correct values.
	var dbAgent model.Agent
	if err := db.First(&dbAgent, "id = ?", agent.ID).Error; err != nil {
		t.Fatalf("query agent from db: %v", err)
	}

	if dbAgent.ModelID != "claude-opus" {
		t.Errorf("db agent.ModelID = %q, want %q", dbAgent.ModelID, "claude-opus")
	}
	if dbAgent.Effort != "high" {
		t.Errorf("db agent.Effort = %q, want %q", dbAgent.Effort, "high")
	}

	// Clean up.
	_ = runner.StopAgent(agent.ID)
}

// TestSpawnAgentWithEmptyModelConfig verifies that when a config has no model set,
// the agent's ModelID is empty and Effort is populated correctly.
func TestSpawnAgentWithEmptyModelConfig(t *testing.T) {
	db := testutil.NewTestDB(t)

	// Create a fake Claude binary.
	binDir := t.TempDir()
	claudeBin := writeFakeClaudeBin(t, binDir, 0)

	// Create a project and task.
	project := testutil.CreateProject(t, db, "test-project", "/tmp/test.git", "master")
	task := testutil.CreateTask(t, db, project.ID, "Default model task", model.StatusInProgress)

	// Define agentConfigs that returns no model (empty string) but does set effort.
	agentConfigs := func(at model.AgentType) model.AgentCLIConfig {
		return model.AgentCLIConfig{
			Model:  "", // No model specified - should use Claude CLI default
			Effort: "low",
		}
	}

	// Create a Runner with the custom agentConfigs.
	runner := &Runner{
		db:              db,
		startProcess:    StartAgentProcess,
		tmuxSessionName: "test-session",
		worktree:        nil,
		claudeBin:       claudeBin,
		maxConcurrent:   4,
		agentConfigs:    agentConfigs,
		running:         make(map[uuid.UUID]*RunningAgent),
		completions:     make(chan Completion, 4),
		semaphore:       make(chan struct{}, 4),
	}

	prompt := "Perform the task."
	worktreeDir := t.TempDir()

	// Spawn the agent in an existing worktree.
	agent, err := runner.SpawnAgentInWorktree(&task, worktreeDir, model.AgentCoder, prompt)
	if err != nil {
		t.Fatalf("SpawnAgentInWorktree: %v", err)
	}

	// Verify ModelID is empty.
	if agent.ModelID != "" {
		t.Errorf("agent.ModelID = %q, want empty", agent.ModelID)
	}

	// Verify Effort is still populated.
	if agent.Effort != "low" {
		t.Errorf("agent.Effort = %q, want %q", agent.Effort, "low")
	}

	// Verify in the database.
	var dbAgent model.Agent
	if err := db.First(&dbAgent, "id = ?", agent.ID).Error; err != nil {
		t.Fatalf("query agent from db: %v", err)
	}

	if dbAgent.ModelID != "" {
		t.Errorf("db agent.ModelID = %q, want empty", dbAgent.ModelID)
	}
	if dbAgent.Effort != "low" {
		t.Errorf("db agent.Effort = %q, want %q", dbAgent.Effort, "low")
	}

	// Clean up.
	_ = runner.StopAgent(agent.ID)
}

// TestDatabaseQueryRetrievesModelIDAndEffort verifies that database queries
// correctly retrieve the ModelID and Effort values for agents.
func TestDatabaseQueryRetrievesModelIDAndEffort(t *testing.T) {
	db := testutil.NewTestDB(t)

	// Create a project.
	project := testutil.CreateProject(t, db, "test-project", "/tmp/test.git", "master")

	// Create multiple agents with different ModelID and Effort values using the factory.
	agent1 := testutil.CreateAgentWithOptions(t, db, uuid.Nil, model.AgentPlanner, model.AgentWorking, "claude-opus", "high")
	agent1.ProjectID = project.ID
	db.Save(&agent1)

	agent2 := testutil.CreateAgentWithOptions(t, db, uuid.Nil, model.AgentCoder, model.AgentWorking, "claude-sonnet", "medium")
	agent2.ProjectID = project.ID
	db.Save(&agent2)

	agent3 := testutil.CreateAgentWithOptions(t, db, uuid.Nil, model.AgentResearcher, model.AgentWorking, "claude-haiku", "low")
	agent3.ProjectID = project.ID
	db.Save(&agent3)

	// Query agent1 by ID.
	var queriedAgent1 model.Agent
	if err := db.First(&queriedAgent1, "id = ?", agent1.ID).Error; err != nil {
		t.Fatalf("query agent1: %v", err)
	}
	if queriedAgent1.ModelID != "claude-opus" {
		t.Errorf("agent1.ModelID = %q, want %q", queriedAgent1.ModelID, "claude-opus")
	}
	if queriedAgent1.Effort != "high" {
		t.Errorf("agent1.Effort = %q, want %q", queriedAgent1.Effort, "high")
	}

	// Query agent2 by ID.
	var queriedAgent2 model.Agent
	if err := db.First(&queriedAgent2, "id = ?", agent2.ID).Error; err != nil {
		t.Fatalf("query agent2: %v", err)
	}
	if queriedAgent2.ModelID != "claude-sonnet" {
		t.Errorf("agent2.ModelID = %q, want %q", queriedAgent2.ModelID, "claude-sonnet")
	}
	if queriedAgent2.Effort != "medium" {
		t.Errorf("agent2.Effort = %q, want %q", queriedAgent2.Effort, "medium")
	}

	// Query all agents in the project.
	var allAgents []model.Agent
	if err := db.Where("project_id = ?", project.ID).Order("created_at").Find(&allAgents).Error; err != nil {
		t.Fatalf("query all agents: %v", err)
	}
	if len(allAgents) != 3 {
		t.Errorf("expected 3 agents, got %d", len(allAgents))
	}

	// Verify the query results have the expected values.
	expectedAgents := []struct {
		id     uuid.UUID
		model  string
		effort string
	}{
		{agent1.ID, "claude-opus", "high"},
		{agent2.ID, "claude-sonnet", "medium"},
		{agent3.ID, "claude-haiku", "low"},
	}

	for i, expected := range expectedAgents {
		if allAgents[i].ModelID != expected.model {
			t.Errorf("allAgents[%d].ModelID = %q, want %q", i, allAgents[i].ModelID, expected.model)
		}
		if allAgents[i].Effort != expected.effort {
			t.Errorf("allAgents[%d].Effort = %q, want %q", i, allAgents[i].Effort, expected.effort)
		}
	}
}

// TestNoRegressionInSpawnBehavior verifies that existing spawn behavior is not
// affected by the ModelID and Effort changes.
func TestNoRegressionInSpawnBehavior(t *testing.T) {
	db := testutil.NewTestDB(t)

	// Create a fake Claude binary.
	binDir := t.TempDir()
	claudeBin := writeFakeClaudeBin(t, binDir, 0)

	// Create a project and task.
	project := testutil.CreateProject(t, db, "test-project", "/tmp/test.git", "master")
	task := testutil.CreateTask(t, db, project.ID, "Main task for planner", model.StatusBacklog)

	// Create a Runner with default agentConfigs (effort="medium", no model).
	runner := &Runner{
		db:              db,
		startProcess:    StartAgentProcess,
		tmuxSessionName: "test-session",
		worktree:        nil,
		claudeBin:       claudeBin,
		maxConcurrent:   4,
		agentConfigs: func(model.AgentType) model.AgentCLIConfig {
			return model.AgentCLIConfig{Effort: "medium"}
		},
		running:     make(map[uuid.UUID]*RunningAgent),
		completions: make(chan Completion, 4),
		semaphore:   make(chan struct{}, 4),
	}

	prompt := "Plan the implementation."
	worktreeDir := t.TempDir()

	// Spawn a planner agent in an existing worktree.
	agent, err := runner.SpawnAgentInWorktree(&task, worktreeDir, model.AgentPlanner, prompt)
	if err != nil {
		t.Fatalf("SpawnAgentInWorktree: %v", err)
	}

	// Verify existing spawn behavior is preserved.
	if agent.ProjectID != project.ID {
		t.Errorf("agent.ProjectID = %v, want %v", agent.ProjectID, project.ID)
	}

	if agent.AgentType != model.AgentPlanner {
		t.Errorf("agent.AgentType = %q, want %q", agent.AgentType, model.AgentPlanner)
	}

	if agent.Status != model.AgentWorking {
		t.Errorf("agent.Status = %q, want %q", agent.Status, model.AgentWorking)
	}

	if agent.CurrentTaskID == nil || *agent.CurrentTaskID != task.ID {
		t.Errorf("agent.CurrentTaskID is not linked to task")
	}

	if agent.WorktreePath != worktreeDir {
		t.Errorf("agent.WorktreePath = %q, want %q", agent.WorktreePath, worktreeDir)
	}

	if agent.TmuxSession == "" {
		t.Error("agent.TmuxSession is empty")
	}

	if agent.HeartbeatAt == nil {
		t.Error("agent.HeartbeatAt is nil")
	}

	// Verify the agent is in the running map.
	runner.mu.Lock()
	ra, inRunning := runner.running[agent.ID]
	runner.mu.Unlock()

	if !inRunning {
		t.Error("agent not found in running map")
	}
	if ra == nil {
		t.Error("running agent entry is nil")
	}
	if ra.Process == nil {
		t.Error("agent process is nil")
	}

	// Clean up.
	_ = runner.StopAgent(agent.ID)
}
