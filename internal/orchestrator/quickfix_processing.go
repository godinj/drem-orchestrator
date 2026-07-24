package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/prompt"
)

// MaxQuickFixRetries is the number of times the orchestrator will retry a
// quick fix agent before failing the task.
const MaxQuickFixRetries = 3

const quickFixDeliveryReworkPendingKey = "delivery_rework_pending"

func quickFixNeedsRespawn(task *model.Task) bool {
	if task == nil || task.Context == nil {
		return false
	}
	_, emptyWork := task.Context["empty_work"]
	_, deliveryRework := task.Context[quickFixDeliveryReworkPendingKey]
	return emptyWork || deliveryRework
}

func clearQuickFixRespawnFlags(task *model.Task) {
	if task == nil || task.Context == nil {
		return
	}
	delete(task.Context, "empty_work")
	delete(task.Context, quickFixDeliveryReworkPendingKey)
}

// processQuickFix handles quick fix tasks, transitioning them from BACKLOG
// directly to IN_PROGRESS and spawning a coder agent. Quick fix tasks skip
// the planning and TDD lifecycle gates.
func (o *Orchestrator) processQuickFix(task *model.Task) error {
	// 1. If an agent is already assigned, check if it's still running.
	if task.AssignedAgentID != nil {
		var ag model.Agent
		if err := o.db.First(&ag, "id = ?", task.AssignedAgentID).Error; err != nil {
			// Agent record missing — clear assignment and retry.
			task.AssignedAgentID = nil
			retries := o.incrementRetryCount(task)
			if retries >= MaxQuickFixRetries {
				return o.failTask(task, "quick fix agent disappeared after max retries")
			}
			return o.db.Save(task).Error
		}

		if ag.Status == model.AgentDead || ag.Status == model.AgentIdle {
			if ag.WorktreeBranch != "" {
				if err := o.cleanupTaskWorkerBranch(context.Background(), task, ag.WorktreeBranch); err != nil {
					o.logger.Warn("cleanup dead quickfix agent worktree", "agent_id", ag.ID, "error", err)
				}
			}
			task.AssignedAgentID = nil
			retries := o.incrementRetryCount(task)
			if retries >= MaxQuickFixRetries {
				return o.failTask(task, "quick fix agent failed after max retries")
			}
			o.logger.Warn("quickfix agent dead/idle, will retry", "task_id", task.ID, "retries", retries)
			return o.db.Save(task).Error
		}

		// Agent is still working — nothing to do.
		return nil
	}

	// 2. Check capacity. Container-mode dispatch (o.Spawner != nil) bypasses
	// this gate because container lifecycles are governed by docker's
	// scheduler, not the in-process subprocess limiter. Legacy runner path
	// keeps the CanSpawn gate so host-dev invocations still backpressure.
	if o.Spawner == nil && (o.runner == nil || !o.runner.CanSpawn()) {
		return nil
	}

	// 3. Create feature worktree if needed.
	if task.WorktreeBranch == "" {
		featureName := taskFeatureName(task)
		wtInfo, err := o.worktree.CreateFeature(featureName)
		if err != nil {
			return fmt.Errorf("process quick fix: create feature: %w", err)
		}

		// Generate repo map in the new feature worktree (non-blocking on failure).
		o.worktree.GenerateRepoMapAsync(wtInfo.Path)

		task.WorktreeBranch = wtInfo.Branch
		task.WorktreeBaseSHA = wtInfo.Head
		if err := o.db.Save(task).Error; err != nil {
			return fmt.Errorf("process quick fix: save worktree branch: %w", err)
		}
	}

	// 4. Transition backlog → in_progress.
	oldStatus := task.Status
	if err := o.transitionTaskAtomic(task, model.StatusInProgress, "orchestrator", "quickfix_dispatch",
		"quick fix selected for direct implementation", nil); err != nil {
		return fmt.Errorf("process quick fix: %w", err)
	}
	event := &model.TaskEvent{TaskID: task.ID, OldValue: string(oldStatus), NewValue: string(task.Status)}

	// 5. Dispatch: container mode first (o.Spawner wired), legacy runner
	// fallback second. See plans/phase-3.5b-quickfix-migration.md for the
	// migration rationale (T3 canary regressed because the classifier
	// picked quickfix, which still shelled out to `claude` on the orch
	// container's PATH).
	if o.shouldUseDirectToolAgent(task, model.AgentCoder) {
		return o.dispatchQuickFixDirect(task, event)
	}
	if o.Spawner != nil {
		return o.dispatchQuickFixViaSpawner(task, event)
	}

	// Legacy host-subprocess path. Retained for local dev on a host with
	// claude installed; production runs the container path above.
	var project model.Project
	if err := o.db.First(&project, "id = ?", o.projectID).Error; err != nil {
		return fmt.Errorf("process quick fix: load project: %w", err)
	}

	featureName := strings.TrimPrefix(task.WorktreeBranch, "feature/")
	featureDir := o.worktree.FeatureWorktreePath(featureName)
	comments, _ := o.GetComments(task.ID)
	coderPrompt := prompt.Generate(prompt.Opts{
		Task:         task,
		Project:      &project,
		AgentType:    model.AgentCoder,
		WorktreePath: featureDir,
		Comments:     comments,
	})

	ag, err := o.runner.SpawnAgent(task, featureName, model.AgentCoder, coderPrompt)
	if err != nil {
		return fmt.Errorf("process quick fix: spawn agent: %w", err)
	}

	task.AssignedAgentID = &ag.ID
	if err := o.db.Save(task).Error; err != nil {
		return fmt.Errorf("process quick fix: save assigned agent: %w", err)
	}

	o.emit("quickfix_started", map[string]any{"task_id": task.ID, "agent_id": ag.ID})
	o.publishTaskTransition(task.ID.String(), event.OldValue, event.NewValue, "quickfix started")
	o.publishAgentStatus(task.ID.String(), ag.ID.String(), string(ag.AgentType), string(model.AgentWorking))
	o.logger.Info("quickfix started", "task_id", task.ID, "agent_id", ag.ID)
	return nil
}

