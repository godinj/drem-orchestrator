// Package watchdog implements the crash-recovery loop baked into worker
// container images.
//
// The watchdog is deliberately small and independent: it runs inside the
// container against the container-local working copy, periodically commits
// any in-progress changes, and pushes them to the bare repository so that a
// replacement container can resume from the most recent checkpoint.
//
// The package has no dependencies on other internal/ packages except for
// the heartbeat constant from internal/extract once that is exported
// (see TODO in loop.go). All git operations go through os/exec directly so
// that the binary shipped into a minimal worker image does not pull in
// unrelated orchestrator code.
package watchdog

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// gitError carries structured context when a git command fails. It is only
// used internally — callers receive a wrapped error with enough detail to
// log the failed command and git's stderr.
type gitError struct {
	Args       []string
	ReturnCode int
	Stderr     string
}

func (e *gitError) Error() string {
	return fmt.Sprintf("git %s: exit %d: %s",
		strings.Join(e.Args, " "), e.ReturnCode, e.Stderr)
}

// runGit executes a git command in the given directory and returns stdout.
// A non-zero exit is reported as a *gitError so that callers can inspect
// the exit code and stderr without re-parsing the error string.
func runGit(ctx context.Context, repo string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repo
	stdout, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", &gitError{
				Args:       args,
				ReturnCode: exitErr.ExitCode(),
				Stderr:     strings.TrimSpace(string(exitErr.Stderr)),
			}
		}
		return "", fmt.Errorf("run git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(stdout)), nil
}

// isDirty reports whether the working tree has uncommitted changes that the
// watchdog should capture. It uses `git status --porcelain` and treats any
// non-empty output as dirty.
//
// Unlike internal/worktree.IsClean, this helper does not special-case
// .claude/ — the watchdog is running inside an ephemeral container image
// that does not carry worktree-local agent settings, so every dirty file
// is a legitimate candidate for commit-and-push.
func isDirty(ctx context.Context, repo string) (bool, error) {
	out, err := runGit(ctx, repo, "status", "--porcelain")
	if err != nil {
		return false, fmt.Errorf("watchdog: check dirty: %w", err)
	}
	return out != "", nil
}

// commitAll stages every modified, deleted, and untracked file in the
// working tree and creates a single commit with the supplied message.
// Returns true if a commit was created, false when there was nothing to
// commit after staging (for example, when only ignored files changed).
func commitAll(ctx context.Context, repo, message string) (bool, error) {
	if _, err := runGit(ctx, repo, "add", "-A", "--", "."); err != nil {
		return false, fmt.Errorf("watchdog: add: %w", err)
	}
	// `diff --cached --quiet` exits 0 when there are no staged differences.
	// We treat that as a no-op rather than an error.
	if _, err := runGit(ctx, repo, "diff", "--cached", "--quiet"); err == nil {
		return false, nil
	}
	if _, err := runGit(ctx, repo, "commit", "-m", message); err != nil {
		return false, fmt.Errorf("watchdog: commit: %w", err)
	}
	return true, nil
}

// pushBranch pushes the named branch to the given remote. The watchdog
// performs a single retry on failure so that a transient bare-repo outage
// (for example, a concurrent push) does not require waiting for the next
// tick.
func pushBranch(ctx context.Context, repo, remote, branch string) error {
	ref := branch + ":" + branch
	if _, err := runGit(ctx, repo, "push", remote, ref); err == nil {
		return nil
	}
	// Single retry — deliberately not a retry loop. If the bare repo is
	// unreachable, the next tick will try again.
	if _, err := runGit(ctx, repo, "push", remote, ref); err != nil {
		return fmt.Errorf("watchdog: push %s %s: %w", remote, branch, err)
	}
	return nil
}
