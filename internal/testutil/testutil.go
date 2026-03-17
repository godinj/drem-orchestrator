// Package testutil provides shared test helpers for database and git operations.
package testutil

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/godinj/drem-orchestrator/internal/model"
)

// NewTestDB creates a UUID-isolated in-memory SQLite database with auto-migration.
// Each call returns a fully independent database instance.
func NewTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + uuid.New().String() + "?mode=memory&cache=private"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(
		&model.Project{},
		&model.Task{},
		&model.Agent{},
		&model.TaskEvent{},
		&model.Memory{},
		&model.TaskComment{},
	); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}
	return db
}

// NewSharedTestDB creates a shared in-memory SQLite database.
// Multiple connections can access the same data (needed for multi-goroutine tests).
func NewSharedTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(
		&model.Project{},
		&model.Task{},
		&model.Agent{},
		&model.TaskEvent{},
		&model.Memory{},
		&model.TaskComment{},
	); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}
	return db
}

// runGit executes a git command in the given directory and returns trimmed stdout.
// This is a private helper to avoid importing the worktree package (which would
// create an import cycle when worktree tests use this package).
func runGit(args []string, cwd string) (string, error) {
	cmd := exec.Command("git", args...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	out, err := cmd.Output()
	if err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if ok {
			return "", fmt.Errorf("git %s failed (exit %d): %s",
				strings.Join(args, " "), exitErr.ExitCode(),
				strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}

// SetupBareRepo creates a bare git repo with an initial commit in a temp dir.
// Returns the bare repo path.
func SetupBareRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bareRepo := filepath.Join(dir, "test.git")

	// Init bare repo
	if _, err := runGit([]string{"init", "--bare", bareRepo}, ""); err != nil {
		t.Fatalf("init bare repo: %v", err)
	}

	// Create a temporary clone to make an initial commit
	cloneDir := filepath.Join(dir, "clone")
	if _, err := runGit([]string{"clone", bareRepo, cloneDir}, ""); err != nil {
		t.Fatalf("clone bare repo: %v", err)
	}

	// Configure git user for commits
	runGit([]string{"config", "user.email", "test@test.com"}, cloneDir)
	runGit([]string{"config", "user.name", "Test"}, cloneDir)

	// Create initial commit
	initFile := filepath.Join(cloneDir, "README.md")
	if err := os.WriteFile(initFile, []byte("# Test\n"), 0o644); err != nil {
		t.Fatalf("write init file: %v", err)
	}
	runGit([]string{"add", "."}, cloneDir)
	if _, err := runGit([]string{"commit", "-m", "initial commit"}, cloneDir); err != nil {
		t.Fatalf("initial commit: %v", err)
	}

	// Push to bare repo
	if _, err := runGit([]string{"push", "origin", "HEAD"}, cloneDir); err != nil {
		t.Fatalf("push initial commit: %v", err)
	}

	return bareRepo
}

// AddWorktree creates a worktree from the bare repo with a new branch.
// Returns the worktree path.
func AddWorktree(t *testing.T, bareRepo, branch, dir string) string {
	t.Helper()
	if _, err := runGit([]string{"worktree", "add", "-b", branch, dir}, bareRepo); err != nil {
		t.Fatalf("add worktree %s: %v", branch, err)
	}
	// Configure git user in the worktree
	runGit([]string{"config", "user.email", "test@test.com"}, dir)
	runGit([]string{"config", "user.name", "Test"}, dir)
	return dir
}

// CommitFile creates or overwrites a file and commits it in the given worktree.
func CommitFile(t *testing.T, wt, filename, content, message string) {
	t.Helper()
	fpath := filepath.Join(wt, filename)
	if err := os.WriteFile(fpath, []byte(content), 0o644); err != nil {
		t.Fatalf("write file %s: %v", filename, err)
	}
	if _, err := runGit([]string{"add", filename}, wt); err != nil {
		t.Fatalf("git add %s: %v", filename, err)
	}
	if _, err := runGit([]string{"commit", "-m", message}, wt); err != nil {
		t.Fatalf("commit %s: %v", message, err)
	}
}

// RunGitCmd runs a git command in the given directory and returns stdout.
// Fails the test on error.
func RunGitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := runGit(args, dir)
	if err != nil {
		t.Fatalf("git %s in %s: %v", strings.Join(args, " "), dir, err)
	}
	return out
}
