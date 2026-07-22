package merger

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// gitTimeout bounds every single git invocation. Merges that legitimately
// need longer should be fought out in the test command, not the plumbing.
const gitTimeout = 10 * time.Minute

// gitOutput wraps the combined stdout/stderr of a git command along with the
// exit error, if any. It lets callers decide whether a non-zero exit is an
// error (push failed) or an expected condition (merge produced conflicts).
type gitOutput struct {
	Stdout string
	Stderr string
	Err    error
}

// Combined returns stdout and stderr concatenated, which is what callers log
// on failure.
func (g gitOutput) Combined() string {
	if g.Stderr == "" {
		return g.Stdout
	}
	if g.Stdout == "" {
		return g.Stderr
	}
	return g.Stdout + "\n" + g.Stderr
}

// runGit invokes `git <args...>` with workDir as the working directory. It
// always uses exec.CommandContext so the caller's deadline propagates, and
// it captures stdout and stderr separately so conflict output (which git
// writes to stdout) is not mixed with structural errors.
func runGit(ctx context.Context, workDir string, args ...string) gitOutput {
	callCtx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()

	cmd := exec.CommandContext(callCtx, "git", args...)
	if workDir != "" {
		cmd.Dir = workDir
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	return gitOutput{
		Stdout: strings.TrimRight(stdout.String(), "\n"),
		Stderr: strings.TrimRight(stderr.String(), "\n"),
		Err:    err,
	}
}

// cloneBranch clones bareRepo into workDir at the given branch.
//
// Bug J shape change: the merger runs inside a container where workDir
// is the bind-mount point of a pre-existing host directory, so workDir
// MUST be preserved. We clone INTO workDir (git's `.` target) rather
// than asking git to create workDir for us. The directory must exist
// and be empty; resetWorkDir takes care of both. If workDir does not
// exist (legacy callers running on a host filesystem), we create it so
// the old semantics still work.
//
// See plans/bug-j-merger-reset-workdir-unlinkat-busy.md.
func cloneBranch(ctx context.Context, bareRepo, branch, workDir string) error {
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", workDir, err)
	}
	out := runGit(ctx, workDir, "clone", "--branch", branch, bareRepo, ".")
	if out.Err != nil {
		return fmt.Errorf("git clone --branch %s %s into %s: %w: %s",
			branch, bareRepo, workDir, out.Err, out.Combined())
	}
	return nil
}

// fetchBranch fetches `origin <branch>:<branch>` into workDir, making the
// feature branch locally available so we can merge it.
func fetchBranch(ctx context.Context, workDir, branch string) error {
	out := runGit(ctx, workDir, "fetch", "origin", fmt.Sprintf("%s:%s", branch, branch))
	if out.Err != nil {
		return fmt.Errorf("git fetch origin %s:%s: %w: %s",
			branch, branch, out.Err, out.Combined())
	}
	return nil
}

// mergeBranch runs `git merge --no-ff <branch>` in workDir. A non-zero exit
// is NOT a hard error — it typically means there are conflicts. The caller
// inspects the returned gitOutput plus conflictedFiles() to decide.
func mergeBranch(ctx context.Context, workDir, branch, message string) gitOutput {
	args := []string{"merge", "--no-ff"}
	if message != "" {
		args = append(args, "-m", message)
	}
	args = append(args, branch)
	return runGit(ctx, workDir, args...)
}

// abortMerge aborts an in-progress merge. It is only called when we detect a
// conflict; a failed abort is logged by the caller but does not change the
// outward result (the workspace is disposable).
func abortMerge(ctx context.Context, workDir string) error {
	out := runGit(ctx, workDir, "merge", "--abort")
	if out.Err != nil {
		return fmt.Errorf("git merge --abort: %w: %s", out.Err, out.Combined())
	}
	return nil
}

// pushBranch pushes `origin <branch>` from workDir.
func pushBranch(ctx context.Context, workDir, branch string) error {
	out := runGit(ctx, workDir, "push", "origin", branch)
	if out.Err != nil {
		return fmt.Errorf("git push origin %s: %w: %s",
			branch, out.Err, out.Combined())
	}
	return nil
}

// pushDelete deletes <branch> on the `origin` remote.
func pushDelete(ctx context.Context, workDir, branch string) error {
	out := runGit(ctx, workDir, "push", "origin", "--delete", branch)
	if out.Err != nil {
		return fmt.Errorf("git push origin --delete %s: %w: %s",
			branch, out.Err, out.Combined())
	}
	return nil
}

// conflictedFiles parses `git status --porcelain` for conflict markers
// (UU, AA, DD, AU, UA, DU, UD) and returns the deduplicated list of paths.
// An empty return value means the workspace has no unresolved conflicts.
func conflictedFiles(ctx context.Context, workDir string) ([]string, error) {
	out := runGit(ctx, workDir, "status", "--porcelain")
	if out.Err != nil {
		return nil, fmt.Errorf("git status --porcelain: %w: %s", out.Err, out.Combined())
	}
	if out.Stdout == "" {
		return nil, nil
	}
	seen := make(map[string]struct{})
	var paths []string
	for _, line := range strings.Split(out.Stdout, "\n") {
		if len(line) < 3 {
			continue
		}
		code := line[:2]
		if !isConflictCode(code) {
			continue
		}
		path := strings.TrimSpace(line[3:])
		if path == "" {
			continue
		}
		if _, dup := seen[path]; dup {
			continue
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}
	return paths, nil
}

// isConflictCode returns true for the two-character porcelain codes that
// indicate an unresolved merge conflict.
func isConflictCode(code string) bool {
	switch code {
	case "UU", "AA", "DD", "AU", "UA", "DU", "UD":
		return true
	}
	return false
}

// headSHA returns the SHA of HEAD in workDir. Used after a successful merge
// and push to record the integration SHA in MergeResult.
func headSHA(ctx context.Context, workDir string) (string, error) {
	out := runGit(ctx, workDir, "rev-parse", "HEAD")
	if out.Err != nil {
		return "", fmt.Errorf("git rev-parse HEAD: %w: %s", out.Err, out.Combined())
	}
	return strings.TrimSpace(out.Stdout), nil
}

func refSHA(ctx context.Context, workDir, ref string) (string, error) {
	out := runGit(ctx, workDir, "rev-parse", ref)
	if out.Err != nil {
		return "", fmt.Errorf("git rev-parse %s: %w: %s", ref, out.Err, out.Combined())
	}
	return strings.TrimSpace(out.Stdout), nil
}

// remoteBranchExists returns true when `git ls-remote --heads origin <branch>`
// reports a matching ref. Used for the idempotency check: a second merger
// invocation on an already-merged branch short-circuits to success without
// re-running tests.
func remoteBranchExists(ctx context.Context, workDir, branch string) (bool, error) {
	out := runGit(ctx, workDir, "ls-remote", "--heads", "origin", branch)
	if out.Err != nil {
		return false, fmt.Errorf("git ls-remote --heads origin %s: %w: %s",
			branch, out.Err, out.Combined())
	}
	return strings.TrimSpace(out.Stdout) != "", nil
}
