package orchestrator

import (
	"time"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/agent"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/workeridentity"
)

// reconcileStuckAgents finds tasks in actionable statuses (classifying,
// planning, test_writing, in_progress) whose legacy host agent is absent from
// the runner. It never infers success from Git state. Container workers are
// exclusively owned by the spawner/WorkerAttempt lifecycle and are skipped;
// a legacy loss is fed into the normal typed failure handler.
func (o *Orchestrator) reconcileStuckAgents() (int, error) {
	var tasks []model.Task
	if err := o.db.Where(
		"project_id = ? AND status IN ? AND assigned_agent_id IS NOT NULL",
		o.projectID, []model.TaskStatus{model.StatusClassifying, model.StatusPlanning, model.StatusTestWriting, model.StatusInProgress},
	).Find(&tasks).Error; err != nil {
		return 0, err
	}

	// Build a set of agent IDs that the runner considers active.
	runningSet := make(map[uuid.UUID]bool)
	if o.runner != nil {
		for _, ra := range o.runner.GetRunningAgents() {
			runningSet[ra.AgentID] = true
		}
	}

	fixed := 0
	for i := range tasks {
		task := &tasks[i]

		var ag model.Agent
		if err := o.db.First(&ag, "id = ?", task.AssignedAgentID).Error; err != nil {
			continue
		}

		// Only act on agents that are still marked as working in the DB.
		if ag.Status != model.AgentWorking {
			continue
		}

		// Skip agents that the runner still considers active.
		if runningSet[ag.ID] {
			continue
		}

		handle := workeridentity.FromAgent(ag)

		// The spawner's terminal WorkerAttempt result is the only normal
		// completion/failure source for container workers. Absence, a stale DB
		// heartbeat, or agentmon visibility is not evidence that permits a task
		// transition, so recovery leaves the worker untouched.
		if handle.HasContainer() {
			continue
		}

		// Grace period: skip agents that were recently spawned. This prevents
		// false positives when an agent's process hasn't been fully registered
		// in the runner's running map yet.
		if ag.CreatedAt.After(time.Now().Add(-agentSpawnGracePeriod)) {
			continue
		}

		// Direct-tool agents (sglang-direct) run as goroutines, not subprocess
		// sessions, so they never appear in the runner's running map. Use
		// heartbeat freshness instead — if heartbeat was updated within the
		// timeout window, the goroutine is still alive and making API calls.
		// Use 5 minutes (not agentSpawnGracePeriod=60s) because a single
		// API round-trip to a 26B local model can take 1-3 minutes.
		if ag.Provider == string(model.ProviderSGLangDirect) && ag.HeartbeatAt != nil {
			if ag.HeartbeatAt.After(time.Now().Add(-5 * time.Minute)) {
				continue
			}
		}

		o.logger.Warn("legacy host agent disappeared without a completion",
			"agent_id", ag.ID, "task", task.Title, "session", handle.TmuxSession)
		if err := o.processAgentResult(agent.Completion{
			AgentID:    ag.ID,
			ReturnCode: 1,
			ExitInfo: &agent.ExitInfo{
				ExitReason:  "error",
				ExitSummary: "legacy host agent disappeared before reporting a result",
			},
		}); err != nil {
			o.logger.Error("reconcile stuck: route typed failure",
				"agent_id", ag.ID, "task_id", task.ID, "error", err)
			continue
		}
		fixed++
	}
	return fixed, nil
}
