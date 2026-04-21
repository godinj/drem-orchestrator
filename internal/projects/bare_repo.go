package projects

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// configureBareRepoTimeout bounds the git invocation so a stuck git
// process cannot wedge `drem project register`. The operation is a
// single config write, so the ceiling is small on purpose.
const configureBareRepoTimeout = 10 * time.Second

// ConfigureBareRepo sets receive.denyCurrentBranch=updateInstead on
// the bare git repository at barePath. This lets the worker
// watchdog's final `git push origin <feature-branch>` succeed even
// though our "bare" repo has host worktrees checked out under it —
// when a push targets a checked-out branch, git updates the worktree
// instead of rejecting (and rejects safely when the worktree is
// dirty).
//
// The function is idempotent: `git config <key> <value>` overwrites
// with the same value (no-op) or sets a differing value. Calling it
// repeatedly across fresh registrations and `--update` refreshes is
// safe.
//
// Returns an error when git is missing, barePath does not exist, or
// barePath is not a git repository (no HEAD + objects/ layout).
func ConfigureBareRepo(barePath string) error {
	if barePath == "" {
		return errors.New("projects: ConfigureBareRepo: barePath is empty")
	}

	info, err := os.Stat(barePath)
	if err != nil {
		return fmt.Errorf("projects: ConfigureBareRepo: stat %q: %w", barePath, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("projects: ConfigureBareRepo: %q is not a directory", barePath)
	}

	// Minimum bare-repo shape check: HEAD file + objects/ dir. Matches
	// the predicate used by validateProject in registry.go, duplicated
	// here so the helper is usable without going through Registry.Add.
	if _, err := os.Stat(filepath.Join(barePath, "HEAD")); err != nil {
		return fmt.Errorf("projects: ConfigureBareRepo: %q has no HEAD (not a git repo): %w", barePath, err)
	}
	objInfo, err := os.Stat(filepath.Join(barePath, "objects"))
	if err != nil || !objInfo.IsDir() {
		return fmt.Errorf("projects: ConfigureBareRepo: %q has no objects/ directory (not a git repo)", barePath)
	}

	ctx, cancel := context.WithTimeout(context.Background(), configureBareRepoTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git",
		"--git-dir="+barePath,
		"config", "receive.denyCurrentBranch", "updateInstead")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("projects: ConfigureBareRepo: git config on %q failed: %w (%s)",
			barePath, err, out)
	}
	return nil
}
