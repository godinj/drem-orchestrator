package orchestrator

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/spawner"
)

// MergeDispatcher is the orchestrator-internal contract for dispatching a
// feature-into-main merge. In production it is satisfied by Orchestrator's
// own dispatchMerge method (via a method-value bound at construction time);
// tests substitute a stub so merge-state-machine behaviour can be exercised
// without a real merger container.
type MergeDispatcher interface {
	Dispatch(ctx context.Context, task *model.Task) (*MergeResult, error)
}

// mergeDispatcherFunc is a function adapter that lets Orchestrator.dispatchMerge
// satisfy MergeDispatcher without wrapping it in a struct.
type mergeDispatcherFunc func(ctx context.Context, task *model.Task) (*MergeResult, error)

// Dispatch delegates to the underlying function.
func (f mergeDispatcherFunc) Dispatch(ctx context.Context, task *model.Task) (*MergeResult, error) {
	return f(ctx, task)
}

// mergeDispatch runs the active MergeDispatcher for the orchestrator. When
// tests have injected an override via SetMergeDispatcher, that override is
// used; otherwise the default production path (dispatchMerge) runs. This
// indirection is the replacement for the retired mergerClient interface:
// tests can now stub the feature-into-main merge without needing to re-host
// an in-process merger.
func (o *Orchestrator) mergeDispatch(ctx context.Context, task *model.Task) (*MergeResult, error) {
	if o.mergeDispatcher != nil {
		return o.mergeDispatcher.Dispatch(ctx, task)
	}
	return o.dispatchMerge(ctx, task)
}

// SetMergeDispatcher overrides the MergeDispatcher used for
// feature-into-main merges. Passing nil restores the default (dispatchMerge).
// Tests use this to substitute a stub that returns canned MergeResults
// without spawning a real merger container.
func (o *Orchestrator) SetMergeDispatcher(d MergeDispatcher) {
	o.mergeDispatcher = d
}

// mergeDispatchTimeout bounds how long the orchestrator waits for a merger
// container to finish before giving up. The merger is expected to finish
// quickly (seconds) for a successful fast-forward and up to ~5 minutes for
// a complex three-way merge with build verification.
const mergeDispatchTimeout = 10 * time.Minute

// mergerPollInterval is how often dispatchMerge polls Inspect when waiting
// for the merger container to exit. Event-driven waiting would be ideal but
// the event channel is already consumed by watchDockerEvents, so polling
// keeps the flows decoupled.
const mergerPollInterval = 2 * time.Second

// MergeResult is the public shape dispatchMerge returns. It intentionally
// mirrors the worktree-package MergeResult so downstream error handling in
// executeMerge works unchanged regardless of which path produced the result.
type MergeResult struct {
	Success     bool
	MergeCommit string
	Conflicts   []string
	// TrivialCount / NonTrivialCount / ClassifiedDetails echo the classified
	// conflict summary produced by the pre-containerization merge queue. They
	// are optional: the merger container does not yet populate them, but
	// executeMerge reads them defensively for conflict-path reporting.
	TrivialCount      int
	NonTrivialCount   int
	ClassifiedDetails string
	// ContainerID is the merger container that produced the result, recorded
	// so the audit trail in user story 49 can correlate merge outcomes with
	// the specific worker that produced them.
	ContainerID string
	ExitCode    int
	// FailureReason mirrors drem-merger's typed exit-code map so executeMerge
	// can branch on the root cause (conflict vs tests_failed vs push_failed)
	// instead of inspecting ExitCode directly. Empty on success. Populated
	// from ExitCode by dispatchMerge via failureReasonForExit.
	FailureReason string
}

