package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/gitexec"
	"github.com/godinj/drem-orchestrator/internal/model"
)

type acceptedDeliveryCandidate struct {
	Branch     string
	CommitSHA  string
	BaseBranch string
	BaseSHA    string
}

type deliveryGateResult struct {
	Candidate              acceptedDeliveryCandidate
	WorkspaceID            string
	EnvironmentFingerprint string
	Evidence               CommandEvidence
	Outcome                model.PreliminaryGateOutcome
	Passed                 bool
	FailureStage           deliveryGateFailureStage
	FailureStartedAt       time.Time
}

type deliveryGateFailureStage string

const (
	deliveryGateFailureWorktreeManager deliveryGateFailureStage = "worktree-manager"
	deliveryGateFailureBareRepository  deliveryGateFailureStage = "bare-repository"
	deliveryGateFailureAcceptedRef     deliveryGateFailureStage = "accepted-ref"
	deliveryGateFailureAcceptedHead    deliveryGateFailureStage = "accepted-head"
	deliveryGateFailureAcceptedBase    deliveryGateFailureStage = "accepted-base"
	deliveryGateFailureAncestry        deliveryGateFailureStage = "accepted-ancestry"
	deliveryGateFailureWorkspaceRoot   deliveryGateFailureStage = "workspace-root"
	deliveryGateFailureWorktreeAdd     deliveryGateFailureStage = "worktree-add"
	deliveryGateFailureCheckoutBefore  deliveryGateFailureStage = "checkout-pre-command"
	deliveryGateFailureCheckoutAfter   deliveryGateFailureStage = "checkout-post-command"
	deliveryGateFailureCleanup         deliveryGateFailureStage = "cleanup"
)

func (r deliveryGateResult) artifactSnapshot() ArtifactSnapshot {
	return ArtifactSnapshot{
		Branch:                 r.Candidate.Branch,
		CommitSHA:              r.Candidate.CommitSHA,
		BaseBranch:             r.Candidate.BaseBranch,
		BaseSHA:                r.Candidate.BaseSHA,
		GateWorkspaceID:        r.WorkspaceID,
		EnvironmentFingerprint: r.EnvironmentFingerprint,
		PreliminaryEvidence:    []CommandEvidence{r.Evidence},
		Actor:                  "orchestrator",
		Source:                 "testing_ready",
	}
}

func gateEnvironmentFingerprint() string {
	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		host = "unknown-host"
	}
	return fmt.Sprintf("%s/%s;%s;%s", runtime.GOOS, runtime.GOARCH, runtime.Version(), host)
}

// closeExecutionFailure converts an execution error that occurred after
// exact candidate resolution into durable preliminary-gate evidence. Errors
// before candidate resolution cannot be attributed to a specific delivery and
// deliberately remain fail-closed orchestration errors.
func (r *deliveryGateResult) closeExecutionFailure(err error) bool {
	if err == nil || r.FailureStage == "" || r.Candidate.Branch == "" ||
		!validObjectID(r.Candidate.CommitSHA) || !validObjectID(r.Candidate.BaseSHA) ||
		strings.TrimSpace(r.WorkspaceID) == "" || strings.TrimSpace(r.EnvironmentFingerprint) == "" {
		return false
	}
	finishedAt := time.Now()
	startedAt := r.FailureStartedAt
	if startedAt.IsZero() || finishedAt.Before(startedAt) {
		startedAt = finishedAt
	}
	r.Passed = false
	r.Outcome = preliminaryGateFailureOutcome(r.FailureStage)
	r.Evidence = CommandEvidence{
		Command:    "delivery-gate:" + string(r.FailureStage),
		Passed:     false,
		ExitCode:   -1,
		Output:     truncate(err.Error(), maxTestOutputLen),
		StartedAt:  startedAt,
		FinishedAt: finishedAt,
	}
	return true
}

func preliminaryGateFailureOutcome(stage deliveryGateFailureStage) model.PreliminaryGateOutcome {
	switch stage {
	case deliveryGateFailureWorktreeManager, deliveryGateFailureBareRepository,
		deliveryGateFailureAcceptedRef, deliveryGateFailureAcceptedHead,
		deliveryGateFailureAcceptedBase, deliveryGateFailureAncestry:
		return model.PreliminaryGateConfiguration
	case deliveryGateFailureCheckoutAfter:
		return model.PreliminaryGateCodeFailure
	default:
		return model.PreliminaryGateInfraFailure
	}
}

