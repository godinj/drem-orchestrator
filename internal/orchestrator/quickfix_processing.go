package orchestrator

import (
	"fmt"
	"strings"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/prompt"
	"github.com/godinj/drem-orchestrator/internal/state"
	"github.com/godinj/drem-orchestrator/internal/worktree"
)

// MaxQuickFixRetries is the number of times the orchestrator will retry a
// quick fix agent before failing the task.
const MaxQuickFixRetries = 3

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
				if err := o.worktree.RemoveAgentWorktree(ag.WorktreeBranch); err != nil {
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

	// 2. Check capacity.
	if o.runner == nil || !o.runner.CanSpawn() {
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
		worktree.GenerateRepoMapAsync(wtInfo.Path)

		task.WorktreeBranch = wtInfo.Branch
		if err := o.db.Save(task).Error; err != nil {
			return fmt.Errorf("process quick fix: save worktree branch: %w", err)
		}
	}

	// 4. Transition backlog → in_progress.
	event, err := state.TransitionTask(task, model.StatusInProgress, "orchestrator", map[string]any{"reason": "quickfix-direct"})
	if err != nil {
		return fmt.Errorf("process quick fix: %w", err)
	}
	if err := o.db.Save(task).Error; err != nil {
		return fmt.Errorf("process quick fix: save task: %w", err)
	}
	if err := o.db.Create(event).Error; err != nil {
		return fmt.Errorf("process quick fix: save event: %w", err)
	}

	// 5. Generate coder prompt and spawn agent.
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

// respawnQuickFixAgent handles the retry path for quickfix tasks. When a
// quickfix agent produces empty work (exits without commits), onAgentEmptyWork
// clears AssignedAgentID and sets empty_work=true. This method is called from
// the doTick IN_PROGRESS handler to spawn a fresh agent for the retry.
// The prompt_adjustment from supervisor diagnosis is automatically included
// in the regenerated prompt via task.Context["prompt_adjustment"].
func (o *Orchestrator) respawnQuickFixAgent(task *model.Task) error {
	// Check capacity.
	if o.runner == nil || !o.runner.CanSpawn() {
		return nil // wait for capacity
	}

	// Load project for prompt context.
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
	delete(task.Context, "empty_work")
	task.AssignedAgentID = &ag.ID
	if err := o.db.Save(task).Error; err != nil {
		return fmt.Errorf("respawn quickfix agent: save: %w", err)
	}

	o.emit("quickfix_retry", map[string]any{"task_id": task.ID, "agent_id": ag.ID})
	o.publishAgentStatus(task.ID.String(), ag.ID.String(), string(ag.AgentType), string(model.AgentWorking))
	o.logger.Info("quickfix agent respawned for retry", "task_id", task.ID, "agent_id", ag.ID)
	return nil
}
