package merger

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// defaultTestTimeout is the upper bound on the project-supplied test command.
// Tests that legitimately need longer should raise this in the parent caller;
// inside the merger container we fail closed to keep merge latency bounded.
const defaultTestTimeout = 10 * time.Minute

// MergeRequest is the input to a single container-local merge operation.
// Fields marked required are validated up front by Merger.Merge.
type MergeRequest struct {
	FeatureBranch     string // required
	IntegrationBranch string // required
	Project           string // required (used in reports)
	TaskID            string // required (used in reports)
}

// MergeResult summarises the outcome of one merge attempt. Success is true
// only when: merge was clean, tests passed, integration was pushed, and the
// feature branch was deleted (or the branch was already merged and we
// short-circuited).
type MergeResult struct {
	// Success indicates the full merge+test+push+delete pipeline completed.
	Success bool

	// Conflicts lists files with unresolved merge conflicts. Non-empty iff
	// the merge itself failed due to file-level conflicts.
	Conflicts []string

	// TestsPassed reflects the exit status of TestCmd. It is only meaningful
	// when the merge itself succeeded; on a conflict it is false.
	TestsPassed bool

	// TestOutput is the combined stdout+stderr of the test command. Empty
	// when tests did not run (conflict path or idempotent short-circuit).
	TestOutput string

	// MergedSHA is the HEAD of integration after the merge was pushed. Empty
	// on failure paths.
	MergedSHA string

	// StartedAt / FinishedAt bracket the merge invocation.
	StartedAt  time.Time
	FinishedAt time.Time

	// FailureReason captures the high-level reason on failure paths so that
	// downstream reporters can route without re-inferring from flags.
	// Values: "conflict", "tests_failed", "push_failed", "other" (or "").
	FailureReason string
}

// Merger performs a single container-local merge per invocation. It owns no
// long-lived state and is safe to construct per request.
type Merger struct {
	// WorkDir is the path to the container-local clone. Merger.Merge blows
	// this directory away on entry, so it must be disposable.
	WorkDir string

	// BareRepo is the bind-mounted bare repository path.
	BareRepo string

	// TestCmd is the project's test invocation, run via `sh -c <TestCmd>`
	// inside WorkDir after a clean merge. Empty TestCmd means "no tests",
	// which is treated as a passing test (documented behaviour mirroring
	// internal/merge/DetectBuildCommand's no-op return).
	//
	// TODO(seth): tighten the contract so library-side silent-skip on
	// empty TestCmd either requires an explicit opt-in
	// (e.g. AllowSkipTests bool) or is removed entirely. The orch-side
	// fail-close guard in merge_dispatch.go now refuses to spawn
	// drem-merger with an empty --test-cmd, but the in-process library
	// path still defaults to "no tests" for any caller that forgets to
	// populate TestCmd. See Bug H follow-up plan.
	TestCmd string

	// TestTimeout overrides the default 10-minute bound on TestCmd.
	TestTimeout time.Duration

	// Reporter is invoked exactly once per Merge call, even on failure.
	Reporter MergeReporter

	// Registry records the merged/deleted transitions for the feature
	// branch. Use NoopRegistry in tests that do not care.
	Registry GitrefRegistry

	// Logger is an optional slog.Logger; if nil, slog.Default is used.
	Logger *slog.Logger
}

