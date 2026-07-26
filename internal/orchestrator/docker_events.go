package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/container"
	"github.com/godinj/drem-orchestrator/internal/gitexec"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/spawner"
	"github.com/godinj/drem-orchestrator/internal/state"
	"gorm.io/gorm/clause"
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

const workerLifecycleInspectTimeout = 2 * time.Second

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

func (o *Orchestrator) replacementTracker() *replacementTracker {
	if o.workerReplacements == nil {
		o.workerReplacements = newReplacementTracker()
	}
	return o.workerReplacements
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
			o.dispatchEvent(ctx, ev, o.replacementTracker())
		}
	}
}

// reconcileWorkerAttemptLifecycles consumes terminal worker state from the
// spawner during the normal tick. The spawner is the sole Docker owner in the
// containerized deployment, so waiting for stale leases to infer a worker's
// completion throws away an authoritative fact that is already available.
//
// ListWorkers is the cheap project-wide status sample. InspectWorker is used
// only for terminal entries because it carries the exact exit code, timestamps,
// and OOM bit. Missing or temporarily uninspectable containers are left to the
// recovery reconciler; absence is not proof of death.
func (o *Orchestrator) reconcileWorkerAttemptLifecycles(ctx context.Context) {
	if o.Spawner == nil {
		return
	}

	var attempts []model.WorkerAttempt
	if err := o.db.Table("worker_attempts").
		Joins("JOIN tasks ON tasks.id = worker_attempts.task_id").
		Where("tasks.project_id = ? AND worker_attempts.state IN ? AND worker_attempts.container_id <> ''",
			o.projectID, []string{model.WorkerAttemptReserved, model.WorkerAttemptRunning}).
		Find(&attempts).Error; err != nil {
		o.logger.Error("worker lifecycle poll: query active attempts", "error", err)
		return
	}
	if len(attempts) == 0 {
		return
	}

	// Bound the entire remote sample, not every container independently. A
	// slow spawner must not stretch one orchestrator tick by N×timeout.
	pollCtx, cancel := context.WithTimeout(ctx, workerLifecycleInspectTimeout)
	defer cancel()
	live, err := o.Spawner.ListWorkers(pollCtx, spawner.ListWorkersParams{ProjectID: o.projectID.String()})
	if err != nil {
		o.logger.Warn("worker lifecycle poll: list workers", "error", err)
		return
	}

	observed := make(map[string]spawner.WorkerInfo)
	for _, worker := range live.Workers {
		observed[worker.ContainerID] = worker
	}

	for i := range attempts {
		attempt := &attempts[i]
		worker, listed := observed[attempt.ContainerID]
		if listed && worker.Status != string(container.StatusExited) && worker.Status != string(container.StatusDead) && worker.Status != string(container.StatusRemoved) {
			continue
		}
		// A spawner restart loses its in-memory inventory, so an absent list
		// entry is not terminal evidence. Inspect the exact persisted
		// container ID: Docker can distinguish a still-running container from
		// one that was authoritatively removed.
		state, inspectErr := o.Spawner.InspectWorker(pollCtx, spawner.InspectWorkerParams{ContainerID: attempt.ContainerID})
		if inspectErr != nil {
			o.logger.Warn("worker lifecycle poll: inspect worker",
				"attempt_id", attempt.ID, "container_id", attempt.ContainerID, "error", inspectErr)
			continue
		}
		if state.Status != string(container.StatusExited) && state.Status != string(container.StatusDead) && state.Status != string(container.StatusRemoved) {
			continue
		}
		exitCode := state.ExitCode
		if state.Status == string(container.StatusRemoved) {
			exitCode = 1
		}
		ev := container.Event{
			Type:           container.EventDie,
			ContainerID:    attempt.ContainerID,
			ExitCode:       exitCode,
			OOMKilled:      state.OOMKilled,
			Usage:          state.Usage,
			UsageInspected: true,
			Timestamp:      state.FinishedAt,
			Labels: map[string]string{
				"drem.task_id":    attempt.TaskID.String(),
				"drem.worker_id":  attempt.WorkerID,
				"drem.agent_type": attempt.AgentType,
			},
		}
		o.dispatchEvent(ctx, ev, o.replacementTracker())
	}
}

