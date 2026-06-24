package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/gitexec"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/prompt"
	"github.com/godinj/drem-orchestrator/internal/state"
)

// processBacklog transitions a task from BACKLOG to PLANNING.
// Quick fix tasks skip planning and go directly to IN_PROGRESS.
// When replanning (PlanFeedback is set), it detaches old subtasks first
// to prevent the planner from seeing stale done subtasks and auto-advancing.
// When experiments are active, normal (non-experiment) tasks are blocked.
func (o *Orchestrator) processBacklog(task *model.Task) error {
	if task.Category.IsQuickFix() {
		return o.processQuickFix(task)
	}

	// Experiment-aware scheduling: block normal tasks when experiments are active.
	if o.experimentScheduler != nil {
		canSchedule, reason, err := o.experimentScheduler.CanScheduleTask(task.ID)
		if err != nil {
			o.logger.Warn("experiment scheduler check failed", "task_id", task.ID, "error", err)
		} else if !canSchedule {
			o.logger.Debug("task blocked by experiment scheduler", "task_id", task.ID, "reason", reason)
			return nil
		}
	}

	if task.PlanFeedback != "" {
		var oldSubtasks []model.Task
		o.db.Where("parent_task_id = ?", task.ID).Find(&oldSubtasks)
		if len(oldSubtasks) > 0 {
			for i := range oldSubtasks {
				oldSubtasks[i].ParentTaskID = nil
				o.db.Save(&oldSubtasks[i])
			}
			slog.Info("detached old subtasks for replanning",
				"task_id", task.ID, "count", len(oldSubtasks))
		}
	}

	event, err := state.TransitionTask(task, model.StatusPlanning, "orchestrator", nil)
	if err != nil {
		return fmt.Errorf("process backlog: %w", err)
	}
	if err := o.db.Save(task).Error; err != nil {
		return fmt.Errorf("process backlog: save task: %w", err)
	}
	if err := o.db.Create(event).Error; err != nil {
		return fmt.Errorf("process backlog: save event: %w", err)
	}
	o.emit("task_updated", task)
	o.publishTaskTransition(task.ID.String(), event.OldValue, event.NewValue, "")
	o.logger.Info("task transitioned to planning", "task_id", task.ID, "title", task.Title)
	return nil
}

