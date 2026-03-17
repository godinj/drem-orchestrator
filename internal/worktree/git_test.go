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
