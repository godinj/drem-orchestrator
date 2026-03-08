// Package merge provides multi-step merge orchestration for the Drem
// Orchestrator.  It coordinates merging agent branches into feature branches,
// feature branches into the default (main) branch, build verification, and
// post-merge syncing.
package merge

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/worktree"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// maxMergeRetries is the maximum number of attempts for transient merge failures.
const maxMergeRetries = 3

// MergePlan describes a planned merge (analysis only, no side effects).
type MergePlan struct {
	SourceBranch       string
	TargetWorktree     string
	TargetBranch       string
	CommitsToMerge     []worktree.CommitInfo
	FilesChanged       []string
	PotentialConflicts []string
}

// FeatureMergeReport summarises merging all agent branches into a feature.
type FeatureMergeReport struct {
	FeatureBranch string
	AgentMerges   []worktree.MergeResult
	AllSucceeded  bool
	BuildVerified bool
	BuildOutput   string
	FilesChanged  []string
	CommitCount   int
}

// MergeStatus provides an overview of merge readiness for a project.
type MergeStatus struct {
	ReadyToMerge []string // branches ready to merge cleanly
	Conflicted   []string // branches that would conflict
	Behind       []string // branches behind the default branch
}

// mergeWorktreeClient abstracts worktree operations needed by the merge
// retry loop. *worktree.Manager satisfies this interface.
type mergeWorktreeClient interface {
	FindWorktreeByBranch(branch string) (string, error)
	MergeBranch(sourceBranch, targetWorktree string) (*worktree.MergeResult, error)
}

// Orchestrator coordinates multi-step merges.
type Orchestrator struct {
	wt *worktree.Manager
	db *gorm.DB
}

// NewOrchestrator creates a merge Orchestrator.
func NewOrchestrator(wt *worktree.Manager, db *gorm.DB) *Orchestrator {
	return &Orchestrator{
		wt: wt,
		db: db,
	}
}

// PlanAgentMerge analyses a potential merge of agentBranch into
// featureWorktree without executing it.  It returns the list of commits
// that would be merged, the files that changed, and any files changed on
// both sides (potential conflicts).
func (o *Orchestrator) PlanAgentMerge(agentBranch, featureWorktree string) (*MergePlan, error) {
	// Determine the current branch of the feature worktree.
	featureBranch, err := worktree.RunGit([]string{"rev-parse", "--abbrev-ref", "HEAD"}, featureWorktree)
	if err != nil {
		return nil, fmt.Errorf("plan agent merge: get feature branch: %w", err)
	}

	// Commits on the agent branch since it diverged from the feature.
	commits, err := worktree.GetCommitLog(featureWorktree, featureBranch, 50)
	if err != nil {
		// If there are no commits to merge, that is not an error.
		commits = nil
	}

	// Files changed on the agent branch relative to the feature.
	agentFiles, err := worktree.GetChangedFiles(featureWorktree, featureBranch)
	if err != nil {
		agentFiles = nil
	}

	// Files changed on the feature branch since the agent branched off.
	// We approximate the merge-base with the agent branch.
	mergeBase, _ := worktree.RunGit([]string{"merge-base", featureBranch, agentBranch}, featureWorktree)
	var featureFiles []string
	if mergeBase != "" {
		featureOutput, fErr := worktree.RunGit([]string{
			"diff", "--name-only", fmt.Sprintf("%s..%s", mergeBase, featureBranch),
		}, featureWorktree)
		if fErr == nil && featureOutput != "" {
			featureFiles = strings.Split(featureOutput, "\n")
		}
	}

	// Potential conflicts: files changed on both sides.
	conflicts := intersect(agentFiles, featureFiles)

	return &MergePlan{
		SourceBranch:       agentBranch,
		TargetWorktree:     featureWorktree,
		TargetBranch:       featureBranch,
		CommitsToMerge:     commits,
		FilesChanged:       agentFiles,
		PotentialConflicts: conflicts,
	}, nil
}