// dispatchEvent routes a single Docker event to the appropriate handler.
// EventStart is a no-op; EventDie with non-zero exit or OOMKilled triggers
// handleWorkerDeath; EventDie with exit 0 records normal completion.
func (o *Orchestrator) dispatchEvent(ctx context.Context, ev container.Event, tracker *replacementTracker) {
	o.workerLifecycleMu.Lock()
	defer o.workerLifecycleMu.Unlock()

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

		// Exit 0 is completion evidence for the current attempt. Authoritative
		// OOMKilled flag overrides any zero exit code.
		if ev.ExitCode == 0 && !ev.OOMKilled {
			o.handleWorkerExitZero(ctx, taskID, ev)
			return
		}

		// Abnormal exit — look up the task and respawn if still active.
		var task model.Task
		if err := o.db.First(&task, "id = ?", taskID).Error; err != nil {
			o.logger.Warn("docker event: task not found", "task_id", taskID, "error", err)
			return
		}
		attempt, ok := o.workerAttemptForDeathEvent(task.ID, ev)
		if !ok {
			o.logger.Warn("docker death event has no matching worker attempt",
				"task_id", task.ID, "container_id", ev.ContainerID,
				"worker_id", ev.Labels["drem.worker_id"])
			return
		}
		ev.Usage = o.captureWorkerUsage(ctx, attempt, ev)
		if err := o.recordAttemptTerminalObservation(attempt, ev); err != nil {
			o.logger.Error("docker death event: persist terminal observation",
				"task_id", task.ID, "attempt_id", attempt.ID, "error", err)
			return
		}
		if attempt.AgentType == string(model.AgentMerger) && attempt.AgentID == nil {
			if err := o.finalizeObservedAttempt(attempt, ev); err != nil {
				o.logger.Error("docker death event: finalize merger attempt",
					"task_id", task.ID, "attempt_id", attempt.ID, "error", err)
			}
			return
		}
		if isTerminal(task.Status) {
			if activeWorkerAttempt(attempt) {
				if err := o.finalizeObservedAttempt(attempt, ev); err != nil {
					o.logger.Error("docker death event: finalize late attempt for terminal task",
						"task_id", task.ID, "attempt_id", attempt.ID, "error", err)
				}
			}
			return
		}
		if !currentAssignedAttempt(&task, attempt) {
			if activeWorkerAttempt(attempt) {
				if err := o.finalizeObservedAttempt(attempt, ev); err != nil {
					o.logger.Error("docker death event: finalize stale attempt",
						"task_id", task.ID, "attempt_id", attempt.ID, "error", err)
				}
			}
			o.logger.Info("ignoring stale docker death event for non-current attempt",
				"task_id", task.ID, "container_id", ev.ContainerID,
				"worker_id", ev.Labels["drem.worker_id"], "attempt_id", attempt.ID)
			return
		}
		if !activeWorkerAttempt(attempt) {
			o.logger.Info("ignoring duplicate docker death event for finalized attempt",
				"task_id", task.ID, "container_id", ev.ContainerID, "attempt_id", attempt.ID)
			return
		}
		o.handleWorkerDeath(ctx, &task, attempt, ev, tracker)
	case container.EventDestroy:
		// Destroy is emitted post-Destroy; no state machine impact.
		return
	}
}

