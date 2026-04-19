package orchestrator

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/container"
	"github.com/godinj/drem-orchestrator/internal/model"
)

// replacementCap is the per-task maximum number of container replacements
// the orchestrator will attempt inside a rolling one-hour window. Beyond
// the cap the task transitions to failed — unbounded respawning would
// mask structural issues (user story 14 in containerization PRD).
const replacementCap = 3

// replacementWindow is the rolling window over which replacementCap is
// enforced. Reset on each window expiry so long-running tasks remain
// recoverable.
const replacementWindow = time.Hour

// replacementTracker records recent respawn timestamps per task so the
// per-hour cap can be enforced without a database round-trip on every
// event. Entries older than replacementWindow are dropped on access.
type replacementTracker struct {
	mu sync.Mutex
	// timestamps maps taskID → slice of attempt times, newest last.
	timestamps map[uuid.UUID][]time.Time
}

// newReplacementTracker creates a fresh tracker ready for use.
func newReplacementTracker() *replacementTracker {
	return &replacementTracker{timestamps: make(map[uuid.UUID][]time.Time)}
}

// recordAndCount appends now to the task's slice after pruning expired
// entries, returning the post-append count. Callers compare the count
// against replacementCap to decide whether to respawn or fail.
func (t *replacementTracker) recordAndCount(taskID uuid.UUID, now time.Time) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	cutoff := now.Add(-replacementWindow)
	prior := t.timestamps[taskID]
	fresh := prior[:0]
	for _, ts := range prior {
		if ts.After(cutoff) {
			fresh = append(fresh, ts)
		}
	}
	fresh = append(fresh, now)
	t.timestamps[taskID] = fresh
	return len(fresh)
}

// watchDockerEvents subscribes to Docker lifecycle events scoped to this
// orchestrator's project and routes die/OOM events into handleWorkerDeath.
// It blocks until ctx is cancelled or the underlying runtime terminates
// the subscription. Returns nil on clean shutdown.
func (o *Orchestrator) watchDockerEvents(ctx context.Context) error {
	if o.Runtime == nil {
		return fmt.Errorf("watchDockerEvents: no Runtime configured")
	}
	filter := container.EventFilter{
		Labels: map[string]string{"drem.project": o.projectID.String()},
	}
	ch, err := o.Runtime.SubscribeEvents(ctx, filter)
	if err != nil {
		return fmt.Errorf("subscribe docker events: %w", err)
	}

	tracker := newReplacementTracker()
	o.logger.Info("docker event watcher started", "project_id", o.projectID)

	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-ch:
			if !ok {
				o.logger.Info("docker event channel closed")
				return nil
			}
			o.dispatchEvent(ctx, ev, tracker)
		}
	}
}

// dispatchEvent routes a single Docker event to the appropriate handler.
// EventStart is a no-op; EventDie with non-zero exit or OOMKilled triggers
// handleWorkerDeath; EventDie with exit 0 records normal completion.
func (o *Orchestrator) dispatchEvent(ctx context.Context, ev container.Event, tracker *replacementTracker) {
	switch ev.Type {
	case container.EventStart:
		// Nothing to do — spawns already record their own audit row.
		return
	case container.EventDie, container.EventOOM:
		taskID, ok := parseTaskIDFromLabels(ev.Labels)
		if !ok {
			o.logger.Warn("docker event without drem.task_id label",
				"event", ev.Type, "container_id", ev.ContainerID)
			return
		}
		o.recordContainerLifecycleEvent(taskID, ev)

		// Exit 0 is a normal completion: no replacement, no respawn.
		// Authoritative OOMKilled flag overrides any zero exit code.
		if ev.ExitCode == 0 && !ev.OOMKilled {
			return
		}

		// Abnormal exit — look up the task and respawn if still active.
		var task model.Task
		if err := o.db.First(&task, "id = ?", taskID).Error; err != nil {
			o.logger.Warn("docker event: task not found", "task_id", taskID, "error", err)
			return
		}
		if isTerminal(task.Status) {
			return
		}
		o.handleWorkerDeath(ctx, &task, ev, tracker)
	case container.EventDestroy:
		// Destroy is emitted post-Destroy; no state machine impact.
		return
	}
}

// parseTaskIDFromLabels extracts the drem.task_id label value as a UUID.
// Returns ok=false for missing or malformed labels so callers can skip
// the event without panicking.
func parseTaskIDFromLabels(labels map[string]string) (uuid.UUID, bool) {
	raw, ok := labels["drem.task_id"]
	if !ok || raw == "" {
		return uuid.UUID{}, false
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.UUID{}, false
	}
	return id, true
}

// handleWorkerDeath is invoked for any EventDie whose exit code is non-zero
// or whose OOMKilled flag is set. It respawns a replacement coder for the
// task unless the per-hour cap has been reached, in which case the task
// transitions to failed (user story 14).
func (o *Orchestrator) handleWorkerDeath(ctx context.Context, task *model.Task, ev container.Event, tracker *replacementTracker) {
	now := time.Now()
	attempts := tracker.recordAndCount(task.ID, now)
	if attempts > replacementCap {
		reason := fmt.Sprintf("worker death cap exceeded (%d/%d in %s)",
			attempts, replacementCap, replacementWindow)
		if err := o.failTask(task, reason); err != nil {
			o.logger.Error("handle worker death: fail task", "task_id", task.ID, "error", err)
		}
		return
	}

	o.logger.Warn("worker death detected, respawning",
		"task_id", task.ID, "container_id", ev.ContainerID,
		"exit_code", ev.ExitCode, "oom", ev.OOMKilled, "attempt", attempts)

	if err := o.spawnCoder(ctx, task); err != nil {
		o.logger.Error("handle worker death: respawn failed",
			"task_id", task.ID, "error", err)
	}
}

// recordContainerLifecycleEvent writes a TaskEvent row for every observed
// container death so the audit trail (user story 49 + Kyle's "what happened
// when") is complete regardless of whether the death triggered a respawn.
func (o *Orchestrator) recordContainerLifecycleEvent(taskID uuid.UUID, ev container.Event) {
	detail := model.JSONField{
		"container_id": ev.ContainerID,
		"exit_code":    ev.ExitCode,
		"oom_killed":   ev.OOMKilled,
		"event_type":   string(ev.Type),
	}
	evt := &model.TaskEvent{
		ID:        uuid.New(),
		TaskID:    taskID,
		EventType: "container_died",
		OldValue:  "",
		NewValue:  string(ev.Type),
		Details:   detail,
		Actor:     "docker-events",
		CreatedAt: time.Now(),
	}
	if err := o.db.Create(evt).Error; err != nil {
		o.logger.Error("record container death event", "task_id", taskID, "error", err)
	}
}
