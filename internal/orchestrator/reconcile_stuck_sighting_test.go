package orchestrator

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
)

// fakeSightingProbe is a minimal ContainerSightingProbe that returns
// a configured result and records each query. Used to exercise the
// reconciler's agentmon-correlation predicate without needing a real
// Docker subscription.
type fakeSightingProbe struct {
	seen     map[string]bool
	queries  []string
	defaultV bool
}

func (f *fakeSightingProbe) HasSeen(containerID string) bool {
	f.queries = append(f.queries, containerID)
	if v, ok := f.seen[containerID]; ok {
		return v
	}
	return f.defaultV
}

// TestReconcileStuck_SkipsKillWhenAgentmonUnsighted asserts the
// v12–v14 fix: when the configured ContainerSightingProbe reports
// that agentmon has NEVER observed the agent's container, the
// reconciler must skip the kill rather than proceed on stale DB
// heartbeats. The agent's status must be unchanged.
func TestReconcileStuck_SkipsKillWhenAgentmonUnsighted(t *testing.T) {
	orch, db, bareRepo := setupReconcileTest(t)

	featureName := "unsighted-agent"
	createFeatureWorktree(t, bareRepo, featureName)

	agentID := uuid.New()
	taskID := uuid.New()
	containerID := "ctr-unsighted"

	ag := model.Agent{
		ID:             agentID,
		ProjectID:      orch.projectID,
		AgentType:      model.AgentCoder,
		Name:           "unsighted-agent",
		Status:         model.AgentWorking,
		WorktreeBranch: "",
		CurrentTaskID:  &taskID,
		// TmuxSession carries the container ID in container mode; see
		// recordContainerOnAgent. The probe keys off this field.
		TmuxSession: containerID,
	}
	db.Create(&ag)
	// Age past the grace period so reconcileStuckAgents does not skip.
	db.Model(&ag).Update("created_at", time.Now().Add(-2*agentSpawnGracePeriod))

	task := model.Task{
		ID:              taskID,
		ProjectID:       orch.projectID,
		Title:           "unsighted-task",
		Description:     "container agent that agentmon never saw",
		Status:          model.StatusInProgress,
		AssignedAgentID: &agentID,
		WorktreeBranch:  "feature/" + featureName,
	}
	db.Create(&task)

	// Install a probe that claims NOT to have seen this container.
	probe := &fakeSightingProbe{defaultV: false}
	orch.SetContainerSightingProbe(probe)

	fixes, err := orch.reconcileStuckAgents()
	if err != nil {
		t.Fatalf("reconcileStuckAgents: %v", err)
	}
	// Skip should NOT count as a fix — the agent was untouched.
	if fixes != 0 {
		t.Errorf("expected 0 fixes when agentmon unsighted, got %d", fixes)
	}

	// Agent must still be AgentWorking — the reconciler bailed out
	// before touching the row.
	var agAfter model.Agent
	if err := db.First(&agAfter, "id = ?", agentID).Error; err != nil {
		t.Fatalf("load agent: %v", err)
	}
	if agAfter.Status != model.AgentWorking {
		t.Errorf("agent status was mutated despite unsighted-skip: got %s, want %s",
			agAfter.Status, model.AgentWorking)
	}
	if agAfter.CurrentTaskID == nil || *agAfter.CurrentTaskID != taskID {
		t.Errorf("agent CurrentTaskID was cleared despite unsighted-skip")
	}

	// Task must still be InProgress — no retry_count bump, no status flip.
	var taskAfter model.Task
	if err := db.First(&taskAfter, "id = ?", taskID).Error; err != nil {
		t.Fatalf("load task: %v", err)
	}
	if taskAfter.Status != model.StatusInProgress {
		t.Errorf("task status was mutated despite unsighted-skip: got %s, want %s",
			taskAfter.Status, model.StatusInProgress)
	}
	if taskAfter.AssignedAgentID == nil {
		t.Errorf("task AssignedAgentID was cleared despite unsighted-skip")
	}

	// The probe must have been consulted with the container ID.
	if len(probe.queries) == 0 {
		t.Error("probe was never consulted; correlation predicate did not fire")
	}
	if probe.queries[0] != containerID {
		t.Errorf("probe queried with %q, want %q", probe.queries[0], containerID)
	}
}

