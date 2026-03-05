package worktree

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// initBareRepo creates a bare git repo with an initial empty commit in tmpDir.
// Returns the path to the bare repo.
func initBareRepo(t *testing.T) string {
	t.Helper()

	tmpDir := t.TempDir()
	bareRepo := filepath.Join(tmpDir, "test.git")

	// Create a bare repo
	_, err := RunGit([]string{"init", "--bare", bareRepo}, "")
	if err != nil {
		t.Fatalf("failed to init bare repo: %v", err)
	}

	// We need a commit to base worktrees on. Create a temporary clone,
	// make an empty commit, and push back to the bare repo.
	cloneDir := filepath.Join(tmpDir, "clone-init")
	_, err = RunGit([]string{"clone", bareRepo, cloneDir}, "")
	if err != nil {
		t.Fatalf("failed to clone bare repo: %v", err)
	}

	// Configure git user for the clone
	RunGit([]string{"config", "user.email", "test@test.com"}, cloneDir)
	RunGit([]string{"config", "user.name", "Test"}, cloneDir)

	_, err = RunGit([]string{"commit", "--allow-empty", "-m", "init"}, cloneDir)
	if err != nil {
		t.Fatalf("failed to create initial commit: %v", err)
	}

	_, err = RunGit([]string{"push", "origin", "HEAD"}, cloneDir)
	if err != nil {
		t.Fatalf("failed to push initial commit: %v", err)
	}

	// Detect the default branch that was created
	defaultBranch, err := RunGit([]string{"rev-parse", "--abbrev-ref", "HEAD"}, cloneDir)
	if err != nil {
		t.Fatalf("failed to determine default branch: %v", err)
	}

	// Create a main worktree from the bare repo so worktrees can reference it.
	// The bare repo HEAD should already point to the default branch.
	mainWorktree := filepath.Join(bareRepo, defaultBranch)
	_, err = RunGit([]string{"worktree", "add", mainWorktree, defaultBranch}, bareRepo)
	if err != nil {
		t.Fatalf("failed to create main worktree: %v", err)
	}

	// Clean up the temporary clone
	os.RemoveAll(cloneDir)

	return bareRepo
}

// getDefaultBranch returns the default branch name for the bare repo.
func getDefaultBranch(t *testing.T, bareRepo string) string {
	t.Helper()
	branch, err := RunGit([]string{"symbolic-ref", "--short", "HEAD"}, bareRepo)
	if err != nil {
		t.Fatalf("failed to get default branch: %v", err)
	}
	return branch
}

func TestCreateAndListFeature(t *testing.T) {
	bareRepo := initBareRepo(t)
	defaultBranch := getDefaultBranch(t, bareRepo)
	mgr := NewManager(bareRepo, defaultBranch)

	// Create a feature worktree
	info, err := mgr.CreateFeature("my-feature")
	if err != nil {
		t.Fatalf("CreateFeature failed: %v", err)
	}

	if info.Branch != "feature/my-feature" {
		t.Errorf("expected branch feature/my-feature, got %s", info.Branch)
	}

	expectedPath := filepath.Join(bareRepo, "feature", "my-feature", "integration")
	if info.Path != expectedPath {
		t.Errorf("expected path %s, got %s", expectedPath, info.Path)
	}

	if info.Head == "" {
		t.Error("expected non-empty HEAD SHA")
	}

	if info.IsBare {
		t.Error("expected IsBare to be false")
	}

	// Verify the directory was created
	if _, err := os.Stat(info.Path); os.IsNotExist(err) {
		t.Error("feature worktree directory was not created")
	}

	// List worktrees and verify the feature appears
	worktrees, err := mgr.ListWorktrees()
	if err != nil {
		t.Fatalf("ListWorktrees failed: %v", err)
	}

	found := false
	for _, wt := range worktrees {
		if wt.Branch == "feature/my-feature" {
			found = true
			break
		}
	}
	if !found {
		t.Error("feature/my-feature not found in worktree list")
	}

	// Creating the same feature again should return existing info without error
	info2, err := mgr.CreateFeature("my-feature")
	if err != nil {
		t.Fatalf("CreateFeature (idempotent) failed: %v", err)
	}
	if info2.Branch != info.Branch {
		t.Errorf("expected same branch on re-create, got %s", info2.Branch)
	}
}