// runAcceptedDeliveryGate executes the preliminary gate in a temporary,
// detached worktree created directly from the bare repository at the exact
// head accepted from the worker. Cleanup completes before the result is
// returned, so artifact freezing never depends on a live mutable checkout.
func (o *Orchestrator) runAcceptedDeliveryGate(ctx context.Context, task *model.Task) (result deliveryGateResult, retErr error) {
	candidate, err := o.acceptedDeliveryCandidate(task)
	if err != nil {
		return result, err
	}
	result.Candidate = candidate
	result.WorkspaceID = "setup-" + uuid.NewString()
	result.EnvironmentFingerprint = gateEnvironmentFingerprint()
	result.FailureStartedAt = time.Now()

	result.FailureStage = deliveryGateFailureWorktreeManager
	if o.worktree == nil {
		return result, errors.New("delivery gate: worktree manager is unavailable")
	}

	result.FailureStage = deliveryGateFailureBareRepository
	bareRepo := strings.TrimSpace(o.worktree.BareRepo())
	if bareRepo == "" {
		return result, errors.New("delivery gate: bare repository is unavailable")
	}
	branchRef := "refs/heads/" + candidate.Branch
	result.FailureStage = deliveryGateFailureAcceptedRef
	branchSHA, err := gitexec.RunGit(ctx, bareRepo, "rev-parse", "--verify", branchRef)
	if err != nil {
		return result, fmt.Errorf("delivery gate: resolve accepted branch %s: %w", candidate.Branch, err)
	}
	if strings.TrimSpace(branchSHA) != candidate.CommitSHA {
		return result, fmt.Errorf("delivery gate: accepted ref drift: branch %s is %s, accepted %s", candidate.Branch, strings.TrimSpace(branchSHA), candidate.CommitSHA)
	}
	for label, sha := range map[string]string{"head": candidate.CommitSHA, "base": candidate.BaseSHA} {
		if label == "head" {
			result.FailureStage = deliveryGateFailureAcceptedHead
		} else {
			result.FailureStage = deliveryGateFailureAcceptedBase
		}
		if _, err := gitexec.RunGit(ctx, bareRepo, "cat-file", "-e", sha+"^{commit}"); err != nil {
			return result, fmt.Errorf("delivery gate: accepted %s %s is not a commit: %w", label, sha, err)
		}
	}
	result.FailureStage = deliveryGateFailureAncestry
	if _, err := gitexec.RunGit(ctx, bareRepo, "merge-base", "--is-ancestor", candidate.BaseSHA, candidate.CommitSHA); err != nil {
		return result, fmt.Errorf("delivery gate: accepted base %s is not an ancestor of head %s: %w", candidate.BaseSHA, candidate.CommitSHA, err)
	}

	result.FailureStage = deliveryGateFailureWorkspaceRoot
	root, err := os.MkdirTemp("", "drem-delivery-gate-")
	if err != nil {
		return result, fmt.Errorf("delivery gate: create workspace root: %w", err)
	}
	checkout := filepath.Join(root, "checkout")
	result.WorkspaceID = filepath.Base(root)
	added := false
	defer func() {
		var cleanupErr error
		if added {
			if _, err := gitexec.RunGit(context.Background(), bareRepo, "worktree", "remove", "--force", checkout); err != nil {
				cleanupErr = fmt.Errorf("remove detached worktree: %w", err)
			}
		}
		if err := os.RemoveAll(root); err != nil && cleanupErr == nil {
			cleanupErr = fmt.Errorf("remove workspace root: %w", err)
		}
		if cleanupErr != nil {
			result.FailureStage = deliveryGateFailureCleanup
			if retErr == nil {
				retErr = fmt.Errorf("delivery gate cleanup: %w", cleanupErr)
			} else {
				retErr = errors.Join(retErr, fmt.Errorf("delivery gate cleanup: %w", cleanupErr))
			}
		}
	}()

	result.FailureStage = deliveryGateFailureWorktreeAdd
	if _, err := gitexec.RunGit(ctx, bareRepo, "worktree", "add", "--detach", checkout, candidate.CommitSHA); err != nil {
		return result, fmt.Errorf("delivery gate: create detached worktree: %w", err)
	}
	added = true
	result.FailureStage = deliveryGateFailureCheckoutBefore
	if err := verifyDeliveryGateCheckout(ctx, checkout, candidate.CommitSHA); err != nil {
		return result, err
	}

	command := strings.TrimSpace(o.testGate.TestCommand)
	if command == "" {
		command = "go test ./..."
	}
	timeout := o.testGate.TestTimeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	startedAt := time.Now()
	commandResult := o.runCommandWithTimeout(checkout, command, timeout)
	finishedAt := time.Now()
	result.Evidence = CommandEvidence{
		Command:    command,
		Passed:     commandResult.ExitCode == 0,
		ExitCode:   commandResult.ExitCode,
		Output:     truncate(commandResult.Output, maxTestOutputLen),
		StartedAt:  startedAt,
		FinishedAt: finishedAt,
	}
	result.Passed = result.Evidence.Passed
	result.Outcome = classifyPreliminaryGateOutcome(result.Evidence)
	result.FailureStage = deliveryGateFailureCheckoutAfter
	if err := verifyDeliveryGateCheckout(ctx, checkout, candidate.CommitSHA); err != nil {
		return result, err
	}
	result.FailureStage = ""
	return result, nil
}

