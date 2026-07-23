// Package gitexec runs git against an arbitrary working directory.
//
// It has no knowledge of Drem's worktree layout or branch conventions;
// that lives in internal/worktreehost. Use this package for leaf-level git
// operations that only need a directory and a handful of command args.
//
// The functions here match the signatures of the host worktree package's
// package-level helpers (RunGit, GetChangedFiles, IsClean, etc.) with a
// context.Context added as the first argument. Callers that do not yet
// plumb a context can pass context.Background().
package gitexec

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// GitError represents a git command failure.
type GitError struct {
	Command    string
	ReturnCode int
	Stderr     string
	Stdout     string // stdout captured on failure (may contain conflict info)
}

// Error returns a human-readable description of the git failure.
func (e *GitError) Error() string {
	return fmt.Sprintf("git command failed (exit %d): %s\n%s", e.ReturnCode, e.Command, e.Stderr)
}

// Commit holds parsed git log data for a single commit.
type Commit struct {
	SHA       string
	Author    string
	Committed time.Time
	Subject   string
	Body      string
}

// RunGit executes a git command in the given directory and returns stdout
// (trimmed). Returns GitError on non-zero exit, with both stderr and stdout
// captured for diagnostics.
func RunGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}

	stdout, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", &GitError{
				Command:    fmt.Sprintf("git %s", strings.Join(args, " ")),
				ReturnCode: exitErr.ExitCode(),
				Stderr:     strings.TrimSpace(string(exitErr.Stderr)),
				Stdout:     strings.TrimSpace(string(stdout)),
			}
		}
		return "", fmt.Errorf("failed to run git %s: %w", strings.Join(args, " "), err)
	}

	return strings.TrimSpace(string(stdout)), nil
}

// GetChangedFiles returns files changed between baseRef and HEAD in dir.
// Uses: git diff --name-only <baseRef>..HEAD
func GetChangedFiles(ctx context.Context, dir, baseRef string) ([]string, error) {
	output, err := RunGit(ctx, dir, "diff", "--name-only", fmt.Sprintf("%s..HEAD", baseRef))
	if err != nil {
		return nil, fmt.Errorf("get changed files: %w", err)
	}
	if output == "" {
		return nil, nil
	}
	return strings.Split(output, "\n"), nil
}

// IsClean returns true if the working directory has no uncommitted changes.
// Ignores .claude/ directory entries in the output so agent-local settings
// do not mark a worktree as dirty.
func IsClean(ctx context.Context, dir string) (bool, error) {
	output, err := RunGit(ctx, dir, "status", "--porcelain")
	if err != nil {
		return false, fmt.Errorf("check clean status: %w", err)
	}
	if output == "" {
		return true, nil
	}
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// Porcelain format: "XY path". Skip .claude/ entries.
		if len(line) > 3 {
			path := line[3:]
			if strings.HasPrefix(path, ".claude/") {
				continue
			}
		}
		return false, nil
	}
	return true, nil
}

// WorktreeHeadDiffersFromBranchTip reports whether dir's checked-out HEAD is
// stale relative to the named branch ref visible from that worktree.
func WorktreeHeadDiffersFromBranchTip(ctx context.Context, dir, branch string) (bool, error) {
	head, err := RunGit(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return false, fmt.Errorf("worktree freshness: resolve HEAD: %w", err)
	}
	tip, err := RunGit(ctx, dir, "rev-parse", branch)
	if err != nil {
		return false, fmt.Errorf("worktree freshness: resolve branch %s: %w", branch, err)
	}
	return strings.TrimSpace(head) != strings.TrimSpace(tip), nil
}