func (o *Orchestrator) handleWorkerExitZero(ctx context.Context, taskID uuid.UUID, ev container.Event) {
	var task model.Task
	if err := o.db.First(&task, "id = ?", taskID).Error; err != nil {
		o.logger.Warn("docker completion event: task not found", "task_id", taskID, "error", err)
		return
	}
	attempt, ok := o.workerAttemptForDeathEvent(task.ID, ev)
	if !ok {
		o.recordWorkerCompletionEvidence(taskID, nil, ev, "ignored", "unmatched_attempt")
		o.logger.Warn("docker completion event has no matching worker attempt",
			"task_id", task.ID, "container_id", ev.ContainerID,
			"worker_id", ev.Labels["drem.worker_id"])
		return
	}
	ev.Usage = o.captureWorkerUsage(ctx, attempt, ev)
	if err := o.recordAttemptTerminalObservation(attempt, ev); err != nil {
		o.recordWorkerCompletionEvidence(taskID, attempt, ev, "failed", "terminal_observation_persist_failed")
		o.logger.Error("docker completion event: persist terminal observation",
			"task_id", task.ID, "attempt_id", attempt.ID, "error", err)
		return
	}
	if attempt.AgentType == string(model.AgentMerger) && attempt.AgentID == nil {
		if err := o.finalizeObservedAttempt(attempt, ev); err != nil {
			o.recordWorkerCompletionEvidence(taskID, attempt, ev, "failed", "merger_attempt_finalize_failed")
			o.logger.Error("docker completion event: finalize merger attempt",
				"task_id", task.ID, "attempt_id", attempt.ID, "error", err)
			return
		}
		o.recordWorkerCompletionEvidence(taskID, attempt, ev, "accepted", "merger_exit_zero")
		return
	}
	if isTerminal(task.Status) || task.Status == model.StatusTestingReady {
		// The task effect may have committed before the attempt finalization.
		// A repeated authoritative terminal observation closes that narrow
		// crash window without replaying the task transition.
		if activeWorkerAttempt(attempt) && attempt.AgentID != nil &&
			(task.Status == model.StatusDone || task.Status == model.StatusTestingReady) {
			if err := o.finalizeObservedAttempt(attempt, ev); err != nil {
				o.recordWorkerCompletionEvidence(taskID, attempt, ev, "failed", "attempt_replay_finalize_failed")
				o.logger.Error("docker completion event: finalize replayed attempt",
					"task_id", task.ID, "attempt_id", attempt.ID, "error", err)
				return
			}
			o.recordWorkerCompletionEvidence(taskID, attempt, ev, "accepted", "task_effect_already_applied")
			return
		}
		if activeWorkerAttempt(attempt) {
			if err := o.finalizeObservedAttempt(attempt, ev); err != nil {
				o.recordWorkerCompletionEvidence(taskID, attempt, ev, "failed", "terminal_task_attempt_finalize_failed")
				o.logger.Error("docker completion event: finalize late attempt for terminal task",
					"task_id", task.ID, "attempt_id", attempt.ID, "error", err)
				return
			}
		}
		o.recordWorkerCompletionEvidence(taskID, attempt, ev, "ignored", "terminal_task")
		return
	}
	if !currentAssignedAttempt(&task, attempt) {
		if activeWorkerAttempt(attempt) {
			if err := o.finalizeObservedAttempt(attempt, ev); err != nil {
				o.recordWorkerCompletionEvidence(taskID, attempt, ev, "failed", "stale_attempt_finalize_failed")
				o.logger.Error("docker completion event: finalize stale attempt",
					"task_id", task.ID, "attempt_id", attempt.ID, "error", err)
				return
			}
		}
		o.recordWorkerCompletionEvidence(taskID, attempt, ev, "ignored", "stale_attempt")
		o.logger.Info("ignoring stale docker completion event for non-current attempt",
			"task_id", task.ID, "container_id", ev.ContainerID,
			"worker_id", ev.Labels["drem.worker_id"], "attempt_id", attempt.ID)
		return
	}
	if !activeWorkerAttempt(attempt) {
		o.recordWorkerCompletionEvidence(taskID, attempt, ev, "ignored", "finalized_attempt")
		return
	}
	if attempt.AgentID == nil {
		o.recordWorkerCompletionEvidence(taskID, attempt, ev, "ignored", "attempt_without_agent")
		return
	}

	if err := o.synthesizeCompletion(*attempt.AgentID); err != nil {
		o.recordWorkerCompletionEvidence(taskID, attempt, ev, "failed", "completion_synthesis_failed")
		o.logger.Error("docker completion event: synthesize completion", "task_id", task.ID, "attempt_id", attempt.ID, "error", err)
		return
	}

	var accepted model.Task
	if err := o.db.First(&accepted, "id = ?", taskID).Error; err != nil {
		o.recordWorkerCompletionEvidence(taskID, attempt, ev, "failed", "accepted_task_reload_failed")
		o.logger.Error("docker completion event: reload accepted task", "task_id", task.ID, "attempt_id", attempt.ID, "error", err)
		return
	}
	if accepted.Status != model.StatusDone && accepted.Status != model.StatusTestingReady {
		if testContractReworkDispatched(&accepted, attempt) {
			o.recordWorkerCompletionEvidence(taskID, attempt, ev, "accepted", "test_contract_rework_dispatched")
			return
		}
		o.recordWorkerCompletionEvidence(taskID, attempt, ev, "failed", "branch_acceptance_pending")
		return
	}
	if err := o.finalizeObservedAttempt(attempt, ev); err != nil {
		o.recordWorkerCompletionEvidence(taskID, attempt, ev, "failed", "attempt_completion_update_failed")
		o.logger.Error("docker completion event: mark attempt completed", "task_id", task.ID, "attempt_id", attempt.ID, "error", err)
		return
	}
	o.recordWorkerCompletionEvidence(taskID, attempt, ev, "accepted", "exit_zero_current_attempt")
}

