package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/gitexec"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/state"
)

// ReconcileResult describes the fixes applied by a single Reconcile run.
type ReconcileResult struct {
	StaleSubtasksReset         int
	OrphanedSubtasksFixed      int
	EmptyFeaturesFailed        int
	OrphanWorktreesCleaned     int
	StuckAgentsRecovered       int
	OrphanedAssignmentsCleared int
	AlreadyMergedFeaturesFixed int
	CompletedParentsAdvanced   int
	FailedParentsRecovered     int
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

	if n, err := o.reconcileOrphanedSubtasks(); err != nil {
		return 0, fmt.Errorf("reconcile orphaned subtasks: %w", err)
	} else {
		r.OrphanedSubtasksFixed = n
	}

	if n, err := o.reconcileEmptyFeatures(); err != nil {
		return 0, fmt.Errorf("reconcile empty features: %w", err)
	} else {
		r.EmptyFeaturesFailed = n
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

	if n, err := o.reconcileAlreadyMergedFeatures(); err != nil {
		return 0, fmt.Errorf("reconcile already-merged features: %w", err)
	} else {
		r.AlreadyMergedFeaturesFixed = n
	}

	if n, err := o.reconcileCompletedParents(); err != nil {
		return 0, fmt.Errorf("reconcile completed parents: %w", err)
	} else {
		r.CompletedParentsAdvanced = n
	}

	if n, err := o.reconcileFailedParents(); err != nil {
		return 0, fmt.Errorf("reconcile failed parents: %w", err)
	} else {
		r.FailedParentsRecovered = n
	}

	total := r.StaleSubtasksReset + r.OrphanedSubtasksFixed + r.EmptyFeaturesFailed + r.OrphanWorktreesCleaned + r.StuckAgentsRecovered + r.OrphanedAssignmentsCleared + r.AlreadyMergedFeaturesFixed + r.CompletedParentsAdvanced + r.FailedParentsRecovered
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
		if len(changedFiles) > 0 {
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

// reconcileOrphanedSubtasks finds IN_PROGRESS subtasks whose assigned agent
// is idle or dead — meaning the agent finished but the completion signal was
// lost before the subtask could be transitioned. For each orphaned subtask,
// it attempts to merge any remaining agent work into the feature branch and
// transitions the subtask to TESTING_READY so the normal quality gate can
// verify build and test correctness. If the agent branch has no mergeable
// work and the feature branch is empty, the subtask is reset to BACKLOG.
func (o *Orchestrator) reconcileOrphanedSubtasks() (int, error) {
	var subtasks []model.Task
	if err := o.db.Where(
		"project_id = ? AND status = ? AND parent_task_id IS NOT NULL AND assigned_agent_id IS NOT NULL",
		o.projectID, model.StatusInProgress,
	).Find(&subtasks).Error; err != nil {
		return 0, err
	}

	fixed := 0
	for i := range subtasks {
		sub := &subtasks[i]

		var ag model.Agent
		if err := o.db.First(&ag, "id = ?", sub.AssignedAgentID).Error; err != nil {
			// Agent record missing — reset subtask for rescheduling.
			o.logger.Warn("reconcile: assigned agent not found, resetting subtask",
				"subtask_id", sub.ID, "agent_id", sub.AssignedAgentID)
			sub.Status = model.StatusBacklog
			sub.AssignedAgentID = nil
			sub.UpdatedAt = time.Now()
			if err := o.db.Save(sub).Error; err != nil {
				o.logger.Error("reconcile: save subtask", "subtask_id", sub.ID, "error", err)
			}
			fixed++
			continue
		}

		// Only act if the agent is no longer actively working.
		if ag.Status == model.AgentWorking || ag.Status == model.AgentBlocked {
			continue
		}

		o.logger.Info("reconcile: processing orphaned in_progress subtask",
			"subtask_id", sub.ID, "agent_id", ag.ID, "agent_status", ag.Status)

		// Resolve the feature branch from the parent task.
		featureBranch := ""
		if sub.ParentTaskID != nil {
			var parent model.Task
			if err := o.db.Select("worktree_branch").First(&parent, "id = ?", sub.ParentTaskID).Error; err == nil {
				featureBranch = parent.WorktreeBranch
			}
		}

		// Before resetting, check if work is already merged.
		if featureBranch != "" {
			fn := strings.TrimPrefix(featureBranch, "feature/")
			featureDir := o.worktree.FeatureWorktreePath(fn)
			if o.isWorkAlreadyMerged(sub, featureDir) {
				o.logger.Info("reconcile: work already merged, transitioning to quality gate",
					"subtask_id", sub.ID, "agent_id", ag.ID)
				// Fast-track subtask to done (matches onAgentCompleted / scheduleSubtasks).
				transitions := []model.TaskStatus{
					model.StatusTestingReady,
					model.StatusMerging,
					model.StatusDone,
				}
				for _, target := range transitions {
					if sub.Status == target {
						continue
					}
					evt, tErr := state.TransitionTask(sub, target, "orchestrator",
						map[string]any{"reason": "reconcile-already-merged"})
					if tErr != nil {
						continue
					}
					if err := o.db.Create(evt).Error; err != nil {
						o.logger.Error("reconcile: save event", "subtask_id", sub.ID, "error", err)
						break
					}
				}
				if err := o.db.Save(sub).Error; err != nil {
					o.logger.Error("reconcile: save subtask", "subtask_id", sub.ID, "error", err)
				}
				// Clean up agent worktree since work is merged.
				if ag.WorktreeBranch != "" {
					if err := o.worktree.RemoveAgentWorktree(ag.WorktreeBranch); err != nil {
						o.logger.Warn("reconcile: cleanup agent worktree", "agent_id", ag.ID, "error", err)
					}
				}
				fixed++
				continue
			}
		}

		// Attempt to merge agent work if the branch still exists.
		merged := false
		if ag.WorktreeBranch != "" && featureBranch != "" {
			fn := strings.TrimPrefix(featureBranch, "feature/")
			featureDir := o.worktree.FeatureWorktreePath(fn)

			// Ensure the feature worktree is clean before merge attempts.
			// Leftover changes (e.g. plan.json) block MergeAgentIntoFeature.
			if committed, cErr := gitexec.CommitUnstagedChanges(
				context.Background(), featureDir, "Auto-commit uncommitted feature worktree changes (reconcile)",
			); cErr != nil {
				o.logger.Warn("reconcile: failed to clean feature worktree", "feature", featureBranch, "error", cErr)
			} else if committed {
				o.logger.Info("reconcile: committed leftover changes in feature worktree", "feature", featureBranch)
			}

			hasCommits, err := gitexec.BranchHasNewCommits(context.Background(), featureDir, ag.WorktreeBranch)
			if err != nil {
				// Branch likely already cleaned up — assume merge happened.
				merged = true
			} else if hasCommits {
				result, mergeErr := o.mergeAgentBranchIntoFeature(context.Background(), ag.WorktreeBranch, featureDir)
				if mergeErr != nil {
					o.logger.Error("reconcile: merge agent into feature failed",
						"subtask_id", sub.ID, "agent_id", ag.ID, "error", mergeErr)
				} else if result.Success {
					merged = true
					if err := o.worktree.RemoveAgentWorktree(ag.WorktreeBranch); err != nil {
						o.logger.Warn("reconcile: cleanup agent worktree", "agent_id", ag.ID, "error", err)
					}
				} else {
					o.logger.Error("reconcile: merge had conflicts",
						"subtask_id", sub.ID, "conflicts", result.Conflicts)
				}
			} else {
				// No commits on agent branch — already merged or empty work.
				merged = true
			}
		} else {
			merged = true
		}

		if !merged {
			// Merge failed — keep the agent worktree/branch intact so work
			// is not lost (consistent with onAgentCompleted behavior).
			if err := o.failTask(sub, "reconcile: agent work could not be merged into feature branch (agent branch preserved)"); err != nil {
				o.logger.Error("reconcile: fail subtask", "subtask_id", sub.ID, "error", err)
			}
			// Leave agent worktree intact for manual resolution or retry.
			fixed++
			continue
		}

		// Clean up the agent record if it still references this subtask.
		if ag.CurrentTaskID != nil && *ag.CurrentTaskID == sub.ID {
			ag.CurrentTaskID = nil
			if ag.Status == model.AgentDead {
				ag.Status = model.AgentIdle
			}
			if err := o.db.Save(&ag).Error; err != nil {
				o.logger.Error("reconcile: save agent", "agent_id", ag.ID, "error", err)
			}
		}

		// Fast-track subtask to done (matches onAgentCompleted / scheduleSubtasks).
		transitions := []model.TaskStatus{
			model.StatusTestingReady,
			model.StatusMerging,
			model.StatusDone,
		}
		for _, target := range transitions {
			if sub.Status == target {
				continue
			}
			evt, err := state.TransitionTask(sub, target, "orchestrator",
				map[string]any{"reason": "reconcile-merged"})
			if err != nil {
				o.logger.Debug("reconcile transition skip",
					"subtask_id", sub.ID, "from", sub.Status, "to", target, "error", err)
				continue
			}
			if err := o.db.Create(evt).Error; err != nil {
				o.logger.Error("reconcile: save event", "subtask_id", sub.ID, "error", err)
				break
			}
		}

		if err := o.db.Save(sub).Error; err != nil {
			o.logger.Error("reconcile: save subtask", "subtask_id", sub.ID, "error", err)
			continue
		}
		fixed++
	}
	return fixed, nil
}

// reconcileEmptyFeatures finds parent tasks in TESTING_READY whose feature
// branch has no file changes relative to the default branch and fails them.
func (o *Orchestrator) reconcileEmptyFeatures() (int, error) {
	var tasks []model.Task
	if err := o.db.Where(
		"project_id = ? AND status = ? AND parent_task_id IS NULL",
		o.projectID, model.StatusTestingReady,
	).Find(&tasks).Error; err != nil {
		return 0, err
	}

	fixed := 0
	for i := range tasks {
		task := &tasks[i]
		if task.WorktreeBranch == "" {
			continue
		}

		fn := strings.TrimPrefix(task.WorktreeBranch, "feature/")
		featureDir := o.worktree.FeatureWorktreePath(fn)

		changed, err := gitexec.GetChangedFiles(context.Background(), featureDir, o.worktree.DefaultBranchName())
		if err != nil {
			continue
		}
		if len(changed) > 0 {
			continue
		}

		o.logger.Warn("reconcile: failing testing_ready task with empty feature branch",
			"task_id", task.ID)
		if task.Context == nil {
			task.Context = make(model.JSONField)
		}
		task.Context["empty_feature"] = true
		task.Context["reconciled"] = true
		if err := o.failTask(task, "feature branch has no changes (detected by reconcile)"); err != nil {
			o.logger.Error("reconcile: fail empty feature task", "task_id", task.ID, "error", err)
			continue
		}
		fixed++
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
