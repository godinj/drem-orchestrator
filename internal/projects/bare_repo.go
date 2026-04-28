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

// ConfigureBareRepo sets receive.denyCurrentBranch=ignore and
// receive.denyDeleteCurrent=ignore on the bare git repository at barePath.
// The former lets the worker watchdog's final `git push origin
// <feature-branch>` succeed even though our "bare" repo has host worktrees
// checked out under it. The latter lets merger delete a merged feature branch
// even when that branch is still the current branch for a stale host worktree.
//
// Why ignore and not updateInstead: the worker pushes from inside a
// container with the bare repo bind-mounted at /bare. `updateInstead`
// tries to `cd` into the worktree's working directory, whose absolute
// path was recorded at `git worktree add` time as a host-absolute
// path. That host path is not visible inside the container, so
// updateInstead fails with "fatal: exec 'update-index': cd to
// '<host-path>' failed: No such file or directory" and the push is
// rejected. `ignore` accepts the push without touching the worktree.
// The host worktree goes stale, which is safe because merger
// clones the integration branch fresh into a disposable workspace
// on every run (see internal/merger/merger.go: resetWorkDir +
// cloneBranch) — it reads the bare repo's refs directly, never the
// host worktree's working tree.
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

	for key, value := range map[string]string{
		"receive.denyCurrentBranch": "ignore",
		"receive.denyDeleteCurrent": "ignore",
	} {
		cmd := exec.CommandContext(ctx, "git",
			"--git-dir="+barePath,
			"config", key, value)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("projects: ConfigureBareRepo: git config %s on %q failed: %w (%s)",
				key, barePath, err, out)
		}
	}
	return nil
}
