package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/clarification"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/prompt"
	"github.com/godinj/drem-orchestrator/internal/state"
	"github.com/godinj/drem-orchestrator/internal/supervisor"
	"github.com/godinj/drem-orchestrator/internal/worktree"
)

// MaxQuickFixRetries is the number of times the orchestrator will retry a
// quick fix agent before failing the task.
const MaxQuickFixRetries = 3

// processBacklog transitions a task from BACKLOG to PLANNING.
// Quick fix tasks skip planning and go directly to IN_PROGRESS.
// When replanning (PlanFeedback is set), it detaches old subtasks first
// to prevent the planner from seeing stale done subtasks and auto-advancing.
func (o *Orchestrator) processBacklog(task *model.Task) error {
	if task.Category.IsQuickFix() {
		return o.processQuickFix(task)
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
	o.logger.Info("task transitioned to planning", "task_id", task.ID, "title", task.Title)
	return nil
}

// processQuickFix handles quick fix tasks, transitioning them from BACKLOG
// directly to IN_PROGRESS and spawning a coder agent. Quick fix tasks skip
// the planning and TDD lifecycle gates.
func (o *Orchestrator) processQuickFix(task *model.Task) error {
	// 1. If an agent is already assigned, check if it's still running.
	if task.AssignedAgentID != nil {
		var ag model.Agent
		if err := o.db.First(&ag, "id = ?", task.AssignedAgentID).Error; err != nil {
			// Agent record missing — clear assignment and retry.
			task.AssignedAgentID = nil
			retries := o.incrementRetryCount(task)
			if retries >= MaxQuickFixRetries {
				return o.failTask(task, "quick fix agent disappeared after max retries")
			}
			return o.db.Save(task).Error
		}

		if ag.Status == model.AgentDead || ag.Status == model.AgentIdle {
			if ag.WorktreeBranch != "" {
				if err := o.worktree.RemoveAgentWorktree(ag.WorktreeBranch); err != nil {
					o.logger.Warn("cleanup dead quickfix agent worktree", "agent_id", ag.ID, "error", err)
				}
			}
			task.AssignedAgentID = nil
			retries := o.incrementRetryCount(task)
			if retries >= MaxQuickFixRetries {
				return o.failTask(task, "quick fix agent failed after max retries")
			}
			o.logger.Warn("quickfix agent dead/idle, will retry", "task_id", task.ID, "retries", retries)
			return o.db.Save(task).Error
		}

		// Agent is still working — nothing to do.
		return nil
	}

	// 2. Check capacity.
	if o.runner == nil || !o.runner.CanSpawn() {
		return nil
	}

	// 3. Create feature worktree if needed.
	if task.WorktreeBranch == "" {
		featureName := taskFeatureName(task)
		wtInfo, err := o.worktree.CreateFeature(featureName)
		if err != nil {
			return fmt.Errorf("process quick fix: create feature: %w", err)
		}
		task.WorktreeBranch = wtInfo.Branch
		if err := o.db.Save(task).Error; err != nil {
			return fmt.Errorf("process quick fix: save worktree branch: %w", err)
		}
	}

	// 4. Transition backlog → in_progress.
	event, err := state.TransitionTask(task, model.StatusInProgress, "orchestrator", map[string]any{"reason": "quickfix-direct"})
	if err != nil {
		return fmt.Errorf("process quick fix: %w", err)
	}
	if err := o.db.Save(task).Error; err != nil {
		return fmt.Errorf("process quick fix: save task: %w", err)
	}
	if err := o.db.Create(event).Error; err != nil {
		return fmt.Errorf("process quick fix: save event: %w", err)
	}

	// 5. Generate coder prompt and spawn agent.
	var project model.Project
	if err := o.db.First(&project, "id = ?", o.projectID).Error; err != nil {
		return fmt.Errorf("process quick fix: load project: %w", err)
	}

	featureName := strings.TrimPrefix(task.WorktreeBranch, "feature/")
	featureDir := o.worktree.FeatureWorktreePath(featureName)
	comments, _ := o.GetComments(task.ID)
	coderPrompt := prompt.Generate(prompt.Opts{
		Task:         task,
		Project:      &project,
		AgentType:    model.AgentCoder,
		WorktreePath: featureDir,
		Comments:     comments,
	})

	ag, err := o.runner.SpawnAgent(task, featureName, model.AgentCoder, coderPrompt)
	if err != nil {
		return fmt.Errorf("process quick fix: spawn agent: %w", err)
	}

	task.AssignedAgentID = &ag.ID
	if err := o.db.Save(task).Error; err != nil {
		return fmt.Errorf("process quick fix: save assigned agent: %w", err)
	}

	o.emit("quickfix_started", map[string]any{"task_id": task.ID, "agent_id": ag.ID})
	o.logger.Info("quickfix started", "task_id", task.ID, "agent_id", ag.ID)
	return nil
}

// processPlanning handles tasks in the PLANNING state by either transitioning
// them to PLAN_REVIEW (if a plan exists), monitoring an assigned planner agent,
// or spawning a new planner.
func (o *Orchestrator) processPlanning(task *model.Task) error {
	// 1. If plan already exists, evaluate for clarification needs.
	if task.Plan != nil {
		// Parse plan to extract assumptions.
		planResult, err := parsePlan(task.Plan)
		if err != nil {
			o.logger.Warn("process planning: parse plan for assumptions failed", "task_id", task.ID, "error", err)
			// Fall through to plan_review without clarification.
		}

		var assumptions []clarification.Assumption
		if planResult != nil {
			assumptions = planResult.Assumptions
		}

		// Supervisor cross-check for missed assumptions (if supervisor available).
		var supervisorAnalysis string
		if o.supervisor != nil && planResult != nil {
			planJSON, _ := json.Marshal(task.Plan)
			assumptionsJSON, _ := json.Marshal(assumptions)
			crossCheckPrompt := supervisor.AssumptionCrossCheckPrompt(
				task.Title, task.Description, string(planJSON), string(assumptionsJSON),
			)
			var crossCheck supervisor.AssumptionCrossCheck
			if err := o.supervisor.EvaluateJSON(context.Background(), crossCheckPrompt, &crossCheck); err != nil {
				o.logger.Warn("supervisor assumption cross-check failed", "task_id", task.ID, "error", err)
			} else {
				analysisJSON, _ := json.Marshal(crossCheck.MissedAssumptions)
				supervisorAnalysis = string(analysisJSON)
			}
		}

		// Evaluate whether clarification is needed.
		planJSON, _ := json.Marshal(task.Plan)
		evalResult, evalErr := clarification.Evaluate(string(planJSON), assumptions, supervisorAnalysis)
		if evalErr != nil {
			o.logger.Warn("clarification evaluate failed", "task_id", task.ID, "error", evalErr)
		}

		if evalResult != nil && evalResult.NeedsClarification {
			// Transition to NEEDS_CLARIFICATION.
			if task.Context == nil {
				task.Context = make(model.JSONField)
			}
			task.Context["clarification_session"] = evalResult.SessionData
			task.Context["clarification_questions"] = evalResult.Questions
			if len(evalResult.Questions) > 0 {
				task.Context["clarification_current_question"] = evalResult.Questions[0]
			}

			event, err := state.TransitionTask(task, model.StatusNeedsClarification, "orchestrator", nil)
			if err != nil {
				return fmt.Errorf("process planning: transition to needs_clarification: %w", err)
			}
			if err := o.db.Save(task).Error; err != nil {
				return fmt.Errorf("process planning: save task: %w", err)
			}
			if err := o.db.Create(event).Error; err != nil {
				return fmt.Errorf("process planning: save event: %w", err)
			}
			o.emit("needs_clarification", map[string]any{"task_id": task.ID, "questions": evalResult.Questions})
			return nil
		}

		// No clarification needed — proceed to plan_review.
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
		task.WorktreeBranch = wtInfo.Branch
		if err := o.db.Save(task).Error; err != nil {
			return fmt.Errorf("process planning: save worktree branch: %w", err)
		}
	}

	// Generate planner prompt.
	featureName := strings.TrimPrefix(task.WorktreeBranch, "feature/")
	featureDir := o.worktree.FeatureWorktreePath(featureName)
	comments, _ := o.GetComments(task.ID)
	plannerPrompt := prompt.Generate(prompt.Opts{
		Task:         task,
		Project:      &project,
		AgentType:    model.AgentPlanner,
		WorktreePath: featureDir,
		Comments:     comments,
	})

	// Spawn planner agent.
	ag, err := o.runner.SpawnAgent(task, featureName, model.AgentPlanner, plannerPrompt)
	if err != nil {
		return fmt.Errorf("process planning: spawn planner: %w", err)
	}

	task.AssignedAgentID = &ag.ID
	if err := o.db.Save(task).Error; err != nil {
		return fmt.Errorf("process planning: save assigned agent: %w", err)
	}

	o.emit("planner_spawned", map[string]any{"task_id": task.ID, "agent_id": ag.ID})
	o.logger.Info("planner spawned", "task_id", task.ID, "agent_id", ag.ID)
	return nil
}

// scheduleSubtasks looks for BACKLOG subtasks — and IN_PROGRESS subtasks
// whose agent has been cleared (e.g. after empty-work retry) — of the parent
// that have their dependencies met and spawns agents for them.
// If phaseFilter is non-empty, only subtasks with a matching Phase are
// considered (used by processTestWriting to limit scheduling to test-phase).
func (o *Orchestrator) scheduleSubtasks(parent *model.Task, phaseFilter ...string) error {
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

	// Query in-progress sibling tasks for conflict detection.
	var inProgress []model.Task
	if err := o.db.Where(
		"parent_task_id = ? AND status IN ?",
		parent.ID, []model.TaskStatus{model.StatusInProgress, model.StatusMerging},
	).Find(&inProgress).Error; err != nil {
		return fmt.Errorf("schedule subtasks: query in-progress: %w", err)
	}

	// Evaluate dispatch decisions: dependencies, wave groups, file conflicts.
	policy := NewSchedulingPolicy(o.db)
	decisions := policy.EvaluateDispatch(candidates, inProgress)
	dispatchDecisions := make(map[uuid.UUID]DispatchDecision, len(decisions))
	for _, d := range decisions {
		dispatchDecisions[d.TaskID] = d
	}

	for i := range candidates {
		sub := &candidates[i]

		// Content-aware dedup: if the subtask's estimated files already
		// appear in the integration branch and commit messages match,
		// fast-track to done without spawning an agent.
		if estimatedFiles := getEstimatedFiles(sub.Context); len(estimatedFiles) > 0 {
			featureName := strings.TrimPrefix(parent.WorktreeBranch, "feature/")
			featureDir := o.worktree.FeatureWorktreePath(featureName)
			changedFiles, diffErr := getChangedFiles(featureDir, o.worktree.DefaultBranch)
			if diffErr == nil && len(changedFiles) > 0 {
				commitMsgs, logErr := getCommitMessages(featureDir, o.worktree.DefaultBranch)
				if logErr == nil && hasExistingWork(estimatedFiles, changedFiles, commitMsgs, sub.Title) {
					o.logger.Info("schedule: dedup detected existing work, fast-tracking to done",
						"subtask_id", sub.ID)
					transitions := []model.TaskStatus{
						model.StatusPlanning,
						model.StatusPlanReview,
						model.StatusInProgress,
						model.StatusTestingReady,
						model.StatusMerging,
						model.StatusDone,
					}
					for _, target := range transitions {
						if sub.Status == target {
							continue
						}
						evt, tErr := state.TransitionTask(sub, target, "orchestrator",
							map[string]any{"reason": "dedup-existing-work"})
						if tErr != nil {
							continue
						}
						if err := o.db.Create(evt).Error; err != nil {
							o.logger.Error("schedule: save event", "subtask_id", sub.ID, "error", err)
							break
						}
					}
					if err := o.db.Save(sub).Error; err != nil {
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
				// Fast-track to done.
				transitions := []model.TaskStatus{
					model.StatusPlanning,
					model.StatusPlanReview,
					model.StatusInProgress,
					model.StatusTestingReady,
					model.StatusMerging,
					model.StatusDone,
				}
				for _, target := range transitions {
					if sub.Status == target {
						continue
					}
					evt, tErr := state.TransitionTask(sub, target, "orchestrator",
						map[string]any{"reason": "already-merged-skip-spawn"})
					if tErr != nil {
						continue
					}
					if err := o.db.Create(evt).Error; err != nil {
						o.logger.Error("schedule: save event", "subtask_id", sub.ID, "error", err)
						break
					}
				}
				if err := o.db.Save(sub).Error; err != nil {
					o.logger.Error("schedule: save subtask", "subtask_id", sub.ID, "error", err)
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

		// Check capacity.
		if !o.runner.CanSpawn() {
			break
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

		// Fast-track subtask: BACKLOG -> PLANNING -> PLAN_REVIEW -> IN_PROGRESS.
		fastTrack := []model.TaskStatus{
			model.StatusPlanning,
			model.StatusPlanReview,
			model.StatusInProgress,
		}
		for _, target := range fastTrack {
			evt, err := state.TransitionTask(sub, target, "orchestrator", map[string]any{"reason": "auto-schedule"})
			if err != nil {
				o.logger.Debug("fast-track subtask skip", "subtask_id", sub.ID, "to", target, "error", err)
				continue
			}
			if err := o.db.Create(evt).Error; err != nil {
				return fmt.Errorf("schedule subtasks: save event: %w", err)
			}
		}

		sub.AssignedAgentID = &ag.ID
		if err := o.db.Save(sub).Error; err != nil {
			return fmt.Errorf("schedule subtasks: save subtask: %w", err)
		}

		o.emit("subtask_scheduled", map[string]any{
			"task_id":    sub.ID,
			"agent_id":   ag.ID,
			"agent_type": agentType,
		})
		o.logger.Info("subtask scheduled", "subtask_id", sub.ID, "agent_id", ag.ID, "type", agentType)
	}

	return nil
}

// findCurrentGroup returns the earliest group that has subtasks not yet in a
// terminal state (done or failed). Returns nil if all groups are complete.
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
			if sub.Status != model.StatusDone && sub.Status != model.StatusFailed {
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
		case model.StatusFailed:
			anyFailed = true
			allDone = false
		default:
			allTerminal = false
			allDone = false
		}
	}

	if allDone && parent.Status == model.StatusInProgress {
		// Verify the feature branch actually has changes before declaring
		// testing ready. If all subtasks "completed" without producing commits,
		// fail the parent so the user can replan.
		if parent.WorktreeBranch != "" {
			fn := strings.TrimPrefix(parent.WorktreeBranch, "feature/")
			featureDir := o.worktree.FeatureWorktreePath(fn)
			// Check if the feature branch has any file changes relative to
			// the default branch.
			changed, changeErr := worktree.GetChangedFiles(featureDir, o.worktree.DefaultBranch)
			if changeErr != nil {
				o.logger.Warn("failed to check feature branch changes", "task_id", parent.ID, "error", changeErr)
			} else if len(changed) == 0 {
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
		if parent.WorktreeBranch != "" {
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
		if err := o.db.Save(parent).Error; err != nil {
			return fmt.Errorf("check feature completion: save parent: %w", err)
		}
		if err := o.db.Create(evt).Error; err != nil {
			return fmt.Errorf("check feature completion: save event: %w", err)
		}
		o.emit("testing_ready", map[string]any{"task_id": parent.ID})
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