func TestCreateAgentWorktree(t *testing.T) {
	bareRepo := initBareRepo(t)
	defaultBranch := getDefaultBranch(t, bareRepo)
	mgr := NewManager(bareRepo, defaultBranch)

	// Create a feature first
	featureInfo, err := mgr.CreateFeature("agent-test")
	if err != nil {
		t.Fatalf("CreateFeature failed: %v", err)
	}

	// Create an agent worktree inside the feature
	agentInfo, err := mgr.CreateAgentWorktree("agent-test")
	if err != nil {
		t.Fatalf("CreateAgentWorktree failed: %v", err)
	}

	// Verify the agent worktree is a sibling of the integration worktree
	groupDir := filepath.Dir(featureInfo.Path) // parent of integration/
	if !strings.HasPrefix(agentInfo.Path, groupDir) {
		t.Errorf("agent path %s is not inside group dir %s", agentInfo.Path, groupDir)
	}

	// Verify the agent is at <groupDir>/agent-<uuid>, not nested in .claude/worktrees/
	if strings.Contains(agentInfo.Path, ".claude") {
		t.Errorf("agent path %s should not contain .claude", agentInfo.Path)
	}
	agentDirName := filepath.Base(agentInfo.Path)
	if !strings.HasPrefix(agentDirName, "agent-") {
		t.Errorf("agent dir name %s does not start with agent-", agentDirName)
	}

	// Verify the branch name pattern
	if !strings.HasPrefix(agentInfo.Branch, "worktree-agent-") {
		t.Errorf("expected branch prefix worktree-agent-, got %s", agentInfo.Branch)
	}

	// Verify parent feature is set correctly
	if agentInfo.ParentFeature != "feature/agent-test" {
		t.Errorf("expected parent feature feature/agent-test, got %s", agentInfo.ParentFeature)
	}

	if agentInfo.Head == "" {
		t.Error("expected non-empty HEAD SHA for agent worktree")
	}

	// Verify the directory was created
	if _, err := os.Stat(agentInfo.Path); os.IsNotExist(err) {
		t.Error("agent worktree directory was not created")
	}

	// List agent worktrees and verify ours appears
	agents, err := mgr.ListAgentWorktrees("agent-test")
	if err != nil {
		t.Fatalf("ListAgentWorktrees failed: %v", err)
	}

	if len(agents) != 1 {
		t.Fatalf("expected 1 agent worktree, got %d", len(agents))
	}

	if agents[0].Branch != agentInfo.Branch {
		t.Errorf("expected agent branch %s in list, got %s", agentInfo.Branch, agents[0].Branch)
	}
}

func TestRemoveFeature(t *testing.T) {
	bareRepo := initBareRepo(t)
	defaultBranch := getDefaultBranch(t, bareRepo)
	mgr := NewManager(bareRepo, defaultBranch)

	// Create a feature
	info, err := mgr.CreateFeature("remove-me")
	if err != nil {
		t.Fatalf("CreateFeature failed: %v", err)
	}

	// Verify it exists
	if _, err := os.Stat(info.Path); os.IsNotExist(err) {
		t.Fatal("feature worktree directory was not created")
	}

	// Remove it
	err = mgr.RemoveFeature("remove-me")
	if err != nil {
		t.Fatalf("RemoveFeature failed: %v", err)
	}

	// Verify the integration worktree directory is gone
	if _, err := os.Stat(info.Path); !os.IsNotExist(err) {
		t.Error("integration worktree directory still exists after removal")
	}

	// Verify the entire group directory is gone
	groupDir := filepath.Dir(info.Path)
	if _, err := os.Stat(groupDir); !os.IsNotExist(err) {
		t.Error("feature group directory still exists after removal")
	}

	// Verify it no longer appears in the list
	worktrees, err := mgr.ListWorktrees()
	if err != nil {
		t.Fatalf("ListWorktrees failed: %v", err)
	}

	for _, wt := range worktrees {
		if wt.Branch == "feature/remove-me" {
			t.Error("feature/remove-me still appears in worktree list after removal")
		}
	}
}