// processPlanning handles tasks in the PLANNING state by either transitioning
// them to PLAN_REVIEW (if a plan exists), monitoring an assigned planner agent,
// or spawning a new planner.
func (o *Orchestrator) processPlanning(task *model.Task) error {
	// 0. If plan exists but PlanFeedback requests a re-plan, clear the stale
	// plan so a new planner is spawned below. Without this, the same plan
	// would be auto-advanced to PLAN_REVIEW without addressing the feedback.
	if task.Plan != nil && task.PlanFeedback != "" {
		o.logger.Info("clearing stale plan for replan with feedback",
			"task_id", task.ID, "feedback", task.PlanFeedback)
		task.Plan = nil
		task.AssignedAgentID = nil
		if err := o.db.Save(task).Error; err != nil {
			return fmt.Errorf("process planning: save cleared plan: %w", err)
		}
	}

	// 1. If plan already exists, evaluate for clarification needs.
	if task.Plan != nil {
		if err := o.ensureFeatureWorktree(task, "process planning"); err != nil {
			return err
		}

		dec := o.decideClarification(task)
		if dec.CapReached {
			// Round cap hit — record it for observability then fall through to plan_review.
			if task.Context == nil {
				task.Context = make(model.JSONField)
			}
			task.Context["clarification_cap_reached"] = true
			o.logger.Info("clarification round cap reached, skipping to plan_review",
				"task_id", task.ID, "rounds", dec.Rounds)
		} else if !dec.Proceed {
			// Clarification needed — set context and transition.
			if task.Context == nil {
				task.Context = make(model.JSONField)
			}
			task.Context["clarification_session"] = dec.SessionData
			task.Context["clarification_questions"] = dec.Questions
			if len(dec.Questions) > 0 {
				task.Context["clarification_current_question"] = dec.Questions[0]
			}
			task.Context["clarification_rounds"] = float64(dec.Rounds + 1)
			event, transErr := state.TransitionTask(task, model.StatusNeedsClarification, "orchestrator", nil)
			if transErr != nil {
				return fmt.Errorf("process planning: transition to needs_clarification: %w", transErr)
			}
			if err := o.db.Save(task).Error; err != nil {
				return fmt.Errorf("process planning: save task: %w", err)
			}
			if err := o.db.Create(event).Error; err != nil {
				return fmt.Errorf("process planning: save event: %w", err)
			}
			o.emit("needs_clarification", map[string]any{"task_id": task.ID, "questions": dec.Questions})
			o.publishTaskTransition(task.ID.String(), event.OldValue, event.NewValue, "needs clarification")
			return nil
		}

		// No clarification needed (or cap reached) — proceed to plan_review.
		event, err := state.TransitionTask(task, model.StatusPlanReview, "orchestrator", nil)
		if err != nil {
			return fmt.Errorf("process planning: transition to plan_review: %w", err)
		}
		if err := o.db.Save(task).Error; err != nil {
			return fmt.Errorf("process planning: save task: %w", err)
		}
		if err := o.db.Create(event).Error; err != nil {
			return fmt.Errorf("process planning: save event: %w", err)
		}
		o.emit("plan_ready", map[string]any{"task_id": task.ID})
		o.publishTaskTransition(task.ID.String(), event.OldValue, event.NewValue, "plan ready for review")
		return nil
	}

	// 2. If an agent is assigned, check if it's still running.
	if task.AssignedAgentID != nil {
		var ag model.Agent
		if err := o.db.First(&ag, "id = ?", task.AssignedAgentID).Error; err != nil {
			// Agent record missing — clear assignment.
			o.logger.Warn("assigned planner agent not found, clearing", "task_id", task.ID, "agent_id", task.AssignedAgentID)
			task.AssignedAgentID = nil
			retries := o.incrementRetryCount(task)
			if retries >= MaxPlannerRetries {
				return o.failTask(task, "planner agent disappeared after max retries")
			}
			return o.db.Save(task).Error
		}

		// If agent is dead or idle (finished without plan), clean up its
		// worktree, clear assignment, and maybe retry.
		if ag.Status == model.AgentDead || ag.Status == model.AgentIdle {
			if ag.WorktreeBranch != "" {
				if err := o.worktree.RemoveAgentWorktree(ag.WorktreeBranch); err != nil {
					o.logger.Warn("cleanup dead planner worktree failed", "agent_id", ag.ID, "error", err)
				}
			}
			task.AssignedAgentID = nil
			retries := o.incrementRetryCount(task)
			if retries >= MaxPlannerRetries {
				return o.failTask(task, "planner agent failed after max retries")
			}
			o.logger.Warn("planner agent dead/idle, will retry", "task_id", task.ID, "retries", retries)
			return o.db.Save(task).Error
		}

		// Agent is still working — do nothing (recoverStuckAgents handles fallback).
		return nil
	}

	// 3. No agent assigned — spawn a planner if capacity allows.

	// Hard cap: prevent runaway planner spawns across all cycles.
	totalSpawns := 0
	if task.Context != nil {
		if v, ok := task.Context["total_planner_spawns"].(float64); ok {
			totalSpawns = int(v)
		}
	}
	if totalSpawns >= MaxTotalPlannerSpawns {
		return o.blockPlannerCapacityExhausted(task, totalSpawns)
	}

	if !o.runner.CanSpawn() {
		return nil // wait for capacity
	}

	// Load project for prompt context.
	var project model.Project
	if err := o.db.First(&project, "id = ?", o.projectID).Error; err != nil {
		return fmt.Errorf("process planning: load project: %w", err)
	}

	// Create feature worktree if needed.
	if task.WorktreeBranch == "" {
		featureName := taskFeatureName(task)
		wtInfo, err := o.worktree.CreateFeature(featureName)
		if err != nil {
			return fmt.Errorf("process planning: create feature: %w", err)
		}

		// Generate repo map in the new feature worktree (non-blocking on failure).
		o.worktree.GenerateRepoMapAsync(wtInfo.Path)

		task.WorktreeBranch = wtInfo.Branch
		if err := o.db.Save(task).Error; err != nil {
			return fmt.Errorf("process planning: save worktree branch: %w", err)
		}
	}

	// HTTP path: when the planner provider resolves to claude AND a warm
	// drem-planner endpoint is configured, POST the plan request to the
	// long-lived planner container (plans/warm-planner-pivot.md).
	// Otherwise fall through to the legacy runner.SpawnAgent path so
	// operator overrides and sandboxes without a warm planner still work.
	if o.shouldDispatchPlanHTTP() {
		plannerPrompt := o.plannerPromptFor(task, &project)
		if err := o.spawnPlannerHTTP(task, &project, plannerPrompt); err != nil {
			return fmt.Errorf("process planning: dispatch plan http: %w", err)
		}
		o.logger.Info("planner http: dispatched", "task_id", task.ID)
		return nil
	}

	// Generate planner prompt.
	featureName := strings.TrimPrefix(task.WorktreeBranch, "feature/")
	featureDir := o.worktree.FeatureWorktreePath(featureName)
	comments, _ := o.GetComments(task.ID)
	// Resolve the target coder model so the planner can adjust detail level.
	var targetProvider, targetModel string
	if o.runner != nil {
		coderCfg := o.runner.AgentConfig(model.AgentCoder)
		targetProvider = string(coderCfg.EffectiveProvider())
		targetModel = coderCfg.Model
	}
	plannerPrompt := prompt.Generate(prompt.Opts{
		Task:                task,
		Project:             &project,
		AgentType:           model.AgentPlanner,
		WorktreePath:        featureDir,
		Comments:            comments,
		TargetCoderProvider: targetProvider,
		TargetCoderModel:    targetModel,
	})

	// Spawn planner agent (legacy path).
	ag, err := o.runner.SpawnAgent(task, featureName, model.AgentPlanner, plannerPrompt)
	if err != nil {
		return fmt.Errorf("process planning: spawn planner: %w", err)
	}

	task.AssignedAgentID = &ag.ID
	if task.Context == nil {
		task.Context = make(model.JSONField)
	}
	task.Context["total_planner_spawns"] = float64(totalSpawns + 1)
	if err := o.db.Save(task).Error; err != nil {
		return fmt.Errorf("process planning: save assigned agent: %w", err)
	}

	retryCount := 0
	if v, ok := task.Context["retry_count"].(float64); ok {
		retryCount = int(v)
	}
	o.emit("planner_spawned", map[string]any{
		"task_id":              task.ID,
		"agent_id":             ag.ID,
		"total_planner_spawns": totalSpawns + 1,
		"retry_count":          retryCount,
	})
	o.publishAgentStatus(task.ID.String(), ag.ID.String(), string(model.AgentPlanner), string(model.AgentWorking))
	o.logger.Info("planner spawned", "task_id", task.ID, "agent_id", ag.ID,
		"total_planner_spawns", totalSpawns+1)
	return nil
}

