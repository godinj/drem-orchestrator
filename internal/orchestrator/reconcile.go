package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/state"
	"github.com/godinj/drem-orchestrator/internal/supervisor"
	"github.com/godinj/drem-orchestrator/internal/worktree"
)

// ReconcileResult describes the fixes applied by a single Reconcile run.
type ReconcileResult struct {
	StaleSubtasksReset         int
	OrphanedSubtasksFixed      int
	EmptyFeaturesFailed        int
	OrphanWorktreesCleaned     int
	StuckAgentsRecovered       int
	AlreadyMergedFeaturesFixed int
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

	if n, err := o.reconcileAlreadyMergedFeatures(); err != nil {
		return 0, fmt.Errorf("reconcile already-merged features: %w", err)
	} else {
		r.AlreadyMergedFeaturesFixed = n
	}

	total := r.StaleSubtasksReset + r.OrphanedSubtasksFixed + r.EmptyFeaturesFailed + r.OrphanWorktreesCleaned + r.StuckAgentsRecovered + r.AlreadyMergedFeaturesFixed
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
		changedFiles, err := worktree.GetChangedFiles(featureDir, o.worktree.DefaultBranch)
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
			o.logger.Warn("reconcile: resetting done subtask with no feature changes",
				"subtask_id", sub.ID, "parent_id", parent.ID)

			// Force status back to backlog (bypasses state machine since
			// DONE is terminal and has no valid outbound transitions).
			sub.Status = model.StatusBacklog
			sub.AssignedAgentID = nil
			sub.UpdatedAt = time.Now()
			if sub.Context == nil {
				sub.Context = make(model.JSONField)
			}
			sub.Context["reconciled"] = true
			sub.Context["reconcile_reason"] = "subtask was done but feature branch has no changes"
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
				// Transition to testing_ready so the quality gate verifies build/tests.
				transitions := []model.TaskStatus{
					model.StatusTestingReady,
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
			if committed, cErr := worktree.CommitUnstagedChanges(
				featureDir, "Auto-commit uncommitted feature worktree changes (reconcile)",
			); cErr != nil {
				o.logger.Warn("reconcile: failed to clean feature worktree", "feature", featureBranch, "error", cErr)
			} else if committed {
				o.logger.Info("reconcile: committed leftover changes in feature worktree", "feature", featureBranch)
			}

			hasCommits, err := worktree.BranchHasNewCommits(featureDir, ag.WorktreeBranch)
			if err != nil {
				// Branch likely already cleaned up — assume merge happened.
				merged = true
			} else if hasCommits {
				result, mergeErr := o.merger.MergeAgentIntoFeature(ag.WorktreeBranch, featureDir)
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

		// Transition subtask to testing_ready so the quality gate verifies build/tests.
		transitions := []model.TaskStatus{
			model.StatusTestingReady,
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

		changed, err := worktree.GetChangedFiles(featureDir, o.worktree.DefaultBranch)
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
			hasCommits, err := worktree.BranchHasNewCommits(featureDir, awt.Branch)
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
	runningAgents := o.runner.GetRunningAgents()
	runningSet := make(map[uuid.UUID]bool, len(runningAgents))
	for _, ra := range runningAgents {
		runningSet[ra.AgentID] = true
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

		// Agent is NOT in the runner's running map AND DB status is working.
		o.logger.Warn("detected dead agent session without completion",
			"agent_id", ag.ID, "task", task.Title, "session", ag.TmuxSession)

		// Check if the agent branch has commits.
		featureDir := o.resolveFeatureWorktree(task)
		hasCommits := false
		if featureDir != "" && ag.WorktreeBranch != "" {
			var err error
			hasCommits, err = worktree.BranchHasNewCommits(featureDir, ag.WorktreeBranch)
			if err != nil {
				o.logger.Warn("reconcile stuck: failed to check commits",
					"agent_id", ag.ID, "error", err)
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

			// Auto-retry if under the limit.
			retryCount := 0
			if task.Context != nil {
				if v, ok := task.Context["retry_count"].(float64); ok {
					retryCount = int(v)
				}
			}
			if retryCount < MaxEmptyWorkRetries {
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

// reconcileAlreadyMergedFeatures finds FAILED parent tasks whose feature
// branch is already an ancestor of the default branch (i.e. was merged
// manually or by a supervisor). These tasks are transitioned directly to DONE
// since their work is already on the default branch.
func (o *Orchestrator) reconcileAlreadyMergedFeatures() (int, error) {
	var tasks []model.Task
	if err := o.db.Where(
		"project_id = ? AND status = ? AND parent_task_id IS NULL AND worktree_branch != ''",
		o.projectID, model.StatusFailed,
	).Find(&tasks).Error; err != nil {
		return 0, err
	}

	mainWorktree, err := o.worktree.MainWorktreePath()
	if err != nil {
		// No main worktree available (e.g. in tests) — skip this check.
		return 0, nil
	}

	fixed := 0
	for i := range tasks {
		task := &tasks[i]

		// Check if the feature branch tip is an ancestor of the default branch HEAD.
		_, err := worktree.RunGit(
			[]string{"merge-base", "--is-ancestor", task.WorktreeBranch, "HEAD"},
			mainWorktree,
		)
		if err != nil {
			continue // not merged yet
		}

		// Guard: if the task has subtasks but none completed, the feature
		// branch was never successfully worked on. A branch created from
		// HEAD with zero commits is trivially an ancestor — don't treat
		// it as "already merged".
		var totalSubs, doneSubs int64
		o.db.Model(&model.Task{}).Where("parent_task_id = ?", task.ID).Count(&totalSubs)
		if totalSubs > 0 {
			o.db.Model(&model.Task{}).Where(
				"parent_task_id = ? AND status = ?", task.ID, model.StatusDone,
			).Count(&doneSubs)
			if doneSubs == 0 {
				continue // has subtasks but none completed — not actually merged
			}
		}

		o.logger.Info("reconcile: failed task's feature branch already merged to default, transitioning to done",
			"task_id", task.ID, "branch", task.WorktreeBranch)

		// Bypass the state machine (failed -> done is not a valid transition)
		// since the work is provably on the default branch.
		now := time.Now()
		event := &model.TaskEvent{
			ID:        uuid.New(),
			TaskID:    task.ID,
			EventType: "status_change",
			OldValue:  string(task.Status),
			NewValue:  string(model.StatusDone),
			Details:   model.JSONField{"reason": "reconcile-already-merged-to-default"},
			Actor:     "orchestrator",
			CreatedAt: now,
		}
		task.Status = model.StatusDone
		task.UpdatedAt = now

		if err := o.db.Save(task).Error; err != nil {
			o.logger.Error("reconcile: save already-merged task", "task_id", task.ID, "error", err)
			continue
		}
		if err := o.db.Create(event).Error; err != nil {
			o.logger.Error("reconcile: save event for already-merged task", "task_id", task.ID, "error", err)
			continue
		}

		// Clean up the feature worktree since the branch is merged.
		featureName := strings.TrimPrefix(task.WorktreeBranch, "feature/")
		if featureName != "" {
			if err := o.worktree.RemoveFeature(featureName); err != nil {
				o.logger.Warn("reconcile: cleanup merged feature worktree", "task_id", task.ID, "error", err)
			}
		}

		o.emit("task_updated", task)
		fixed++
	}
	return fixed, nil
}

// handleAgentMergeFailure handles the case where merging an agent's work into
// the feature branch fails. When a supervisor is available, it diagnoses the
// conflict and may spawn a fixer agent. Without a supervisor, it falls back to
// the current behavior: fail the task and preserve the agent branch.
func (o *Orchestrator) handleAgentMergeFailure(ag *model.Agent, task *model.Task, result *worktree.MergeResult, featureDir string) error {
	// Supervisor-powered merge conflict diagnosis.
	if o.supervisor != nil && result != nil && len(result.Conflicts) > 0 {
		var analysis supervisor.MergeConflictAnalysis
		diffOutput, _ := worktree.RunGit([]string{
			"diff", result.TargetBranch + "..." + ag.WorktreeBranch,
		}, featureDir)

		mcPrompt := supervisor.MergeConflictPrompt(
			ag.WorktreeBranch, result.TargetBranch,
			result.Conflicts, diffOutput,
		)
		if mcErr := o.supervisor.EvaluateJSON(context.Background(), mcPrompt, &analysis); mcErr != nil {
			o.logger.Warn("supervisor agent merge conflict analysis failed", "task_id", task.ID, "error", mcErr)
		} else {
			if task.Context == nil {
				task.Context = make(model.JSONField)
			}
			task.Context["merge_conflict_severity"] = analysis.Severity
			task.Context["merge_conflict_strategy"] = analysis.ResolutionStrategy
			task.Context["merge_conflict_hints"] = analysis.ResolutionHints

			if analysis.ResolutionStrategy == "spawn_agent" {
				task.Context["merge_conflict_files"] = result.Conflicts
				task.Context["merge_resolution_hints"] = analysis.ResolutionHints

				// Set agent to idle and fail the task.
				ag.Status = model.AgentIdle
				ag.CurrentTaskID = nil
				if err := o.db.Save(ag).Error; err != nil {
					return fmt.Errorf("handle agent merge failure: save agent: %w", err)
				}
				if err := o.failTask(task, "merge conflicts — spawning resolver agent"); err != nil {
					return err
				}
				if _, fixerErr := o.SpawnFixerSession(task.ID); fixerErr != nil {
					o.logger.Warn("failed to spawn fixer for agent merge conflict",
						"task_id", task.ID, "error", fixerErr)
				}

				return nil
			}
		}
	}

	// Default fallback: set agent to idle, fail the task, preserve agent branch.
	ag.Status = model.AgentIdle
	ag.CurrentTaskID = nil
	if err := o.db.Save(ag).Error; err != nil {
		return fmt.Errorf("on agent completed: save agent: %w", err)
	}
	evt, err := state.TransitionTask(task, model.StatusFailed, "orchestrator",
		map[string]any{"reason": "merge into feature branch failed, agent branch preserved"})
	if err != nil {
		o.logger.Warn("failed to transition task to failed after merge failure", "task_id", task.ID, "error", err)
	} else {
		if err := o.db.Save(task).Error; err != nil {
			return fmt.Errorf("on agent completed: save task after merge failure: %w", err)
		}
		if err := o.db.Create(evt).Error; err != nil {
			return fmt.Errorf("on agent completed: save merge-failure event: %w", err)
		}
	}
	return nil
}

// getChangedFilesDiff returns the diff of changed files between the worktree
// HEAD and the given base branch. Returns empty string on error.
func getChangedFilesDiff(worktreeDir, baseBranch string) (string, error) {
	output, err := worktree.RunGit([]string{"diff", baseBranch + "...HEAD"}, worktreeDir)
	if err != nil {
		return "", fmt.Errorf("get changed files diff: %w", err)
	}
	// Truncate to avoid overly large diffs in prompts.
	if len(output) > maxGitDiffLen {
		output = output[:maxGitDiffLen] + "\n... (truncated)"
	}
	return output, nil
}
