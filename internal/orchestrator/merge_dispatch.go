package orchestrator

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/spawner"
)

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
	// ContainerID is the merger container that produced the result, recorded
	// so the audit trail in user story 49 can correlate merge outcomes with
	// the specific worker that produced them.
	ContainerID string
	ExitCode    int
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
	if o.worktree != nil && o.worktree.DefaultBranch != "" {
		defaultBranch = o.worktree.DefaultBranch
	}

	workerID := fmt.Sprintf("merger-%s-%s", task.ID.String()[:shortIDLen], uuid.New().String()[:shortIDLen])
	bareRepo := ""
	if o.worktree != nil {
		bareRepo = o.worktree.BareRepoPath
	}
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
		ContainerID: res.ContainerID,
		ExitCode:    finalState.ExitCode,
		Success:     finalState.ExitCode == 0 && finalState.Status == string(spawnerStatusExited),
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
