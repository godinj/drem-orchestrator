package orchestrator

import (
	"context"
	"fmt"
	"strings"

	"github.com/godinj/drem-orchestrator/internal/gitexec"
	"github.com/godinj/drem-orchestrator/internal/model"
)

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

// reconcileCompletedParents repairs only a missed parent advancement. Child
// completion remains the normal transition source; this audit never revives a
// failed parent or derives success from branch topology.
func (o *Orchestrator) reconcileCompletedParents() (int, error) {
	var parents []model.Task
	if err := o.db.Where(
		"project_id = ? AND status = ? AND parent_task_id IS NULL",
		o.projectID, model.StatusInProgress,
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
		if err := o.checkFeatureCompletion(parent); err != nil {
			o.logger.Error("reconcile: checkFeatureCompletion failed",
				"task_id", parent.ID, "error", err)
			continue
		}
		advanced++
	}
	return advanced, nil
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
