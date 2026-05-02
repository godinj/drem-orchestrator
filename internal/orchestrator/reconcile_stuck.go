package orchestrator

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/gitexec"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/spawner"
)

// reconcileStuckAgents finds tasks in actionable statuses (classifying,
// planning, test_writing, in_progress) whose assigned agent's tmux session
// is dead but no completion was ever received. This catches agents that
// exited without triggering the monitor goroutine. Covers both top-level
// tasks and subtasks.
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

	// Build a set of container IDs that the spawner considers running. In
	// container mode, workers are dispatched via o.Spawner.SpawnWorker and
	// never register with the legacy runner, so without this set every
	// container worker ages past the grace period and gets false-positive
	// killed. On RPC error, log a Warn and proceed with an empty set — this
	// falls back to pre-fix behaviour (container agents may be flagged dead)
	// rather than making a transient spawner outage newly catastrophic.
	containerRunningSet := o.buildContainerRunningSet(context.Background())

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

		// Skip container-mode agents whose container is still running per
		// the spawner. TmuxSession is repurposed to carry the container ID
		// in container mode (see recordContainerOnAgent).
		if ag.TmuxSession != "" && containerRunningSet[ag.TmuxSession] {
			o.logger.Debug("reconcile stuck: container-mode agent still running, skipping",
				"agent_id", ag.ID, "container_id", ag.TmuxSession, "task", task.Title)
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

		// Agentmon-correlation predicate: before declaring a container-mode
		// agent dead, consult the ContainerSightingProbe (backed in prod by
		// agentmon's DockerSource.HasSeen). If the probe is wired AND
		// returns false, agentmon has no live signal for this container —
		// which means either the container is truly gone OR agentmon itself
		// is blind. The v12–v14 incident was the latter: a 41h label-
		// filter mismatch meant agentmon matched zero events and the
		// reconciler killed live agents based on stale DB heartbeats.
		// Skipping the kill here preserves correctness under that failure
		// mode — the operator sees distinct log spam pointing at agentmon,
		// not false-positive kills. Probe=nil preserves pre-container host
		// behaviour unchanged.
		if o.sightingProbe != nil && ag.TmuxSession != "" && !o.sightingProbe.HasSeen(ag.TmuxSession) {
			o.logger.Warn("reconcile stuck: skipping dead-agent kill because agentmon has no sighting",
				"agent_id", ag.ID, "task", task.Title, "container_id", ag.TmuxSession)
			continue
		}

		// Agent is NOT in the runner's running map AND DB status is working.
		o.logger.Warn("detected dead agent session without completion",
			"agent_id", ag.ID, "task", task.Title, "session", ag.TmuxSession)

		// Check if the agent branch has commits.
		featureDir := o.resolveFeatureWorktree(task)
		featureBranch := o.featureBranchForTask(task)
		hasCommits := false
		if featureDir != "" && ag.WorktreeBranch != "" {
			if ag.WorktreeBranch == featureBranch {
				hasCommits = o.featureBranchHasChanges(task, featureDir)
			} else {
				var err error
				hasCommits, err = gitexec.BranchHasNewCommits(context.Background(), featureDir, ag.WorktreeBranch)
				if err != nil {
					o.logger.Warn("reconcile stuck: failed to check commits",
						"agent_id", ag.ID, "error", err)
				}
			}
		}

		if hasCommits {
			// Route through the normal completion path.
			o.logger.Info("reconcile stuck: agent has commits, sending completion",
				"agent_id", ag.ID, "task", task.Title)
			if err := o.synthesizeCompletion(ag.ID); err != nil {
				o.logger.Error("reconcile stuck: process completion",
					"agent_id", ag.ID, "error", err)
			}
		} else {
			// No work produced — check if we can retry before failing.
			ag.Status = model.AgentDead
			ag.CurrentTaskID = nil
			if err := o.db.Save(&ag).Error; err != nil {
				o.logger.Error("reconcile stuck: save agent", "agent_id", ag.ID, "error", err)
				continue
			}
			o.publishAgentStatus(task.ID.String(), ag.ID.String(), string(ag.AgentType), string(model.AgentDead))

			// Auto-retry if under the limit. Use MaxPlannerRetries for
			// PLANNING tasks (planner agents) since they have a different
			// retry budget than generic empty-work retries.
			retryCount := 0
			if task.Context != nil {
				if v, ok := task.Context["retry_count"].(float64); ok {
					retryCount = int(v)
				}
			}
			maxRetries := MaxEmptyWorkRetries
			if task.Status == model.StatusPlanning {
				maxRetries = MaxPlannerRetries
			}
			if retryCount < maxRetries {
				o.logger.Info("reconcile stuck: auto-retrying dead agent task",
					"task_id", task.ID, "retry_count", retryCount)
				task.AssignedAgentID = nil
				if task.Context == nil {
					task.Context = make(model.JSONField)
				}
				task.Context["retry_count"] = float64(retryCount + 1)
				// For pre-dispatch statuses, keep current status so the
				// dispatch loop (e.g. processClassifyingTasks) re-picks
				// the task. Only in_progress subtasks reset to backlog.
				if task.Status == model.StatusInProgress {
					task.Status = model.StatusBacklog
				}
				task.UpdatedAt = time.Now()
				if err := o.db.Save(task).Error; err != nil {
					o.logger.Error("reconcile stuck: save task for retry", "task_id", task.ID, "error", err)
				}
			} else {
				if err := o.failTask(task, "agent session died without producing commits"); err != nil {
					o.logger.Error("reconcile stuck: fail task", "task_id", task.ID, "error", err)
				}
			}
		}
		fixed++
	}
	return fixed, nil
}

// buildContainerRunningSet returns the set of container IDs the spawner
// currently reports as running for this project. When no spawner is
// configured (host-only mode) the set is empty. When the spawner RPC
// fails, a Warn is logged and the empty set is returned — the reconciler
// then behaves as it did before container-awareness was added: legacy
// agents still work, container agents may be false-positive killed. This
// is a deliberate trade: we do NOT want a transient spawner outage to
// block stuck-agent recovery for host-mode agents that actually are dead.
func (o *Orchestrator) buildContainerRunningSet(ctx context.Context) map[string]bool {
	set := make(map[string]bool)
	if o.Spawner == nil {
		return set
	}
	res, err := o.Spawner.ListWorkers(ctx, spawner.ListWorkersParams{ProjectID: o.projectID.String()})
	if err != nil {
		o.logger.Warn("reconcile stuck: list workers failed, falling back to empty container-running set",
			"error", err)
		return set
	}
	for _, w := range res.Workers {
		if w.Status == "running" {
			set[w.ContainerID] = true
		}
	}
	return set
}
