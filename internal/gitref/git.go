package gitref

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// gitOpTimeout bounds every subcommand so a hung git invocation cannot wedge
// the orchestrator. The operations performed here are cheap ref inspections,
// so a small ceiling is appropriate.
const gitOpTimeout = 10 * time.Second

// BranchExists reports whether refs/heads/<branch> is present in the bare
// repository at bareRepo. A missing branch is not an error: the function
// returns (false, nil). Any other failure (repo missing, permission denied,
// malformed ref name) returns a wrapped error.
func BranchExists(ctx context.Context, bareRepo, branch string) (bool, error) {
	if bareRepo == "" {
		return false, errors.New("gitref: BranchExists: bareRepo is empty")
	}
	if branch == "" {
		return false, errors.New("gitref: BranchExists: branch is empty")
	}

	ref := "refs/heads/" + branch
	_, err := runGit(ctx, bareRepo, "show-ref", "--verify", "--quiet", ref)
	if err == nil {
		return true, nil
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		// git show-ref --verify --quiet exits 1 when the ref does not
		// exist; every other non-zero exit is a real error.
		return false, nil
	}
	return false, fmt.Errorf("gitref: BranchExists %s in %s: %w", branch, bareRepo, err)
}

// HeadCommit returns the 40-character hex SHA at refs/heads/<branch> inside
// the bare repository. Returns an error if the branch does not exist.
func HeadCommit(ctx context.Context, bareRepo, branch string) (string, error) {
	if bareRepo == "" {
		return "", errors.New("gitref: HeadCommit: bareRepo is empty")
	}
	if branch == "" {
		return "", errors.New("gitref: HeadCommit: branch is empty")
	}

	out, err := runGit(ctx, bareRepo, "rev-parse", "refs/heads/"+branch)
	if err != nil {
		return "", fmt.Errorf("gitref: HeadCommit %s in %s: %w", branch, bareRepo, err)
	}
	return strings.TrimSpace(out), nil
}

// DefaultBranch reads HEAD's symbolic ref from the bare repository and
// returns the branch name (without the refs/heads/ prefix). Works for both
// "main" and "master" initialisations — the value is whatever HEAD currently
// points at.
func DefaultBranch(ctx context.Context, bareRepo string) (string, error) {
	if bareRepo == "" {
		return "", errors.New("gitref: DefaultBranch: bareRepo is empty")
	}

	out, err := runGit(ctx, bareRepo, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return "", fmt.Errorf("gitref: DefaultBranch in %s: %w", bareRepo, err)
	}
	return strings.TrimSpace(out), nil
}

// EnsureBranch creates refs/heads/<branch> in the bare repository pointing
// at refs/heads/<fromBranch>, if <branch> does not already exist. It is a
// no-op when the branch is already present — the tip is NOT force-reset.
// Idempotence matters here because the container respawn loop can invoke
// this for a task whose worker has already pushed commits, and silently
// rewinding the branch to the source tip would clobber that work.
//
// Returns an error when fromBranch is missing in the bare repo (callers
// must not fork off a ghost ref), when any argument is empty, or when
// the underlying git invocation fails for any reason outside the two
// sentinel "exists" / "doesn't exist" outcomes.
func EnsureBranch(ctx context.Context, bareRepo, branch, fromBranch string) error {
	if bareRepo == "" {
		return errors.New("gitref: EnsureBranch: bareRepo is empty")
	}
	if branch == "" {
		return errors.New("gitref: EnsureBranch: branch is empty")
	}
	if fromBranch == "" {
		return errors.New("gitref: EnsureBranch: fromBranch is empty")
	}

	// Idempotent path: the branch already exists, so we must not touch
	// it. A worker may have pushed commits; moving the tip back to
	// fromBranch would discard that work.
	exists, err := BranchExists(ctx, bareRepo, branch)
	if err != nil {
		return fmt.Errorf("gitref: EnsureBranch: check %s: %w", branch, err)
	}
	if exists {
		return nil
	}

	// Validate the source branch before asking git to create. Without
	// this check a typo in fromBranch would fail on the git branch call
	// with a less specific error; callers that pass bad refs get a clear
	// signal at the boundary.
	fromExists, err := BranchExists(ctx, bareRepo, fromBranch)
	if err != nil {
		return fmt.Errorf("gitref: EnsureBranch: check source %s: %w", fromBranch, err)
	}
	if !fromExists {
		return fmt.Errorf("gitref: EnsureBranch: source branch %s does not exist in %s", fromBranch, bareRepo)
	}

	// `git branch <new> <start-point>` refuses to overwrite an existing
	// ref and is the narrow verb we want. It has no working-tree side
	// effect in a bare repo — just a ref write against the object DB.
	if _, err := runGit(ctx, bareRepo, "branch", branch, fromBranch); err != nil {
		if exists, existsErr := BranchExists(ctx, bareRepo, branch); existsErr == nil && exists {
			return nil
		}
		return fmt.Errorf("gitref: EnsureBranch: create %s from %s in %s: %w",
			branch, fromBranch, bareRepo, err)
	}
	return nil
}

// runGit executes `git --git-dir=<bareRepo> <args...>` with the configured
// timeout budget. It returns stdout on success; on failure it returns the
// *exec.ExitError wrapped with stderr so callers can type-assert for exit
// codes while keeping diagnostic output.
func runGit(ctx context.Context, bareRepo string, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, gitOpTimeout)
	defer cancel()

	full := append([]string{"--git-dir=" + bareRepo}, args...)
	cmd := exec.CommandContext(cctx, "git", full...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// Preserve *exec.ExitError so BranchExists can distinguish exit 1
		// (missing ref) from other failures. Wrap with stderr context.
		return stdout.String(), &gitError{
			underlying: err,
			stderr:     strings.TrimSpace(stderr.String()),
			args:       strings.Join(full, " "),
		}
	}
	return stdout.String(), nil
}

// gitError wraps a non-zero-exit git invocation with stderr and the argument
// list. It unwraps to the original *exec.ExitError so errors.As can still be
// used to inspect the exit code.
type gitError struct {
	underlying error
	stderr     string
	args       string
}

func (e *gitError) Error() string {
	if e.stderr == "" {
		return fmt.Sprintf("git %s: %v", e.args, e.underlying)
	}
	return fmt.Sprintf("git %s: %v: %s", e.args, e.underlying, e.stderr)
}

func (e *gitError) Unwrap() error { return e.underlying }
