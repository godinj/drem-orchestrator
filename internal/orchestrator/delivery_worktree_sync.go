package orchestrator

import (
	"context"
	"fmt"
	"strings"

	"github.com/godinj/drem-orchestrator/internal/gitexec"
	"github.com/godinj/drem-orchestrator/internal/model"
)

type deliveryWorktreeSync struct {
	FromSHA string
	ToSHA   string
	Changed bool
}

func hasAcceptedBranchEvidence(task *model.Task) bool {
	if task == nil || task.Context == nil {
		return false
	}
	_, ok := task.Context["branch_acceptance"]
	return ok
}

// synchronizeAcceptedWorktree updates the orchestrator-owned integration
// worktree after a container worker pushes the accepted feature commit into
// the shared bare repository. Git updates the branch ref but intentionally
// does not update another linked worktree's index or files.
//
// The reset is allowed only when the filesystem and index still match the
// worktree's own last checked-out commit and the feature ref still names the
// exact SHA accepted at worker completion. Any user edit or ref drift fails
// closed instead of being overwritten or tested as the wrong artifact.
func (o *Orchestrator) synchronizeAcceptedWorktree(ctx context.Context, task *model.Task, worktreePath string) (deliveryWorktreeSync, error) {
	result := deliveryWorktreeSync{}
	if task == nil || strings.TrimSpace(worktreePath) == "" {
		return result, fmt.Errorf("delivery worktree sync: task and worktree path are required")
	}
	branch := strings.TrimSpace(task.WorktreeBranch)
	if branch == "" {
		return result, fmt.Errorf("delivery worktree sync: task branch is required")
	}
	acceptedSHA, err := acceptedBranchHead(task)
	if err != nil {
		return result, err
	}
	branchSHA, err := gitexec.RunGit(ctx, worktreePath, "rev-parse", branch)
	if err != nil {
		return result, fmt.Errorf("delivery worktree sync: resolve branch %s: %w", branch, err)
	}
	branchSHA = strings.TrimSpace(branchSHA)
	if branchSHA != acceptedSHA {
		return result, fmt.Errorf("delivery worktree sync: accepted ref drift: branch %s is %s, accepted %s", branch, branchSHA, acceptedSHA)
	}

	checkedOutSHA, err := gitexec.RunGit(ctx, worktreePath, "reflog", "-1", "--format=%H", "HEAD")
	if err != nil {
		return result, fmt.Errorf("delivery worktree sync: resolve private worktree HEAD: %w", err)
	}
	checkedOutSHA = strings.TrimSpace(checkedOutSHA)
	if checkedOutSHA == "" {
		return result, fmt.Errorf("delivery worktree sync: private worktree HEAD is empty")
	}
	result.FromSHA = checkedOutSHA
	result.ToSHA = acceptedSHA

	if checkedOutSHA != acceptedSHA {
		for _, check := range [][]string{
			{"diff", "--name-only", checkedOutSHA, "--"},
			{"diff", "--cached", "--name-only", checkedOutSHA, "--"},
			{"ls-files", "--others", "--exclude-standard"},
		} {
			out, runErr := gitexec.RunGit(ctx, worktreePath, check...)
			if runErr != nil {
				return result, fmt.Errorf("delivery worktree sync: prove unchanged worktree with git %s: %w", strings.Join(check, " "), runErr)
			}
			if strings.TrimSpace(out) != "" {
				return result, fmt.Errorf("delivery worktree sync: refusing to overwrite changes relative to %s: %s", checkedOutSHA, strings.TrimSpace(out))
			}
		}
		if _, err := gitexec.RunGit(ctx, worktreePath, "reset", "--hard", acceptedSHA); err != nil {
			return result, fmt.Errorf("delivery worktree sync: reset to accepted commit: %w", err)
		}
		result.Changed = true
	}

	status, err := gitexec.RunGit(ctx, worktreePath, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return result, fmt.Errorf("delivery worktree sync: verify status: %w", err)
	}
	if strings.TrimSpace(status) != "" {
		return result, fmt.Errorf("delivery worktree sync: accepted worktree is not clean: %s", strings.TrimSpace(status))
	}
	headSHA, err := gitexec.RunGit(ctx, worktreePath, "rev-parse", "HEAD")
	if err != nil {
		return result, fmt.Errorf("delivery worktree sync: verify HEAD: %w", err)
	}
	if strings.TrimSpace(headSHA) != acceptedSHA {
		return result, fmt.Errorf("delivery worktree sync: HEAD is %s after sync, expected %s", strings.TrimSpace(headSHA), acceptedSHA)
	}
	return result, nil
}

func acceptedBranchHead(task *model.Task) (string, error) {
	if task == nil || task.Context == nil {
		return "", fmt.Errorf("delivery worktree sync: accepted branch evidence is missing")
	}
	raw, ok := task.Context["branch_acceptance"]
	if !ok {
		return "", fmt.Errorf("delivery worktree sync: accepted branch evidence is missing")
	}
	var head any
	switch evidence := raw.(type) {
	case map[string]any:
		head = evidence["head_sha"]
	case model.JSONField:
		head = evidence["head_sha"]
	default:
		return "", fmt.Errorf("delivery worktree sync: accepted branch evidence has type %T", raw)
	}
	sha := strings.TrimSpace(fmt.Sprint(head))
	if sha == "" || sha == "<nil>" {
		return "", fmt.Errorf("delivery worktree sync: accepted branch SHA is missing")
	}
	return sha, nil
}
