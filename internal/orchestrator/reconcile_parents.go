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
		_, err := gitexec.RunGit(
			context.Background(), mainWorktree,
			"merge-base", "--is-ancestor", task.WorktreeBranch, "HEAD",
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
		o.publishTaskTransition(task.ID.String(), string(model.StatusFailed), string(model.StatusDone), "reconcile: feature already merged to default")
		fixed++
	}
	return fixed, nil
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
	evt, err := state.TransitionTask(parent, model.StatusInProgress, "orchestrator",
		map[string]any{"reason": "reconcile-failed-parent-all-subtasks-done"})
	if err != nil {
		return err
	}
	if err := o.db.Save(parent).Error; err != nil {
		return err
	}
	if err := o.db.Create(evt).Error; err != nil {
		return err
	}
	o.publishTaskTransition(parent.ID.String(), evt.OldValue, evt.NewValue, "reconcile: recovering failed parent")
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