func testContractReworkDispatched(task *model.Task, prior *model.WorkerAttempt) bool {
	if task == nil || prior == nil || task.Context == nil || task.AssignedAgentID == nil || prior.AgentID == nil {
		return false
	}
	if *task.AssignedAgentID == *prior.AgentID || task.Status != model.StatusInProgress {
		return false
	}
	_, ok := task.Context["test_contract_rework"].(map[string]any)
	return ok
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

func activeWorkerAttempt(attempt *model.WorkerAttempt) bool {
	if attempt == nil || attempt.CompletedAt != nil {
		return false
	}
	return attempt.State == model.WorkerAttemptReserved || attempt.State == model.WorkerAttemptRunning
}

func (o *Orchestrator) recordAttemptTerminalObservation(attempt *model.WorkerAttempt, ev container.Event) error {
	if attempt == nil {
		return nil
	}
	o.attemptTerminalMu.Lock()
	defer o.attemptTerminalMu.Unlock()
	at := ev.Timestamp
	if at.IsZero() {
		at = time.Now()
	}
	event := model.AttemptEvent{
		ID: uuid.New(), TaskID: attempt.TaskID, AttemptID: attempt.ID,
		State: attempt.State, Type: "terminal_observed", Actor: "spawner-lifecycle", CreatedAt: at,
		Details: model.JSONField{
			"container_id":      attempt.ContainerID,
			"worker_id":         attempt.WorkerID,
			"exit_code":         ev.ExitCode,
			"oom_killed":        ev.OOMKilled,
			"normalized_reason": normalizedDockerExitReason(ev),
		},
	}
	return o.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&event).Error
}

func (o *Orchestrator) finalizeObservedAttempt(attempt *model.WorkerAttempt, ev container.Event) error {
	if attempt == nil {
		return nil
	}
	at := ev.Timestamp
	if at.IsZero() {
		at = time.Now()
	}
	updates := map[string]any{"completed_at": &at}
	if ev.ExitCode == 0 && !ev.OOMKilled {
		updates["state"] = model.WorkerAttemptCompleted
	} else {
		updates["state"] = model.WorkerAttemptFailed
		updates["failed_at"] = &at
		updates["failure_classification"] = normalizeFailureClass(normalizedDockerExitReason(ev), fmt.Sprintf("exit_code=%d oom=%t", ev.ExitCode, ev.OOMKilled))
		updates["first_error"] = fmt.Sprintf("container %s exited with code %d (oom=%t)", ev.ContainerID, ev.ExitCode, ev.OOMKilled)
	}
	res := o.db.Model(&model.WorkerAttempt{}).
		Where("id = ? AND task_id = ? AND completed_at IS NULL", attempt.ID, attempt.TaskID).
		Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected > 0 {
		attempt.CompletedAt = &at
		attempt.State = updates["state"].(string)
	}
	return nil
}

