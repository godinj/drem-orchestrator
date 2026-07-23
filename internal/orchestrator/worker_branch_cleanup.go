package orchestrator

import (
	"context"
	"fmt"
	"strings"

	"github.com/godinj/drem-orchestrator/internal/gitexec"
	"github.com/godinj/drem-orchestrator/internal/gitref"
	"github.com/godinj/drem-orchestrator/internal/model"
)

// cleanupTaskWorkerBranch removes either a legacy host-agent worktree or a
// container worker's ref. Container workers do not create host worktrees, so
// routing their feature/<child> refs through RemoveAgentWorktree leaked every
// completed child branch and logged a misleading prefix rejection.
func (o *Orchestrator) cleanupTaskWorkerBranch(ctx context.Context, task *model.Task, branch string) error {
	branch = strings.TrimSpace(branch)
	if branch == "" || task == nil || o.worktree == nil {
		return nil
	}
	if strings.HasPrefix(branch, "worktree-agent-") {
		return o.worktree.RemoveAgentWorktree(branch)
	}
	// Top-level feature refs are the deliverable and must survive through
	// host verification/integration. Only child-task refs are ephemeral.
	if task.ParentTaskID == nil {
		return nil
	}
	if !strings.HasPrefix(branch, "feature/") {
		return fmt.Errorf("cleanup worker branch: refusing unexpected branch %q", branch)
	}

	var parent model.Task
	if err := o.db.Select("worktree_branch").First(&parent, "id = ?", task.ParentTaskID).Error; err != nil {
		return fmt.Errorf("cleanup worker branch: load parent: %w", err)
	}
	if branch == parent.WorktreeBranch {
		return fmt.Errorf("cleanup worker branch: refusing parent integration branch %q", branch)
	}
	if o.GitrefRegistry == nil {
		return fmt.Errorf("cleanup worker branch: no ownership registry for %q", branch)
	}

	bare := o.worktree.BareRepo()
	ref, err := o.GitrefRegistry.FindByBranch(ctx, bare, branch)
	if err != nil {
		return fmt.Errorf("cleanup worker branch: resolve ownership for %q: %w", branch, err)
	}
	if ref.TaskID != task.ID.String() {
		return fmt.Errorf("cleanup worker branch: %q belongs to task %s, not %s", branch, ref.TaskID, task.ID)
	}
	if ref.Status == gitref.StatusDeleted {
		return nil
	}

	porcelain, err := gitexec.RunGit(ctx, bare, "worktree", "list", "--porcelain")
	if err != nil {
		return fmt.Errorf("cleanup worker branch: list worktrees: %w", err)
	}
	if worktreeListContainsBranch(porcelain, branch) {
		return fmt.Errorf("cleanup worker branch: refusing checked-out branch %q", branch)
	}
	if _, err := gitexec.RunGit(ctx, bare, "update-ref", "-d", "refs/heads/"+branch); err != nil {
		return fmt.Errorf("cleanup worker branch: delete %q: %w", branch, err)
	}
	if err := o.GitrefRegistry.MarkDeleted(ctx, ref.ID); err != nil {
		return fmt.Errorf("cleanup worker branch: mark %q deleted: %w", branch, err)
	}
	return nil
}

func worktreeListContainsBranch(porcelain, branch string) bool {
	want := "branch refs/heads/" + branch
	for _, line := range strings.Split(porcelain, "\n") {
		if strings.TrimSpace(line) == want {
			return true
		}
	}
	return false
}