func classifyPreliminaryGateOutcome(evidence CommandEvidence) model.PreliminaryGateOutcome {
	if evidence.Passed && evidence.ExitCode == 0 {
		return model.PreliminaryGatePassed
	}
	lower := strings.ToLower(evidence.Output)
	if evidence.ExitCode == -1 && strings.Contains(lower, "timeout exceeded") {
		return model.PreliminaryGateTimeout
	}
	if evidence.ExitCode == 127 || strings.Contains(lower, "command not found") {
		return model.PreliminaryGateConfiguration
	}
	return model.PreliminaryGateCodeFailure
}

func verifyDeliveryGateCheckout(ctx context.Context, checkout, expectedSHA string) error {
	head, err := gitexec.RunGit(ctx, checkout, "rev-parse", "HEAD")
	if err != nil {
		return fmt.Errorf("delivery gate: resolve detached HEAD: %w", err)
	}
	if strings.TrimSpace(head) != expectedSHA {
		return fmt.Errorf("delivery gate: detached HEAD drifted to %s, expected %s", strings.TrimSpace(head), expectedSHA)
	}
	status, err := gitexec.RunGit(ctx, checkout, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return fmt.Errorf("delivery gate: verify detached worktree cleanliness: %w", err)
	}
	if strings.TrimSpace(status) != "" {
		return fmt.Errorf("delivery gate: preliminary command mutated the accepted checkout: %s", strings.TrimSpace(status))
	}
	return nil
}

func (o *Orchestrator) acceptedDeliveryCandidate(task *model.Task) (acceptedDeliveryCandidate, error) {
	var candidate acceptedDeliveryCandidate
	if task == nil {
		return candidate, errors.New("delivery gate: task is required")
	}
	candidate.Branch = strings.TrimSpace(task.WorktreeBranch)
	if candidate.Branch == "" || strings.HasPrefix(candidate.Branch, "refs/") || strings.Contains(candidate.Branch, "..") {
		return candidate, errors.New("delivery gate: canonical feature branch is required")
	}
	if task.ID == uuid.Nil {
		return candidate, errors.New("delivery gate: task ID is required for typed branch acceptance")
	}
	var acceptance model.BranchAcceptanceRecord
	if err := o.db.Where("task_id = ? AND accepted = ?", task.ID, true).
		Order("created_at DESC, id DESC").First(&acceptance).Error; err != nil {
		return candidate, fmt.Errorf("delivery gate: typed branch acceptance is missing: %w", err)
	}
	if strings.TrimSpace(acceptance.Branch) != candidate.Branch {
		return candidate, fmt.Errorf("delivery gate: accepted branch %s does not match task branch %s", acceptance.Branch, candidate.Branch)
	}
	candidate.CommitSHA = strings.TrimSpace(acceptance.HeadSHA)
	candidate.BaseBranch = strings.TrimSpace(acceptance.BaseBranch)
	candidate.BaseSHA = strings.TrimSpace(acceptance.BaseSHA)
	if !validObjectID(candidate.CommitSHA) || !validObjectID(candidate.BaseSHA) {
		return candidate, errors.New("delivery gate: branch acceptance requires full head and base commit SHAs")
	}
	if candidate.BaseBranch == "" || candidate.BaseBranch == "<nil>" {
		return candidate, errors.New("delivery gate: branch acceptance base ref is missing")
	}
	return candidate, nil
}
