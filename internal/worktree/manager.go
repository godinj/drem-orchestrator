package worktree

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

const featurePrefix = "feature/"

// WorktreeInfo describes a feature worktree.
type WorktreeInfo struct {
	Path   string
	Branch string
	Head   string // commit SHA
	IsBare bool
}

// AgentWorktreeInfo describes an agent's nested worktree.
type AgentWorktreeInfo struct {
	Path          string
	Branch        string
	Head          string
	ParentFeature string
}

// MergeResult describes the outcome of a git merge.
type MergeResult struct {
	Success      bool
	SourceBranch string
	TargetBranch string
	MergeCommit  string
	Conflicts    []string
}

// SyncResult describes the outcome of syncing a feature branch.
type SyncResult struct {
	Branch  string
	Success bool
	Error   string
}

// BranchStatus describes the state of a branch relative to its upstream.
type BranchStatus struct {
	Branch            string
	Ahead             int
	Behind            int
	DirtyFiles        int
	LastCommitSHA     string
	LastCommitMessage string
}

// Manager manages git worktrees in a 3-tier hierarchy:
// bare-repo.git/ -> feature/X/ -> .claude/worktrees/agent-<uuid>/
type Manager struct {
	BareRepoPath  string
	DefaultBranch string
}

// NewManager creates a Manager for the given bare repo.
func NewManager(bareRepoPath, defaultBranch string) *Manager {
	return &Manager{
		BareRepoPath:  bareRepoPath,
		DefaultBranch: defaultBranch,
	}
}

// ensurePrefix adds the feature/ prefix if not already present.
func ensurePrefix(name string) string {
	if strings.HasPrefix(name, featurePrefix) {
		return name
	}
	return featurePrefix + name
}

// CreateFeature creates a feature worktree at <bare-repo>/feature/<name>
// with branch feature/<name>. If the worktree already exists (directory exists),
// returns its info without error.
func (m *Manager) CreateFeature(name string) (*WorktreeInfo, error) {
	branch := ensurePrefix(name)
	worktreeDir := filepath.Join(m.BareRepoPath, branch)

	// If the worktree directory already exists, return its info
	if info, err := os.Stat(worktreeDir); err == nil && info.IsDir() {
		head, headErr := RunGit([]string{"rev-parse", "HEAD"}, worktreeDir)
		if headErr != nil {
			return nil, fmt.Errorf("create feature: read HEAD of existing worktree: %w", headErr)
		}
		return &WorktreeInfo{
			Path:   worktreeDir,
			Branch: branch,
			Head:   head,
			IsBare: false,
		}, nil
	}

	// Create the worktree with a new branch
	_, err := RunGit([]string{
		"worktree", "add", "-b", branch, worktreeDir,
	}, m.BareRepoPath)
	if err != nil {
		return nil, fmt.Errorf("create feature %q: %w", name, err)
	}

	// Get the HEAD commit of the new worktree
	head, err := RunGit([]string{"rev-parse", "HEAD"}, worktreeDir)
	if err != nil {
		return nil, fmt.Errorf("create feature: read HEAD: %w", err)
	}

	return &WorktreeInfo{
		Path:   worktreeDir,
		Branch: branch,
		Head:   head,
		IsBare: false,
	}, nil
}

// RemoveFeature removes a feature worktree and its branch.
func (m *Manager) RemoveFeature(name string) error {
	branch := ensurePrefix(name)
	worktreeDir := filepath.Join(m.BareRepoPath, branch)

	// Remove the worktree
	_, err := RunGit([]string{
		"worktree", "remove", worktreeDir, "--force",
	}, m.BareRepoPath)
	if err != nil {
		// Fallback: manual removal + prune
		os.RemoveAll(worktreeDir)
		RunGit([]string{"worktree", "prune"}, m.BareRepoPath)
	}

	// Delete the branch
	_, err = RunGit([]string{"branch", "-D", branch}, m.BareRepoPath)
	if err != nil {
		// Branch may already be gone, not a fatal error
		return nil
	}

	return nil
}

// ListWorktrees lists all worktrees using git worktree list --porcelain.
// Each worktree block has "worktree <path>", "HEAD <sha>", "branch refs/heads/<branch>"
// lines separated by blank lines.
func (m *Manager) ListWorktrees() ([]WorktreeInfo, error) {
	output, err := RunGit([]string{"worktree", "list", "--porcelain"}, m.BareRepoPath)
	if err != nil {
		return nil, fmt.Errorf("list worktrees: %w", err)
	}

	var worktrees []WorktreeInfo
	var wtPath, head, branch string
	isBare := false

	lines := strings.Split(output, "\n")
	// Append an empty line to flush the last record
	lines = append(lines, "")

	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "worktree "):
			wtPath = strings.TrimPrefix(line, "worktree ")
		case strings.HasPrefix(line, "HEAD "):
			head = strings.TrimPrefix(line, "HEAD ")
		case strings.HasPrefix(line, "branch "):
			ref := strings.TrimPrefix(line, "branch ")
			branch = strings.TrimPrefix(ref, "refs/heads/")
		case line == "bare":
			isBare = true
		case line == "" && wtPath != "":
			worktrees = append(worktrees, WorktreeInfo{
				Path:   wtPath,
				Branch: branch,
				Head:   head,
				IsBare: isBare,
			})
			// Reset for next record
			wtPath = ""
			head = ""
			branch = ""
			isBare = false
		}
	}

	return worktrees, nil
}