// CommitUnstagedChanges stages all tracked non-.claude/ changes and commits
// them. Returns true if a commit was created, false if there was nothing to
// commit.
func CommitUnstagedChanges(ctx context.Context, dir, message string) (bool, error) {
	clean, err := IsClean(ctx, dir)
	if err != nil {
		return false, fmt.Errorf("commit unstaged: check clean: %w", err)
	}
	if clean {
		return false, nil
	}

	// Stage only tracked (modified/deleted) files. Using -u instead of --all
	// avoids staging untracked files such as build artifacts, editor configs,
	// and other files not yet in the index, which would cause merge conflicts
	// when multiple agents commit overlapping untracked files.
	if _, err := RunGit(ctx, dir, "add", "-u", "--", "."); err != nil {
		return false, fmt.Errorf("commit unstaged: add: %w", err)
	}

	// Unstage any .claude/ files that were tracked and staged above.
	_, _ = RunGit(ctx, dir, "reset", "HEAD", "--", ".claude/")

	// Check if anything was actually staged.
	if _, err := RunGit(ctx, dir, "diff", "--cached", "--quiet"); err == nil {
		// Exit code 0 means no staged differences — nothing to commit.
		return false, nil
	}

	if _, err := RunGit(ctx, dir,
		"-c", "user.name=drem-orchestrator",
		"-c", "user.email=drem-orchestrator@localhost",
		"commit", "-m", message,
	); err != nil {
		return false, fmt.Errorf("commit unstaged: commit: %w", err)
	}
	return true, nil
}

// UntrackEphemeralFiles removes orchestrator-generated plan.json from git
// tracking if it is currently tracked. Repository-owned files such as
// .claude/settings.json must remain part of the delivery branch. The file
// remains on disk. Returns true if a commit was created.
func UntrackEphemeralFiles(ctx context.Context, dir string) (bool, error) {
	ephemeralFiles := []string{"plan.json"}
	var untracked bool
	for _, f := range ephemeralFiles {
		out, err := RunGit(ctx, dir, "ls-files", f)
		if err != nil || out == "" {
			continue
		}
		if _, err := RunGit(ctx, dir, "rm", "--cached", f); err != nil {
			return false, fmt.Errorf("untrack ephemeral file %s: %w", f, err)
		}
		untracked = true
	}
	if !untracked {
		return false, nil
	}
	if _, err := RunGit(ctx, dir,
		"-c", "user.name=drem-orchestrator",
		"-c", "user.email=drem-orchestrator@localhost",
		"commit", "-m", "chore: untrack ephemeral files before merge",
	); err != nil {
		return false, fmt.Errorf("commit ephemeral file removal: %w", err)
	}
	return true, nil
}

// BranchHasNewCommits returns true if sourceBranch has commits that are not
// yet in the working directory's current HEAD (i.e. there is work to merge).
func BranchHasNewCommits(ctx context.Context, dir, sourceBranch string) (bool, error) {
	output, err := RunGit(ctx, dir, "rev-list", "--count", "HEAD.."+sourceBranch)
	if err != nil {
		return false, fmt.Errorf("check new commits: %w", err)
	}
	return strings.TrimSpace(output) != "0", nil
}

// CommitInfo resolves a commit SHA in dir and returns its parsed metadata.
// Uses: git log -1 --format="%H|%an|%aI|%s|%b" <sha>
func CommitInfo(ctx context.Context, dir, sha string) (*Commit, error) {
	const sep = "---GITEXEC-SEP---"
	format := fmt.Sprintf("%%H%s%%an%s%%aI%s%%s%s%%b", sep, sep, sep, sep)
	output, err := RunGit(ctx, dir, "log", "-1", fmt.Sprintf("--format=%s", format), sha)
	if err != nil {
		return nil, fmt.Errorf("commit info: %w", err)
	}
	parts := strings.SplitN(output, sep, 5)
	if len(parts) < 4 {
		return nil, fmt.Errorf("commit info: unexpected output: %q", output)
	}
	committed, perr := time.Parse(time.RFC3339, parts[2])
	if perr != nil {
		committed = time.Time{}
	}
	body := ""
	if len(parts) == 5 {
		body = strings.TrimSpace(parts[4])
	}
	return &Commit{
		SHA:       parts[0],
		Author:    parts[1],
		Committed: committed,
		Subject:   parts[3],
		Body:      body,
	}, nil
}