// dispatchQuickFixViaSpawner routes the quickfix coder spawn through the
// container spawner (o.spawnCoder → o.Spawner.SpawnWorker). Called when
// o.Spawner is wired; the caller has already transitioned the task to
// IN_PROGRESS (event is the resulting TaskEvent) so this function only
// handles spawn + post-spawn bookkeeping. Mirrors dispatchSubtaskViaSpawner
// in subtask_scheduling.go: reload the task so AssignedAgentID written by
// worker identity recording is visible, then emit the quickfix_started event
// and publish task-transition + agent-status updates using the reloaded
// agent ID (no local `ag` handle exists in the container path).
func (o *Orchestrator) dispatchQuickFixViaSpawner(task *model.Task, event *model.TaskEvent) error {
	launch, err := o.workerLaunchService().Launch(context.Background(), task, model.AgentCoder)
	if err != nil {
		if errors.Is(err, errWorkerImageUnavailable) {
			if failErr := o.failTask(task, err.Error()); failErr != nil {
				return fmt.Errorf("process quick fix: fail after worker image preflight: %w", failErr)
			}
		}
		return fmt.Errorf("process quick fix: spawn coder: %w", err)
	}

	// Reload so AssignedAgentID (written by worker identity recording during
	// the spawn) is visible to the rest of the quickfix flow.
	if err := o.db.First(task, "id = ?", task.ID).Error; err != nil {
		return fmt.Errorf("process quick fix: reload task after container spawn: %w", err)
	}
	if task.AssignedAgentID == nil {
		return fmt.Errorf("process quick fix: no agent assignment after container spawn")
	}

	// Source the agent handle from the reloaded task — there's no local
	// `ag` in the container path.
	var ag model.Agent
	if err := o.db.First(&ag, "id = ?", launch.AgentID).Error; err != nil {
		return fmt.Errorf("process quick fix: load agent after spawn: %w", err)
	}

	o.emit("quickfix_started", map[string]any{"task_id": task.ID, "agent_id": ag.ID})
	o.publishTaskTransition(task.ID.String(), event.OldValue, event.NewValue, "quickfix started")
	o.publishAgentStatus(task.ID.String(), ag.ID.String(), string(ag.AgentType), string(model.AgentWorking))
	o.logger.Info("quickfix started via spawner", "task_id", task.ID, "agent_id", ag.ID)
	return nil
}