// Merge performs the full pipeline:
//
//  1. Reset WorkDir.
//  2. Clone bare repo at integration branch.
//  3. Fetch feature branch locally.
//  4. Merge --no-ff feature into integration.
//  5. On a clean merge: run TestCmd.
//  6. On passing tests: push integration.
//  7. On successful push: delete feature branch on origin; mark
//     registry as merged then deleted.
//  8. Report the result via Reporter.
//
// If the feature branch no longer exists on origin (for example because a
// previous merger invocation already succeeded), Merge short-circuits and
// returns Success=true without re-running the merge (idempotency).
//
// Test-failure behaviour: tests run against the merge commit, but if they
// fail we simply never push. The workspace is disposable, so there is no
// rollback to perform; the stale merge commit lives only in this container
// and is discarded with the directory on the next invocation.
func (m *Merger) Merge(ctx context.Context, req MergeRequest) (*MergeResult, error) {
	result := &MergeResult{StartedAt: time.Now().UTC()}
	defer func() {
		result.FinishedAt = time.Now().UTC()
		m.report(ctx, req, *result)
	}()

	if err := req.validate(); err != nil {
		result.FailureReason = "other"
		return result, err
	}
	if m.WorkDir == "" || m.BareRepo == "" {
		result.FailureReason = "other"
		return result, errors.New("merger: WorkDir and BareRepo are required")
	}

	log := m.logger().With(
		"project", req.Project,
		"task_id", req.TaskID,
		"feature", req.FeatureBranch,
		"integration", req.IntegrationBranch,
	)

	if err := resetWorkDir(m.WorkDir); err != nil {
		result.FailureReason = "other"
		return result, fmt.Errorf("merger: reset work dir: %w", err)
	}

	if err := cloneBranch(ctx, m.BareRepo, req.IntegrationBranch, m.WorkDir); err != nil {
		result.FailureReason = "other"
		return result, fmt.Errorf("merger: clone integration: %w", err)
	}

	// Idempotency: if the feature branch is already gone on origin, a prior
	// merger invocation succeeded. Return success without re-merging.
	exists, err := remoteBranchExists(ctx, m.WorkDir, req.FeatureBranch)
	if err != nil {
		result.FailureReason = "other"
		return result, fmt.Errorf("merger: check remote branch: %w", err)
	}
	if !exists {
		log.Info("feature branch already deleted on origin — treating as idempotent success")
		sha, shaErr := headSHA(ctx, m.WorkDir)
		if shaErr != nil {
			result.FailureReason = "other"
			return result, fmt.Errorf("merger: read HEAD after idempotent short-circuit: %w", shaErr)
		}
		result.Success = true
		result.TestsPassed = true
		result.MergedSHA = sha
		return result, nil
	}

	if err := fetchBranch(ctx, m.WorkDir, req.FeatureBranch); err != nil {
		result.FailureReason = "other"
		return result, fmt.Errorf("merger: fetch feature: %w", err)
	}

	mergeMsg := fmt.Sprintf("Merge branch '%s' into %s", req.FeatureBranch, req.IntegrationBranch)
	mergeOut := mergeBranch(ctx, m.WorkDir, req.FeatureBranch, mergeMsg)
	if mergeOut.Err != nil {
		conflicts, cErr := conflictedFiles(ctx, m.WorkDir)
		if cErr != nil {
			result.FailureReason = "other"
			return result, fmt.Errorf("merger: inspect conflicts: %w", cErr)
		}
		if len(conflicts) > 0 {
			result.Conflicts = conflicts
			result.FailureReason = "conflict"
			log.Warn("merge produced conflicts",
				"conflicts", conflicts,
				"stderr", mergeOut.Stderr)
			// Abort so the workspace is in a known state if anything later
			// inspects it. The directory is disposable either way.
			if abortErr := abortMerge(ctx, m.WorkDir); abortErr != nil {
				log.Warn("merge --abort failed", "error", abortErr)
			}
			return result, nil
		}
		// Non-zero exit with no file-level conflicts is a structural failure
		// (e.g. refusing to merge unrelated histories). Surface it.
		result.FailureReason = "other"
		return result, fmt.Errorf("merger: merge: %w: %s", mergeOut.Err, mergeOut.Combined())
	}

	// Merge succeeded. Gate push on the test command.
	testsPassed, testOutput, testErr := m.runTests(ctx, log)
	result.TestOutput = testOutput
	result.TestsPassed = testsPassed
	if testErr != nil {
		result.FailureReason = "other"
		return result, fmt.Errorf("merger: run tests: %w", testErr)
	}
	if !testsPassed {
		result.FailureReason = "tests_failed"
		log.Warn("tests failed; integration not pushed", "output_bytes", len(testOutput))
		return result, nil
	}

	if err := pushBranch(ctx, m.WorkDir, req.IntegrationBranch); err != nil {
		result.FailureReason = "push_failed"
		return result, fmt.Errorf("merger: push integration: %w", err)
	}
	sha, err := headSHA(ctx, m.WorkDir)
	if err != nil {
		result.FailureReason = "other"
		return result, fmt.Errorf("merger: read merged SHA: %w", err)
	}
	result.MergedSHA = sha

	if err := pushDelete(ctx, m.WorkDir, req.FeatureBranch); err != nil {
		// The merge itself succeeded — surface the delete failure but do
		// not claim overall success, because the registry would desync.
		result.FailureReason = "push_failed"
		return result, fmt.Errorf("merger: delete feature branch: %w", err)
	}

	if err := m.markRegistry(ctx, log, m.BareRepo, req.FeatureBranch); err != nil {
		// Registry bookkeeping is best-effort from the merger's perspective
		// — the git-level state is already consistent. Log and continue.
		log.Warn("registry bookkeeping failed", "error", err)
	}

	result.Success = true
	return result, nil
}

