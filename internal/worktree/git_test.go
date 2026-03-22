package worktree

import (
	"os"
	"path/filepath"
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
