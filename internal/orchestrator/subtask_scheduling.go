package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/prompt"
	"gorm.io/gorm"
)

// scheduleSubtasks looks for BACKLOG subtasks — and IN_PROGRESS subtasks
// whose agent has been cleared (e.g. after empty-work retry) — of the parent
// that have their dependencies met and spawns agents for them.
// If phaseFilter is non-empty, only subtasks with a matching Phase are
// considered (used by processTestWriting to limit scheduling to test-phase).
// When experiments are active, variant tasks are preferred based on under-allocation.
func (o *Orchestrator) scheduleSubtasks(parent *model.Task, phaseFilter ...string) error {
	// Never dispatch subtasks for paused parents. Without this guard,
	// the tick loop spawns agents that the pause handler immediately
	// tries (and fails) to stop, creating an infinite dispatch loop.
	if parent.Status == model.StatusPaused {
		return nil
	}
	if err := repairSupersededTestDependencies(o.db, parent.ID); err != nil {
		return fmt.Errorf("schedule subtasks: repair superseded test dependencies: %w", err)
	}

	var subtasks []model.Task
	if err := o.db.Where(
		"parent_task_id = ? AND ((status = ?) OR (status = ? AND assigned_agent_id IS NULL))",
		parent.ID, model.StatusBacklog, model.StatusInProgress,
	).Order("priority DESC").Find(&subtasks).Error; err != nil {
		return fmt.Errorf("schedule subtasks: query: %w", err)
	}

	// Apply phase filter if provided.
	var filterPhase string
	if len(phaseFilter) > 0 && phaseFilter[0] != "" {
		filterPhase = phaseFilter[0]
	}

	// Build filtered candidate list.
	var candidates []model.Task
	for _, sub := range subtasks {
		if filterPhase != "" && sub.Phase != filterPhase {
			continue
		}
		candidates = append(candidates, sub)
	}
	if len(candidates) == 0 {
		return nil
	}

	// A completed test-phase task may intentionally add a production-file
	// scaffold so its tests compile before the implementation exists. File
	// presence and a fuzzy commit-subject match are therefore insufficient
	// evidence that a later implementation task is already complete. Keep the
	// dedup fast path for unrelated prior work, but never let it consume a
	// production task whose declared scope overlaps a completed test scaffold.
	var completedTestTasks []model.Task
	if err := o.db.Where("parent_task_id = ? AND phase = ? AND status = ?",
		parent.ID, "test", model.StatusDone).Find(&completedTestTasks).Error; err != nil {
		return fmt.Errorf("schedule subtasks: query completed test scaffolds: %w", err)
	}
	completedTestScaffoldFiles := make(map[string]struct{})
	for i := range completedTestTasks {
		for _, path := range getEstimatedFiles(completedTestTasks[i].Context) {
			completedTestScaffoldFiles[path] = struct{}{}
		}
	}

	// Experiment-aware ordering: when experiments are active, reorder candidates
	// to prefer variant tasks from under-allocated variants.
	if o.experimentScheduler != nil {
		active, _ := o.experimentScheduler.IsActive()
		if active {
			candidates = o.orderCandidatesByExperimentPriority(candidates)
		}
	}

	// Query in-progress sibling tasks for conflict detection. Exclude tasks
	// that are candidates for dispatch (in_progress with no assigned agent) —
	// otherwise they conflict with themselves and can never be re-dispatched.
	var inProgress []model.Task
	if err := o.db.Where(
		"parent_task_id = ? AND status IN ? AND assigned_agent_id IS NOT NULL",
		parent.ID, []model.TaskStatus{model.StatusInProgress, model.StatusMerging},
	).Find(&inProgress).Error; err != nil {
		return fmt.Errorf("schedule subtasks: query in-progress: %w", err)
	}

	// Evaluate dispatch decisions: dependencies, wave groups, file conflicts.
	policy := NewSchedulingPolicy(o.db)
	if filterPhase != "" {
		policy = policy.WithoutWaveGating()
	}
	decisions := policy.EvaluateDispatch(candidates, inProgress)
	dispatchDecisions := make(map[uuid.UUID]DispatchDecision, len(decisions))
	dispatchableCount := 0
	blockedDecisions := make([]DispatchDecision, 0, len(decisions))
	for _, d := range decisions {
		dispatchDecisions[d.TaskID] = d
		if d.Dispatchable {
			dispatchableCount++
		} else {
			blockedDecisions = append(blockedDecisions, d)
		}
	}
	if dispatchableCount == 0 && len(blockedDecisions) > 0 {
		o.recordSubtaskDispatchBlocked(parent, filterPhase, blockedDecisions)
	}

	for i := range candidates {
		sub := &candidates[i]

		// Content-aware dedup: if the subtask's estimated files already
		// appear in the integration branch and commit messages match,
		// fast-track to done without spawning an agent.
		if estimatedFiles := getEstimatedFiles(sub.Context); len(estimatedFiles) > 0 &&
			!skipExistingWorkDedup(sub.Context) &&
			!overlapsFileSet(estimatedFiles, completedTestScaffoldFiles) {
			featureName := strings.TrimPrefix(parent.WorktreeBranch, "feature/")
			featureDir := o.worktree.FeatureWorktreePath(featureName)
			changedFiles, diffErr := getChangedFiles(featureDir, o.worktree.DefaultBranchName())
			if diffErr == nil && len(changedFiles) > 0 {
				commitMsgs, logErr := getCommitMessages(featureDir, o.worktree.DefaultBranchName())
				if logErr == nil && hasExistingWork(estimatedFiles, changedFiles, commitMsgs, sub.Title) {
					o.logger.Info("schedule: dedup detected existing work, fast-tracking to done",
						"subtask_id", sub.ID)
					if err := o.db.Transaction(func(tx *gorm.DB) error {
						return casAcceptedExistingSubtask(tx, sub, map[string]any{
							"estimated_files": estimatedFiles, "changed_files": changedFiles,
						})
					}); err != nil {
						o.logger.Error("schedule: save subtask", "subtask_id", sub.ID, "error", err)
					}
					continue
				}
			}
		}

		// If subtask was previously assigned an agent, check if its work
		// is already merged into the feature branch before re-spawning.
		if sub.AssignedAgentID != nil {
			featureName := strings.TrimPrefix(parent.WorktreeBranch, "feature/")
			featureDir := o.worktree.FeatureWorktreePath(featureName)
			if o.isWorkAlreadyMerged(sub, featureDir) {
				o.logger.Info("schedule: work already merged, fast-tracking to done",
					"subtask_id", sub.ID)
				if err := o.db.Transaction(func(tx *gorm.DB) error {
					return casAcceptedExistingSubtask(tx, sub, map[string]any{"reason": "already-merged-skip-spawn"})
				}); err != nil {
					o.logger.Error("schedule: accept already-merged subtask", "subtask_id", sub.ID, "error", err)
				}
				continue
			}
		}

		// Check dispatch decision (dependencies, wave groups, file conflicts).
		if d, ok := dispatchDecisions[sub.ID]; ok && !d.Dispatchable {
			o.logger.Debug("subtask dispatch blocked",
				"subtask_id", sub.ID, "reason", d.Reason)
			continue
		}

		// Determine agent type from subtask context.
		agentType := model.AgentCoder
		if sub.Context != nil {
			if atStr, ok := sub.Context["agent_type"].(string); ok {
				if at, err := model.ParseAgentType(atStr); err == nil {
					agentType = at
				}
			}
		}

		// Legacy no-container direct tool intercept. When a spawner is wired,
		// shouldUseDirectToolAgent returns false and sglang-direct coders go
		// through the worker harness so tools execute outside orch.
		if agentType == model.AgentCoder && o.shouldUseDirectToolAgent(sub, agentType) {
			if err := o.processCoderDirect(sub, parent); err != nil {
				o.logger.Error("direct coder dispatch failed", "subtask_id", sub.ID, "error", err)
			}
			continue
		}

		// Prep agent intercept: if the subtask needs task preparation (local
		// model coder with estimated_files), route to the prep agent pipeline
		// instead of spawning a coder directly. The prep agent reads the
		// codebase and produces a tactical brief; when it completes,
		// onPrepCompleted marks the subtask prep_complete and it gets
		// dispatched to a coder on the next tick. This also runs before
		// the runner capacity gate since prep agents use the direct path.
		if agentType == model.AgentCoder && o.needsPrep(sub) {
			if err := o.spawnPrepAgent(sub, parent); err != nil {
				o.logger.Error("spawn prep agent failed, proceeding without prep",
					"subtask_id", sub.ID, "error", err)
				// Graceful degradation: mark prep as failed and let coder proceed.
				if sub.Context == nil {
					sub.Context = make(model.JSONField)
				}
				sub.Context["prep_complete"] = true
				sub.Context["prep_failed"] = true
				if err := o.db.Save(sub).Error; err != nil {
					o.logger.Error("save subtask after prep failure", "subtask_id", sub.ID, "error", err)
				}
				// Fall through to spawn coder without prep data.
			} else {
				// Prep agent spawned successfully — skip coder spawn this tick.
				continue
			}
		}

		// Dispatch routing: container mode (o.Spawner wired) routes
		// through spawnTypedWorker so the worker runs inside a
		// drem-worker-<lang> container and the claude CLI execs from
		// the bind-mounted prompt. Legacy mode (o.Spawner nil) falls
		// through to o.runner.SpawnAgent, which shells out to claude
		// on the host — only viable when the orchestrator runs on a
		// host with claude installed, never inside the orch container.
		// See plans/phase-3.5-subtask-dispatch-migration.md §"Why".
		if o.Spawner != nil {
			// Skip subtasks that are currently being prepped (waiting for prep agent).
			if sub.Context != nil {
				if _, inProgress := sub.Context["prep_in_progress"]; inProgress {
					continue
				}
			}
			if err := o.dispatchSubtaskViaSpawner(sub, agentType); err != nil {
				o.logger.Error("spawn agent for subtask failed",
					"subtask_id", sub.ID, "error", err)
				// spawnTypedWorker already emitted a worker_spawn_failed
				// TaskEvent; do not double-record. Fall through to the
				// next candidate so a single subtask's failure does not
				// starve the rest.
				continue
			}
			continue
		}

		// Legacy host-subprocess path. Retained for development on a
		// host with claude installed (e.g. running `drem` directly
		// against a host sqlite DB) and for tests that exercise the
		// schedule loop without wiring a Spawner. Production runs the
		// container path above.

		// Check subprocess runner capacity. Only applies to non-direct
		// agents (OpenCode/Claude subprocess path). Direct agents already
		// dispatched above bypass this gate.
		if o.runner == nil || !o.runner.CanSpawn() {
			break
		}

		// Skip subtasks that are currently being prepped (waiting for prep agent).
		if sub.Context != nil {
			if _, inProgress := sub.Context["prep_in_progress"]; inProgress {
				continue
			}
		}

		// Use the feature integration worktree for prompt generation context.
		// The actual agent worktree is created inside SpawnAgent.
		featureName := strings.TrimPrefix(parent.WorktreeBranch, "feature/")
		featureDir := o.worktree.FeatureWorktreePath(featureName)

		// Load project for prompt generation.
		var project model.Project
		if err := o.db.First(&project, "id = ?", o.projectID).Error; err != nil {
			return fmt.Errorf("schedule subtasks: load project: %w", err)
		}

		// Build parent context for the prompt.
		parentCtx := map[string]any{
			"parent_title":       parent.Title,
			"parent_description": parent.Description,
			"feature_branch":     parent.WorktreeBranch,
		}

		// Build prompt.
		subComments, _ := o.GetComments(parent.ID)
		agentPrompt := prompt.Generate(prompt.Opts{
			Task:         sub,
			Project:      &project,
			AgentType:    agentType,
			WorktreePath: featureDir,
			Comments:     subComments,
			ParentCtx:    parentCtx,
		})

		// Spawn agent (creates worktree internally).
		ag, err := o.runner.SpawnAgent(sub, featureName, agentType, agentPrompt)
		if err != nil {
			o.logger.Error("spawn agent for subtask failed", "subtask_id", sub.ID, "error", err)
			continue
		}

		// Verify agent record was created in the DB.
		var verifyAgent model.Agent
		if err := o.db.Where("current_task_id = ? AND status = ?",
			sub.ID, model.AgentWorking).First(&verifyAgent).Error; err != nil {
			o.logger.Error("agent record missing after spawn",
				"subtask", sub.Title, "error", err)
			if err := o.failTask(sub, "agent record not found after spawn"); err != nil {
				o.logger.Error("schedule: fail subtask after missing agent", "subtask_id", sub.ID, "error", err)
			}
			continue
		}

		sub.AssignedAgentID = &ag.ID
		if err := o.transitionTaskAtomic(sub, model.StatusInProgress, "orchestrator", "worker_dispatch",
			"worker claimed subtask", map[string]any{"agent_id": ag.ID.String(), "agent_type": string(agentType)}); err != nil {
			return fmt.Errorf("schedule subtasks: claim subtask: %w", err)
		}

		o.emit("subtask_scheduled", map[string]any{
			"task_id":    sub.ID,
			"agent_id":   ag.ID,
			"agent_type": agentType,
		})
		o.publishTaskTransition(sub.ID.String(), string(model.StatusBacklog), string(sub.Status), "subtask scheduled")
		o.publishAgentStatus(sub.ID.String(), ag.ID.String(), string(agentType), string(model.AgentWorking))
		o.logger.Info("subtask scheduled", "subtask_id", sub.ID, "agent_id", ag.ID, "type", agentType)
	}

	return nil
}