func (o *Orchestrator) ensureFeatureWorktree(task *model.Task, caller string) error {
	if task.WorktreeBranch != "" {
		return nil
	}
	featureName := taskFeatureName(task)
	wtInfo, err := o.worktree.CreateFeature(featureName)
	if err != nil {
		return fmt.Errorf("%s: create feature: %w", caller, err)
	}
	o.worktree.GenerateRepoMapAsync(wtInfo.Path)
	task.WorktreeBranch = wtInfo.Branch
	if err := o.db.Save(task).Error; err != nil {
		return fmt.Errorf("%s: save worktree branch: %w", caller, err)
	}
	return nil
}

func (o *Orchestrator) blockPlannerCapacityExhausted(task *model.Task, totalSpawns int) error {
	if task.Context == nil {
		task.Context = make(model.JSONField)
	}
	alreadyBlocked, _ := task.Context["planner_capacity_exhausted"].(bool)
	task.Context["planner_capacity_exhausted"] = true
	task.Context["planner_capacity_reason"] = "planner_capacity_exhausted"
	task.Context["planner_capacity_message"] = fmt.Sprintf(
		"planner spawn cap reached (%d/%d); task remains in planning for recoverable backpressure",
		totalSpawns, MaxTotalPlannerSpawns)
	task.Context["planner_capacity_total_spawns"] = float64(totalSpawns)
	task.Context["planner_capacity_max_spawns"] = float64(MaxTotalPlannerSpawns)
	if err := o.db.Save(task).Error; err != nil {
		return fmt.Errorf("process planning: save planner capacity block: %w", err)
	}
	if alreadyBlocked {
		return nil
	}
	now := time.Now()
	event := &model.TaskEvent{
		ID:        uuid.New(),
		TaskID:    task.ID,
		EventType: "planner_capacity_exhausted",
		OldValue:  string(model.StatusPlanning),
		NewValue:  string(model.StatusPlanning),
		Details: model.JSONField{
			"reason":        "planner_capacity_exhausted",
			"total_spawns":  totalSpawns,
			"max_spawns":    MaxTotalPlannerSpawns,
			"recovery_hint": "wait for planner capacity or operator recovery; do not mark task failed for admission backpressure",
		},
		Actor:     "orchestrator",
		CreatedAt: now,
	}
	if err := model.ValidateTaskEventDetails(event.Details); err != nil {
		return fmt.Errorf("process planning: validate planner capacity event: %w", err)
	}
	if err := o.db.Create(event).Error; err != nil {
		return fmt.Errorf("process planning: save planner capacity event: %w", err)
	}
	o.emit("planner_capacity_exhausted", map[string]any{
		"task_id":      task.ID,
		"total_spawns": totalSpawns,
		"max_spawns":   MaxTotalPlannerSpawns,
	})
	o.logger.Warn("planner capacity exhausted; task remains in planning",
		"task_id", task.ID, "total_spawns", totalSpawns, "max_spawns", MaxTotalPlannerSpawns)
	return nil
}

