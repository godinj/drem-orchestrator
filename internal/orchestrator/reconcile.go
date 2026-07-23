package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/gitexec"
	"github.com/godinj/drem-orchestrator/internal/model"
)

// ReconcileResult describes the fixes applied by a single Reconcile run.
type ReconcileResult struct {
	StaleSubtasksReset         int
	OrphanWorktreesCleaned     int
	StuckAgentsRecovered       int
	OrphanedAssignmentsCleared int
	CompletedParentsAdvanced   int
}

// ReapOrphanedSessions kills tmux agent sessions that have no active agent
// and whose process has exited. This is manual-only to avoid destroying
// worktrees before merges complete. Returns the number of sessions reaped.
func (o *Orchestrator) ReapOrphanedSessions() (int, error) {
	return o.runner.ReapOrphanedSessions()
}

// Reconcile audits the project for state inconsistencies and corrects them.
// It is called periodically from doTick and can also be invoked on demand
// from the TUI. Returns the number of fixes applied.
func (o *Orchestrator) Reconcile() (int, error) {
	var r ReconcileResult

	if n, err := o.reconcileStaleSubtasks(); err != nil {
		return 0, fmt.Errorf("reconcile stale subtasks: %w", err)
	} else {
		r.StaleSubtasksReset = n
	}

	if n, err := o.reconcileOrphanWorktrees(); err != nil {
		return 0, fmt.Errorf("reconcile orphan worktrees: %w", err)
	} else {
		r.OrphanWorktreesCleaned = n
	}

	if n, err := o.reconcileStuckAgents(); err != nil {
		return 0, fmt.Errorf("reconcile stuck agents: %w", err)
	} else {
		r.StuckAgentsRecovered = n
	}

	if n, err := o.reconcileOrphanedTaskAssignments(); err != nil {
		return 0, fmt.Errorf("reconcile orphaned task assignments: %w", err)
	} else {
		r.OrphanedAssignmentsCleared = n
	}

	if n, err := o.reconcileCompletedParents(); err != nil {
		return 0, fmt.Errorf("reconcile completed parents: %w", err)
	} else {
		r.CompletedParentsAdvanced = n
	}

	total := r.StaleSubtasksReset + r.OrphanWorktreesCleaned + r.StuckAgentsRecovered + r.OrphanedAssignmentsCleared + r.CompletedParentsAdvanced
	if total > 0 {
		o.emit("reconcile", r)
	}
	return total, nil
}

// reconcileStaleSubtasks finds subtasks marked DONE whose parent is still
// IN_PROGRESS and verifies the subtask's agent actually contributed commits
// to the feature branch. Subtasks that are DONE but have no corresponding
// work are reset to BACKLOG for rescheduling.
func (o *Orchestrator) reconcileStaleSubtasks() (int, error) {
	// Find IN_PROGRESS parents with at least one DONE subtask.
	var parents []model.Task
	if err := o.db.Where(
		"project_id = ? AND status = ? AND parent_task_id IS NULL",
		o.projectID, model.StatusInProgress,
	).Find(&parents).Error; err != nil {
		return 0, err
	}

	fixed := 0
	for _, parent := range parents {
		if parent.WorktreeBranch == "" {
			continue
		}

		var subs []model.Task
		if err := o.db.Where("parent_task_id = ? AND status = ?", parent.ID, model.StatusDone).
			Find(&subs).Error; err != nil {
			continue
		}

		fn := strings.TrimPrefix(parent.WorktreeBranch, "feature/")
		featureDir := o.worktree.FeatureWorktreePath(fn)

		// Get the set of files changed on the feature branch. If empty,
		// every DONE subtask is suspect.
		changedFiles, err := gitexec.GetChangedFiles(context.Background(), featureDir, o.worktree.DefaultBranchName())
		if err != nil {
			continue
		}
		if hasMeaningfulWorkPaths(changedFiles) {
			// Feature branch has changes — subtasks plausibly contributed.
			continue
		}

		// Feature branch has no changes but subtasks claim to be done.
		for i := range subs {
			sub := &subs[i]
			o.logger.Warn("reconcile: preserving done subtask with no feature changes",
				"subtask_id", sub.ID, "parent_id", parent.ID)

			// DONE is terminal. Do not reopen it implicitly: doing so can
			// reschedule already-merged child work and corrupt parent accounting.
			sub.AssignedAgentID = nil
			sub.UpdatedAt = time.Now()
			if sub.Context == nil {
				sub.Context = make(model.JSONField)
			}
			sub.Context["reconciled"] = true
			sub.Context["reconcile_reason"] = "subtask was done but feature branch has no changes; terminal status preserved"
			if err := o.db.Save(sub).Error; err != nil {
				o.logger.Error("reconcile: save subtask", "subtask_id", sub.ID, "error", err)
				continue
			}
			fixed++
		}
	}
	return fixed, nil
}

