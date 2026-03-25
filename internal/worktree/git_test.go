package worktree

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/godinj/drem-orchestrator/internal/testutil"
)

func TestCleanClaudeArtifacts_RemovesFiles(t *testing.T) {
	bareRepo := testutil.SetupBareRepo(t)
	dir := filepath.Dir(bareRepo)

	// Create a worktree
	wtDir := filepath.Join(dir, "wt")
	testutil.AddWorktree(t, bareRepo, "test-clean-branch", wtDir)

	// Add .claude/ artifacts (untracked files)
	claudeDir := filepath.Join(wtDir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, "projects.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	cleaned, err := CleanClaudeArtifacts(wtDir)
	if err != nil {
		t.Fatalf("CleanClaudeArtifacts returned error: %v", err)
	}
	if !cleaned {
		t.Error("expected cleaned=true when .claude/ files exist")
	}

	// Verify .claude/ directory is gone
	if _, err := os.Stat(claudeDir); !os.IsNotExist(err) {
		t.Error("expected .claude/ directory to be removed")
	}
}

func TestCleanClaudeArtifacts_NoOpWhenClean(t *testing.T) {
	bareRepo := testutil.SetupBareRepo(t)
	dir := filepath.Dir(bareRepo)

	wtDir := filepath.Join(dir, "wt")
	testutil.AddWorktree(t, bareRepo, "test-clean-noop", wtDir)

	// No .claude/ directory — should be a no-op
	cleaned, err := CleanClaudeArtifacts(wtDir)
	if err != nil {
		t.Fatalf("CleanClaudeArtifacts returned error: %v", err)
	}
	if cleaned {
		t.Error("expected cleaned=false when no .claude/ files")
	}
}

func TestUntrackEphemeralFiles_TrackedPlanJson(t *testing.T) {
	bareRepo := testutil.SetupBareRepo(t)
	dir := filepath.Dir(bareRepo)

	wtDir := filepath.Join(dir, "wt")
	testutil.AddWorktree(t, bareRepo, "test-untrack-tracked", wtDir)

	// Commit plan.json so it's tracked.
	testutil.CommitFile(t, wtDir, "plan.json", `{"subtasks":[]}`, "add plan")

	removed, err := UntrackEphemeralFiles(wtDir)
	if err != nil {
		t.Fatalf("UntrackEphemeralFiles: %v", err)
	}
	if !removed {
		t.Error("expected removed=true when plan.json is tracked")
	}

	// plan.json should still exist on disk.
	if _, err := os.Stat(filepath.Join(wtDir, "plan.json")); err != nil {
		t.Error("expected plan.json to remain on disk after untracking")
	}

	// plan.json should no longer be tracked.
	out, _ := RunGit([]string{"ls-files", "plan.json"}, wtDir)
	if out != "" {
		t.Error("expected plan.json to be untracked after UntrackEphemeralFiles")
	}

	// The removal should be committed (no staged changes).
	if _, err := RunGit([]string{"diff", "--cached", "--quiet"}, wtDir); err != nil {
		t.Error("expected no staged changes after UntrackEphemeralFiles commit")
	}
}

func TestUntrackEphemeralFiles_UntrackedPlanJson(t *testing.T) {
	bareRepo := testutil.SetupBareRepo(t)
	dir := filepath.Dir(bareRepo)

	wtDir := filepath.Join(dir, "wt")
	testutil.AddWorktree(t, bareRepo, "test-untrack-untracked", wtDir)

	// Write plan.json but don't commit it (untracked).
	if err := os.WriteFile(filepath.Join(wtDir, "plan.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}

	removed, err := UntrackEphemeralFiles(wtDir)
	if err != nil {
		t.Fatalf("UntrackEphemeralFiles: %v", err)
	}
	if removed {
		t.Error("expected removed=false when plan.json is not tracked")
	}

	// File should still exist on disk.
	if _, err := os.Stat(filepath.Join(wtDir, "plan.json")); err != nil {
		t.Error("expected plan.json to remain on disk")
	}
}

func TestUntrackEphemeralFiles_NoPlanJson(t *testing.T) {
	bareRepo := testutil.SetupBareRepo(t)
	dir := filepath.Dir(bareRepo)

	wtDir := filepath.Join(dir, "wt")
	testutil.AddWorktree(t, bareRepo, "test-untrack-none", wtDir)

	removed, err := UntrackEphemeralFiles(wtDir)
	if err != nil {
		t.Fatalf("UntrackEphemeralFiles: %v", err)
	}
	if removed {
		t.Error("expected removed=false when no plan.json exists")
	}
}

func TestCommitUnstagedChanges_ExcludesTrackedClaudeFiles(t *testing.T) {
	bareRepo := testutil.SetupBareRepo(t)
	dir := filepath.Dir(bareRepo)

	wtDir := filepath.Join(dir, "wt")
	testutil.AddWorktree(t, bareRepo, "test-commit-exclude-claude", wtDir)

	// Commit .claude/settings.json so it becomes tracked.
	claudeDir := filepath.Join(wtDir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	testutil.CommitFile(t, wtDir, ".claude/settings.json", `{"old":"value"}`, "add claude settings")

	// Also commit a regular file.
	testutil.CommitFile(t, wtDir, "main.go", "package main\n", "add main.go")

	// Modify both files.
	if err := os.WriteFile(filepath.Join(wtDir, ".claude/settings.json"), []byte(`{"new":"value"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wtDir, "main.go"), []byte("package main\n// updated\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	committed, err := CommitUnstagedChanges(wtDir, "auto-commit test")
	if err != nil {
		t.Fatalf("CommitUnstagedChanges: %v", err)
	}
	if !committed {
		t.Fatal("expected committed=true when main.go changed")
	}

	// Verify the commit does NOT include .claude/settings.json.
	out, err := RunGit([]string{"diff-tree", "--no-commit-id", "--name-only", "-r", "HEAD"}, wtDir)
	if err != nil {
		t.Fatalf("diff-tree: %v", err)
	}
	changedFiles := strings.Split(strings.TrimSpace(out), "\n")
	for _, f := range changedFiles {
		if f == ".claude/settings.json" {
			t.Error("auto-commit should NOT include .claude/settings.json")
		}
	}
	// main.go should be in the commit.
	found := false
	for _, f := range changedFiles {
		if f == "main.go" {
			found = true
		}
	}
	if !found {
		t.Error("expected main.go to be in the auto-commit")
	}
}

func TestCommitUnstagedChanges_OnlyClaudeChange_ReturnsFalse(t *testing.T) {
	bareRepo := testutil.SetupBareRepo(t)
	dir := filepath.Dir(bareRepo)

	wtDir := filepath.Join(dir, "wt")
	testutil.AddWorktree(t, bareRepo, "test-commit-claude-only", wtDir)

	// Commit .claude/settings.json so it becomes tracked.
	claudeDir := filepath.Join(wtDir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	testutil.CommitFile(t, wtDir, ".claude/settings.json", `{"old":"value"}`, "add claude settings")

	// Modify only .claude/settings.json.
	if err := os.WriteFile(filepath.Join(wtDir, ".claude/settings.json"), []byte(`{"new":"value"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	committed, err := CommitUnstagedChanges(wtDir, "auto-commit test")
	if err != nil {
		t.Fatalf("CommitUnstagedChanges: %v", err)
	}
	if committed {
		t.Error("expected committed=false when only .claude/settings.json changed")
	}
}

func TestUntrackEphemeralFiles_TrackedClaudeSettings(t *testing.T) {
	bareRepo := testutil.SetupBareRepo(t)
	dir := filepath.Dir(bareRepo)

	wtDir := filepath.Join(dir, "wt")
	testutil.AddWorktree(t, bareRepo, "test-untrack-claude", wtDir)

	// Commit .claude/settings.json so it's tracked.
	claudeDir := filepath.Join(wtDir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	testutil.CommitFile(t, wtDir, ".claude/settings.json", `{"settings":true}`, "add claude settings")

	removed, err := UntrackEphemeralFiles(wtDir)
	if err != nil {
		t.Fatalf("UntrackEphemeralFiles: %v", err)
	}
	if !removed {
		t.Error("expected removed=true when .claude/settings.json is tracked")
	}

	// File should still exist on disk.
	if _, err := os.Stat(filepath.Join(wtDir, ".claude/settings.json")); err != nil {
		t.Error("expected .claude/settings.json to remain on disk after untracking")
	}

	// File should no longer be tracked.
	out, _ := RunGit([]string{"ls-files", ".claude/settings.json"}, wtDir)
	if out != "" {
		t.Error("expected .claude/settings.json to be untracked after UntrackEphemeralFiles")
	}
}

func TestUntrackEphemeralFiles_UntrackedClaudeSettings(t *testing.T) {
	bareRepo := testutil.SetupBareRepo(t)
	dir := filepath.Dir(bareRepo)

	wtDir := filepath.Join(dir, "wt")
	testutil.AddWorktree(t, bareRepo, "test-untrack-claude-noop", wtDir)

	// Write .claude/settings.json but don't commit it (untracked).
	claudeDir := filepath.Join(wtDir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wtDir, ".claude/settings.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}

	removed, err := UntrackEphemeralFiles(wtDir)
	if err != nil {
		t.Fatalf("UntrackEphemeralFiles: %v", err)
	}
	if removed {
		t.Error("expected removed=false when .claude/settings.json is not tracked")
	}
}

func TestUntrackEphemeralFiles_BothPlanAndClaudeTracked(t *testing.T) {
	bareRepo := testutil.SetupBareRepo(t)
	dir := filepath.Dir(bareRepo)

	wtDir := filepath.Join(dir, "wt")
	testutil.AddWorktree(t, bareRepo, "test-untrack-both", wtDir)

	// Commit both plan.json and .claude/settings.json.
	testutil.CommitFile(t, wtDir, "plan.json", `{"subtasks":[]}`, "add plan")
	claudeDir := filepath.Join(wtDir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	testutil.CommitFile(t, wtDir, ".claude/settings.json", `{"settings":true}`, "add claude settings")

	removed, err := UntrackEphemeralFiles(wtDir)
	if err != nil {
		t.Fatalf("UntrackEphemeralFiles: %v", err)
	}
	if !removed {
		t.Error("expected removed=true when both files are tracked")
	}

	// Both files should remain on disk.
	if _, err := os.Stat(filepath.Join(wtDir, "plan.json")); err != nil {
		t.Error("expected plan.json to remain on disk")
	}
	if _, err := os.Stat(filepath.Join(wtDir, ".claude/settings.json")); err != nil {
		t.Error("expected .claude/settings.json to remain on disk")
	}

	// Neither should be tracked.
	out, _ := RunGit([]string{"ls-files", "plan.json"}, wtDir)
	if out != "" {
		t.Error("expected plan.json to be untracked")
	}
	out, _ = RunGit([]string{"ls-files", ".claude/settings.json"}, wtDir)
	if out != "" {
		t.Error("expected .claude/settings.json to be untracked")
	}

	// Should be a single commit (not two separate ones).
	if _, err := RunGit([]string{"diff", "--cached", "--quiet"}, wtDir); err != nil {
		t.Error("expected no staged changes after UntrackEphemeralFiles commit")
	}
}

func TestRebaseBranch_WithClaudeArtifacts(t *testing.T) {
	bareRepo := testutil.SetupBareRepo(t)
	dir := filepath.Dir(bareRepo)

	// Create feature worktree (target)
	featureDir := filepath.Join(dir, "feature")
	testutil.AddWorktree(t, bareRepo, "feature/test-rebase", featureDir)

	// Create agent worktree (source)
	agentDir := filepath.Join(dir, "agent")
	testutil.AddWorktree(t, bareRepo, "worktree-agent-rebase", agentDir)

	// Add a commit to feature (target)
	testutil.CommitFile(t, featureDir, "feature-file.txt", "feature work\n", "feature commit")

	// Add a commit to agent (source)
	testutil.CommitFile(t, agentDir, "agent-file.txt", "agent work\n", "agent commit")

	// Add .claude/ artifacts to the agent worktree (source)
	claudeDir := filepath.Join(agentDir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Rebase should succeed because CleanClaudeArtifacts is called first
	result, err := RebaseBranch(agentDir, featureDir)
	if err != nil {
		t.Fatalf("RebaseBranch returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected rebase to succeed after auto-clean, got conflicts=%v stderr=%s",
			result.Conflicts, result.GitStderr)
	}

	// .claude/ should be cleaned up
	if _, err := os.Stat(claudeDir); !os.IsNotExist(err) {
		t.Error("expected .claude/ to be cleaned before rebase")
	}
}
