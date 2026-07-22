package orchestrator

import (
	"context"
	"fmt"
	"strings"

	"github.com/godinj/drem-orchestrator/internal/gitexec"
	"github.com/godinj/drem-orchestrator/internal/model"
)

// reconcileAlreadyMergedFeatures finds FAILED parent tasks whose feature
// branch provably advanced from its recorded creation base and is already an
// ancestor of the default branch. It routes those tasks back through delivery
// verification; integration inferred from branch topology is not completion.
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
		_, err := gitexec.RunGit(
			context.Background(), mainWorktree,
			"merge-base", "--is-ancestor", task.WorktreeBranch, "HEAD",
		)
		if err != nil {
			continue // not merged yet
		}

		// An ancestor relation alone is insufficient: an empty branch created
		// from the default branch is also an ancestor. Require the typed creation
		// base and prove that this feature ref advanced beyond it.
		branchHead, branchErr := gitexec.RunGit(context.Background(), mainWorktree, "rev-parse", task.WorktreeBranch)
		if branchErr != nil || task.WorktreeBaseSHA == "" ||
			strings.TrimSpace(branchHead) == strings.TrimSpace(task.WorktreeBaseSHA) {
			continue
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

		o.logger.Info("reconcile: failed task's feature branch already merged; routing through delivery verification",
			"task_id", task.ID, "branch", task.WorktreeBranch)
		if err := o.transitionTaskAtomic(task, model.StatusInProgress, "orchestrator", "out_of_band_merge_recovery",
			"feature branch is already present on default; verification still required", nil); err != nil {
			o.logger.Error("reconcile: recover already-merged task", "task_id", task.ID, "error", err)
			continue
		}
		if totalSubs == 0 {
			if err := o.transitionTaskAtomic(task, model.StatusTestingReady, "orchestrator", "out_of_band_merge_recovery",
				"already-integrated implementation requires evidence freeze", nil); err != nil {
				o.logger.Error("reconcile: prepare already-merged task delivery", "task_id", task.ID, "error", err)
				continue
			}
		} else if err := o.checkFeatureCompletion(task); err != nil {
			o.logger.Error("reconcile: evaluate already-merged parent", "task_id", task.ID, "error", err)
			continue
		}

		o.emit("task_updated", task)
		o.publishTaskTransition(task.ID.String(), string(model.StatusFailed), string(task.Status), "reconcile: feature already merged; verification required")
		fixed++
	}
	return fixed, nil
}

func (o *Orchestrator) featureBranchAlreadyMergedToDefault(task *model.Task) bool {
	if task == nil || task.WorktreeBranch == "" || task.WorktreeBaseSHA == "" {
		return false
	}
	mainWorktree, err := o.worktree.MainWorktreePath()
	if err != nil {
		return false
	}
	branchHead, err := gitexec.RunGit(context.Background(), mainWorktree, "rev-parse", task.WorktreeBranch)
	if err != nil {
		return false
	}
	if strings.TrimSpace(branchHead) == strings.TrimSpace(task.WorktreeBaseSHA) {
		return false
	}
	_, err = gitexec.RunGit(
		context.Background(), mainWorktree,
		"merge-base", "--is-ancestor", task.WorktreeBranch, "HEAD",
	)
	return err == nil
}

// parentReconcilePolicy defines how to reconcile parent tasks in a specific
// status whose subtasks are all done. This avoids duplicating the
// query-filter-advance loop across reconcileCompletedParents and
// reconcileFailedParents.
type parentReconcilePolicy struct {
	status  model.TaskStatus
	logVerb string
	// recover transitions the parent into a state where checkFeatureCompletion
	// can act on it. Nil means the parent is already in the right state.
	recover func(o *Orchestrator, parent *model.Task) error
}

func (o *Orchestrator) reconcileParentsByPolicy(p parentReconcilePolicy) (int, error) {
	var parents []model.Task
	if err := o.db.Where(
		"project_id = ? AND status = ? AND parent_task_id IS NULL",
		o.projectID, p.status,
	).Find(&parents).Error; err != nil {
		return 0, err
	}

	advanced := 0
	for i := range parents {
		parent := &parents[i]

		var subtasks []model.Task
		if err := o.db.Where("parent_task_id = ?", parent.ID).Find(&subtasks).Error; err != nil {
			continue
		}
		if len(subtasks) == 0 {
			continue
		}

		allDone := true
		for _, sub := range subtasks {
			if sub.Status != model.StatusDone {
				allDone = false
				break
			}
		}
		if !allDone {
			continue
		}

		if p.recover != nil && hasTerminalMergerFailure(parent) {
			o.logger.Info("reconcile: skipping terminal merger-failed parent",
				"task_id", parent.ID)
			continue
		}

		if p.recover != nil {
			if err := p.recover(o, parent); err != nil {
				o.logger.Error("reconcile: recovery transition failed",
					"task_id", parent.ID, "error", err)
				continue
			}
		}

		o.logger.Info("reconcile: all subtasks done, "+p.logVerb+" parent",
			"task_id", parent.ID, "subtask_count", len(subtasks))

		if err := o.checkFeatureCompletion(parent); err != nil {
			o.logger.Error("reconcile: checkFeatureCompletion failed",
				"task_id", parent.ID, "error", err)
			continue
		}
		advanced++
	}
	return advanced, nil
}

// reconcileCompletedParents finds in_progress parent tasks whose subtasks are
// all done and advances them via checkFeatureCompletion.
func (o *Orchestrator) reconcileCompletedParents() (int, error) {
	return o.reconcileParentsByPolicy(parentReconcilePolicy{
		status:  model.StatusInProgress,
		logVerb: "advancing",
	})
}

// recoverFailedParent transitions a failed parent to in_progress via the state
// machine so checkFeatureCompletion can evaluate quality gates.
func recoverFailedParent(o *Orchestrator, parent *model.Task) error {
	oldStatus := parent.Status
	if err := o.transitionTaskAtomic(parent, model.StatusInProgress, "orchestrator", "parent_reconciliation",
		"failed parent has completed subtasks and requires delivery evaluation", nil); err != nil {
		return err
	}
	o.publishTaskTransition(parent.ID.String(), string(oldStatus), string(parent.Status), "reconcile: recovering failed parent")
	return nil
}

// getChangedFilesDiff returns the diff of changed files between the worktree
// HEAD and the given base branch. Returns empty string on error.
func getChangedFilesDiff(worktreeDir, baseBranch string) (string, error) {
	output, err := gitexec.RunGit(context.Background(), worktreeDir, "diff", baseBranch+"...HEAD")
	if err != nil {
		return "", fmt.Errorf("get changed files diff: %w", err)
	}
	// Truncate to avoid overly large diffs in prompts.
	if len(output) > maxGitDiffLen {
		output = output[:maxGitDiffLen] + "\n... (truncated)"
	}
	return output, nil
}

// reconcileFailedParents finds failed parent tasks whose subtasks have all
// completed successfully (status done) and recovers them. The parent is
// transitioned from failed → in_progress via the state machine, then
// checkFeatureCompletion is called to evaluate quality gates and advance
// to the appropriate next status.
//
// This covers the bug where a parent task fails (e.g., due to a subtask merge
// conflict) but all subtasks eventually succeed via retry. Without this check,
// such parents remain stuck in failed status indefinitely.
func (o *Orchestrator) reconcileFailedParents() (int, error) {
	return o.reconcileParentsByPolicy(parentReconcilePolicy{
		status:  model.StatusFailed,
		logVerb: "recovering",
		recover: recoverFailedParent,
	})
}