func TestMergeBranch(t *testing.T) {
	bareRepo := initBareRepo(t)
	defaultBranch := getDefaultBranch(t, bareRepo)
	mgr := NewManager(bareRepo, defaultBranch)

	// Create a feature worktree
	featureInfo, err := mgr.CreateFeature("merge-source")
	if err != nil {
		t.Fatalf("CreateFeature failed: %v", err)
	}

	// Configure git user in the feature worktree
	RunGit([]string{"config", "user.email", "test@test.com"}, featureInfo.Path)
	RunGit([]string{"config", "user.name", "Test"}, featureInfo.Path)

	// Create a file and commit in the feature worktree
	testFile := filepath.Join(featureInfo.Path, "test.txt")
	if err := os.WriteFile(testFile, []byte("hello from feature"), 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	RunGit([]string{"add", "test.txt"}, featureInfo.Path)
	_, err = RunGit([]string{"commit", "-m", "add test file"}, featureInfo.Path)
	if err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	// Create a target worktree to merge into
	targetInfo, err := mgr.CreateFeature("merge-target")
	if err != nil {
		t.Fatalf("CreateFeature for target failed: %v", err)
	}

	// Merge the source feature into the target
	result, err := mgr.MergeBranch("feature/merge-source", targetInfo.Path)
	if err != nil {
		t.Fatalf("MergeBranch failed: %v", err)
	}

	if !result.Success {
		t.Errorf("expected merge to succeed, got conflicts: %v", result.Conflicts)
	}

	if result.SourceBranch != "feature/merge-source" {
		t.Errorf("expected source branch feature/merge-source, got %s", result.SourceBranch)
	}

	if result.TargetBranch != "feature/merge-target" {
		t.Errorf("expected target branch feature/merge-target, got %s", result.TargetBranch)
	}

	if result.MergeCommit == "" {
		t.Error("expected non-empty merge commit SHA")
	}

	// Verify the file exists in the target worktree after merge
	mergedFile := filepath.Join(targetInfo.Path, "test.txt")
	content, err := os.ReadFile(mergedFile)
	if err != nil {
		t.Fatalf("failed to read merged file: %v", err)
	}
	if string(content) != "hello from feature" {
		t.Errorf("unexpected file content after merge: %s", string(content))
	}
}

func TestMergeBranchConflict(t *testing.T) {
	bareRepo := initBareRepo(t)
	defaultBranch := getDefaultBranch(t, bareRepo)
	mgr := NewManager(bareRepo, defaultBranch)

	// Create source feature with a file
	sourceInfo, err := mgr.CreateFeature("conflict-source")
	if err != nil {
		t.Fatalf("CreateFeature (source) failed: %v", err)
	}

	RunGit([]string{"config", "user.email", "test@test.com"}, sourceInfo.Path)
	RunGit([]string{"config", "user.name", "Test"}, sourceInfo.Path)

	sourceFile := filepath.Join(sourceInfo.Path, "conflict.txt")
	if err := os.WriteFile(sourceFile, []byte("source content"), 0o644); err != nil {
		t.Fatalf("failed to write source file: %v", err)
	}
	RunGit([]string{"add", "conflict.txt"}, sourceInfo.Path)
	_, err = RunGit([]string{"commit", "-m", "source change"}, sourceInfo.Path)
	if err != nil {
		t.Fatalf("failed to commit source: %v", err)
	}

	// Create target feature with conflicting content in the same file
	targetInfo, err := mgr.CreateFeature("conflict-target")
	if err != nil {
		t.Fatalf("CreateFeature (target) failed: %v", err)
	}

	RunGit([]string{"config", "user.email", "test@test.com"}, targetInfo.Path)
	RunGit([]string{"config", "user.name", "Test"}, targetInfo.Path)

	targetFile := filepath.Join(targetInfo.Path, "conflict.txt")
	if err := os.WriteFile(targetFile, []byte("target content"), 0o644); err != nil {
		t.Fatalf("failed to write target file: %v", err)
	}
	RunGit([]string{"add", "conflict.txt"}, targetInfo.Path)
	_, err = RunGit([]string{"commit", "-m", "target change"}, targetInfo.Path)
	if err != nil {
		t.Fatalf("failed to commit target: %v", err)
	}

	// Attempt to merge source into target - should conflict
	result, err := mgr.MergeBranch("feature/conflict-source", targetInfo.Path)
	if err != nil {
		t.Fatalf("MergeBranch failed: %v", err)
	}

	if result.Success {
		t.Error("expected merge to fail due to conflicts")
	}

	if len(result.Conflicts) == 0 {
		t.Error("expected at least one conflict file")
	}

	// Verify conflict.txt is in the conflicts list
	foundConflict := false
	for _, f := range result.Conflicts {
		if f == "conflict.txt" {
			foundConflict = true
			break
		}
	}
	if !foundConflict {
		t.Errorf("expected conflict.txt in conflicts list, got %v", result.Conflicts)
	}

	// Verify the merge was aborted - the target should be clean
	clean, err := IsClean(targetInfo.Path)
	if err != nil {
		t.Fatalf("IsClean failed: %v", err)
	}
	if !clean {
		t.Error("expected target worktree to be clean after merge abort")
	}
}

func TestRemoveAgentWorktree(t *testing.T) {
	bareRepo := initBareRepo(t)
	defaultBranch := getDefaultBranch(t, bareRepo)
	mgr := NewManager(bareRepo, defaultBranch)

	// Create a feature
	_, err := mgr.CreateFeature("agent-rm-test")
	if err != nil {
		t.Fatalf("CreateFeature failed: %v", err)
	}

	// Create an agent worktree
	agentInfo, err := mgr.CreateAgentWorktree("agent-rm-test")
	if err != nil {
		t.Fatalf("CreateAgentWorktree failed: %v", err)
	}

	// Verify it exists
	if _, err := os.Stat(agentInfo.Path); os.IsNotExist(err) {
		t.Fatal("agent worktree directory was not created")
	}

	// Remove it
	err = mgr.RemoveAgentWorktree(agentInfo.Branch)
	if err != nil {
		t.Fatalf("RemoveAgentWorktree failed: %v", err)
	}

	// Verify the directory is gone
	if _, err := os.Stat(agentInfo.Path); !os.IsNotExist(err) {
		t.Error("agent worktree directory still exists after removal")
	}

	// Verify it no longer appears in agent list
	agents, err := mgr.ListAgentWorktrees("agent-rm-test")
	if err != nil {
		t.Fatalf("ListAgentWorktrees failed: %v", err)
	}

	for _, a := range agents {
		if a.Branch == agentInfo.Branch {
			t.Error("removed agent worktree still appears in agent list")
		}
	}
}

func TestGetBranchStatus(t *testing.T) {
	bareRepo := initBareRepo(t)
	defaultBranch := getDefaultBranch(t, bareRepo)
	mgr := NewManager(bareRepo, defaultBranch)

	// Create a feature worktree
	featureInfo, err := mgr.CreateFeature("status-test")
	if err != nil {
		t.Fatalf("CreateFeature failed: %v", err)
	}

	RunGit([]string{"config", "user.email", "test@test.com"}, featureInfo.Path)
	RunGit([]string{"config", "user.name", "Test"}, featureInfo.Path)

	// Make a commit
	testFile := filepath.Join(featureInfo.Path, "status.txt")
	if err := os.WriteFile(testFile, []byte("status test"), 0o644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}
	RunGit([]string{"add", "status.txt"}, featureInfo.Path)
	_, err = RunGit([]string{"commit", "-m", "add status file"}, featureInfo.Path)
	if err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	// Get branch status
	status, err := mgr.GetBranchStatus(featureInfo.Path)
	if err != nil {
		t.Fatalf("GetBranchStatus failed: %v", err)
	}

	if status.Branch != "feature/status-test" {
		t.Errorf("expected branch feature/status-test, got %s", status.Branch)
	}

	if status.Ahead < 1 {
		t.Errorf("expected at least 1 commit ahead, got %d", status.Ahead)
	}

	if status.LastCommitMessage != "add status file" {
		t.Errorf("expected last commit message 'add status file', got %q", status.LastCommitMessage)
	}

	if status.LastCommitSHA == "" {
		t.Error("expected non-empty last commit SHA")
	}
}

func TestGitHelpers(t *testing.T) {
	bareRepo := initBareRepo(t)
	defaultBranch := getDefaultBranch(t, bareRepo)
	mgr := NewManager(bareRepo, defaultBranch)

	// Create a feature worktree
	featureInfo, err := mgr.CreateFeature("helpers-test")
	if err != nil {
		t.Fatalf("CreateFeature failed: %v", err)
	}

	RunGit([]string{"config", "user.email", "test@test.com"}, featureInfo.Path)
	RunGit([]string{"config", "user.name", "Test"}, featureInfo.Path)

	// Initially should be clean
	clean, err := IsClean(featureInfo.Path)
	if err != nil {
		t.Fatalf("IsClean failed: %v", err)
	}
	if !clean {
		t.Error("expected new worktree to be clean")
	}

	// Create a file (makes it dirty)
	testFile := filepath.Join(featureInfo.Path, "helper.txt")
	if err := os.WriteFile(testFile, []byte("helper test"), 0o644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}

	// Now should be dirty
	clean, err = IsClean(featureInfo.Path)
	if err != nil {
		t.Fatalf("IsClean failed: %v", err)
	}
	if clean {
		t.Error("expected worktree to be dirty after adding a file")
	}

	// But .claude/ changes should be ignored
	claudeDir := filepath.Join(featureInfo.Path, ".claude")
	os.MkdirAll(claudeDir, 0o755)
	claudeFile := filepath.Join(claudeDir, "test-file")
	os.WriteFile(claudeFile, []byte("claude data"), 0o644)
	os.Remove(testFile) // remove the non-.claude file

	clean, err = IsClean(featureInfo.Path)
	if err != nil {
		t.Fatalf("IsClean failed: %v", err)
	}
	if !clean {
		t.Error("expected worktree to be clean when only .claude/ files are dirty")
	}

	// Commit something and test GetCommitLog
	os.WriteFile(testFile, []byte("helper test"), 0o644)
	RunGit([]string{"add", "helper.txt"}, featureInfo.Path)
	_, err = RunGit([]string{"commit", "-m", "test commit for log"}, featureInfo.Path)
	if err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	commits, err := GetCommitLog(featureInfo.Path, defaultBranch, 10)
	if err != nil {
		t.Fatalf("GetCommitLog failed: %v", err)
	}
	if len(commits) < 1 {
		t.Fatal("expected at least 1 commit in log")
	}
	if commits[0].Message != "test commit for log" {
		t.Errorf("expected commit message 'test commit for log', got %q", commits[0].Message)
	}

	// Test GetChangedFiles
	files, err := GetChangedFiles(featureInfo.Path, defaultBranch)
	if err != nil {
		t.Fatalf("GetChangedFiles failed: %v", err)
	}
	foundFile := false
	for _, f := range files {
		if f == "helper.txt" {
			foundFile = true
			break
		}
	}
	if !foundFile {
		t.Errorf("expected helper.txt in changed files, got %v", files)
	}

	// Test GetDefaultBranch
	detected, err := GetDefaultBranch(bareRepo)
	if err != nil {
		t.Fatalf("GetDefaultBranch failed: %v", err)
	}
	if detected != defaultBranch {
		t.Errorf("expected default branch %s, got %s", defaultBranch, detected)
	}
}

func TestRunGitError(t *testing.T) {
	// Running git in a non-existent directory should return an error
	_, err := RunGit([]string{"status"}, "/nonexistent-path-that-does-not-exist")
	if err == nil {
		t.Error("expected error when running git in non-existent directory")
	}

	// Running an invalid git command should return a GitError
	tmpDir := t.TempDir()
	_, err = RunGit([]string{"init", tmpDir}, "")
	if err != nil {
		t.Fatalf("failed to init temp repo: %v", err)
	}

	_, err = RunGit([]string{"log", "--invalid-flag-that-does-not-exist"}, tmpDir)
	if err == nil {
		t.Error("expected error for invalid git flag")
	}

	var gitErr *GitError
	if ok := isGitError(err, &gitErr); ok {
		if gitErr.ReturnCode == 0 {
			t.Error("expected non-zero return code in GitError")
		}
	}
}

// isGitError checks if err is a *GitError and sets target if so.
// This is a helper to avoid importing errors package in tests.
func isGitError(err error, target **GitError) bool {
	if ge, ok := err.(*GitError); ok {
		*target = ge
		return true
	}
	return false
}

func TestSyncAll(t *testing.T) {
	bareRepo := initBareRepo(t)
	defaultBranch := getDefaultBranch(t, bareRepo)
	mgr := NewManager(bareRepo, defaultBranch)

	// Create a feature worktree
	_, err := mgr.CreateFeature("sync-test")
	if err != nil {
		t.Fatalf("CreateFeature failed: %v", err)
	}

	// Sync all should succeed even if nothing has diverged
	results, err := mgr.SyncAll()
	if err != nil {
		t.Fatalf("SyncAll failed: %v", err)
	}

	if len(results) < 1 {
		t.Fatal("expected at least 1 sync result")
	}

	for _, r := range results {
		if !r.Success {
			t.Errorf("expected sync to succeed for %s, got error: %s", r.Branch, r.Error)
		}
	}
}
