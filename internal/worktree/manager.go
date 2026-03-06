package worktree

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"
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

// Manager manages git worktrees in a grouped hierarchy:
// bare-repo.git/feature/<name>/integration/ (feature worktree)
// bare-repo.git/feature/<name>/agent-<uuid>/ (agent worktrees as siblings)
type Manager struct {
	BareRepoPath  string
	DefaultBranch string
}

// FeatureGroupDir returns the parent directory that groups a feature's
// integration worktree and its agent worktrees:
// <bare>/feature/<name>/
func (m *Manager) FeatureGroupDir(name string) string {
	return filepath.Join(m.BareRepoPath, "feature", name)
}

// FeatureWorktreePath returns the path to the integration worktree inside
// a feature group directory: <bare>/feature/<name>/integration/
func (m *Manager) FeatureWorktreePath(name string) string {
	return filepath.Join(m.FeatureGroupDir(name), "integration")
}

// MainWorktreePath returns the filesystem path of the worktree that has the
// default branch checked out.  It queries `git worktree list` to find the
// actual path rather than guessing from directory names, which may not match
// the branch name.
func (m *Manager) MainWorktreePath() (string, error) {
	output, err := RunGit([]string{"worktree", "list", "--porcelain"}, m.BareRepoPath)
	if err != nil {
		return "", fmt.Errorf("main worktree path: list worktrees: %w", err)
	}

	var currentPath string
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "worktree ") {
			currentPath = strings.TrimPrefix(line, "worktree ")
		}
		if strings.TrimSpace(line) == "branch refs/heads/"+m.DefaultBranch && currentPath != "" {
			// Skip the bare repo entry itself.
			if currentPath == m.BareRepoPath {
				continue
			}
			return currentPath, nil
		}
	}

	return "", fmt.Errorf("main worktree path: no worktree found for branch %s", m.DefaultBranch)
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

// CreateFeature creates a feature worktree at
// <bare-repo>/feature/<name>/integration/ with branch feature/<name>.
// The parent directory <bare-repo>/feature/<name>/ is a plain directory
// that groups the integration worktree with its agent worktrees.
// If the integration worktree already exists, returns its info without error.
func (m *Manager) CreateFeature(name string) (*WorktreeInfo, error) {
	branch := ensurePrefix(name)
	featureName := strings.TrimPrefix(branch, featurePrefix)
	groupDir := m.FeatureGroupDir(featureName)
	integrationDir := m.FeatureWorktreePath(featureName)

	// If the integration worktree already exists, return its info
	if info, err := os.Stat(integrationDir); err == nil && info.IsDir() {
		head, headErr := RunGit([]string{"rev-parse", "HEAD"}, integrationDir)
		if headErr != nil {
			return nil, fmt.Errorf("create feature: read HEAD of existing worktree: %w", headErr)
		}
		return &WorktreeInfo{
			Path:   integrationDir,
			Branch: branch,
			Head:   head,
			IsBare: false,
		}, nil
	}

	// Create the group parent directory
	if err := os.MkdirAll(groupDir, 0o755); err != nil {
		return nil, fmt.Errorf("create feature %q: mkdir group: %w", name, err)
	}

	// Create the worktree with a new branch inside the group dir
	_, err := RunGit([]string{
		"worktree", "add", "-b", branch, integrationDir,
	}, m.BareRepoPath)
	if err != nil {
		return nil, fmt.Errorf("create feature %q: %w", name, err)
	}

	// Get the HEAD commit of the new worktree
	head, err := RunGit([]string{"rev-parse", "HEAD"}, integrationDir)
	if err != nil {
		return nil, fmt.Errorf("create feature: read HEAD: %w", err)
	}

	return &WorktreeInfo{
		Path:   integrationDir,
		Branch: branch,
		Head:   head,
		IsBare: false,
	}, nil
}

