// Package testutil provides shared test helpers for the Drem Orchestrator
// test suite. All test setup helpers (DB init, git repo scaffolding, entity
// factories) live here to eliminate duplication across packages.
package testutil

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/supervisor"
)

// ---------------------------------------------------------------------------
// Database helpers
// ---------------------------------------------------------------------------

// NewTestDB creates an in-memory SQLite database with cache=shared mode and
// runs auto-migration for all models. Each call uses a unique name so
// concurrent tests get isolated databases.
func NewTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	name := uuid.New().String()
	dsn := "file:" + name + "?mode=memory&cache=shared"
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

// NewSharedTestDB creates an in-memory SQLite database with cache=shared.
// Use this for tests that need a single shared in-memory DB.
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

// ---------------------------------------------------------------------------
// Entity factory helpers
// ---------------------------------------------------------------------------

// CreateProject creates a test project in the database and returns it.
func CreateProject(t *testing.T, db *gorm.DB, name, bareRepoPath, defaultBranch string) model.Project {
	t.Helper()
	p := model.Project{
		ID:            uuid.New(),
		Name:          name,
		BareRepoPath:  bareRepoPath,
		DefaultBranch: defaultBranch,
	}
	if err := db.Create(&p).Error; err != nil {
		t.Fatalf("create test project: %v", err)
	}
	return p
}

// CreateTask creates a test task in the database and returns it.
func CreateTask(t *testing.T, db *gorm.DB, projectID uuid.UUID, title string, status model.TaskStatus) model.Task {
	t.Helper()
	task := model.Task{
		ID:          uuid.New(),
		ProjectID:   projectID,
		Title:       title,
		Description: title,
		Status:      status,
		Category:    model.CategoryStandard,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("create test task: %v", err)
	}
	return task
}

// CreateQuickFixTask creates a test task with CategoryQuickFix in the database
// and returns it.
func CreateQuickFixTask(t *testing.T, db *gorm.DB, projectID uuid.UUID, title string, status model.TaskStatus) *model.Task {
	t.Helper()
	task := &model.Task{
		ID:          uuid.New(),
		ProjectID:   projectID,
		Title:       title,
		Description: title,
		Status:      status,
		Category:    model.CategoryQuickFix,
	}
	if err := db.Create(task).Error; err != nil {
		t.Fatalf("create quick fix task: %v", err)
	}
	return task
}

// CreateAgent creates a test agent in the database and returns it.
func CreateAgent(t *testing.T, db *gorm.DB, taskID uuid.UUID, agentType model.AgentType, status model.AgentStatus) model.Agent {
	t.Helper()
	ag := model.Agent{
		ID:        uuid.New(),
		AgentType: agentType,
		Status:    status,
	}
	if taskID != uuid.Nil {
		ag.CurrentTaskID = &taskID
	}
	if err := db.Create(&ag).Error; err != nil {
		t.Fatalf("create test agent: %v", err)
	}
	return ag
}

// ---------------------------------------------------------------------------
// Supervisor mock helpers
// ---------------------------------------------------------------------------

// NewMockSupervisor creates a supervisor backed by a fake claude binary that
// echoes the given response string. Useful for testing supervisor-dependent
// code paths without calling the real LLM.
func NewMockSupervisor(t *testing.T, response string) *supervisor.Supervisor {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-claude")
	// Use printf to avoid echo adding a trailing newline interpretation issue.
	script := fmt.Sprintf("#!/bin/sh\ncat <<'FAKE_EOF'\n%s\nFAKE_EOF\n", response)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake claude bin: %v", err)
	}
	return supervisor.New(bin, 5*time.Second)
}
