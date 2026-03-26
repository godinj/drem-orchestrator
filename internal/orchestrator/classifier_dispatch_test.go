package orchestrator

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/agent"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/testutil"
	"github.com/godinj/drem-orchestrator/internal/worktree"
)

// TestClassifierDispatchPicksUpNewlyFiledTasks verifies that the classifier
// dispatch function re-scans for classifying tasks on every orchestrator tick,
// not just on startup. This is a regression test for the bug where newly filed
// tasks were never picked up by classifier agents.
//
// The test:
// 1. Creates an orchestrator with a runner
// 2. Files an initial classifying task
// 3. Runs one doTick() — expects the initial task to get an agent assigned
// 4. Files a NEW classifying task (after the first tick)
// 5. Runs another doTick() — expects the NEW task to get an agent assigned
// 6. Asserts that both tasks have classifier agents dispatched to them
func TestClassifierDispatchPicksUpNewlyFiledTasks(t *testing.T) {
	// Set up orchestrator with a runner and real bare repo
	bareRepo := testutil.InitBareRepoWithMainWorktree(t)
	defaultBranch := testutil.GetDefaultBranch(t, bareRepo)
	db := testutil.NewTestDB(t)
	projectID := uuid.New()
	events := make(chan Event, 100)

	wt := worktree.NewManager(bareRepo, defaultBranch)

	project := model.Project{
		ID:            projectID,
		Name:          "classifier-dispatch-test",
		BareRepoPath:  bareRepo,
		DefaultBranch: defaultBranch,
	}
	if err := db.Create(&project).Error; err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Create a fake Claude binary that reads stdin and exits 0.
	binDir := t.TempDir()
	fakeBin := filepath.Join(binDir, "fake-claude")
	script := "#!/bin/sh\ncat > /dev/null\nexit 0\n"
	if err := os.WriteFile(fakeBin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake claude binary: %v", err)
	}

	runner := agent.NewRunner(db, nil, wt, fakeBin, 4, nil)

	orch := &Orchestrator{
		db:              db,
		projectID:       projectID,
		worktree:        wt,
		runner:          runner,
		events:          events,
		contextWarnPct:  75,
		contextStopPct:  90,
		contextFixerPct: 85,
		logger:          slog.Default().With("component", "classifier-dispatch-test"),
	}

	// PHASE 1: Create initial task, run one tick, verify agent is assigned
	initialTask := testutil.CreateTask(t, db, projectID, "Initial classifying task", model.StatusClassifying)

	// Run the first tick — should dispatch a classifier to the initial task
	orch.doTick(nil)

	// Reload the initial task and verify an agent was assigned
	var reloadedInitial model.Task
	if err := db.First(&reloadedInitial, "id = ?", initialTask.ID).Error; err != nil {
		t.Fatalf("reload initial task: %v", err)
	}
	if reloadedInitial.AssignedAgentID == nil {
		t.Fatal("expected initial task to have an assigned classifier agent after first tick")
	}
	initialAgentID := *reloadedInitial.AssignedAgentID

	// PHASE 2: Create a NEW task after the first tick, run another tick,
	// verify the new task ALSO gets a classifier agent assigned.
	// This is the key test: the new task was filed AFTER the first tick,
	// so the dispatch function must re-scan to find it.
	newTask := testutil.CreateTask(t, db, projectID, "Newly filed classifying task", model.StatusClassifying)

	// Sanity check: the new task should not have an agent assigned yet
	if newTask.AssignedAgentID != nil {
		t.Fatal("expected new task to have no assigned agent before second tick")
	}

	// Run the second tick — should dispatch a classifier to the newly filed task
	orch.doTick(nil)

	// Reload the new task and verify an agent was assigned
	var reloadedNew model.Task
	if err := db.First(&reloadedNew, "id = ?", newTask.ID).Error; err != nil {
		t.Fatalf("reload new task: %v", err)
	}
	if reloadedNew.AssignedAgentID == nil {
		t.Fatal("expected newly filed task to have an assigned classifier agent after second tick")
	}
	newAgentID := *reloadedNew.AssignedAgentID

	// Verify the agents are different (not the same agent re-assigned)
	if initialAgentID == newAgentID {
		t.Fatal("expected initial and new tasks to be assigned different classifier agents")
	}

	// Verify both agents are classifier agents
	var initialAgent, newAgent model.Agent
	if err := db.First(&initialAgent, "id = ?", initialAgentID).Error; err != nil {
		t.Fatalf("load initial agent: %v", err)
	}
	if err := db.First(&newAgent, "id = ?", newAgentID).Error; err != nil {
		t.Fatalf("load new agent: %v", err)
	}

	if initialAgent.AgentType != model.AgentClassifier {
		t.Errorf("initial agent type = %q, want %q", initialAgent.AgentType, model.AgentClassifier)
	}
	if newAgent.AgentType != model.AgentClassifier {
		t.Errorf("new agent type = %q, want %q", newAgent.AgentType, model.AgentClassifier)
	}
}
