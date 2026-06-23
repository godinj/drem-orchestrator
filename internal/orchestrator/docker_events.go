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
	// Filter on the stable UUID label (drem.project_id) so a project
	// rename never drops events. drem.project carries the human-readable
	// name and is consumed by agentmon; the orch side uses the UUID.
	// See plans/dual-label-worker-spawn.md.
	filter := container.EventFilter{
		Labels: map[string]string{"drem.project_id": o.projectID.String()},
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
		attempt, ok := o.workerAttemptForDeathEvent(task.ID, ev)
		if !ok {
			o.logger.Warn("docker death event has no matching worker attempt",
				"task_id", task.ID, "container_id", ev.ContainerID,
				"worker_id", ev.Labels["drem.worker_id"])
			return
		}
		if !currentAssignedAttempt(&task, attempt) {
			o.logger.Info("ignoring stale docker death event for non-current attempt",
				"task_id", task.ID, "container_id", ev.ContainerID,
				"worker_id", ev.Labels["drem.worker_id"], "attempt_id", attempt.ID)
			return
		}
		o.handleWorkerDeath(ctx, &task, attempt, ev, tracker)
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
func (o *Orchestrator) workerAttemptForDeathEvent(taskID uuid.UUID, ev container.Event) (*model.WorkerAttempt, bool) {
	var attempt model.WorkerAttempt
	if ev.ContainerID != "" {
		if err := o.db.Where("task_id = ? AND container_id = ?", taskID, ev.ContainerID).
			Order("created_at DESC").First(&attempt).Error; err == nil {
			return &attempt, true
		}
	}
	if workerID := ev.Labels["drem.worker_id"]; workerID != "" {
		if err := o.db.Where("task_id = ? AND worker_id = ?", taskID, workerID).
			Order("created_at DESC").First(&attempt).Error; err == nil {
			return &attempt, true
		}
	}
	return nil, false
}

func currentAssignedAttempt(task *model.Task, attempt *model.WorkerAttempt) bool {
	if task == nil || attempt == nil || task.AssignedAgentID == nil || attempt.AgentID == nil {
		return false
	}
	return *task.AssignedAgentID == *attempt.AgentID
}

func (o *Orchestrator) handleWorkerDeath(ctx context.Context, task *model.Task, attempt *model.WorkerAttempt, ev container.Event, tracker *replacementTracker) {
	now := time.Now()
	o.markWorkerAttemptDead(task, attempt, ev, now)
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

	if err := o.respawnWorkerRole(ctx, task, attempt.AgentType); err != nil {
		o.logger.Error("handle worker death: respawn failed",
			"task_id", task.ID, "error", err)
	}
}

func (o *Orchestrator) respawnWorkerRole(ctx context.Context, task *model.Task, role string) error {
	switch role {
	case string(model.AgentCoder):
		return o.spawnCoder(ctx, task)
	case string(model.AgentReviewer):
		return o.spawnReviewer(ctx, task)
	case string(model.AgentFixer):
		return o.spawnFixer(ctx, task)
	case "supervisor":
		return o.spawnSupervisor(ctx, task)
	case string(model.AgentMerger):
		return nil
	default:
		return fmt.Errorf("unknown worker role %q for respawn", role)
	}
}

func (o *Orchestrator) markWorkerAttemptDead(task *model.Task, attempt *model.WorkerAttempt, ev container.Event, now time.Time) {
	if task == nil || attempt == nil || task.AssignedAgentID == nil || attempt.AgentID == nil || *task.AssignedAgentID != *attempt.AgentID {
		return
	}
	var ag model.Agent
	if err := o.db.First(&ag, "id = ?", *attempt.AgentID).Error; err != nil {
		o.logger.Warn("worker death: load assigned agent", "task_id", task.ID, "agent_id", *attempt.AgentID, "error", err)
		return
	}
	if ev.ContainerID != "" && ag.TmuxSession != "" && ev.ContainerID != ag.TmuxSession {
		o.logger.Info("ignoring docker death event for mismatched current agent container",
			"task_id", task.ID, "agent_id", ag.ID, "event_container_id", ev.ContainerID,
			"agent_container_id", ag.TmuxSession, "attempt_id", attempt.ID)
		return
	}
	ag.Status = model.AgentDead
	ag.CurrentTaskID = nil
	ag.CompletedAt = &now
	if ev.OOMKilled {
		ag.ExitReason = model.ExitReasonKilled
	} else {
		ag.ExitReason = model.ExitReasonError
	}
	if ag.Config == nil {
		ag.Config = model.JSONField{}
	}
	ag.Config["container_id"] = ev.ContainerID
	ag.Config["exit_code"] = float64(ev.ExitCode)
	ag.Config["oom_killed"] = ev.OOMKilled
	ag.Config["exit_reason"] = ag.ExitReason
	if err := o.db.Save(&ag).Error; err != nil {
		o.logger.Error("worker death: mark agent dead", "task_id", task.ID, "agent_id", ag.ID, "error", err)
	}
	attempt.State = model.WorkerAttemptFailed
	attempt.CompletedAt = &now
	if err := o.db.Save(attempt).Error; err != nil {
		o.logger.Error("worker death: mark attempt failed", "task_id", task.ID, "attempt_id", attempt.ID, "error", err)
	}
	if task.AssignedAgentID == nil || *task.AssignedAgentID != *attempt.AgentID {
		return
	}
	task.AssignedAgentID = nil
	if err := o.db.Save(task).Error; err != nil {
		o.logger.Error("worker death: clear task assignment", "task_id", task.ID, "error", err)
	}
	o.publishAgentStatus(task.ID.String(), ag.ID.String(), string(ag.AgentType), string(model.AgentDead))
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
		"worker_id":    ev.Labels["drem.worker_id"],
		"agent_type":   ev.Labels["drem.agent_type"],
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