// CreateAgentWorktree creates a nested agent worktree inside a feature worktree:
// feature/<featureName>/.claude/worktrees/agent-<uuid>/
// With branch name worktree-agent-<uuid>. Based on the feature branch.
func (m *Manager) CreateAgentWorktree(featureName string) (*AgentWorktreeInfo, error) {
	branch := ensurePrefix(featureName)
	featureDir := filepath.Join(m.BareRepoPath, branch)

	// Verify the feature worktree exists
	if _, err := os.Stat(featureDir); os.IsNotExist(err) {
		return nil, fmt.Errorf("create agent worktree: feature worktree %q does not exist", featureDir)
	}

	agentUUID := uuid.New().String()[:8]
	agentBranch := fmt.Sprintf("worktree-agent-%s", agentUUID)
	agentDir := filepath.Join(featureDir, ".claude", "worktrees", fmt.Sprintf("agent-%s", agentUUID))

	// Ensure parent directory exists
	if err := os.MkdirAll(filepath.Dir(agentDir), 0o755); err != nil {
		return nil, fmt.Errorf("create agent worktree: mkdir: %w", err)
	}

	// Create the worktree branching from the feature branch
	_, err := RunGit([]string{
		"worktree", "add", "-b", agentBranch, agentDir, branch,
	}, m.BareRepoPath)
	if err != nil {
		return nil, fmt.Errorf("create agent worktree: %w", err)
	}

	head, err := RunGit([]string{"rev-parse", "HEAD"}, agentDir)
	if err != nil {
		return nil, fmt.Errorf("create agent worktree: read HEAD: %w", err)
	}

	return &AgentWorktreeInfo{
		Path:          agentDir,
		Branch:        agentBranch,
		Head:          head,
		ParentFeature: branch,
	}, nil
}

// RemoveAgentWorktree removes an agent worktree by its branch name.
// Uses ListWorktrees() to find the path for the given branch.
func (m *Manager) RemoveAgentWorktree(branch string) error {
	// Find the worktree path for this branch
	worktrees, err := m.ListWorktrees()
	if err != nil {
		return fmt.Errorf("remove agent worktree: %w", err)
	}

	var agentPath string
	for _, wt := range worktrees {
		if wt.Branch == branch {
			agentPath = wt.Path
			break
		}
	}

	if agentPath == "" {
		return fmt.Errorf("remove agent worktree: no worktree found for branch %q", branch)
	}

	// Remove the worktree
	_, err = RunGit([]string{
		"worktree", "remove", agentPath, "--force",
	}, m.BareRepoPath)
	if err != nil {
		// Fallback: manual removal + prune
		os.RemoveAll(agentPath)
		RunGit([]string{"worktree", "prune"}, m.BareRepoPath)
	}

	// Delete the branch
	_, _ = RunGit([]string{"branch", "-D", branch}, m.BareRepoPath)

	return nil
}

// ListAgentWorktrees lists agent worktrees inside a feature by scanning
// feature/<name>/.claude/worktrees/.
func (m *Manager) ListAgentWorktrees(featureName string) ([]AgentWorktreeInfo, error) {
	branch := ensurePrefix(featureName)
	featureDir := filepath.Join(m.BareRepoPath, branch)
	agentBase := filepath.Join(featureDir, ".claude", "worktrees")

	if _, err := os.Stat(agentBase); os.IsNotExist(err) {
		return nil, nil
	}

	entries, err := os.ReadDir(agentBase)
	if err != nil {
		return nil, fmt.Errorf("list agent worktrees: read dir: %w", err)
	}

	var agents []AgentWorktreeInfo
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "agent-") {
			continue
		}

		agentDir := filepath.Join(agentBase, entry.Name())

		head, headErr := RunGit([]string{"rev-parse", "HEAD"}, agentDir)
		if headErr != nil {
			continue
		}

		agentBranch, branchErr := RunGit([]string{"rev-parse", "--abbrev-ref", "HEAD"}, agentDir)
		if branchErr != nil {
			continue
		}

		agents = append(agents, AgentWorktreeInfo{
			Path:          agentDir,
			Branch:        agentBranch,
			Head:          head,
			ParentFeature: branch,
		})
	}

	return agents, nil
}