// respawnQuickFixAgent handles the retry path for quickfix tasks. When a
// quickfix agent produces empty work (exits without commits), onAgentEmptyWork
// clears AssignedAgentID and sets empty_work=true. This method is called from
// the doTick IN_PROGRESS handler to spawn a fresh agent for the retry.
// The prompt_adjustment from supervisor diagnosis is automatically included
// in the regenerated prompt via task.Context["prompt_adjustment"].
func (o *Orchestrator) respawnQuickFixAgent(task *model.Task) error {
	// Check capacity. Container mode bypasses the subprocess-runner gate
	// for the same reason processQuickFix does (per-container scheduling
	// lives in docker, not in-process).
	if o.Spawner == nil && (o.runner == nil || !o.runner.CanSpawn()) {
		return nil // wait for capacity
	}

	// Direct tool dispatch only bypasses the spawner in legacy no-container
	// mode. In production, sglang-direct runs as a worker harness inside its
	// own container so tool execution never happens in orch.
	if o.shouldUseDirectToolAgent(task, model.AgentCoder) {
		if err := o.dispatchQuickFixDirect(task, nil); err != nil {
			return err
		}
		clearQuickFixRespawnFlags(task)
		return o.db.Save(task).Error
	}

	// Container-mode dispatch: prefer o.spawnCoder when the spawner is wired,
	// falling back to runner.SpawnAgent only when o.Spawner is nil.
	// State-machine cleanup (clearing empty_work) must still happen in both
	// paths — it's not a spawn concern.
	if o.Spawner != nil {
		return o.respawnQuickFixAgentViaSpawner(task)
	}

	// Legacy host-subprocess path. Load project for prompt context.
	var project model.Project
	if err := o.db.First(&project, "id = ?", o.projectID).Error; err != nil {
		return fmt.Errorf("respawn quickfix agent: load project: %w", err)
	}

	// Generate coder prompt (includes prompt_adjustment from prior diagnosis).
	featureName := strings.TrimPrefix(task.WorktreeBranch, "feature/")
	featureDir := o.worktree.FeatureWorktreePath(featureName)
	comments, _ := o.GetComments(task.ID)
	coderPrompt := prompt.Generate(prompt.Opts{
		Task:         task,
		Project:      &project,
		AgentType:    model.AgentCoder,
		WorktreePath: featureDir,
		Comments:     comments,
	})

	ag, err := o.runner.SpawnAgent(task, featureName, model.AgentCoder, coderPrompt)
	if err != nil {
		return fmt.Errorf("respawn quickfix agent: spawn: %w", err)
	}

	// Clear the empty_work flag now that a new agent is assigned.
	// If this agent also produces empty work, onAgentEmptyWork will set
	// it again and the cycle repeats until MaxEmptyWorkRetries is reached.
	clearQuickFixRespawnFlags(task)
	task.AssignedAgentID = &ag.ID
	if err := o.db.Save(task).Error; err != nil {
		return fmt.Errorf("respawn quickfix agent: save: %w", err)
	}

	o.emit("quickfix_retry", map[string]any{"task_id": task.ID, "agent_id": ag.ID})
	o.publishAgentStatus(task.ID.String(), ag.ID.String(), string(ag.AgentType), string(model.AgentWorking))
	o.logger.Info("quickfix agent respawned for retry", "task_id", task.ID, "agent_id", ag.ID)
	return nil
}

// respawnQuickFixAgentViaSpawner handles the container-mode retry path for
// quickfix empty-work recovery. Mirrors dispatchQuickFixViaSpawner but
// without the state-machine transition (the task is already in IN_PROGRESS
// when the retry fires) and with the empty_work cleanup step that's
// specific to the retry path.
//
// The empty_work context key is cleared regardless of which dispatch path
// ran — it's state-machine cleanup, not a spawn concern. If this respawned
// agent also produces empty work, onAgentEmptyWork will set the flag again
// and the cycle repeats until MaxEmptyWorkRetries.
func (o *Orchestrator) respawnQuickFixAgentViaSpawner(task *model.Task) error {
	launch, err := o.workerLaunchService().Launch(context.Background(), task, model.AgentCoder)
	if err != nil {
		if errors.Is(err, errWorkerImageUnavailable) {
			if failErr := o.failTask(task, err.Error()); failErr != nil {
				return fmt.Errorf("respawn quickfix agent: fail after worker image preflight: %w", failErr)
			}
		}
		return fmt.Errorf("respawn quickfix agent: spawn via spawner: %w", err)
	}

	if err := o.db.First(task, "id = ?", task.ID).Error; err != nil {
		return fmt.Errorf("respawn quickfix agent: reload task: %w", err)
	}
	if task.AssignedAgentID == nil {
		return fmt.Errorf("respawn quickfix agent: no agent assignment after container spawn")
	}

	// Clear the empty_work flag now that a new agent is assigned. Persist
	// the context update on top of the AssignedAgentID that spawnCoder
	// already wrote, so both updates land in a single Save.
	clearQuickFixRespawnFlags(task)
	if err := o.db.Save(task).Error; err != nil {
		return fmt.Errorf("respawn quickfix agent: save after spawn: %w", err)
	}

	var ag model.Agent
	if err := o.db.First(&ag, "id = ?", launch.AgentID).Error; err != nil {
		return fmt.Errorf("respawn quickfix agent: load agent after spawn: %w", err)
	}

	o.emit("quickfix_retry", map[string]any{"task_id": task.ID, "agent_id": ag.ID})
	o.publishAgentStatus(task.ID.String(), ag.ID.String(), string(ag.AgentType), string(model.AgentWorking))
	o.logger.Info("quickfix agent respawned via spawner for retry",
		"task_id", task.ID, "agent_id", ag.ID)
	return nil
}