// findCurrentGroup returns the earliest group that has subtasks not yet in a
// terminal wave state. Returns nil if all groups are complete.
// If a task ID in the schedule no longer exists (e.g. after replanning),
// it is treated as terminal to avoid permanently blocking the wave.
func (o *Orchestrator) findCurrentGroup(parent *model.Task, schedule Schedule) *SubtaskGroup {
	for i := range schedule.Groups {
		group := &schedule.Groups[i]
		allTerminal := true
		for _, taskID := range group.TaskIDs {
			var sub model.Task
			if err := o.db.Select("status").First(&sub, "id = ?", taskID).Error; err != nil {
				// Subtask not found (deleted or replanned) — treat as
				// terminal so this group doesn't block forever.
				o.logger.Warn("wave schedule references missing subtask",
					"parent_id", parent.ID, "subtask_id", taskID)
				continue
			}
			if !isTerminalWaveStatus(sub.Status) {
				allTerminal = false
				break
			}
		}
		if !allTerminal {
			return group
		}
	}
	return nil
}

// checkFeatureCompletion checks whether all subtasks of a parent are DONE and
// transitions the parent accordingly. The parent only fails when ALL subtasks
// are terminal (done or failed) and at least one is failed. While any subtask
// is still in_progress, planning, or backlog, the parent stays in_progress.
func (o *Orchestrator) checkFeatureCompletion(parent *model.Task) error {
	var subtasks []model.Task
	if err := o.db.Where("parent_task_id = ?", parent.ID).Find(&subtasks).Error; err != nil {
		return fmt.Errorf("check feature completion: query: %w", err)
	}

	if len(subtasks) == 0 {
		return nil
	}

	allTerminal := true
	anyFailed := false
	allDone := true

	for _, sub := range subtasks {
		switch sub.Status {
		case model.StatusDone:
			// good
		case model.StatusCancelled:
			// Superseded subtasks are terminal and should not block the active generation.
		case model.StatusFailed:
			anyFailed = true
			allDone = false
		default:
			allTerminal = false
			allDone = false
		}
	}

	if allDone && parent.Status == model.StatusInProgress {
		readiness, err := o.evaluateParentReadiness(parent, model.StatusTestingReady)
		if err != nil {
			return fmt.Errorf("check feature completion: parent readiness: %w", err)
		}
		if !readiness.Ready {
			if err := o.recordParentReadinessBlocked(parent, model.StatusTestingReady, readiness); err != nil {
				return fmt.Errorf("check feature completion: save readiness blockers: %w", err)
			}
			o.logger.Info("testing_ready blocked by parent readiness", "task_id", parent.ID, "blockers", readiness.Blockers)
			return nil
		}

		// Verify the feature branch actually has changes before declaring
		// testing ready. If all subtasks "completed" without producing commits,
		// fail the parent so the user can replan.
		if parent.WorktreeBranch != "" {
			fn := strings.TrimPrefix(parent.WorktreeBranch, "feature/")
			featureDir := o.worktree.FeatureWorktreePath(fn)
			// Check if the feature branch has any file changes relative to
			// the default branch.
			changed, changeErr := gitexec.GetChangedFiles(context.Background(), featureDir, o.worktree.DefaultBranchName())
			if changeErr != nil {
				o.logger.Warn("failed to check feature branch changes", "task_id", parent.ID, "error", changeErr)
			} else if len(changed) == 0 {
				if o.featureBranchAlreadyMergedToDefault(parent.WorktreeBranch) {
					return o.markFeatureAlreadyMergedDone(parent)
				}
				o.logger.Warn("all subtasks done but feature branch has no changes, failing parent", "task_id", parent.ID)
				if parent.Context == nil {
					parent.Context = make(model.JSONField)
				}
				parent.Context["empty_feature"] = true
				return o.failTask(parent, "all subtasks completed but no changes were committed to the feature branch")
			}
		}

		// Run full constraint evaluation on the integration worktree before
		// allowing transition to testing_ready, with retry/backoff gating.
		if parent.WorktreeBranch != "" && !o.skipConstraintGate {
			blocked, err := o.evaluateConstraintGate(parent)
			if err != nil {
				return err
			}
			if blocked {
				return nil
			}
		}

		evt, err := state.TransitionTask(parent, model.StatusTestingReady, "orchestrator", map[string]any{"reason": "all subtasks done"})
		if err != nil {
			return fmt.Errorf("check feature completion: transition to testing_ready: %w", err)
		}
		delete(parent.Context, "parent_readiness_target")
		delete(parent.Context, "parent_readiness_blockers")
		delete(parent.Context, "parent_readiness_blocker_count")
		if err := o.db.Save(parent).Error; err != nil {
			return fmt.Errorf("check feature completion: save parent: %w", err)
		}
		if err := o.db.Create(evt).Error; err != nil {
			return fmt.Errorf("check feature completion: save event: %w", err)
		}
		o.emit("testing_ready", map[string]any{"task_id": parent.ID})
		o.publishTaskTransition(parent.ID.String(), evt.OldValue, evt.NewValue, "all subtasks done, testing ready")
		o.logger.Info("all subtasks done, testing ready", "task_id", parent.ID)
	} else if allTerminal && anyFailed && parent.Status == model.StatusInProgress {
		// All subtasks finished but some failed -> parent fails.
		var failedNames []string
		for _, sub := range subtasks {
			if sub.Status == model.StatusFailed {
				failedNames = append(failedNames, sub.Title)
			}
		}
		if err := o.failTask(parent, fmt.Sprintf("subtasks failed: %s", strings.Join(failedNames, ", "))); err != nil {
			return err
		}
	}
	// Otherwise: subtasks still running, keep parent in_progress — do nothing.

	return nil
}