// TestReconcileStuck_ProcessesKillWhenAgentmonSighted asserts the
// correlation predicate does NOT obstruct genuine stuck-agent kills:
// when the probe confirms sighting, the reconciler proceeds with its
// normal fail/retry behaviour. This keeps the correction narrow —
// we only block kills when agentmon is the suspect.
func TestReconcileStuck_ProcessesKillWhenAgentmonSighted(t *testing.T) {
	orch, db, bareRepo := setupReconcileTest(t)

	featureName := "sighted-agent"
	createFeatureWorktree(t, bareRepo, featureName)

	agentID := uuid.New()
	taskID := uuid.New()
	containerID := "ctr-sighted"

	ag := model.Agent{
		ID:             agentID,
		ProjectID:      orch.projectID,
		AgentType:      model.AgentCoder,
		Name:           "sighted-agent",
		Status:         model.AgentWorking,
		WorktreeBranch: "",
		CurrentTaskID:  &taskID,
		TmuxSession:    containerID,
	}
	db.Create(&ag)
	db.Model(&ag).Update("created_at", time.Now().Add(-2*agentSpawnGracePeriod))

	task := model.Task{
		ID:              taskID,
		ProjectID:       orch.projectID,
		Title:           "sighted-task",
		Description:     "container agent that agentmon DID see",
		Status:          model.StatusInProgress,
		AssignedAgentID: &agentID,
		WorktreeBranch:  "feature/" + featureName,
	}
	db.Create(&task)

	probe := &fakeSightingProbe{defaultV: true}
	orch.SetContainerSightingProbe(probe)

	fixes, err := orch.reconcileStuckAgents()
	if err != nil {
		t.Fatalf("reconcileStuckAgents: %v", err)
	}
	// With agentmon confirming sighting and the agent truly stale
	// (runner map empty, no spawner list), the reconciler proceeds.
	// A container-mode agent with no commits at retry_count=0 is
	// reassigned for retry, not failed.
	if fixes != 1 {
		t.Errorf("expected 1 fix when agentmon confirms sighting and agent is stale, got %d", fixes)
	}
}

// TestReconcileStuck_NilProbePreservesLegacyBehaviour asserts that a
// nil probe (the default, host-mode) leaves the reconciler's
// behaviour unchanged from before the correlation predicate was
// introduced. This is the rollback-safety guarantee.
func TestReconcileStuck_NilProbePreservesLegacyBehaviour(t *testing.T) {
	orch, db, bareRepo := setupReconcileTest(t)

	featureName := "legacy-host"
	createFeatureWorktree(t, bareRepo, featureName)

	agentID := uuid.New()
	taskID := uuid.New()

	ag := model.Agent{
		ID:             agentID,
		ProjectID:      orch.projectID,
		AgentType:      model.AgentCoder,
		Name:           "legacy-agent",
		Status:         model.AgentWorking,
		WorktreeBranch: "",
		CurrentTaskID:  &taskID,
		TmuxSession:    "host-tmux-session",
	}
	db.Create(&ag)
	db.Model(&ag).Update("created_at", time.Now().Add(-2*agentSpawnGracePeriod))

	task := model.Task{
		ID:              taskID,
		ProjectID:       orch.projectID,
		Title:           "legacy-task",
		Description:     "host-mode agent, no probe wired",
		Status:          model.StatusInProgress,
		AssignedAgentID: &agentID,
		WorktreeBranch:  "feature/" + featureName,
	}
	db.Create(&task)

	// No probe installed — sightingProbe stays nil.

	fixes, err := orch.reconcileStuckAgents()
	if err != nil {
		t.Fatalf("reconcileStuckAgents: %v", err)
	}
	// Legacy behaviour: the reconciler proceeds with the fail/retry
	// path since the agent is stale and there is nothing to skip on.
	if fixes != 1 {
		t.Errorf("expected 1 fix in legacy nil-probe path, got %d", fixes)
	}
}
