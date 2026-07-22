package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/spawner"
	"github.com/godinj/drem-orchestrator/internal/workeridentity"
)

// errMergerSpawnSkippedEmptyTestCmd is the sentinel error dispatchMerge
// returns when buildMergerArgv rejects an empty TestCommand. The fail-fast
// guard lives inside dispatchMerge: when this sentinel surfaces, the task
// has already been transitioned to FAILED with the operator-facing
// failure_reason populated — executeMerge swallows the sentinel and
// returns nil so the dispatchMerges tick loop does not log it as an
// error.
//
// Fail-close framing (Seth): spawning drem-merger with an empty
// --test-cmd would crash the container (the CLI rejects the empty
// string). Silent-skipping tests in an automated merge pipeline is a
// quality regression; refusing to spawn at all is the safe choice.
//
//nolint:errname // "err" prefix matches the package's existing sentinel naming.
var errMergerSpawnSkippedEmptyTestCmd = errors.New(
	"merger spawn skipped: project has no test command",
)

var errMergerPreflightFailed = errors.New("merger preflight failed")

// mergerSpawnSkippedReason is the exact operator-facing reason string
// written to task.Context["failure_reason"] when the fail-fast guard
// trips. Kept as a constant so tests can assert on the string without
// re-declaring it.
const mergerSpawnSkippedReason = "merger spawn skipped: project has no test command"

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
	return o.workerLaunchService().LaunchMerge(ctx, task)
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
// 3=tests_failed, 4=push_failed, 5=stale_evidence, 1=misc; anything else is reported as
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
	case 5:
		return "stale_evidence"
	case 1:
		return "misc"
	default:
		return "unknown"
	}
}