// reconcileOrphanWorktrees finds agent worktrees in each feature directory
// that have no commits ahead of the feature branch and no corresponding
// WORKING agent in the database, and removes them.
func (o *Orchestrator) reconcileOrphanWorktrees() (int, error) {
	// Collect all WORKING agent branches.
	var workingAgents []model.Agent
	if err := o.db.Where("project_id = ? AND status = ?", o.projectID, model.AgentWorking).
		Find(&workingAgents).Error; err != nil {
		return 0, err
	}
	activeBranches := make(map[string]bool, len(workingAgents))
	for _, ag := range workingAgents {
		activeBranches[ag.WorktreeBranch] = true
	}

	// Find all feature parents to scan their worktree directories.
	var parents []model.Task
	if err := o.db.Where(
		"project_id = ? AND parent_task_id IS NULL AND worktree_branch != ''",
		o.projectID,
	).Find(&parents).Error; err != nil {
		return 0, err
	}

	cleaned := 0
	for _, parent := range parents {
		fn := strings.TrimPrefix(parent.WorktreeBranch, "feature/")
		featureDir := o.worktree.FeatureWorktreePath(fn)

		agentWorktrees, err := o.worktree.ListAgentWorktrees(fn)
		if err != nil {
			continue
		}

		for _, awt := range agentWorktrees {
			if !strings.HasPrefix(awt.Branch, "worktree-agent-") {
				o.logger.Warn("reconcile: skipping non-agent branch in orphan cleanup",
					"branch", awt.Branch, "feature", parent.WorktreeBranch)
				continue
			}

			if activeBranches[awt.Branch] {
				continue // agent is actively working
			}

			// Check if the worktree has commits.
			hasCommits, err := gitexec.BranchHasNewCommits(context.Background(), featureDir, awt.Branch)
			if err != nil {
				continue
			}
			if hasCommits {
				continue // has real work, leave it
			}

			o.logger.Info("reconcile: removing orphan empty worktree",
				"branch", awt.Branch, "feature", parent.WorktreeBranch)
			if err := o.worktree.RemoveAgentWorktree(awt.Branch); err != nil {
				o.logger.Warn("reconcile: remove orphan worktree", "branch", awt.Branch, "error", err)
				continue
			}
			cleaned++
		}
	}
	return cleaned, nil
}

// reconcileOrphanedTaskAssignments finds tasks in actionable statuses
// (classifying, planning, test_writing, in_progress) that have an
// assigned_agent_id pointing to an agent that is no longer actively working
// (idle or dead). These orphaned assignments prevent the task from being
// re-dispatched. This covers the case where an agent completed or died but the
// task's assigned_agent_id was not properly cleared — e.g. due to the stale
// idle signal race condition fixed in d5407be.
func (o *Orchestrator) reconcileOrphanedTaskAssignments() (int, error) {
	var tasks []model.Task
	if err := o.db.Where(
		"project_id = ? AND status IN ? AND assigned_agent_id IS NOT NULL",
		o.projectID, []model.TaskStatus{
			model.StatusClassifying, model.StatusPlanning,
			model.StatusTestWriting, model.StatusInProgress,
		},
	).Find(&tasks).Error; err != nil {
		return 0, err
	}

	// Build set of agent IDs the runner considers active.
	runningSet := make(map[uuid.UUID]bool)
	if o.runner != nil {
		for _, ra := range o.runner.GetRunningAgents() {
			runningSet[ra.AgentID] = true
		}
	}

	cleared := 0
	for i := range tasks {
		task := &tasks[i]
		if task.AssignedAgentID == nil {
			continue
		}

		// Skip agents the runner still considers active.
		if runningSet[*task.AssignedAgentID] {
			continue
		}

		var ag model.Agent
		if err := o.db.First(&ag, "id = ?", task.AssignedAgentID).Error; err != nil {
			if o.hasActiveWorkerAttemptForAgent(task.ID, *task.AssignedAgentID) {
				continue
			}
			// Agent record missing entirely — clear the assignment.
			o.logger.Warn("reconcile: assigned agent not found, clearing task assignment",
				"task_id", task.ID, "agent_id", task.AssignedAgentID)
			task.AssignedAgentID = nil
			task.UpdatedAt = time.Now()
			if err := o.db.Save(task).Error; err != nil {
				o.logger.Error("reconcile: save task after clearing missing agent",
					"task_id", task.ID, "error", err)
			}
			cleared++
			continue
		}

		// Only clear assignments for agents that are NOT actively working.
		// Working agents are handled by reconcileStuckAgents.
		// Blocked agents are legitimately waiting — leave them alone.
		if ag.Status == model.AgentWorking || ag.Status == model.AgentBlocked {
			continue
		}
		if o.hasActiveWorkerAttemptForAgent(task.ID, ag.ID) {
			continue
		}

		// Agent is idle or dead but still assigned to this task — orphaned.
		o.logger.Info("reconcile: clearing orphaned task assignment (agent not working)",
			"task_id", task.ID, "agent_id", ag.ID, "agent_status", ag.Status, "task_status", task.Status)

		task.AssignedAgentID = nil
		task.UpdatedAt = time.Now()

		// Increment retry_count for PLANNING tasks so this counts toward
		// the retry budget. Without this, orphaned planner assignments get
		// cleared silently, bypassing all retry limits.
		if task.Status == model.StatusPlanning {
			o.incrementRetryCount(task)
		}

		if err := o.db.Save(task).Error; err != nil {
			o.logger.Error("reconcile: save task after clearing orphaned assignment",
				"task_id", task.ID, "error", err)
			continue
		}
		cleared++
	}
	return cleared, nil
}