// repairSupersededTestDependencies reconnects implementation work to the
// accepted revision of a rejected test task. Test-review rejection clones the
// test and preserves TestsFor, but historical plans point implementation
// DependencyIDs at the original row. Without this reconciliation the parent
// can return to in_progress while every implementation remains permanently
// blocked on a rejected dependency.
func repairSupersededTestDependencies(db *gorm.DB, parentID uuid.UUID) error {
	var siblings []model.Task
	if err := db.Where("parent_task_id = ?", parentID).Order("created_at DESC, id DESC").Find(&siblings).Error; err != nil {
		return err
	}
	byID := make(map[string]model.Task, len(siblings))
	completedTestFor := make(map[string]string)
	for _, sibling := range siblings {
		byID[sibling.ID.String()] = sibling
		if sibling.Phase != "test" || sibling.Status != model.StatusDone {
			continue
		}
		for _, implementationID := range sibling.TestsFor {
			if _, exists := completedTestFor[implementationID]; !exists {
				completedTestFor[implementationID] = sibling.ID.String()
			}
		}
	}
	for i := range siblings {
		sibling := &siblings[i]
		if sibling.Phase == "test" || len(sibling.DependencyIDs) == 0 {
			continue
		}
		replacementID := completedTestFor[sibling.ID.String()]
		if replacementID == "" {
			continue
		}
		changed := false
		for j, dependencyID := range sibling.DependencyIDs {
			dependency, exists := byID[dependencyID]
			if exists && dependency.Phase == "test" && dependency.Status == model.StatusRejected {
				sibling.DependencyIDs[j] = replacementID
				changed = true
			}
		}
		if changed {
			if err := db.Model(&model.Task{}).Where("id = ?", sibling.ID).Update("dependency_ids", sibling.DependencyIDs).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

func overlapsFileSet(paths []string, files map[string]struct{}) bool {
	for _, path := range paths {
		if _, ok := files[path]; ok {
			return true
		}
	}
	return false
}

func skipExistingWorkDedup(ctx model.JSONField) bool {
	if ctx == nil {
		return false
	}
	skip, _ := ctx["skip_existing_work_dedup"].(bool)
	return skip
}

func (o *Orchestrator) recordSubtaskDispatchBlocked(parent *model.Task, filterPhase string, decisions []DispatchDecision) {
	counts := make(map[string]int)
	details := make([]map[string]any, 0, len(decisions))
	for _, d := range decisions {
		reasonClass := classifyDispatchBlockReason(d.Reason)
		counts[reasonClass]++
		details = append(details, map[string]any{
			"task_id": d.TaskID.String(),
			"reason":  d.Reason,
		})
	}
	sort.Slice(details, func(i, j int) bool {
		return fmt.Sprint(details[i]["task_id"]) < fmt.Sprint(details[j]["task_id"])
	})
	blockedState := model.JSONField{
		"phase_filter": filterPhase,
		"counts":       counts,
		"blocked":      details,
	}
	signature := blockedEvidenceSignature(blockedState)
	if parent.Context == nil {
		parent.Context = make(model.JSONField)
	}
	previousSignature, _ := parent.Context["subtask_dispatch_blocked_signature"].(string)
	parent.Context["subtask_dispatch_blocked"] = blockedState
	parent.Context["subtask_dispatch_blocked_signature"] = signature
	if err := o.db.Save(parent).Error; err != nil {
		o.logger.Error("record subtask dispatch blocked state", "parent_id", parent.ID, "error", err)
		return
	}
	if previousSignature == signature {
		return
	}

	event := &model.TaskEvent{
		ID:        uuid.New(),
		TaskID:    parent.ID,
		EventType: "subtask_dispatch_blocked",
		OldValue:  string(parent.Status),
		NewValue:  string(parent.Status),
		Details: model.JSONField{
			"phase_filter": filterPhase,
			"counts":       counts,
			"blocked":      details,
		},
		Actor:     "orchestrator",
		CreatedAt: time.Now(),
	}
	if err := model.ValidateTaskEventDetails(event.Details); err != nil {
		o.logger.Error("record subtask dispatch blocked event validation", "parent_id", parent.ID, "error", err)
		return
	}
	if err := o.db.Create(event).Error; err != nil {
		o.logger.Error("record subtask dispatch blocked event", "parent_id", parent.ID, "error", err)
	}
	o.emit("subtask_dispatch_blocked", map[string]any{
		"task_id":      parent.ID,
		"phase_filter": filterPhase,
		"counts":       counts,
		"blocked":      details,
	})
}

func blockedEvidenceSignature(fields model.JSONField) string {
	b, err := json.Marshal(fields)
	if err != nil {
		return fmt.Sprint(fields)
	}
	return string(b)
}

func classifyDispatchBlockReason(reason string) string {
	switch {
	case strings.Contains(reason, "unmet dependencies"):
		return "dependencies"
	case strings.Contains(reason, "wave group"):
		return "wave"
	case strings.Contains(reason, "file conflict"):
		return "file_conflict"
	default:
		return "other"
	}
}

// dispatchSubtaskViaSpawner routes a single subtask through the
// container-mode spawner (spawnTypedWorker). It mirrors the post-spawn
// bookkeeping the legacy path performs — fast-track transitions from
// BACKLOG through PLANNING and PLAN_REVIEW to IN_PROGRESS, plus the
// subtask_scheduled event, task_transition and agent_status publishes,
// and the Info log line. The Agent row is created by spawnTypedWorker
// (via worker identity recording) with the container handle;
// this function reloads the subtask after the spawn call so the
// scheduler picks up the assignment that worker identity recording wrote.
//
// Errors are returned to the caller, which logs them and continues to
// the next candidate — a single subtask's failure must not starve the
// rest. A non-nil error from spawnTypedWorker already carries a
// worker_spawn_failed audit event emitted inside that method, so this
// function does not duplicate the event write.
//
// See plans/phase-3.5-subtask-dispatch-migration.md §"Migration recipe".
func (o *Orchestrator) dispatchSubtaskViaSpawner(sub *model.Task, agentType model.AgentType) error {
	if o.Spawner == nil {
		return fmt.Errorf("dispatchSubtaskViaSpawner: o.Spawner is nil")
	}

	ctx := context.Background()
	previousStatus := sub.Status
	launch, err := o.workerLaunchService().Launch(ctx, sub, agentType)
	if err != nil {
		if errors.Is(err, errWorkerImageUnavailable) {
			if failErr := o.failTask(sub, err.Error()); failErr != nil {
				return fmt.Errorf("fail subtask after worker image preflight: %w", failErr)
			}
		}
		return fmt.Errorf("spawn %s via spawner: %w", agentType, err)
	}

	// Reload the subtask so AssignedAgentID (written by
	// the worker launch service during the spawn) and the task row's
	// container-carrying Agent handle are visible to the rest of the
	// scheduling loop and the downstream publishers.
	if err := o.db.First(sub, "id = ?", sub.ID).Error; err != nil {
		return fmt.Errorf("reload subtask after container spawn: %w", err)
	}
	if sub.AssignedAgentID == nil {
		// worker identity recording should always populate this; if it
		// did not, treat it as a spawn failure and fail the subtask so
		// the operator surfaces the gap rather than seeing a silent
		// stall. Mirrors the legacy path's "agent record not found
		// after spawn" failure mode, just with a container-specific
		// reason string.
		if err := o.failTask(sub, "agent record not found after container spawn"); err != nil {
			o.logger.Error("schedule: fail subtask after missing agent",
				"subtask_id", sub.ID, "error", err)
		}
		return fmt.Errorf("agent assignment missing after container spawn for subtask %s", sub.ID)
	}

	if previousStatus != model.StatusInProgress {
		if err := o.transitionTaskAtomic(sub, model.StatusInProgress, "orchestrator", "worker_dispatch",
			"container worker claimed subtask", map[string]any{"agent_id": launch.AgentID.String(), "agent_type": string(agentType)}); err != nil {
			return fmt.Errorf("claim subtask after container spawn: %w", err)
		}
	}

	o.emit("subtask_scheduled", map[string]any{
		"task_id":    sub.ID,
		"agent_id":   launch.AgentID,
		"agent_type": agentType,
	})
	o.publishTaskTransition(sub.ID.String(), string(previousStatus),
		string(sub.Status), "subtask scheduled")
	o.publishAgentStatus(sub.ID.String(), launch.AgentID.String(),
		string(agentType), string(model.AgentWorking))
	o.logger.Info("subtask scheduled via spawner",
		"subtask_id", sub.ID, "agent_id", launch.AgentID, "type", agentType)
	return nil
}

// dispatchPendingSubtasks is a catch-all that finds backlog subtasks whose
// parents were not processed by the status-specific handlers in doTick
// (e.g. parent in BACKLOG after replan, or parent in PLANNING with
// leftover subtasks from a previous plan cycle). It dispatches subtasks
// for any parent in a non-terminal status, skipping parents that were
// already handled (IN_PROGRESS, TEST_WRITING).
func (o *Orchestrator) dispatchPendingSubtasks() {
	// Find distinct parent IDs that have backlog subtasks in this project.
	type parentIDRow struct {
		ParentTaskID uuid.UUID
	}
	var rows []parentIDRow
	if err := o.db.Model(&model.Task{}).
		Select("DISTINCT parent_task_id").
		Where("project_id = ? AND status = ? AND parent_task_id IS NOT NULL",
			o.projectID, model.StatusBacklog,
		).Scan(&rows).Error; err != nil {
		o.logger.Error("dispatch pending subtasks: query parent IDs", "error", err)
		return
	}

	for _, row := range rows {
		var parent model.Task
		if err := o.db.First(&parent, "id = ?", row.ParentTaskID).Error; err != nil {
			continue
		}

		// Skip terminal, paused, and approval-gated parents — their subtasks
		// should not be dispatched until the gate is explicitly advanced.
		if isTerminal(parent.Status) || parent.Status == model.StatusPaused || parent.Status.IsApprovalGate() {
			continue
		}

		// Skip parents already handled by the main doTick handlers.
		if parent.Status == model.StatusInProgress || parent.Status == model.StatusTestWriting {
			continue
		}

		if err := o.scheduleSubtasks(&parent); err != nil {
			o.logger.Error("dispatch pending subtasks", "parent_id", parent.ID, "error", err)
		}
	}
}

// orderCandidatesByExperimentPriority reorders candidates to prefer variant tasks
// from under-allocated experiment variants. Non-experiment tasks are moved to
// the end. Within experiment tasks, those from more under-allocated variants
// come first.
func (o *Orchestrator) orderCandidatesByExperimentPriority(candidates []model.Task) []model.Task {
	if o.experimentScheduler == nil {
		return candidates
	}

	// Get under-allocated variants
	underAllocated, err := o.experimentScheduler.GetUnderAllocatedVariants()
	if err != nil {
		o.logger.Warn("failed to get under-allocated variants", "error", err)
		return candidates
	}

	// Build a priority map: task ID -> priority (lower is higher priority)
	priorityMap := make(map[uuid.UUID]int)
	priority := 0

	// First, add experiment tasks from under-allocated variants
	for _, va := range underAllocated {
		variant := va.Variant
		// Find tasks that belong to this variant
		for i := range candidates {
			if candidates[i].ID == variant.TaskID {
				priorityMap[candidates[i].ID] = priority
				priority++
			}
		}
	}

	// Add remaining experiment tasks (from variants at capacity)
	for i := range candidates {
		task := &candidates[i]
		if _, exists := priorityMap[task.ID]; exists {
			continue
		}
		isExperimentTask, _, _ := o.experimentScheduler.IsExperimentTask(task.ID)
		if isExperimentTask {
			priorityMap[task.ID] = priority
			priority++
		}
	}

	// Add non-experiment tasks at the end
	for i := range candidates {
		task := &candidates[i]
		if _, exists := priorityMap[task.ID]; exists {
			continue
		}
		priorityMap[task.ID] = priority
		priority++
	}

	// Sort candidates by priority
	sort.SliceStable(candidates, func(i, j int) bool {
		return priorityMap[candidates[i].ID] < priorityMap[candidates[j].ID]
	})

	return candidates
}