func (o *Orchestrator) handleWorkerDeath(ctx context.Context, task *model.Task, attempt *model.WorkerAttempt, ev container.Event, tracker *replacementTracker) {
	now := time.Now()
	o.markWorkerAttemptDead(task, attempt, ev, now)
	if priorBranchAcceptanceRejected(task) {
		summary := "worker retry interrupted after a prior deterministic branch-scope rejection"
		budget := consumeRetryBudget(task, retryEdgeForTask(*task, attempt.AgentType), failureClassBranchContam, summary, now)
		if err := o.failTaskWithFailureEvidence(task,
			"worker branch rejected by deterministic scope acceptance", failureClassBranchContam, summary, now, budget); err != nil {
			o.logger.Error("handle worker death: fail task after prior branch rejection", "task_id", task.ID, "error", err)
		}
		return
	}
	failureReason := normalizedDockerExitReason(ev)
	if ev.Usage != nil && strings.TrimSpace(ev.Usage.StopReason) != "" {
		failureReason = ev.Usage.StopReason
	}
	failureClass := normalizeFailureClass(failureReason, fmt.Sprintf("exit_code=%d oom=%t", ev.ExitCode, ev.OOMKilled))
	if checkpoint, sha, err := o.attemptCheckpoint(ctx, attempt); err != nil {
		o.logger.Warn("worker death: inspect checkpoint", "task_id", task.ID, "attempt_id", attempt.ID, "error", err)
	} else if checkpoint {
		summary := fmt.Sprintf("worker stopped after creating checkpoint %s on %s; preserved for bounded continuation or artifact handoff", sha, attempt.Branch)
		if task.Context == nil {
			task.Context = make(model.JSONField)
		}
		task.Context["checkpoint_handoff"] = map[string]any{
			"attempt_id": attempt.ID.String(), "branch": attempt.Branch, "sha": sha,
			"original_failure_class": failureClass, "original_failure_reason": failureReason,
		}
		budget := consumeRetryBudget(task, retryEdgeForTask(*task, attempt.AgentType), failureClassArtifactHandoff, summary, now)
		if err := o.failTaskWithFailureEvidence(task, "worker checkpoint requires bounded artifact handoff", failureClassArtifactHandoff, summary, now, budget); err != nil {
			o.logger.Error("handle worker death: preserve checkpoint handoff", "task_id", task.ID, "error", err)
			return
		}
		if shouldAutoContinueCheckpoint(task, attempt, failureReason) {
			if err := o.ResumeFailedCheckpoint(task.ID, sha, "orchestrator:checkpoint-continuation"); err == nil {
				o.logger.Info("worker checkpoint automatically resumed", "task_id", task.ID, "attempt_id", attempt.ID, "commit_sha", sha)
				return
			} else {
				o.logger.Info("worker checkpoint not eligible for automatic continuation; preserving handoff", "task_id", task.ID, "attempt_id", attempt.ID, "error", err)
			}
		}
		return
	}
	budget := consumeRetryBudget(task, retryEdgeForTask(*task, attempt.AgentType), failureClass,
		fmt.Sprintf("worker %s container %s exited with %s (stop_reason=%s)", attempt.AgentType, ev.ContainerID, failureClass, failureReason), now)
	if budget.Exhausted {
		reason := fmt.Sprintf("retry budget exhausted for %s on %s after %d failure(s)", failureClass, budget.Edge, budget.Attempts)
		if err := o.failTaskWithFailureEvidence(task, reason, failureClass, budget.LastSummary, now, budget); err != nil {
			o.logger.Error("handle worker death: fail task after retry budget", "task_id", task.ID, "error", err)
		}
		return
	}
	if err := o.db.Save(task).Error; err != nil {
		o.logger.Error("handle worker death: save retry budget", "task_id", task.ID, "error", err)
		return
	}
	attempts := tracker.recordAndCount(task.ID, now)
	if attempts > replacementCap {
		reason := fmt.Sprintf("worker death cap exceeded (%d/%d in %s)",
			attempts, replacementCap, replacementWindow)
		if err := o.failTaskWithFailureEvidence(task, reason, failureClass, budget.LastSummary, now, budget); err != nil {
			o.logger.Error("handle worker death: fail task", "task_id", task.ID, "error", err)
		}
		return
	}

	o.logger.Warn("worker death detected, respawning",
		"task_id", task.ID, "container_id", ev.ContainerID,
		"exit_code", ev.ExitCode, "oom", ev.OOMKilled, "attempt", attempts)

	if err := o.respawnWorkerRole(ctx, task, attempt.AgentType); err != nil {
		if errors.Is(err, errWorkerImageUnavailable) {
			if failErr := o.failTask(task, err.Error()); failErr != nil {
				o.logger.Error("handle worker death: fail after worker image preflight", "task_id", task.ID, "error", failErr)
			}
			return
		}
		o.logger.Error("handle worker death: respawn failed",
			"task_id", task.ID, "error", err)
	}
}

func shouldAutoContinueCheckpoint(task *model.Task, attempt *model.WorkerAttempt, failureReason string) bool {
	if task == nil || attempt == nil || task.ParentTaskID == nil || strings.TrimSpace(attempt.RenderedPromptHash) == "" || strings.TrimSpace(attempt.RenderedPromptPath) == "" {
		return false
	}
	switch strings.TrimSpace(failureReason) {
	case "token_budget", "timeout", "context_limit":
		return true
	default:
		return false
	}
}

func (o *Orchestrator) attemptCheckpoint(ctx context.Context, attempt *model.WorkerAttempt) (bool, string, error) {
	if o.worktree == nil || attempt == nil || strings.TrimSpace(attempt.BaseSHA) == "" || strings.TrimSpace(attempt.Branch) == "" {
		return false, "", nil
	}
	repo := o.worktree.BareRepo()
	count, err := gitexec.RunGit(ctx, repo, "rev-list", "--count", attempt.BaseSHA+".."+attempt.Branch)
	if err != nil {
		return false, "", err
	}
	if strings.TrimSpace(count) == "0" {
		return false, "", nil
	}
	sha, err := gitexec.RunGit(ctx, repo, "rev-parse", attempt.Branch)
	if err != nil {
		return false, "", err
	}
	return true, strings.TrimSpace(sha), nil
}