// cleanupOrphanedAssignments is a startup-only sweep that clears
// assigned_agent_id for tasks whose referenced agent is not actively running.
// This ensures any stale assignments left over from a previous orchestrator
// run (e.g. unclean shutdown, race conditions) are cleaned up before the
// first tick.
func (o *Orchestrator) cleanupOrphanedAssignments() {
	var tasks []model.Task
	if err := o.db.Where(
		"project_id = ? AND status IN ? AND assigned_agent_id IS NOT NULL",
		o.projectID, []model.TaskStatus{
			model.StatusClassifying, model.StatusPlanning,
			model.StatusTestWriting, model.StatusInProgress,
		},
	).Find(&tasks).Error; err != nil {
		o.logger.Error("startup cleanup: query assigned tasks", "error", err)
		return
	}

	// Build set of running agent IDs from the runner.
	runningSet := make(map[uuid.UUID]bool)
	if o.runner != nil {
		for _, ra := range o.runner.GetRunningAgents() {
			runningSet[ra.AgentID] = true
		}
	}

	cleared := 0
	for i := range tasks {
		task := &tasks[i]
		if task.AssignedAgentID == nil {
			continue
		}

		// Skip if agent is still running in the process manager.
		if runningSet[*task.AssignedAgentID] {
			continue
		}

		// Check agent status in DB.
		var ag model.Agent
		if err := o.db.First(&ag, "id = ?", task.AssignedAgentID).Error; err != nil {
			if o.hasActiveWorkerAttemptForAgent(task.ID, *task.AssignedAgentID) {
				continue
			}
			// Agent record missing — clear assignment.
			o.logger.Warn("startup cleanup: agent not found, clearing assignment",
				"task_id", task.ID, "agent_id", task.AssignedAgentID)
			task.AssignedAgentID = nil
			task.UpdatedAt = time.Now()
			// Increment retry_count for PLANNING tasks so this startup
			// cleanup counts toward the retry budget.
			if task.Status == model.StatusPlanning {
				o.incrementRetryCount(task)
			}
			if err := o.db.Save(task).Error; err != nil {
				o.logger.Error("startup cleanup: save task", "task_id", task.ID, "error", err)
			}
			cleared++
			continue
		}

		// If the agent is not actively working, clear the assignment.
		// On startup, no agents should be working unless they survived
		// the restart, which the runner's running map would reflect.
		if ag.Status != model.AgentWorking && ag.Status != model.AgentBlocked {
			if o.hasActiveWorkerAttemptForAgent(task.ID, ag.ID) {
				continue
			}
			o.logger.Info("startup cleanup: clearing stale agent assignment",
				"task_id", task.ID, "agent_id", ag.ID, "agent_status", ag.Status)
			task.AssignedAgentID = nil
			task.UpdatedAt = time.Now()
			// Increment retry_count for PLANNING tasks so this startup
			// cleanup counts toward the retry budget.
			if task.Status == model.StatusPlanning {
				o.incrementRetryCount(task)
			}
			if err := o.db.Save(task).Error; err != nil {
				o.logger.Error("startup cleanup: save task", "task_id", task.ID, "error", err)
			}
			cleared++
		}
	}

	if cleared > 0 {
		o.logger.Info("startup cleanup: cleared orphaned task assignments", "count", cleared)
	}
}

func (o *Orchestrator) hasActiveWorkerAttemptForAgent(taskID, agentID uuid.UUID) bool {
	var count int64
	if err := o.db.Model(&model.WorkerAttempt{}).
		Where("task_id = ? AND agent_id = ? AND completed_at IS NULL AND state IN ?",
			taskID, agentID, []string{model.WorkerAttemptReserved, model.WorkerAttemptRunning}).
		Count(&count).Error; err != nil {
		o.logger.Warn("reconcile: active attempt lookup failed",
			"task_id", taskID, "agent_id", agentID, "error", err)
		return true
	}
	return count > 0
}
