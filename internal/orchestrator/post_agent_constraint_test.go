package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/agent"
	"github.com/godinj/drem-orchestrator/internal/model"
)

// ---------------------------------------------------------------------------
// Post-Agent Constraint Gate tests
// ---------------------------------------------------------------------------

// TestPostAgentConstraint_NoConfig verifies that when no .drem/constraints.toml
// exists in the feature worktree, the agent completes normally and the subtask
// transitions to DONE.
func TestPostAgentConstraint_NoConfig(t *testing.T) {
	bareRepoPath := setupTestRepoWithMainBranch(t)

	featureName := "constraint-no-config"
	featureDir := createFeatureWorktree(t, bareRepoPath, featureName)

	// Create an agent branch with a commit (NOT yet merged into feature).
	agentBranch := "worktree-agent-no-cfg"
	featureBranch := "feature/" + featureName
	runGitCmd(t, bareRepoPath, "branch", agentBranch, featureBranch)
	agentDir := filepath.Join(bareRepoPath, "feature", featureName, "agent-no-cfg")
	runGitCmd(t, bareRepoPath, "worktree", "add", agentDir, agentBranch)
	runGitCmd(t, agentDir, "config", "user.email", "test@test.com")
	runGitCmd(t, agentDir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(agentDir, "work.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitCmd(t, agentDir, "add", ".")
	runGitCmd(t, agentDir, "commit", "-m", "agent work")

	// Confirm there is NO .drem/constraints.toml in the feature worktree.
	constraintsPath := filepath.Join(featureDir, ".drem", "constraints.toml")
	if _, err := os.Stat(constraintsPath); !os.IsNotExist(err) {
		t.Fatalf("expected no constraints.toml, but it exists")
	}

	o, _ := agentResultOrchestrator(t, bareRepoPath)
	wt := NewHostWorktreeManager(bareRepoPath, "main")
	o.worktree = wt

	parentID := uuid.New()
	parent := model.Task{
		ID:             parentID,
		ProjectID:      o.projectID,
		Title:          "parent-no-cfg",
		Description:    "parent for no-config constraint test",
		Status:         model.StatusInProgress,
		WorktreeBranch: featureBranch,
	}
	o.db.Create(&parent)

	taskID := uuid.New()
	agentID := uuid.New()
	ag := model.Agent{
		ID:             agentID,
		ProjectID:      o.projectID,
		AgentType:      model.AgentCoder,
		Name:           "coder-no-cfg",
		Status:         model.AgentWorking,
		CurrentTaskID:  &taskID,
		WorktreePath:   agentDir,
		WorktreeBranch: agentBranch,
	}
	o.db.Create(&ag)

	task := model.Task{
		ID:              taskID,
		ProjectID:       o.projectID,
		ParentTaskID:    &parentID,
		Title:           "subtask-no-cfg",
		Description:     "should complete normally without constraints",
		Status:          model.StatusInProgress,
		AssignedAgentID: &agentID,
	}
	o.db.Create(&task)

	err := o.processAgentResult(agent.Completion{
		AgentID:    agentID,
		ReturnCode: 0,
	})
	if err != nil {
		t.Fatalf("processAgentResult: %v", err)
	}

	// Verify task completed to DONE.
	var updatedTask model.Task
	o.db.First(&updatedTask, "id = ?", taskID)
	if updatedTask.Status != model.StatusDone {
		t.Errorf("expected task status done, got %s", updatedTask.Status)
	}

	// Verify agent is idle.
	var updatedAgent model.Agent
	o.db.First(&updatedAgent, "id = ?", agentID)
	if updatedAgent.Status != model.AgentIdle {
		t.Errorf("expected agent status idle, got %s", updatedAgent.Status)
	}

	// Verify no constraint_violations in task context.
	if updatedTask.Context != nil {
		if _, ok := updatedTask.Context["constraint_violations"]; ok {
			t.Error("expected no constraint_violations in task context")
		}
	}
}

// TestPostAgentConstraint_Pass verifies that when constraints exist and pass,
// the agent completes normally and the subtask transitions to DONE.
func TestPostAgentConstraint_Pass(t *testing.T) {
	bareRepoPath := setupTestRepoWithMainBranch(t)

	featureName := "constraint-pass"
	featureDir := createFeatureWorktree(t, bareRepoPath, featureName)

	// Add a .drem/constraints.toml to the feature worktree with a generous max_lines limit.
	dremDir := filepath.Join(featureDir, ".drem")
	if err := os.MkdirAll(dremDir, 0o755); err != nil {
		t.Fatal(err)
	}
	constraintsContent := `
[[max_lines]]
name = "Go file size"
glob = "**/*.go"
limit = 1000
`
	if err := os.WriteFile(filepath.Join(dremDir, "constraints.toml"), []byte(constraintsContent), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitCmd(t, featureDir, "add", ".")
	runGitCmd(t, featureDir, "commit", "-m", "add constraints config")

	// Create an agent branch with a small Go file (under the limit).
	agentBranch := "worktree-agent-pass"
	featureBranch := "feature/" + featureName
	runGitCmd(t, bareRepoPath, "branch", agentBranch, featureBranch)
	agentDir := filepath.Join(bareRepoPath, "feature", featureName, "agent-pass")
	runGitCmd(t, bareRepoPath, "worktree", "add", agentDir, agentBranch)
	runGitCmd(t, agentDir, "config", "user.email", "test@test.com")
	runGitCmd(t, agentDir, "config", "user.name", "Test")

	// Write a small Go file (well under 1000 lines).
	smallFile := "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n"
	if err := os.WriteFile(filepath.Join(agentDir, "small.go"), []byte(smallFile), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitCmd(t, agentDir, "add", ".")
	runGitCmd(t, agentDir, "commit", "-m", "add small go file")

	o, _ := agentResultOrchestrator(t, bareRepoPath)
	wt := NewHostWorktreeManager(bareRepoPath, "main")
	o.worktree = wt

	parentID := uuid.New()
	parent := model.Task{
		ID:             parentID,
		ProjectID:      o.projectID,
		Title:          "parent-pass",
		Description:    "parent for passing constraint test",
		Status:         model.StatusInProgress,
		WorktreeBranch: featureBranch,
	}
	o.db.Create(&parent)

	taskID := uuid.New()
	agentID := uuid.New()
	ag := model.Agent{
		ID:             agentID,
		ProjectID:      o.projectID,
		AgentType:      model.AgentCoder,
		Name:           "coder-pass",
		Status:         model.AgentWorking,
		CurrentTaskID:  &taskID,
		WorktreePath:   agentDir,
		WorktreeBranch: agentBranch,
	}
	o.db.Create(&ag)

	task := model.Task{
		ID:              taskID,
		ProjectID:       o.projectID,
		ParentTaskID:    &parentID,
		Title:           "subtask-pass",
		Description:     "should pass constraints and complete",
		Status:          model.StatusInProgress,
		AssignedAgentID: &agentID,
	}
	o.db.Create(&task)

	err := o.processAgentResult(agent.Completion{
		AgentID:    agentID,
		ReturnCode: 0,
	})
	if err != nil {
		t.Fatalf("processAgentResult: %v", err)
	}

	// Verify task completed to DONE.
	var updatedTask model.Task
	o.db.First(&updatedTask, "id = ?", taskID)
	if updatedTask.Status != model.StatusDone {
		t.Errorf("expected task status done, got %s", updatedTask.Status)
	}

	// Verify agent is idle.
	var updatedAgent model.Agent
	o.db.First(&updatedAgent, "id = ?", agentID)
	if updatedAgent.Status != model.AgentIdle {
		t.Errorf("expected agent status idle, got %s", updatedAgent.Status)
	}
}

// TestPostAgentConstraint_Fail verifies that when constraints are violated,
// the subtask fails with feedback containing the violation report.
func TestPostAgentConstraint_Fail(t *testing.T) {
	bareRepoPath := setupTestRepoWithMainBranch(t)

	constraintsContent := `
[[max_lines]]
name = "Go file size"
glob = "**/*.go"
limit = 5
`

	// Create a main worktree with the constraints config committed so the
	// constraint gate's delta evaluation has a valid baseline to compare
	// against. Without this, EvaluateDelta is skipped and new violations
	// are not detected.
	mainDir := filepath.Join(bareRepoPath, "main")
	runGitCmd(t, bareRepoPath, "worktree", "add", mainDir, "main")
	runGitCmd(t, mainDir, "config", "user.email", "test@test.com")
	runGitCmd(t, mainDir, "config", "user.name", "Test")
	mainDremDir := filepath.Join(mainDir, ".drem")
	if err := os.MkdirAll(mainDremDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mainDremDir, "constraints.toml"), []byte(constraintsContent), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitCmd(t, mainDir, "add", ".")
	runGitCmd(t, mainDir, "commit", "-m", "add constraints config to main")

	featureName := "constraint-fail"
	featureDir := createFeatureWorktree(t, bareRepoPath, featureName)
	_ = featureDir

	// Create an agent branch with a file that exceeds the limit.
	agentBranch := "worktree-agent-fail"
	featureBranch := "feature/" + featureName
	runGitCmd(t, bareRepoPath, "branch", agentBranch, featureBranch)
	agentDir := filepath.Join(bareRepoPath, "feature", featureName, "agent-fail-constraint")
	runGitCmd(t, bareRepoPath, "worktree", "add", agentDir, agentBranch)
	runGitCmd(t, agentDir, "config", "user.email", "test@test.com")
	runGitCmd(t, agentDir, "config", "user.name", "Test")

	// Write a Go file that exceeds 5 lines.
	var bigFile strings.Builder
	bigFile.WriteString("package main\n\n")
	for i := 0; i < 20; i++ {
		bigFile.WriteString("// This is a line that pushes the file over the limit.\n")
	}
	bigFile.WriteString("func main() {}\n")
	if err := os.WriteFile(filepath.Join(agentDir, "big.go"), []byte(bigFile.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitCmd(t, agentDir, "add", ".")
	runGitCmd(t, agentDir, "commit", "-m", "add big go file")

	o, _ := agentResultOrchestrator(t, bareRepoPath)
	wt := NewHostWorktreeManager(bareRepoPath, "main")
	o.worktree = wt

	parentID := uuid.New()
	parent := model.Task{
		ID:             parentID,
		ProjectID:      o.projectID,
		Title:          "parent-fail",
		Description:    "parent for failing constraint test",
		Status:         model.StatusInProgress,
		WorktreeBranch: featureBranch,
	}
	o.db.Create(&parent)

	taskID := uuid.New()
	agentID := uuid.New()
	ag := model.Agent{
		ID:             agentID,
		ProjectID:      o.projectID,
		AgentType:      model.AgentCoder,
		Name:           "coder-fail",
		Status:         model.AgentWorking,
		CurrentTaskID:  &taskID,
		WorktreePath:   agentDir,
		WorktreeBranch: agentBranch,
	}
	o.db.Create(&ag)

	task := model.Task{
		ID:              taskID,
		ProjectID:       o.projectID,
		ParentTaskID:    &parentID,
		Title:           "subtask-fail-constraint",
		Description:     "should fail due to constraint violations",
		Status:          model.StatusInProgress,
		AssignedAgentID: &agentID,
	}
	o.db.Create(&task)

	err := o.processAgentResult(agent.Completion{
		AgentID:    agentID,
		ReturnCode: 0,
	})
	if err != nil {
		t.Fatalf("processAgentResult: %v", err)
	}

	// Verify subtask status is failed.
	var updatedTask model.Task
	o.db.First(&updatedTask, "id = ?", taskID)
	if updatedTask.Status != model.StatusFailed {
		t.Errorf("expected task status failed, got %s", updatedTask.Status)
	}

	// Verify constraint_violations is set in task context.
	if updatedTask.Context == nil {
		t.Fatal("expected task context to be non-nil")
	}
	violations, ok := updatedTask.Context["constraint_violations"]
	if !ok {
		t.Fatal("expected constraint_violations in task context")
	}
	violationNames, ok := violations.([]any)
	if !ok {
		t.Fatalf("expected constraint_violations to be a slice, got %T", violations)
	}
	if len(violationNames) == 0 {
		t.Fatal("expected at least one violation name in constraint_violations")
	}
	joined := ""
	for _, v := range violationNames {
		if s, ok := v.(string); ok {
			joined += s + " "
		}
	}
	if !strings.Contains(joined, "Go file size") {
		t.Errorf("expected violations to reference the failing rule, got: %s", joined)
	}

	// Verify agent status is idle (not dead — work is preserved).
	var updatedAgent model.Agent
	o.db.First(&updatedAgent, "id = ?", agentID)
	if updatedAgent.Status != model.AgentIdle {
		t.Errorf("expected agent status idle, got %s", updatedAgent.Status)
	}
	if updatedAgent.CurrentTaskID != nil {
		t.Error("expected agent's current task to be nil")
	}

	// Verify a task event with the constraint violation reason exists.
	var events []model.TaskEvent
	o.db.Where("task_id = ?", taskID).Find(&events)
	foundConstraintEvent := false
	for _, evt := range events {
		if evt.EventType == "status_change" && evt.NewValue == "failed" {
			// Check the event details contain the constraint reason.
			if evt.Details != nil {
				if reason, ok := evt.Details["reason"]; ok {
					if reasonStr, ok := reason.(string); ok && strings.Contains(reasonStr, "constraint violations") {
						foundConstraintEvent = true
					}
				}
			}
		}
	}
	if !foundConstraintEvent {
		t.Error("expected a task event with reason 'constraint violations after merge'")
	}
}