// MergeAgentIntoFeature merges a single agent branch into the feature
// worktree. It rebases the agent branch onto the feature HEAD first to
// reduce conflicts from stale merge bases, then merges. Transient failures
// (no file conflicts) are retried up to maxMergeRetries times with backoff.
// Real conflicts (from rebase or merge) are returned immediately without retry.
func (o *Orchestrator) MergeAgentIntoFeature(agentBranch, featureWorktree string) (*worktree.MergeResult, error) {
	clean, err := worktree.IsClean(featureWorktree)
	if err != nil {
		return nil, fmt.Errorf("merge agent into feature: check clean: %w", err)
	}
	if !clean {
		return nil, fmt.Errorf("merge agent into feature: feature worktree %s has uncommitted changes", featureWorktree)
	}

	return mergeWithRebaseAndRetry(o.wt, agentBranch, featureWorktree)
}

// mergeWithRebaseAndRetry performs rebase-before-merge with retry logic for
// transient failures. It is separated from MergeAgentIntoFeature for testability.
//
// Retry rules:
//   - Real conflicts (rebase fails or merge returns file conflicts): return immediately
//   - Hard errors (non-GitError from RunGit): return immediately
//   - Transient failures (merge fails with no file conflicts): retry with linear backoff
func mergeWithRebaseAndRetry(wt mergeWorktreeClient, agentBranch, featureWorktree string) (*worktree.MergeResult, error) {
	var lastResult *worktree.MergeResult

	for attempt := 1; attempt <= maxMergeRetries; attempt++ {
		// Find agent worktree (may not exist if already cleaned up)
		agentWorktree, findErr := wt.FindWorktreeByBranch(agentBranch)

		// Rebase if worktree exists
		if findErr == nil {
			rebaseResult, rebaseErr := worktree.RebaseBranch(agentWorktree, featureWorktree)
			if rebaseErr != nil {
				return nil, fmt.Errorf("rebase attempt %d: %w", attempt, rebaseErr)
			}
			if !rebaseResult.Success {
				// Real conflict — don't retry
				return &worktree.MergeResult{
					Success:      false,
					SourceBranch: agentBranch,
					Conflicts:    rebaseResult.Conflicts,
					GitStderr:    rebaseResult.GitStderr,
					GitCommand:   "git rebase",
				}, nil
			}
		}

		// Merge
		var err error
		lastResult, err = wt.MergeBranch(agentBranch, featureWorktree)
		if err != nil {
			return nil, fmt.Errorf("merge attempt %d: %w", attempt, err)
		}

		if lastResult.Success {
			return lastResult, nil
		}

		// If conflicts are real file conflicts, don't retry
		if len(lastResult.Conflicts) > 0 {
			return lastResult, nil
		}

		// Transient failure — retry after backoff
		if attempt < maxMergeRetries {
			slog.Warn("merge failed transiently, retrying",
				"attempt", attempt,
				"max_attempts", maxMergeRetries,
				"agent_branch", agentBranch,
				"stderr", lastResult.GitStderr,
			)
			time.Sleep(time.Duration(attempt) * 2 * time.Second)
		}
	}

	return lastResult, nil
}

