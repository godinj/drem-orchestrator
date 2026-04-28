package worktreehost

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
	Success           bool
	SourceBranch      string
	TargetBranch      string
	MergeCommit       string
	Conflicts         []string
	GitStderr         string // raw git stderr on failure
	GitCommand        string // the exact git command that failed
	ClassifiedDetails string // formatted conflict classification
	TrivialCount      int    // count of trivial conflicts
	NonTrivialCount   int    // count of non-trivial conflicts
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

// BareRepo returns the bare repository path. This accessor exists so
// *Manager can satisfy the orchestrator.WorktreeManager interface, which
// uses methods instead of struct fields.
func (m *Manager) BareRepo() string {
	return m.BareRepoPath
}

// DefaultBranchName returns the default branch for the repo. This accessor
// exists so *Manager can satisfy the orchestrator.WorktreeManager interface.
func (m *Manager) DefaultBranchName() string {
	return m.DefaultBranch
}

// FeatureGroupDir returns the parent directory that groups a feature's
// integration worktree and its agent worktrees: <bare>/feature/<name>/
func (m *Manager) FeatureGroupDir(name string) string {
	return filepath.Join(m.BareRepoPath, "feature", name)
}

// FeatureWorktreePath returns the path to the integration worktree inside
// a feature group directory: <bare>/feature/<name>/integration/
func (m *Manager) FeatureWorktreePath(name string) string {
	return filepath.Join(m.FeatureGroupDir(name), "integration")
}

// MainWorktreePath returns the filesystem path of the worktree that has the
// default branch checked out.
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
func (m *Manager) CreateFeature(name string) (*WorktreeInfo, error) {
	branch := ensurePrefix(name)
	featureName := strings.TrimPrefix(branch, featurePrefix)
	groupDir := m.FeatureGroupDir(featureName)
	integrationDir := m.FeatureWorktreePath(featureName)

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

	if err := os.MkdirAll(groupDir, 0o755); err != nil {
		return nil, fmt.Errorf("create feature %q: mkdir group: %w", name, err)
	}

	_, err := RunGit([]string{
		"worktree", "add", "-b", branch, integrationDir,
	}, m.BareRepoPath)
	if err != nil {
		if strings.Contains(err.Error(), "already exists") {
			_, err = RunGit([]string{
				"worktree", "add", integrationDir, branch,
			}, m.BareRepoPath)
		}
		if err != nil {
			return nil, fmt.Errorf("create feature %q: %w", name, err)
		}
	}

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

	agents, _ := m.ListAgentWorktrees(featureName)
	for _, ag := range agents {
		_ = m.RemoveAgentWorktree(ag.Branch)
	}

	_, err := RunGit([]string{
		"worktree", "remove", integrationDir, "--force",
	}, m.BareRepoPath)
	if err != nil {
		os.RemoveAll(integrationDir)
		RunGit([]string{"worktree", "prune"}, m.BareRepoPath)
	}

	_, _ = RunGit([]string{"branch", "-D", branch}, m.BareRepoPath)

	os.RemoveAll(groupDir)

	return nil
}