// runTests runs m.TestCmd via `sh -c` inside WorkDir. An empty TestCmd is
// treated as a no-op pass, matching internal/merge/DetectBuildCommand's
// behaviour when no build system is detected.
func (m *Merger) runTests(ctx context.Context, log *slog.Logger) (bool, string, error) {
	if strings.TrimSpace(m.TestCmd) == "" {
		return true, "no test command configured", nil
	}

	timeout := m.TestTimeout
	if timeout <= 0 {
		timeout = defaultTestTimeout
	}
	testCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(testCtx, "sh", "-c", m.TestCmd)
	cmd.Dir = m.WorkDir
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	runErr := cmd.Run()
	output := buf.String()

	if testCtx.Err() == context.DeadlineExceeded {
		log.Warn("test command timed out", "timeout", timeout)
		return false, output + "\n[merger: test command timed out]", nil
	}
	if runErr != nil {
		// Non-zero exit is a test failure, not a structural error.
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			return false, output, nil
		}
		return false, output, fmt.Errorf("exec test command: %w", runErr)
	}
	return true, output, nil
}

// markRegistry records the merged + deleted transitions for the feature
// branch. Callers already pushed and deleted the branch on the remote; this
// method only touches the metadata store.
func (m *Merger) markRegistry(ctx context.Context, log *slog.Logger, bareRepo, branch string) error {
	if m.Registry == nil {
		return nil
	}
	if err := m.Registry.MarkMerged(ctx, bareRepo, branch); err != nil {
		return fmt.Errorf("mark merged: %w", err)
	}
	if err := m.Registry.MarkDeleted(ctx, bareRepo, branch); err != nil {
		return fmt.Errorf("mark deleted: %w", err)
	}
	log.Debug("registry transitions recorded", "branch", branch)
	return nil
}

// report invokes the Reporter exactly once. It swallows the reporter's error
// (logging it) so that Merge never fails solely because reporting failed.
func (m *Merger) report(ctx context.Context, req MergeRequest, result MergeResult) {
	if m.Reporter == nil {
		return
	}
	// Use a fresh short-bounded context so we still report when ctx has
	// already been cancelled (e.g. on an upstream deadline).
	reportCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := m.Reporter.Report(reportCtx, req.Project, req.TaskID, result); err != nil {
		m.logger().Warn("merge result reporter failed",
			"project", req.Project, "task_id", req.TaskID, "error", err)
	}
}

// logger returns the configured slog logger, defaulting to slog.Default.
func (m *Merger) logger() *slog.Logger {
	if m.Logger != nil {
		return m.Logger
	}
	return slog.Default()
}

// validate checks the required fields on MergeRequest.
func (req MergeRequest) validate() error {
	var missing []string
	if req.FeatureBranch == "" {
		missing = append(missing, "FeatureBranch")
	}
	if req.IntegrationBranch == "" {
		missing = append(missing, "IntegrationBranch")
	}
	if req.Project == "" {
		missing = append(missing, "Project")
	}
	if req.TaskID == "" {
		missing = append(missing, "TaskID")
	}
	if len(missing) > 0 {
		return fmt.Errorf("merger: MergeRequest missing fields: %s", strings.Join(missing, ", "))
	}
	return nil
}

// resetWorkDir empties workDir without removing the directory itself.
//
// Bug J: the merger container bind-mounts the feature worktree onto
// /work (workDir) directly — workDir IS the mount point, not a child
// of it. `os.RemoveAll(workDir)` eventually tries to unlinkat the
// mount point, which the kernel refuses with EBUSY. We want the same
// semantic ("drop every child"), but only touch children so the mount
// point stays intact. If workDir does not exist, we create it so the
// subsequent clone-into-. has somewhere to land.
//
// See plans/bug-j-merger-reset-workdir-unlinkat-busy.md for the full
// write-up.
func resetWorkDir(workDir string) error {
	entries, err := os.ReadDir(workDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if mkErr := os.MkdirAll(workDir, 0o755); mkErr != nil {
				return fmt.Errorf("mkdir %s: %w", workDir, mkErr)
			}
			return nil
		}
		return fmt.Errorf("read %s: %w", workDir, err)
	}
	for _, e := range entries {
		p := filepath.Join(workDir, e.Name())
		if rmErr := os.RemoveAll(p); rmErr != nil {
			return fmt.Errorf("remove %s: %w", p, rmErr)
		}
	}
	return nil
}