// MergeAllAgentsIntoFeature merges every completed agent branch for a
// task's subtasks into the feature worktree.  After all merges succeed it
// runs build verification.  Successfully merged agent worktrees are cleaned
// up.
func (o *Orchestrator) MergeAllAgentsIntoFeature(task *model.Task, featureWorktree string) (*FeatureMergeReport, error) {
	// Query subtasks that are DONE.
	var subtasks []model.Task
	if err := o.db.Where("parent_task_id = ? AND status = ?", task.ID, model.StatusDone).Find(&subtasks).Error; err != nil {
		return nil, fmt.Errorf("merge all agents: query subtasks: %w", err)
	}

	report := &FeatureMergeReport{
		FeatureBranch: task.WorktreeBranch,
		AllSucceeded:  true,
	}

	var mergedBranches []string

	for i := range subtasks {
		sub := &subtasks[i]
		if sub.AssignedAgentID == nil {
			continue
		}

		// Look up the agent to get its WorktreeBranch.
		var agent model.Agent
		if err := o.db.First(&agent, "id = ?", sub.AssignedAgentID).Error; err != nil {
			report.AllSucceeded = false
			report.AgentMerges = append(report.AgentMerges, worktree.MergeResult{
				Success:      false,
				SourceBranch: "unknown",
				TargetBranch: task.WorktreeBranch,
			})
			continue
		}

		if agent.WorktreeBranch == "" {
			continue
		}

		result, err := o.MergeAgentIntoFeature(agent.WorktreeBranch, featureWorktree)
		if err != nil {
			report.AllSucceeded = false
			report.AgentMerges = append(report.AgentMerges, worktree.MergeResult{
				Success:      false,
				SourceBranch: agent.WorktreeBranch,
				TargetBranch: task.WorktreeBranch,
			})
			continue
		}

		report.AgentMerges = append(report.AgentMerges, *result)
		if !result.Success {
			report.AllSucceeded = false
			continue
		}

		mergedBranches = append(mergedBranches, agent.WorktreeBranch)
	}

	// Count total commits and collect changed files.
	if report.AllSucceeded && len(report.AgentMerges) > 0 {
		filesSet := make(map[string]struct{})
		for _, mr := range report.AgentMerges {
			if mr.MergeCommit != "" {
				report.CommitCount++
			}
		}
		// Get the full set of changed files in the feature worktree since the
		// default branch, now that all merges are in.
		changed, _ := worktree.GetChangedFiles(featureWorktree, o.wt.DefaultBranch)
		for _, f := range changed {
			filesSet[f] = struct{}{}
		}
		for f := range filesSet {
			report.FilesChanged = append(report.FilesChanged, f)
		}
	}

	// If all merges succeeded, verify the build.
	if report.AllSucceeded && len(report.AgentMerges) > 0 {
		ok, output, err := o.VerifyBuild(featureWorktree)
		if err != nil {
			return nil, fmt.Errorf("merge all agents: build verification: %w", err)
		}
		report.BuildVerified = ok
		report.BuildOutput = output
	}

	// Clean up agent worktrees for successfully merged branches.
	for _, branch := range mergedBranches {
		_ = o.wt.RemoveAgentWorktree(branch)
	}

	return report, nil
}

// MergeFeatureIntoMain merges a completed feature branch into the default
// branch.  If the merge succeeds it verifies the build.  A build failure
// causes the merge to be rolled back.  On success, all other feature branches
// are synced with the updated default branch and the feature worktree is
// removed.
func (o *Orchestrator) MergeFeatureIntoMain(task *model.Task) (*worktree.MergeResult, error) {
	// Determine the main worktree path.
	mainWorktree, err := o.wt.MainWorktreePath()
	if err != nil {
		return nil, fmt.Errorf("merge feature into main: %w", err)
	}

	// Ensure the main worktree is clean.
	clean, err := worktree.IsClean(mainWorktree)
	if err != nil {
		return nil, fmt.Errorf("merge feature into main: check clean: %w", err)
	}
	if !clean {
		return nil, fmt.Errorf("merge feature into main: main worktree %s has uncommitted changes", mainWorktree)
	}

	// Perform the merge.
	result, err := o.wt.MergeBranch(task.WorktreeBranch, mainWorktree)
	if err != nil {
		return nil, fmt.Errorf("merge feature into main: %w", err)
	}
	if !result.Success {
		return result, nil
	}

	// Build verification.
	ok, output, err := o.VerifyBuild(mainWorktree)
	if err != nil {
		return nil, fmt.Errorf("merge feature into main: build verification: %w", err)
	}
	if !ok {
		// Roll back the merge commit on main.
		_, resetErr := worktree.RunGit([]string{"reset", "--hard", "HEAD~1"}, mainWorktree)
		if resetErr != nil {
			return nil, fmt.Errorf("merge feature into main: build failed and reset failed: %w", resetErr)
		}
		return &worktree.MergeResult{
			Success:      false,
			SourceBranch: task.WorktreeBranch,
			TargetBranch: o.wt.DefaultBranch,
			Conflicts:    []string{fmt.Sprintf("build verification failed: %s", output)},
		}, nil
	}

	// Sync all other features so they pick up the merged changes.
	_, _ = o.SyncFeaturesAfterMerge(task.WorktreeBranch)

	// Extract the simple feature name for removal.
	featureName := strings.TrimPrefix(task.WorktreeBranch, "feature/")
	if featureName != "" {
		_ = o.wt.RemoveFeature(featureName)
	}

	return result, nil
}