// failureReasonForExit maps a drem-merger process exit code to the typed
// reason string that executeMerge routes on. Codes must match the
// cmd/drem-merger/main.go exitCodeFor table: 0=success, 2=conflict,
// 3=tests_failed, 4=push_failed, 1=misc; anything else is reported as
// "unknown" so unrecognized signals surface loudly rather than
// silently retrying.
func failureReasonForExit(code int) string {
	switch code {
	case 0:
		return ""
	case 2:
		return "conflict"
	case 3:
		return "tests_failed"
	case 4:
		return "push_failed"
	case 1:
		return "misc"
	default:
		return "unknown"
	}
}

// dispatchMerge spawns a merger container to execute the feature→main merge
// and waits for it to exit. The merger's structured output is expected to
// have been ingested into task.Context by agentmon during the container's
// life; this function reads that context back into a MergeResult when the
// container finishes.
//
// The existing retry_policy.go state (MergeAttemptState) is unchanged —
// executeMerge still drives the state machine; this function only replaces
// the in-process merge call with a containerized one. When o.Spawner is
// nil, dispatchMerge returns an error and callers should fall back to the
// legacy mergerClient path.
func (o *Orchestrator) dispatchMerge(ctx context.Context, task *model.Task) (*MergeResult, error) {
	if o.Spawner == nil {
		return nil, fmt.Errorf("dispatchMerge: no Spawner configured")
	}
	if task.WorktreeBranch == "" {
		return nil, fmt.Errorf("dispatchMerge: task %s has no WorktreeBranch", task.ID)
	}

	defaultBranch := "main"
	if o.worktree != nil && o.worktree.DefaultBranchName() != "" {
		defaultBranch = o.worktree.DefaultBranchName()
	}

	workerID := fmt.Sprintf("merger-%s-%s", task.ID.String()[:shortIDLen], uuid.New().String()[:shortIDLen])
	bareRepo := ""
	if o.worktree != nil {
		bareRepo = o.worktree.BareRepo()
	}

	// Resolve the in-cluster orch URL and agentmon token. SetInternalEndpoints
	// is called from cmd/drem/main.go with the DREM_ORCH_URL and
	// DREM_AGENTMON_TOKEN env vars. Tests that drive dispatchMerge without
	// calling SetInternalEndpoints (rare) fall through to empty strings,
	// which the merger rejects with a parseFlags error at startup — fail
	// fast rather than silently skipping result ingestion.
	orchURL := o.orchURL
	if orchURL == "" {
		orchURL = os.Getenv("DREM_ORCH_URL")
	}
	agentmonToken := o.agentmonToken
	if agentmonToken == "" {
		agentmonToken = os.Getenv("DREM_AGENTMON_TOKEN")
	}

	argv := buildMergerArgv(task, o.projectID.String(), defaultBranch, o.testGate.TestCommand, orchURL, agentmonToken)

	params := spawner.SpawnWorkerParams{
		Project:   o.projectID.String(),
		AgentType: "merger",
		WorkerID:  workerID,
		Branch:    task.WorktreeBranch,
		Env: map[string]string{
			"DREM_TASK_ID":        task.ID.String(),
			"DREM_FEATURE_BRANCH": task.WorktreeBranch,
			"DREM_TARGET_BRANCH":  defaultBranch,
		},
		Labels: map[string]string{
			"drem.task_id":  task.ID.String(),
			"drem.role":     "merger",
			"drem.language": o.resolveProjectLanguage(),
		},
		BareRepoMount: bareRepo,
		// Merger must write to /bare (git push of integration branch, git
		// push --delete of the feature branch). Workers keep the hardened
		// read-only default; flipping this only here is the whole reason
		// SpawnWorkerParams.BareRepoReadWrite exists.
		BareRepoReadWrite: true,
		// Pass the merger's required flags as argv. drem-merger's
		// parseFlags rejects empty argv with exit 1, so we must populate
		// every --required flag or the container crash-loops.
		Cmd: argv,
	}

	dispatchCtx, cancel := context.WithTimeout(ctx, mergeDispatchTimeout)
	defer cancel()

	res, err := o.Spawner.SpawnWorker(dispatchCtx, params)
	if err != nil {
		return nil, fmt.Errorf("dispatchMerge: spawn merger: %w", err)
	}

	// Record the merger spawn in the audit trail.
	o.recordSpawnEvent(task, "merger", res.ContainerID, params.Image)

	finalState, err := o.awaitMergerExit(dispatchCtx, res.ContainerID)
	if err != nil {
		return nil, fmt.Errorf("dispatchMerge: await merger exit: %w", err)
	}

	result := &MergeResult{
		ContainerID:   res.ContainerID,
		ExitCode:      finalState.ExitCode,
		Success:       finalState.ExitCode == 0 && finalState.Status == string(spawnerStatusExited),
		FailureReason: failureReasonForExit(finalState.ExitCode),
	}
	// Pull any agentmon-populated merge context off the task (merger's
	// structured output is ingested into task.Context by agentmon during
	// the container's life). Fields are optional — empty means the merger
	// did not publish structured data.
	if task.Context != nil {
		if raw, ok := task.Context["merge_commit"].(string); ok {
			result.MergeCommit = raw
		}
		if raw, ok := task.Context["merge_conflicts"].([]any); ok {
			for _, c := range raw {
				if s, ok := c.(string); ok {
					result.Conflicts = append(result.Conflicts, s)
				}
			}
		}
	}

	return result, nil
}