func priorBranchAcceptanceRejected(task *model.Task) bool {
	if task == nil || task.Context == nil {
		return false
	}
	detail, ok := task.Context["branch_acceptance"].(map[string]any)
	if !ok {
		return false
	}
	accepted, present := detail["accepted"].(bool)
	return present && !accepted
}

func (o *Orchestrator) respawnWorkerRole(ctx context.Context, task *model.Task, role string) error {
	if role == string(model.AgentMerger) {
		return nil
	}
	if _, err := o.workerLaunchService().Launch(ctx, task, model.AgentType(role)); err != nil {
		return fmt.Errorf("launch replacement %s: %w", role, err)
	}
	return nil
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
	attempt, _ := o.workerAttemptForDeathEvent(taskID, ev)
	normalizedReason := normalizedDockerExitReason(ev)
	detail := model.JSONField{
		"container_id":      ev.ContainerID,
		"exit_code":         ev.ExitCode,
		"oom_killed":        ev.OOMKilled,
		"event_type":        string(ev.Type),
		"worker_id":         ev.Labels["drem.worker_id"],
		"agent_type":        ev.Labels["drem.agent_type"],
		"normalized_reason": normalizedReason,
	}
	if attempt != nil {
		detail["attempt_id"] = attempt.ID.String()
		detail["attempt_state"] = attempt.State
	}
	evt := &model.TaskEvent{
		ID:        uuid.New(),
		TaskID:    taskID,
		EventType: "container_died",
		OldValue:  "",
		NewValue:  normalizedReason,
		Details:   detail,
		Actor:     "docker-events",
		CreatedAt: time.Now(),
	}
	if err := o.db.Create(evt).Error; err != nil {
		o.logger.Error("record container death event", "task_id", taskID, "error", err)
	}
}

func normalizedDockerExitReason(ev container.Event) string {
	if labelReason := ev.Labels["drem.failure_class"]; labelReason != "" {
		return normalizeFailureClass(labelReason, ev.Labels["drem.exit_summary"])
	}
	if ev.OOMKilled {
		return "infra_oom_killed"
	}
	if ev.ExitCode == 0 {
		return "exit_zero"
	}
	switch ev.ExitCode {
	case 124:
		return "infra_timeout"
	case 126, 127:
		return "tool_command_failure"
	case 130, 137, 143:
		return "infra_terminated"
	default:
		return "tool_exit_nonzero"
	}
}

func (o *Orchestrator) recordWorkerCompletionEvidence(taskID uuid.UUID, attempt *model.WorkerAttempt, ev container.Event, outcome, normalizedReason string) {
	evidence := state.Evidence{
		TaskID:           taskID,
		Actor:            "docker-events",
		Source:           "docker_exit_zero",
		Reason:           outcome,
		NormalizedReason: normalizedReason,
		Timestamp:        time.Now(),
		References: map[string]any{
			"container_id": ev.ContainerID,
			"exit_code":    ev.ExitCode,
			"worker_id":    ev.Labels["drem.worker_id"],
			"agent_type":   ev.Labels["drem.agent_type"],
			"outcome":      outcome,
		},
	}
	if attempt != nil {
		evidence.AttemptID = attempt.ID
	}
	evt := &model.TaskEvent{
		ID:        uuid.New(),
		TaskID:    taskID,
		EventType: "worker_completion_evidence",
		Details: model.JSONField{
			"evidence": map[string]any{
				"task_id":           evidence.TaskID.String(),
				"attempt_id":        evidence.AttemptID.String(),
				"actor":             evidence.Actor,
				"source":            evidence.Source,
				"reason":            evidence.Reason,
				"normalized_reason": evidence.NormalizedReason,
				"timestamp":         evidence.Timestamp.Format(time.RFC3339Nano),
				"references":        evidence.References,
			},
		},
		Actor:     evidence.Actor,
		CreatedAt: evidence.Timestamp,
	}
	if evidence.AttemptID == uuid.Nil {
		evt.Details["evidence"].(map[string]any)["attempt_id"] = ""
	}
	if err := o.db.Create(evt).Error; err != nil {
		o.logger.Error("record worker completion evidence", "task_id", taskID, "error", err)
	}
}