// RemoveFeature removes all agent worktrees, the integration worktree,
// the feature branch, and the group directory.
func (m *Manager) RemoveFeature(name string) error {
	branch := ensurePrefix(name)
	featureName := strings.TrimPrefix(branch, featurePrefix)
	groupDir := m.FeatureGroupDir(featureName)
	integrationDir := m.FeatureWorktreePath(featureName)

	// Remove all agent worktrees first
	agents, _ := m.ListAgentWorktrees(featureName)
	for _, ag := range agents {
		_ = m.RemoveAgentWorktree(ag.Branch)
	}

	// Remove the integration worktree
	_, err := RunGit([]string{
		"worktree", "remove", integrationDir, "--force",
	}, m.BareRepoPath)
	if err != nil {
		// Fallback: manual removal + prune
		os.RemoveAll(integrationDir)
		RunGit([]string{"worktree", "prune"}, m.BareRepoPath)
	}

	// Delete the feature branch
	_, _ = RunGit([]string{"branch", "-D", branch}, m.BareRepoPath)

	// Remove the group directory
	os.RemoveAll(groupDir)

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

// CreateAgentWorktree creates an agent worktree as a sibling of the
// integration worktree: feature/<featureName>/agent-<uuid>/
// With branch name worktree-agent-<uuid>. Based on the feature branch.
func (m *Manager) CreateAgentWorktree(featureName string) (*AgentWorktreeInfo, error) {
	branch := ensurePrefix(featureName)
	fn := strings.TrimPrefix(branch, featurePrefix)
	integrationDir := m.FeatureWorktreePath(fn)

	// Verify the integration worktree exists
	if _, err := os.Stat(integrationDir); os.IsNotExist(err) {
		return nil, fmt.Errorf("create agent worktree: integration worktree %q does not exist", integrationDir)
	}

	agentUUID := uuid.New().String()[:8]
	agentBranch := fmt.Sprintf("worktree-agent-%s", agentUUID)
	groupDir := m.FeatureGroupDir(fn)
	agentDir := filepath.Join(groupDir, fmt.Sprintf("agent-%s", agentUUID))

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

// ListAgentWorktrees lists agent worktrees inside a feature group directory
// by scanning feature/<name>/agent-* directories.
func (m *Manager) ListAgentWorktrees(featureName string) ([]AgentWorktreeInfo, error) {
	branch := ensurePrefix(featureName)
	fn := strings.TrimPrefix(branch, featurePrefix)
	groupDir := m.FeatureGroupDir(fn)

	if _, err := os.Stat(groupDir); os.IsNotExist(err) {
		return nil, nil
	}

	entries, err := os.ReadDir(groupDir)
	if err != nil {
		return nil, fmt.Errorf("list agent worktrees: read dir: %w", err)
	}

	var agents []AgentWorktreeInfo
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "agent-") {
			continue
		}

		agentDir := filepath.Join(groupDir, entry.Name())

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

// MigrateToGroupedLayout relocates worktrees from the old flat layout
// (feature/<name>/) to the new grouped layout (feature/<name>/integration/).
// Agent worktrees are moved from .claude/worktrees/agent-<uuid> to sibling
// directories at feature/<name>/agent-<uuid>/. Idempotent: skips worktrees
// already at /integration paths.
func (m *Manager) MigrateToGroupedLayout() error {
	worktrees, err := m.ListWorktrees()
	if err != nil {
		return fmt.Errorf("migrate: list worktrees: %w", err)
	}

	featureDir := filepath.Join(m.BareRepoPath, "feature")

	// Build map of feature worktrees in old layout: path starts with
	// <bare>/feature/<name> and does NOT end in /integration.
	var oldFeatures []WorktreeInfo
	for _, wt := range worktrees {
		if wt.IsBare {
			continue
		}
		if !strings.HasPrefix(wt.Branch, featurePrefix) {
			continue
		}
		if !strings.HasPrefix(wt.Path, featureDir+string(os.PathSeparator)) {
			continue
		}
		// Already migrated — path ends in /integration
		if filepath.Base(wt.Path) == "integration" {
			continue
		}
		oldFeatures = append(oldFeatures, wt)
	}

	if len(oldFeatures) == 0 {
		return nil
	}

	migrationTmp := filepath.Join(m.BareRepoPath, ".migration-tmp")

	for _, feat := range oldFeatures {
		featureName := strings.TrimPrefix(feat.Branch, featurePrefix)
		oldPath := feat.Path // e.g. <bare>/feature/<name>
		groupDir := m.FeatureGroupDir(featureName)
		integrationDir := m.FeatureWorktreePath(featureName)

		slog.Info("migrating feature worktree", "feature", featureName, "from", oldPath, "to", integrationDir)

		// (a) Find agent worktrees nested at .claude/worktrees/agent-*
		var agentWTs []WorktreeInfo
		for _, wt := range worktrees {
			if strings.HasPrefix(wt.Path, filepath.Join(oldPath, ".claude", "worktrees", "agent-")) {
				agentWTs = append(agentWTs, wt)
			}
		}

		// (b) Move agents to temp holding area
		if len(agentWTs) > 0 {
			if err := os.MkdirAll(migrationTmp, 0o755); err != nil {
				return fmt.Errorf("migrate: mkdir tmp: %w", err)
			}
		}
		for _, ag := range agentWTs {
			agentDirName := filepath.Base(ag.Path) // agent-<uuid>
			tmpDest := filepath.Join(migrationTmp, agentDirName)
			slog.Info("migrating agent worktree to tmp", "agent", agentDirName, "from", ag.Path, "to", tmpDest)
			if _, err := RunGit([]string{"worktree", "move", ag.Path, tmpDest}, m.BareRepoPath); err != nil {
				return fmt.Errorf("migrate: move agent %s to tmp: %w", agentDirName, err)
			}
		}

		// (c) Move the feature worktree to a temp name
		tmpName := oldPath + "--migrating"
		if _, err := RunGit([]string{"worktree", "move", oldPath, tmpName}, m.BareRepoPath); err != nil {
			return fmt.Errorf("migrate: move feature %s to tmp name: %w", featureName, err)
		}

		// (d) Create the group directory
		if err := os.MkdirAll(groupDir, 0o755); err != nil {
			return fmt.Errorf("migrate: mkdir group %s: %w", groupDir, err)
		}

		// (e) Move the feature worktree into it as integration
		if _, err := RunGit([]string{"worktree", "move", tmpName, integrationDir}, m.BareRepoPath); err != nil {
			return fmt.Errorf("migrate: move feature %s to integration: %w", featureName, err)
		}

		// (f) Move each agent into the group dir as a sibling
		for _, ag := range agentWTs {
			agentDirName := filepath.Base(ag.Path)
			tmpSrc := filepath.Join(migrationTmp, agentDirName)
			dest := filepath.Join(groupDir, agentDirName)
			slog.Info("migrating agent worktree to group", "agent", agentDirName, "to", dest)
			if _, err := RunGit([]string{"worktree", "move", tmpSrc, dest}, m.BareRepoPath); err != nil {
				return fmt.Errorf("migrate: move agent %s to group: %w", agentDirName, err)
			}
		}

		// (g) Clean up .migration-tmp/
		os.RemoveAll(migrationTmp)
	}

	return nil
}

// MigrateAgentPaths updates Agent.WorktreePath in the database, converting
// old-style nested paths (.claude/worktrees/agent-) to the new sibling layout.
func (m *Manager) MigrateAgentPaths(db *gorm.DB) {
	result := db.Exec(
		`UPDATE agents SET worktree_path = REPLACE(worktree_path, '/.claude/worktrees/agent-', '/agent-') WHERE worktree_path LIKE '%/.claude/worktrees/agent-%'`,
	)
	if result.Error != nil {
		slog.Warn("migrate agent paths failed", "error", result.Error)
		return
	}
	if result.RowsAffected > 0 {
		slog.Info("migrated agent worktree paths", "count", result.RowsAffected)
	}
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