// spawnerStatusExited mirrors spawner.InspectWorkerResult.Status for the
// exited state. Kept as a typed constant so the comparison in dispatchMerge
// does not depend on a magic string.
const spawnerStatusExited = "exited"

// defaultIntegrationBranches are the branch names for which dispatchMerge
// omits the --integration-branch flag (drem-merger's own default is
// "master", but any plain main/master is safe to leave implicit so the
// argv stays terse for the common case).
var defaultIntegrationBranches = map[string]bool{
	"main":   true,
	"master": true,
}

// buildMergerArgv composes the argv slice passed as container.Spec.Cmd to
// the drem-merger image. The merger's parseFlags rejects empty argv and
// every --required flag must be paired with a non-empty value. See
// cmd/drem-merger/main.go::parseFlags for the required-flag set and
// plans/merger-spawn-on-demand-impl.md for the rationale behind each
// flag's provenance.
func buildMergerArgv(task *model.Task, projectID, integrationBranch, testCmd, orchURL, agentmonToken string) []string {
	argv := []string{
		"--feature-branch", task.WorktreeBranch,
		"--project", projectID,
		"--task-id", task.ID.String(),
		"--test-cmd", testCmd,
		"--orch-url", orchURL,
		"--agentmon-token", agentmonToken,
	}
	// Only include --integration-branch when it differs from the merger's
	// own default ("master"). For plain main / master the flag is
	// redundant and its omission keeps argv short for the common case.
	if integrationBranch != "" && !defaultIntegrationBranches[integrationBranch] {
		argv = append(argv, "--integration-branch", integrationBranch)
	}
	return argv
}

// awaitMergerExit polls Inspect until the merger container transitions to
// exited or dead. Returns the final state on success; returns an error if
// the context is cancelled or Inspect fails persistently.
func (o *Orchestrator) awaitMergerExit(ctx context.Context, containerID string) (spawner.InspectWorkerResult, error) {
	ticker := time.NewTicker(mergerPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return spawner.InspectWorkerResult{}, ctx.Err()
		case <-ticker.C:
			res, err := o.Spawner.InspectWorker(ctx, spawner.InspectWorkerParams{ContainerID: containerID})
			if err != nil {
				// Transient errors are retried on the next tick; only return
				// on context cancellation.
				o.logger.Debug("await merger exit: inspect error",
					"container_id", containerID, "error", err)
				continue
			}
			if res.Status == string(spawnerStatusExited) || res.Status == "dead" {
				return res, nil
			}
		}
	}
}
