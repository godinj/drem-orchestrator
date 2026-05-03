package orchestrator

import (
	"context"
	"fmt"
	"time"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/spawner"
	"github.com/godinj/drem-orchestrator/internal/workeridentity"
)

// reconcileOnStartup reconstructs in-flight state at orchestrator start by
// cross-referencing tasks whose assigned agent carries a container ID
// against the spawner's live ListWorkers result. The three possible
// outcomes per task are:
//
//   - Task has a recorded container ID that IS in the live list → nothing to
//     do; the event watcher will continue receiving lifecycle events.
//   - Task has a recorded container ID that is NOT in the live list → the
//     container was destroyed out-of-band (or the spawner was restarted);
//     respawn a fresh worker so work resumes.
//   - Task has no recorded container ID (pre-containerization rows) → leave
//     alone; the legacy worktree path handles those tasks.
//
// This runs once on Run() before the event watcher loop starts so the
// initial reconciliation is never racing live events.
func (o *Orchestrator) reconcileOnStartup(ctx context.Context) error {
	if o.Spawner == nil {
		return nil
	}

	live, err := o.Spawner.ListWorkers(ctx, spawner.ListWorkersParams{ProjectID: o.projectID.String()})
	if err != nil {
		return fmt.Errorf("reconcileOnStartup: list workers: %w", err)
	}
	liveIDs := make(map[string]bool, len(live.Workers))
	for _, w := range live.Workers {
		liveIDs[w.ContainerID] = true
	}

	// Only actionable statuses can own a live container; others are either
	// terminal or haven't been dispatched yet.
	inflight, err := o.loadInflightTasks()
	if err != nil {
		return err
	}

	respawned := 0
	for i := range inflight {
		task := &inflight[i]
		handle, err := workeridentity.NewStore(o.db).ForTask(ctx, task)
		if err != nil || !handle.HasContainer() {
			// Older task with no container — legacy path handles it.
			continue
		}
		if liveIDs[handle.ContainerID] {
			// Container is alive; event watcher will pick up future transitions.
			o.logger.Debug("reconcileOnStartup: container alive, reattach",
				"task_id", task.ID, "container_id", handle.ContainerID)
			continue
		}
		// Container is gone; respawn to resume the task.
		o.logger.Warn("reconcileOnStartup: container missing, respawning",
			"task_id", task.ID, "container_id", handle.ContainerID)
		if err := o.respawnForTask(ctx, task); err != nil {
			o.logger.Error("reconcileOnStartup: respawn", "task_id", task.ID, "error", err)
			continue
		}
		respawned++
	}
	if respawned > 0 {
		o.logger.Info("reconcileOnStartup: respawned workers for tasks with missing containers",
			"count", respawned)
	}
	return nil
}

// loadInflightTasks returns tasks in actionable statuses that may own a live
// container. Terminal and pre-dispatch statuses are skipped.
func (o *Orchestrator) loadInflightTasks() ([]model.Task, error) {
	var tasks []model.Task
	actionable := []model.TaskStatus{
		model.StatusPlanning,
		model.StatusTestWriting,
		model.StatusInProgress,
		model.StatusTestingReady,
		model.StatusMerging,
	}
	err := o.db.Where("project_id = ? AND status IN ?", o.projectID, actionable).
		Find(&tasks).Error
	if err != nil {
		return nil, fmt.Errorf("loadInflightTasks: query: %w", err)
	}
	return tasks, nil
}

// respawnForTask dispatches a replacement worker whose agent type matches
// the task's current lifecycle stage. Callers invoke this from startup
// reconciliation when the previously-recorded container is no longer live.
func (o *Orchestrator) respawnForTask(ctx context.Context, task *model.Task) error {
	// Defensive: clear assignment so spawnCoder creates a fresh agent row
	// rather than binding to the stale one whose container is gone.
	task.AssignedAgentID = nil
	task.UpdatedAt = time.Now()
	if err := o.db.Save(task).Error; err != nil {
		return fmt.Errorf("respawnForTask: clear assignment: %w", err)
	}

	switch task.Status {
	case model.StatusPlanning, model.StatusTestWriting, model.StatusInProgress:
		return o.spawnCoder(ctx, task)
	case model.StatusTestingReady:
		return o.spawnReviewer(ctx, task)
	case model.StatusMerging:
		// Merging tasks re-dispatch via dispatchMerge on the next tick; no
		// action required here — leaving the task assigned-less lets
		// dispatchMerges re-enter via executeMerge.
		return nil
	default:
		return nil
	}
}
