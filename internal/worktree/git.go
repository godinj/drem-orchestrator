// Package worktree provides git worktree management for the Drem Orchestrator.
//
// It implements a grouped worktree hierarchy:
//
//	bare-repo.git/
//	  main/                           <- default branch
//	  feature/X/                      <- group directory (not a worktree)
//	    integration/                  <- feature/X branch
//	    agent-<uuid>/                 <- worktree-agent-<uuid> branch
package worktree

import (
	"fmt"
	"os/exec"
	"regexp"
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

// CommitInfo holds parsed git log data.
type CommitInfo struct {
	SHA      string
	ShortSHA string
	Author   string
	Date     time.Time
	Message  string
}

// RunGit executes a git command in the given directory and returns stdout.
// Returns GitError on non-zero exit, with both stderr and stdout captured.
func RunGit(args []string, cwd string) (string, error) {
	cmd := exec.Command("git", args...)
	if cwd != "" {
		cmd.Dir = cwd
	}

	stdout, err := cmd.Output()
	if err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if ok {
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

// GetCommitLog returns commits since baseRef (up to maxCount).
// Uses: git log --format="%H|%h|%an|%aI|%s" <baseRef>..HEAD
func GetCommitLog(worktreePath, baseRef string, maxCount int) ([]CommitInfo, error) {
	sep := "---COMMIT-SEP---"
	format := fmt.Sprintf("%%H%s%%h%s%%an%s%%aI%s%%s", sep, sep, sep, sep)

	output, err := RunGit([]string{
		"log",
		fmt.Sprintf("--format=%s", format),
		fmt.Sprintf("--max-count=%d", maxCount),
		fmt.Sprintf("%s..HEAD", baseRef),
	}, worktreePath)
	if err != nil {
		return nil, fmt.Errorf("get commit log: %w", err)
	}

	if output == "" {
		return nil, nil
	}

	var commits []CommitInfo
	for _, line := range strings.Split(output, "\n") {
		parts := strings.SplitN(line, sep, 5)
		if len(parts) != 5 {
			continue
		}

		date, parseErr := time.Parse(time.RFC3339, parts[3])
		if parseErr != nil {
			// Fall back to zero time if parsing fails
			date = time.Time{}
		}

		commits = append(commits, CommitInfo{
			SHA:      parts[0],
			ShortSHA: parts[1],
			Author:   parts[2],
			Date:     date,
			Message:  parts[4],
		})
	}

	return commits, nil
}

// GetChangedFiles returns files changed since baseRef.
// Uses: git diff --name-only <baseRef>..HEAD
func GetChangedFiles(worktreePath, baseRef string) ([]string, error) {
	output, err := RunGit([]string{
		"diff", "--name-only", fmt.Sprintf("%s..HEAD", baseRef),
	}, worktreePath)
	if err != nil {
		return nil, fmt.Errorf("get changed files: %w", err)
	}

	if output == "" {
		return nil, nil
	}

	return strings.Split(output, "\n"), nil
}

// IsClean returns true if the worktree has no uncommitted changes.
// Uses: git status --porcelain
// Ignores .claude/ directory entries in the output.
func IsClean(worktreePath string) (bool, error) {
	output, err := RunGit([]string{"status", "--porcelain"}, worktreePath)
	if err != nil {
		return false, fmt.Errorf("check clean status: %w", err)
	}

	if output == "" {
		return true, nil
	}

	// Filter out .claude/ directory entries
	for _, line := range strings.Split(output, "\n") {
		// Each porcelain line has a 2-char status prefix followed by a space and path
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// Extract the file path (skip the 2-char status + space)
		if len(line) > 3 {
			path := line[3:]
			if strings.HasPrefix(path, ".claude/") {
				continue
			}
		}
		// Found a non-.claude/ dirty file
		return false, nil
	}

	return true, nil
}

// CommitUnstagedChanges stages all non-.claude/ changes and commits them.
// Returns true if a commit was created, false if there was nothing to commit.
func CommitUnstagedChanges(worktreePath, message string) (bool, error) {
	clean, err := IsClean(worktreePath)
	if err != nil {
		return false, fmt.Errorf("commit unstaged: check clean: %w", err)
	}
	if clean {
		return false, nil
	}

	// Stage all changes except .claude/ directory.
	if _, err := RunGit([]string{"add", "--all", "--", ".", ":(exclude).claude"}, worktreePath); err != nil {
		return false, fmt.Errorf("commit unstaged: add: %w", err)
	}

	// Check if anything was actually staged (add --all may have nothing after exclude).
	if _, err := RunGit([]string{"diff", "--cached", "--quiet"}, worktreePath); err == nil {
		// Exit code 0 means no staged differences — nothing to commit.
		return false, nil
	}

	if _, err := RunGit([]string{"commit", "-m", message}, worktreePath); err != nil {
		return false, fmt.Errorf("commit unstaged: commit: %w", err)
	}

	return true, nil
}

// BranchHasNewCommits returns true if sourceBranch has commits that are not
// yet in the worktree's current HEAD (i.e. there is work to merge).
func BranchHasNewCommits(worktreePath, sourceBranch string) (bool, error) {
	output, err := RunGit([]string{"rev-list", "--count", "HEAD.." + sourceBranch}, worktreePath)
	if err != nil {
		return false, fmt.Errorf("check new commits: %w", err)
	}
	return strings.TrimSpace(output) != "0", nil
}

// GetDefaultBranch detects the default branch name (main or master).
// Uses: git symbolic-ref refs/remotes/origin/HEAD, falls back to checking
// if "main" branch exists, then tries "master".
func GetDefaultBranch(repoPath string) (string, error) {
	// Try symbolic-ref for origin HEAD
	output, err := RunGit([]string{"symbolic-ref", "refs/remotes/origin/HEAD"}, repoPath)
	if err == nil {
		// Output is like "refs/remotes/origin/main"
		branch := strings.TrimPrefix(output, "refs/remotes/origin/")
		if branch != "" {
			return branch, nil
		}
	}

	// Try the bare repo's own HEAD
	output, err = RunGit([]string{"symbolic-ref", "--short", "HEAD"}, repoPath)
	if err == nil && output != "" {
		return output, nil
	}

	// Fall back: check if "main" branch exists
	_, err = RunGit([]string{"show-ref", "--verify", "--quiet", "refs/heads/main"}, repoPath)
	if err == nil {
		return "main", nil
	}

	// Fall back: check if "master" branch exists
	_, err = RunGit([]string{"show-ref", "--verify", "--quiet", "refs/heads/master"}, repoPath)
	if err == nil {
		return "master", nil
	}

	// Default to "main"
	return "main", nil
}

// RebaseResult describes the outcome of a rebase operation.
type RebaseResult struct {
	Success   bool
	Conflicts []string // conflicting file paths if rebase failed
	GitStderr string   // raw git stderr on failure
}

// rebaseConflictRe matches lines like "CONFLICT (content): Merge conflict in <file>".
var rebaseConflictRe = regexp.MustCompile(`CONFLICT \([^)]+\): Merge conflict in (.+)`)

// RebaseBranch rebases the branch checked out in sourceWorktree onto
// the HEAD of targetWorktree. On conflict, the rebase is aborted and
// the source worktree is left unchanged.
func RebaseBranch(sourceWorktree, targetWorktree string) (*RebaseResult, error) {
	// Resolve target HEAD
	targetHEAD, err := RunGit([]string{"rev-parse", "HEAD"}, targetWorktree)
	if err != nil {
		return nil, fmt.Errorf("rebase branch: resolve target HEAD: %w", err)
	}

	// Attempt rebase in the source worktree
	_, rebaseErr := RunGit([]string{"rebase", targetHEAD}, sourceWorktree)
	if rebaseErr == nil {
		return &RebaseResult{Success: true}, nil
	}

	// Rebase failed - extract output and parse conflicts.
	// Git rebase outputs CONFLICT lines to stdout and error details
	// to stderr, so we combine both for diagnostics and conflict parsing.
	var gitStderr, combinedOutput string
	if gitErr, ok := rebaseErr.(*GitError); ok {
		gitStderr = gitErr.Stderr
		combinedOutput = gitErr.Stdout
		if gitErr.Stderr != "" {
			if combinedOutput != "" {
				combinedOutput = combinedOutput + "\n" + gitErr.Stderr
			} else {
				combinedOutput = gitErr.Stderr
			}
		}
		// Store combined output as GitStderr for diagnostics
		if combinedOutput != "" {
			gitStderr = combinedOutput
		}
	}

	conflicts := parseRebaseConflicts(combinedOutput)

	// Abort the rebase to leave the source worktree clean
	RunGit([]string{"rebase", "--abort"}, sourceWorktree)

	return &RebaseResult{
		Success:   false,
		Conflicts: conflicts,
		GitStderr: gitStderr,
	}, nil
}

// parseRebaseConflicts extracts conflicting file paths from rebase stderr output.
func parseRebaseConflicts(stderr string) []string {
	var conflicts []string
	for _, line := range strings.Split(stderr, "\n") {
		matches := rebaseConflictRe.FindStringSubmatch(line)
		if len(matches) == 2 {
			conflicts = append(conflicts, matches[1])
		}
	}
	return conflicts
}