// dispatchMerge spawns a merger container to execute the feature→main merge
// and waits for it to exit. Agentmon may enrich failure diagnostics in
// task.Context, but merge completion is recovered independently from the
// typed intent and authoritative target ref.
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
	if task.ID == uuid.Nil {
		return nil, fmt.Errorf("dispatchMerge: task id is zero UUID")
	}
	if task.WorktreeBranch == "" {
		err := fmt.Errorf("task %s has no WorktreeBranch", task.ID)
		markTerminalMergerFailure(task, terminalMergerFailurePreflight)
		o.recordSpawnFailureEventWithReason(task, "merger", terminalMergerFailurePreflight, err)
		if failErr := o.failTask(task, "merger preflight failed: task has no WorktreeBranch"); failErr != nil {
			return nil, fmt.Errorf("dispatchMerge: %w: %v", errMergerPreflightFailed, failErr)
		}
		return nil, errMergerPreflightFailed
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

	// Resolve optional telemetry coordinates. Correctness does not depend on
	// them: the orchestrator reconciles completion from the target ref.
	orchURL := o.orchURL
	if orchURL == "" {
		orchURL = os.Getenv("DREM_ORCH_URL")
	}
	agentmonToken := o.agentmonToken
	if agentmonToken == "" {
		agentmonToken = os.Getenv("DREM_AGENTMON_TOKEN")
	}
	if strings.TrimSpace(orchURL) == "" || strings.TrimSpace(agentmonToken) == "" {
		orchURL, agentmonToken = "", ""
	}

	artifact, err := currentArtifact(o.db, task.ID)
	if err != nil {
		return nil, fmt.Errorf("dispatchMerge: load authorized delivery artifact: %w", err)
	}
	argv, err := buildMergerArgv(task, o.projectID.String(), defaultBranch, o.testGate.TestCommand, orchURL, agentmonToken, artifact.CommitSHA, artifact.BaseSHA)
	if err != nil {
		// Fail-close: refuse to spawn drem-merger with argv it will
		// reject at parseFlags. Transition the task to FAILED with a
		// first-class operator-facing reason so the state surfaces on
		// the task API without requiring a container log grep.
		// See plans/bug-h-merger-crash-on-v17-advance.md (Option A).
		if errors.Is(err, errMergerSpawnSkippedEmptyTestCmd) {
			markTerminalMergerFailure(task, terminalMergerFailurePreflight)
			o.recordSpawnFailureEventWithReason(task, "merger", terminalMergerFailurePreflight, err)
			if failErr := o.failTask(task, mergerSpawnSkippedReason); failErr != nil {
				return nil, fmt.Errorf("dispatchMerge: fail-fast: %w (fail transition: %v)", err, failErr)
			}
			o.logger.Warn("merger spawn skipped: project has no test command",
				"task_id", task.ID,
				"project_id", o.projectID,
				"hint", "set test_command in drem.toml or register project from a working tree with go.mod/package.json/pyproject.toml/Cargo.toml")
			return nil, err
		}
		return nil, fmt.Errorf("dispatchMerge: build argv: %w", err)
	}

	env := map[string]string{
		"DREM_TASK_ID":        task.ID.String(),
		"DREM_FEATURE_BRANCH": task.WorktreeBranch,
		"DREM_TARGET_BRANCH":  defaultBranch,
		"DREM_WORKER_ID":      workerID,
	}
	// Merger does NOT carry Claude credentials OR a prompt — it is a
	// Go binary (cmd/drem-merger) that takes argv flags and runs no
	// claude CLI. SpawnWorkerParams.CredsMount and PromptMount both
	// stay at the zero value by omission; the promptRequired and
	// credsMountRequired tables in worker_spawn.go both return false
	// for "merger" so a future ordering bug there would not regress
	// this path silently. The same subscription-only policy still
	// applies symmetrically at this spawn boundary: reject
	// ANTHROPIC_API_KEY before the spawn lands so a future env
	// extension cannot sneak an API key onto the merger either.
	// See plans/worker-subscription-auth.md §6 commit 5 and
	// plans/worker-prompt-delivery.md §§5, 8.
	if policyErr := rejectAPIKeyInEnv(env); policyErr != nil {
		o.recordSpawnFailureEventWithReason(task, "merger", spawnPolicyReasonAPIKey, policyErr)
		return nil, fmt.Errorf("dispatchMerge: %w", policyErr)
	}

	params := spawner.SpawnWorkerParams{
		// Project carries the human-readable name (drem.project label);
		// ProjectID carries the stable UUID (drem.project_id label).
		// Agentmon filters on the name env; internal orch filters on
		// the UUID. See plans/dual-label-worker-spawn.md.
		Project:   o.projectName,
		ProjectID: o.projectID.String(),
		AgentType: "merger",
		WorkerID:  workerID,
		Branch:    task.WorktreeBranch,
		Env:       env,
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
	handle, recordErr := workeridentity.NewStore(o.db).RecordSpawn(ctx, workeridentity.SpawnRecord{
		Task:        task,
		ProjectID:   o.projectID,
		AgentType:   "merger",
		WorkerID:    workerID,
		ContainerID: res.ContainerID,
		Image:       params.Image,
		Branch:      task.WorktreeBranch,
	})
	if recordErr != nil {
		return nil, fmt.Errorf("dispatchMerge: record merger identity: %w", recordErr)
	}
	if task.Context == nil {
		task.Context = make(model.JSONField)
	}
	task.Context["current_merge_attempt_id"] = handle.AttemptID.String()
	task.Context["current_merge_container_id"] = res.ContainerID
	task.Context["current_merge_worker_id"] = workerID
	delete(task.Context, "merge_commit")
	delete(task.Context, "merge_conflicts")
	delete(task.Context, "merge_failure_reason")
	delete(task.Context, "merge_test_output")
	delete(task.Context, "merge_result_attempt_id")
	delete(task.Context, "merge_result_container_id")
	if err := o.db.Model(task).Update("context", task.Context).Error; err != nil {
		return nil, fmt.Errorf("dispatchMerge: record current merge attempt context: %w", err)
	}
	o.recordSpawnEventWithWorkerID(task, "merger", res.ContainerID, params.Image, workerID, handle.AttemptID)

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
	if err := o.db.First(task, "id = ?", task.ID).Error; err != nil {
		return nil, fmt.Errorf("dispatchMerge: reload task after merger exit: %w", err)
	}
	ctxAttempt, _ := task.Context["merge_result_attempt_id"].(string)
	ctxContainer, _ := task.Context["merge_result_container_id"].(string)
	if ctxAttempt != handle.AttemptID.String() || ctxContainer != res.ContainerID {
		o.logger.Warn("dispatchMerge: ignoring merge context from non-current attempt",
			"task_id", task.ID,
			"attempt_id", handle.AttemptID,
			"container_id", res.ContainerID,
			"context_attempt_id", ctxAttempt,
			"context_container_id", ctxContainer)
		return result, nil
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
//
// Returns errMergerSpawnSkippedEmptyTestCmd when testCmd is empty after
// trimming whitespace. drem-merger's parseFlags (cmd/drem-merger/main.go)
// rejects empty --test-cmd with exit code 1, producing a container
// crash-loop that is invisible from the orch logs. Failing here instead
// surfaces a first-class failure_reason on the task and skips the spawn
// entirely. See plans/bug-h-merger-crash-on-v17-advance.md (Option A,
// fail-close framing) — the library-side silent-skip contract in
// internal/merger/merger.go is the complementary out-of-scope follow-up.
func buildMergerArgv(task *model.Task, projectID, integrationBranch, testCmd, orchURL, agentmonToken, expectedFeatureSHA, expectedBaseSHA string) ([]string, error) {
	if strings.TrimSpace(testCmd) == "" {
		return nil, errMergerSpawnSkippedEmptyTestCmd
	}
	argv := []string{
		"--feature-branch", task.WorktreeBranch,
		"--project", projectID,
		"--task-id", task.ID.String(),
		"--test-cmd", testCmd,
		"--expected-feature-sha", expectedFeatureSHA,
		"--expected-base-sha", expectedBaseSHA,
	}
	if strings.TrimSpace(orchURL) != "" && strings.TrimSpace(agentmonToken) != "" {
		argv = append(argv, "--orch-url", orchURL, "--agentmon-token", agentmonToken)
	}
	// Only include --integration-branch when it differs from the merger's
	// own default ("master"). For plain main / master the flag is
	// redundant and its omission keeps argv short for the common case.
	if integrationBranch != "" && !defaultIntegrationBranches[integrationBranch] {
		argv = append(argv, "--integration-branch", integrationBranch)
	}
	return argv, nil
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
