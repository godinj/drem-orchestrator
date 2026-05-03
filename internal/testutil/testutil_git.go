package testutil

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Git helpers (use os/exec directly to avoid import cycles with worktree)
// ---------------------------------------------------------------------------

// runGit executes a git command in the given directory and returns stdout.
// Fatals the test on failure.
func runGit(t *testing.T, args []string, cwd string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %q failed: %v\n%s",
			strings.Join(args, " "), cwd, err, string(out))
	}
	return strings.TrimSpace(string(out))
}

// RunGit executes a git command in the given directory and returns stdout.
// Unlike the unexported runGit, this does not require a *testing.T and
// returns an error instead of fataling. This matches the signature pattern
// used by callers that need error handling.
func RunGit(args []string, cwd string) (string, error) {
	cmd := exec.Command("git", args...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s in %q failed: %w\n%s",
			strings.Join(args, " "), cwd, err, string(out))
	}
	return strings.TrimSpace(string(out)), nil
}

// ---------------------------------------------------------------------------
// Git repo helpers
// ---------------------------------------------------------------------------

// InitBareRepo creates an empty bare git repo in a temp dir and returns its
// path. Use richer helpers below when tests need seeded commits or worktrees.
func InitBareRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bareRepo := filepath.Join(dir, "test.git")

	runGit(t, []string{"init", "--bare", bareRepo}, "")

	return bareRepo
}

// SetupBareRepo creates a bare git repo with an initial commit in a temp dir.
// Returns the bare repo path. The temp dir is cleaned up automatically by
// t.TempDir().
func SetupBareRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bareRepo := filepath.Join(dir, "test.git")

	runGit(t, []string{"init", "--bare", bareRepo}, "")

	// Clone, make initial commit, push
	cloneDir := filepath.Join(dir, "clone")
	runGit(t, []string{"clone", bareRepo, cloneDir}, "")
	runGit(t, []string{"config", "user.email", "test@test.com"}, cloneDir)
	runGit(t, []string{"config", "user.name", "Test"}, cloneDir)

	initFile := filepath.Join(cloneDir, "README.md")
	if err := os.WriteFile(initFile, []byte("# Test\n"), 0o644); err != nil {
		t.Fatalf("write init file: %v", err)
	}
	runGit(t, []string{"add", "."}, cloneDir)
	runGit(t, []string{"commit", "-m", "initial commit"}, cloneDir)
	runGit(t, []string{"push", "origin", "HEAD"}, cloneDir)

	return bareRepo
}

// InitBareRepoWithMainWorktree creates a bare git repo with an initial empty
// commit and a main worktree checked out. Returns the path to the bare repo.
// This variant is used by worktree manager tests that need a full worktree
// layout.
func InitBareRepoWithMainWorktree(t *testing.T) string {
	t.Helper()

	tmpDir := t.TempDir()
	bareRepo := filepath.Join(tmpDir, "test.git")

	runGit(t, []string{"init", "--bare", bareRepo}, "")

	// We need a commit to base worktrees on. Create a temporary clone,
	// make an empty commit, and push back to the bare repo.
	cloneDir := filepath.Join(tmpDir, "clone-init")
	runGit(t, []string{"clone", bareRepo, cloneDir}, "")
	runGit(t, []string{"config", "user.email", "test@test.com"}, cloneDir)
	runGit(t, []string{"config", "user.name", "Test"}, cloneDir)
	runGit(t, []string{"commit", "--allow-empty", "-m", "init"}, cloneDir)
	runGit(t, []string{"push", "origin", "HEAD"}, cloneDir)

	// Detect the default branch that was created
	defaultBranch := runGit(t, []string{"rev-parse", "--abbrev-ref", "HEAD"}, cloneDir)

	// Create a main worktree from the bare repo so worktrees can reference it.
	mainWorktree := filepath.Join(bareRepo, defaultBranch)
	runGit(t, []string{"worktree", "add", mainWorktree, defaultBranch}, bareRepo)

	// Clean up the temporary clone
	os.RemoveAll(cloneDir)

	return bareRepo
}

// GetDefaultBranch returns the default branch name for the bare repo.
func GetDefaultBranch(t *testing.T, bareRepo string) string {
	t.Helper()
	return runGit(t, []string{"symbolic-ref", "--short", "HEAD"}, bareRepo)
}

// AddWorktree creates a worktree from the bare repo with a new branch.
// Returns the worktree path.
func AddWorktree(t *testing.T, bareRepo, branch, dir string) string {
	t.Helper()
	runGit(t, []string{"worktree", "add", "-b", branch, dir}, bareRepo)
	// Configure git user in the worktree
	runGit(t, []string{"config", "user.email", "test@test.com"}, dir)
	runGit(t, []string{"config", "user.name", "Test"}, dir)
	return dir
}

// CommitFile creates or overwrites a file and commits it in the given worktree.
func CommitFile(t *testing.T, wt, filename, content, message string) {
	t.Helper()
	fpath := filepath.Join(wt, filename)
	if err := os.WriteFile(fpath, []byte(content), 0o644); err != nil {
		t.Fatalf("write file %s: %v", filename, err)
	}
	runGit(t, []string{"add", filename}, wt)
	runGit(t, []string{"commit", "-m", message}, wt)
}