// ListWorktrees lists all worktrees using git worktree list --porcelain.
func (m *Manager) ListWorktrees() ([]WorktreeInfo, error) {
	output, err := RunGit([]string{"worktree", "list", "--porcelain"}, m.BareRepoPath)
	if err != nil {
		return nil, fmt.Errorf("list worktrees: %w", err)
	}

	var worktrees []WorktreeInfo
	var wtPath, head, branch string
	isBare := false

	lines := strings.Split(output, "\n")
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
func (m *Manager) CreateAgentWorktree(featureName string) (*AgentWorktreeInfo, error) {
	branch := ensurePrefix(featureName)
	fn := strings.TrimPrefix(branch, featurePrefix)
	integrationDir := m.FeatureWorktreePath(fn)

	if _, err := os.Stat(integrationDir); os.IsNotExist(err) {
		return nil, fmt.Errorf("create agent worktree: integration worktree %q does not exist", integrationDir)
	}

	agentUUID := uuid.New().String()[:8]
	agentBranch := fmt.Sprintf("worktree-agent-%s", agentUUID)
	groupDir := m.FeatureGroupDir(fn)
	agentDir := filepath.Join(groupDir, fmt.Sprintf("agent-%s", agentUUID))

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

const agentBranchPrefix = "worktree-agent-"

// RemoveAgentWorktree removes an agent worktree by its branch name.
func (m *Manager) RemoveAgentWorktree(branch string) error {
	if !strings.HasPrefix(branch, agentBranchPrefix) {
		return fmt.Errorf("remove agent worktree: refusing to remove non-agent branch %q", branch)
	}

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

	_, err = RunGit([]string{
		"worktree", "remove", agentPath, "--force",
	}, m.BareRepoPath)
	if err != nil {
		os.RemoveAll(agentPath)
		RunGit([]string{"worktree", "prune"}, m.BareRepoPath)
	}

	_, _ = RunGit([]string{"branch", "-D", branch}, m.BareRepoPath)

	return nil
}

// ListAgentWorktrees lists agent worktrees inside a feature group directory.
func (m *Manager) ListAgentWorktrees(featureName string) ([]AgentWorktreeInfo, error) {
	branch := ensurePrefix(featureName)
	fn := strings.TrimPrefix(branch, featurePrefix)
	groupDir := m.FeatureGroupDir(fn)

	if _, err := os.Stat(groupDir); os.IsNotExist(err) {
		return nil, nil
	}

	allWorktrees, err := m.ListWorktrees()
	if err != nil {
		return nil, fmt.Errorf("list agent worktrees: %w", err)
	}
	wtByPath := make(map[string]WorktreeInfo, len(allWorktrees))
	for _, wt := range allWorktrees {
		wtByPath[wt.Path] = wt
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

		wt, ok := wtByPath[agentDir]
		if !ok {
			continue
		}

		if !strings.HasPrefix(wt.Branch, agentBranchPrefix) {
			continue
		}

		agents = append(agents, AgentWorktreeInfo{
			Path:          agentDir,
			Branch:        wt.Branch,
			Head:          wt.Head,
			ParentFeature: branch,
		})
	}

	return agents, nil
}

// MergeBranch merges a source branch into the target worktree.
func (m *Manager) MergeBranch(sourceBranch, targetWorktree string) (*MergeResult, error) {
	_, err := RunGit([]string{"rev-parse", "--verify", sourceBranch}, targetWorktree)
	if err != nil {
		slog.Warn("branch ref not visible, fetching",
			"branch", sourceBranch,
			"worktree", targetWorktree,
		)
		RunGit([]string{"fetch", ".", sourceBranch + ":" + sourceBranch}, targetWorktree)

		_, err = RunGit([]string{"rev-parse", "--verify", sourceBranch}, targetWorktree)
		if err != nil {
			return nil, fmt.Errorf("branch %s not resolvable after fetch: %w", sourceBranch, err)
		}
	}

	targetBranch, err := RunGit([]string{"rev-parse", "--abbrev-ref", "HEAD"}, targetWorktree)
	if err != nil {
		return nil, fmt.Errorf("merge branch: get target branch: %w", err)
	}

	mergeArgs := []string{
		"-c", "user.name=drem-orchestrator",
		"-c", "user.email=drem-orchestrator@localhost",
		"merge", sourceBranch, "--no-edit",
	}
	_, mergeErr := RunGit(mergeArgs, targetWorktree)
	if mergeErr == nil {
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

	var gitStderr, gitCommand string
	if gitErr, ok := mergeErr.(*GitError); ok {
		gitStderr = gitErr.Stderr
		if gitErr.Stdout != "" {
			if gitStderr != "" {
				gitStderr = gitErr.Stdout + "\n" + gitStderr
			} else {
				gitStderr = gitErr.Stdout
			}
		}
		gitCommand = gitErr.Command
	}

	var conflicts []string
	conflictOutput, conflictErr := RunGit([]string{
		"diff", "--name-only", "--diff-filter=U",
	}, targetWorktree)
	if conflictErr == nil && conflictOutput != "" {
		conflicts = strings.Split(conflictOutput, "\n")
	}

	RunGit([]string{"merge", "--abort"}, targetWorktree)

	return &MergeResult{
		Success:      false,
		SourceBranch: sourceBranch,
		TargetBranch: targetBranch,
		Conflicts:    conflicts,
		GitStderr:    gitStderr,
		GitCommand:   gitCommand,
	}, nil
}

// FindWorktreeByBranch returns the worktree path for the given branch.
func (m *Manager) FindWorktreeByBranch(branch string) (string, error) {
	worktrees, err := m.ListWorktrees()
	if err != nil {
		return "", fmt.Errorf("find worktree by branch: %w", err)
	}

	for _, wt := range worktrees {
		if wt.Branch == branch {
			return wt.Path, nil
		}
	}

	return "", fmt.Errorf("find worktree by branch: no worktree found for branch %q", branch)
}

// RebaseBranchOnto rebases the branch in featureWorktree onto the HEAD of
// mainWorktree. Delegates to the package-level RebaseBranch function.
func (m *Manager) RebaseBranchOnto(featureWorktree, mainWorktree string) (*RebaseResult, error) {
	return RebaseBranch(featureWorktree, mainWorktree)
}

// SyncAll rebases all feature branches onto the default branch.
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
func (m *Manager) MigrateToGroupedLayout() error {
	worktrees, err := m.ListWorktrees()
	if err != nil {
		return fmt.Errorf("migrate: list worktrees: %w", err)
	}

	featureDir := filepath.Join(m.BareRepoPath, "feature")

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
		oldPath := feat.Path
		groupDir := m.FeatureGroupDir(featureName)
		integrationDir := m.FeatureWorktreePath(featureName)

		slog.Info("migrating feature worktree", "feature", featureName, "from", oldPath, "to", integrationDir)

		var agentWTs []WorktreeInfo
		for _, wt := range worktrees {
			if strings.HasPrefix(wt.Path, filepath.Join(oldPath, ".claude", "worktrees", "agent-")) {
				agentWTs = append(agentWTs, wt)
			}
		}

		if len(agentWTs) > 0 {
			if err := os.MkdirAll(migrationTmp, 0o755); err != nil {
				return fmt.Errorf("migrate: mkdir tmp: %w", err)
			}
		}
		for _, ag := range agentWTs {
			agentDirName := filepath.Base(ag.Path)
			tmpDest := filepath.Join(migrationTmp, agentDirName)
			slog.Info("migrating agent worktree to tmp", "agent", agentDirName, "from", ag.Path, "to", tmpDest)
			if _, err := RunGit([]string{"worktree", "move", ag.Path, tmpDest}, m.BareRepoPath); err != nil {
				return fmt.Errorf("migrate: move agent %s to tmp: %w", agentDirName, err)
			}
		}

		tmpName := oldPath + "--migrating"
		if _, err := RunGit([]string{"worktree", "move", oldPath, tmpName}, m.BareRepoPath); err != nil {
			return fmt.Errorf("migrate: move feature %s to tmp name: %w", featureName, err)
		}

		if err := os.MkdirAll(groupDir, 0o755); err != nil {
			return fmt.Errorf("migrate: mkdir group %s: %w", groupDir, err)
		}

		if _, err := RunGit([]string{"worktree", "move", tmpName, integrationDir}, m.BareRepoPath); err != nil {
			return fmt.Errorf("migrate: move feature %s to integration: %w", featureName, err)
		}

		for _, ag := range agentWTs {
			agentDirName := filepath.Base(ag.Path)
			tmpSrc := filepath.Join(migrationTmp, agentDirName)
			dest := filepath.Join(groupDir, agentDirName)
			slog.Info("migrating agent worktree to group", "agent", agentDirName, "to", dest)
			if _, err := RunGit([]string{"worktree", "move", tmpSrc, dest}, m.BareRepoPath); err != nil {
				return fmt.Errorf("migrate: move agent %s to group: %w", agentDirName, err)
			}
		}

		os.RemoveAll(migrationTmp)
	}

	return nil
}

// MigrateAgentPaths updates Agent.WorktreePath in the database.
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

	statusOutput, err := RunGit([]string{"status", "--porcelain"}, worktreePath)
	dirtyFiles := 0
	if err == nil && statusOutput != "" {
		dirtyFiles = len(strings.Split(statusOutput, "\n"))
	}

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