// VerifyBuild detects the project's build system and runs the corresponding
// test command with a 5-minute timeout.  It returns whether the build passed,
// the combined stdout/stderr output, and any execution error.
func (o *Orchestrator) VerifyBuild(worktreePath string) (bool, string, error) {
	cmd, args := detectBuildCommand(worktreePath)
	if cmd == "" {
		return true, "no build system detected", nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	c := exec.CommandContext(ctx, cmd, args...)
	c.Dir = worktreePath
	out, err := c.CombinedOutput()
	if err != nil {
		// A non-zero exit is a build/test failure, not an execution error.
		if ctx.Err() == context.DeadlineExceeded {
			return false, "build timed out after 5 minutes", nil
		}
		return false, string(out), nil
	}
	return true, string(out), nil
}

// SyncFeaturesAfterMerge rebases all remaining feature branches onto the
// default branch after a feature has been merged into main.
func (o *Orchestrator) SyncFeaturesAfterMerge(mergedFeature string) ([]worktree.SyncResult, error) {
	results, err := o.wt.SyncAll()
	if err != nil {
		return nil, fmt.Errorf("sync features after merge of %s: %w", mergedFeature, err)
	}
	return results, nil
}

// GetMergeStatus returns an overview of which project branches are ready to
// merge, have conflicts, or are behind the default branch.
func (o *Orchestrator) GetMergeStatus(projectID uuid.UUID) (*MergeStatus, error) {
	var tasks []model.Task
	if err := o.db.Where(
		"project_id = ? AND status IN ?",
		projectID,
		[]model.TaskStatus{model.StatusMerging, model.StatusTestingReady},
	).Find(&tasks).Error; err != nil {
		return nil, fmt.Errorf("get merge status: query tasks: %w", err)
	}

	status := &MergeStatus{}

	mainWorktree, err := o.wt.MainWorktreePath()
	if err != nil {
		return nil, fmt.Errorf("get merge status: %w", err)
	}

	for _, task := range tasks {
		if task.WorktreeBranch == "" {
			continue
		}

		// Try a dry-run merge to check for conflicts.
		_, mergeErr := worktree.RunGit([]string{
			"merge", "--no-commit", "--no-ff", task.WorktreeBranch,
		}, mainWorktree)

		// Always abort the attempted merge (whether it succeeded or not).
		worktree.RunGit([]string{"merge", "--abort"}, mainWorktree)

		if mergeErr != nil {
			status.Conflicted = append(status.Conflicted, task.WorktreeBranch)
			continue
		}

		// Check if the branch is behind main.
		branchStatus, bsErr := o.wt.GetBranchStatus(mainWorktree)
		if bsErr == nil && branchStatus.Behind > 0 {
			status.Behind = append(status.Behind, task.WorktreeBranch)
			continue
		}

		status.ReadyToMerge = append(status.ReadyToMerge, task.WorktreeBranch)
	}

	return status, nil
}

// detectBuildCommand inspects a worktree path for known build-system markers
// and returns the command and arguments to run.  Returns ("", nil) when no
// build system is detected.
func detectBuildCommand(worktreePath string) (string, []string) {
	if fileExists(filepath.Join(worktreePath, "go.mod")) {
		return "go", []string{"test", "./..."}
	}
	if fileExists(filepath.Join(worktreePath, "pyproject.toml")) {
		// Prefer uv run pytest; fall back to plain pytest.
		if _, err := exec.LookPath("uv"); err == nil {
			return "uv", []string{"run", "pytest"}
		}
		return "pytest", nil
	}
	if fileExists(filepath.Join(worktreePath, "Makefile")) {
		return "make", []string{"test"}
	}
	if fileExists(filepath.Join(worktreePath, "package.json")) {
		return "npm", []string{"test"}
	}
	return "", nil
}

// fileExists returns true if the path exists and is a regular file.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// intersect returns elements present in both a and b.
func intersect(a, b []string) []string {
	set := make(map[string]struct{}, len(b))
	for _, s := range b {
		set[s] = struct{}{}
	}
	var result []string
	for _, s := range a {
		if _, ok := set[s]; ok {
			result = append(result, s)
		}
	}
	return result
}