// handlePaused stops agents on paused tasks and their subtasks.
func (o *Orchestrator) handlePaused(task *model.Task) error {
	// Stop the task's own agent.
	if task.AssignedAgentID != nil {
		if err := o.runner.StopAgent(*task.AssignedAgentID); err != nil {
			o.logger.Warn("stop agent on paused task failed", "task_id", task.ID, "agent_id", task.AssignedAgentID, "error", err)
		}
		task.AssignedAgentID = nil
		if err := o.db.Save(task).Error; err != nil {
			return fmt.Errorf("handle paused: save task: %w", err)
		}
	}

	// Cascade: stop agents on subtasks.
	var subtasks []model.Task
	if err := o.db.Where("parent_task_id = ? AND assigned_agent_id IS NOT NULL", task.ID).
		Find(&subtasks).Error; err != nil {
		return fmt.Errorf("handle paused: query subtasks: %w", err)
	}

	for i := range subtasks {
		sub := &subtasks[i]
		if sub.AssignedAgentID != nil {
			if err := o.runner.StopAgent(*sub.AssignedAgentID); err != nil {
				o.logger.Warn("stop subtask agent on pause failed", "subtask_id", sub.ID, "error", err)
			}
			sub.AssignedAgentID = nil
			if err := o.db.Save(sub).Error; err != nil {
				o.logger.Warn("save subtask after pause stop failed", "subtask_id", sub.ID, "error", err)
			}
		}
	}

	return nil
}