// MergeBranch merges a source branch into the target worktree.
// If the merge fails with conflicts, it aborts the merge and returns
// the list of conflicting files.
func (m *Manager) MergeBranch(sourceBranch, targetWorktree string) (*MergeResult, error) {
	// Get the current branch of the target worktree
	targetBranch, err := RunGit([]string{"rev-parse", "--abbrev-ref", "HEAD"}, targetWorktree)
	if err != nil {
		return nil, fmt.Errorf("merge branch: get target branch: %w", err)
	}

	// Attempt the merge
	_, mergeErr := RunGit([]string{"merge", sourceBranch, "--no-edit"}, targetWorktree)
	if mergeErr == nil {
		// Merge succeeded - get the merge commit SHA
		mergeCommit, err := RunGit([]string{"rev-parse", "HEAD"}, targetWorktree)
		if err != nil {
			return nil, fmt.Errorf("merge branch: read merge commit: %w", err)
		}
		return &MergeResult{
			Success:      true,
			SourceBranch: sourceBranch,
			TargetBranch: targetBranch,
			MergeCommit:  mergeCommit,
		}, nil
	}

	// Merge failed - collect conflicts
	var conflicts []string
	conflictOutput, conflictErr := RunGit([]string{
		"diff", "--name-only", "--diff-filter=U",
	}, targetWorktree)
	if conflictErr == nil && conflictOutput != "" {
		conflicts = strings.Split(conflictOutput, "\n")
	}

	// Abort the merge
	RunGit([]string{"merge", "--abort"}, targetWorktree)

	return &MergeResult{
		Success:      false,
		SourceBranch: sourceBranch,
		TargetBranch: targetBranch,
		Conflicts:    conflicts,
	}, nil
}

// SyncAll rebases all feature branches onto the default branch.
// If a rebase fails, it aborts and records the error.
func (m *Manager) SyncAll() ([]SyncResult, error) {
	worktrees, err := m.ListWorktrees()
	if err != nil {
		return nil, fmt.Errorf("sync all: %w", err)
	}

	var results []SyncResult
	for _, wt := range worktrees {
		if wt.IsBare {
			continue
		}
		if !strings.HasPrefix(wt.Branch, featurePrefix) {
			continue
		}

		_, rebaseErr := RunGit([]string{"rebase", m.DefaultBranch}, wt.Path)
		if rebaseErr == nil {
			results = append(results, SyncResult{
				Branch:  wt.Branch,
				Success: true,
			})
			continue
		}

		// Rebase failed - abort and record error
		RunGit([]string{"rebase", "--abort"}, wt.Path)

		results = append(results, SyncResult{
			Branch:  wt.Branch,
			Success: false,
			Error:   rebaseErr.Error(),
		})
	}

	return results, nil
}

// GetBranchStatus returns ahead/behind counts and dirty file count for a worktree.
func (m *Manager) GetBranchStatus(worktreePath string) (*BranchStatus, error) {
	branch, err := RunGit([]string{"rev-parse", "--abbrev-ref", "HEAD"}, worktreePath)
	if err != nil {
		return nil, fmt.Errorf("get branch status: get branch: %w", err)
	}

	// Get ahead/behind counts relative to default branch
	var ahead, behind int
	revList, revErr := RunGit([]string{
		"rev-list", "--left-right", "--count",
		fmt.Sprintf("%s...%s", m.DefaultBranch, branch),
	}, worktreePath)
	if revErr == nil {
		parts := strings.Fields(revList)
		if len(parts) == 2 {
			behind, _ = strconv.Atoi(parts[0])
			ahead, _ = strconv.Atoi(parts[1])
		}
	}

	// Count dirty files
	statusOutput, err := RunGit([]string{"status", "--porcelain"}, worktreePath)
	dirtyFiles := 0
	if err == nil && statusOutput != "" {
		dirtyFiles = len(strings.Split(statusOutput, "\n"))
	}

	// Last commit info
	lastInfo, err := RunGit([]string{"log", "-1", "--format=%H|%s"}, worktreePath)
	if err != nil {
		return nil, fmt.Errorf("get branch status: get last commit: %w", err)
	}

	var lastSHA, lastMessage string
	parts := strings.SplitN(lastInfo, "|", 2)
	if len(parts) == 2 {
		lastSHA = parts[0]
		lastMessage = parts[1]
	}

	return &BranchStatus{
		Branch:            branch,
		Ahead:             ahead,
		Behind:            behind,
		DirtyFiles:        dirtyFiles,
		LastCommitSHA:     lastSHA,
		LastCommitMessage: lastMessage,
	}, nil
}
