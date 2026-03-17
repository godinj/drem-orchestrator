package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/prompt"
	"github.com/godinj/drem-orchestrator/internal/state"
	"github.com/godinj/drem-orchestrator/internal/supervisor"
	"github.com/godinj/drem-orchestrator/internal/worktree"
)

// processBacklog transitions a task from BACKLOG to PLANNING.
// When replanning (PlanFeedback is set), it detaches old subtasks first
// to prevent the planner from seeing stale done subtasks and auto-advancing.
func (o *Orchestrator) processBacklog(task *model.Task) error {
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

// processPlanning handles tasks in the PLANNING state by either transitioning
// them to PLAN_REVIEW (if a plan exists), monitoring an assigned planner agent,
// or spawning a new planner.
func (o *Orchestrator) processPlanning(task *model.Task) error {
	// 1. If plan already exists, transition to PLAN_REVIEW.
	if task.Plan != nil {
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
func (o *Orchestrator) scheduleSubtasks(parent *model.Task) error {
	// Check for wave schedule in parent context.
	var allowedIDs map[uuid.UUID]bool
	if parent.Context != nil {
		if scheduleRaw, hasSchedule := parent.Context["schedule"]; hasSchedule {
			scheduleJSON, marshalErr := json.Marshal(scheduleRaw)
			if marshalErr == nil {
				var schedule Schedule
				if parseErr := json.Unmarshal(scheduleJSON, &schedule); parseErr == nil {
					currentGroup := o.findCurrentGroup(parent, schedule)
					if currentGroup != nil {
						allowedIDs = make(map[uuid.UUID]bool, len(currentGroup.TaskIDs))
						for _, id := range currentGroup.TaskIDs {
							allowedIDs[id] = true
						}
						o.logger.Debug("wave schedule active",
							"task_id", parent.ID,
							"group_order", currentGroup.Order,
							"group_size", len(currentGroup.TaskIDs))
					} else {
						// All groups complete.
						return nil
					}
				} else {
					o.logger.Warn("schedule parse failed, falling back to legacy scheduling",
						"task_id", parent.ID, "error", parseErr)
				}
			}
		}
	}

	var subtasks []model.Task
	if err := o.db.Where(
		"parent_task_id = ? AND ((status = ?) OR (status = ? AND assigned_agent_id IS NULL))",
		parent.ID, model.StatusBacklog, model.StatusInProgress,
	).Order("priority DESC").Find(&subtasks).Error; err != nil {
		return fmt.Errorf("schedule subtasks: query: %w", err)
	}

	for i := range subtasks {
		sub := &subtasks[i]

		// If wave scheduling is active, only schedule subtasks in the current group.
		if allowedIDs != nil && !allowedIDs[sub.ID] {
			continue
		}

		// Check dependencies.
		if len(sub.DependencyIDs) > 0 {
			met, err := DependenciesMet(o.db, sub.DependencyIDs)
			if err != nil {
				o.logger.Warn("dependency check failed", "subtask_id", sub.ID, "error", err)
				continue
			}
			if !met {
				continue
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

// executeMerge handles tasks in the MERGING state by merging the feature
// branch into main.
func (o *Orchestrator) executeMerge(task *model.Task) error {
	result, err := o.merger.MergeFeatureIntoMain(task)
	if err != nil {
		return fmt.Errorf("execute merge: %w", err)
	}

	if result.Success {
		evt, err := state.TransitionTask(task, model.StatusDone, "orchestrator", map[string]any{"merge_commit": result.MergeCommit})
		if err != nil {
			return fmt.Errorf("execute merge: transition to done: %w", err)
		}
		if err := o.db.Save(task).Error; err != nil {
			return fmt.Errorf("execute merge: save task: %w", err)
		}
		if err := o.db.Create(evt).Error; err != nil {
			return fmt.Errorf("execute merge: save event: %w", err)
		}
		o.emit("merge_complete", map[string]any{"task_id": task.ID})
		o.logger.Info("merge complete", "task_id", task.ID)
	} else {
		// Supervisor-powered analysis of the failure.
		if o.supervisor != nil && len(result.Conflicts) > 0 {
			if task.Context == nil {
				task.Context = make(model.JSONField)
			}

			// Detect whether this is a build failure or a merge conflict.
			isBuildFailure := len(result.Conflicts) == 1 && strings.HasPrefix(result.Conflicts[0], "build verification failed:")
			if isBuildFailure {
				// Build failure diagnosis.
				buildOutput := strings.TrimPrefix(result.Conflicts[0], "build verification failed: ")
				mainWorktree := filepath.Join(o.worktree.BareRepoPath, o.worktree.DefaultBranch)
				changedFiles, _ := worktree.GetChangedFiles(mainWorktree, o.worktree.DefaultBranch)

				var diagnosis supervisor.BuildFailureDiagnosis
				bfPrompt := supervisor.BuildFailurePrompt(mainWorktree, buildOutput, changedFiles)
				if bfErr := o.supervisor.EvaluateJSON(context.Background(), bfPrompt, &diagnosis); bfErr != nil {
					o.logger.Warn("supervisor build failure diagnosis failed", "task_id", task.ID, "error", bfErr)
				} else {
					task.Context["build_diagnosis"] = diagnosis.RootCause
					task.Context["build_suggested_fix"] = diagnosis.SuggestedFix
					task.Context["build_affected_files"] = diagnosis.AffectedFiles
					task.Context["build_can_auto_fix"] = diagnosis.CanAutoFix

					canAutoFix := "no"
					if diagnosis.CanAutoFix {
						canAutoFix = "yes"
					}
					o.logSupervisorAction(supervisor.JournalEntry{
						Timestamp: time.Now(),
						AgentName: "orchestrator",
						TaskID:    task.ID.String(),
						TaskTitle: task.Title,
						Type:      "build_failure",
						Summary:   diagnosis.RootCause,
						Details: map[string]string{
							"Suggested Fix":  diagnosis.SuggestedFix,
							"Affected Files": strings.Join(diagnosis.AffectedFiles, ", "),
							"Can Auto-Fix":   canAutoFix,
						},
						Outcome: "Merge failed — build verification error",
					})
				}
			} else {
				// Merge conflict analysis.
				var analysis supervisor.MergeConflictAnalysis
				mainWorktree := filepath.Join(o.worktree.BareRepoPath, o.worktree.DefaultBranch)
				diffOutput, _ := worktree.RunGit([]string{
					"diff", o.worktree.DefaultBranch + "..." + task.WorktreeBranch,
				}, mainWorktree)

				mcPrompt := supervisor.MergeConflictPrompt(
					task.WorktreeBranch, o.worktree.DefaultBranch,
					result.Conflicts, diffOutput,
				)
				if mcErr := o.supervisor.EvaluateJSON(context.Background(), mcPrompt, &analysis); mcErr != nil {
					o.logger.Warn("supervisor merge conflict analysis failed", "task_id", task.ID, "error", mcErr)
				} else {
					task.Context["merge_conflict_severity"] = analysis.Severity
					task.Context["merge_conflict_strategy"] = analysis.ResolutionStrategy
					task.Context["merge_conflict_hints"] = analysis.ResolutionHints

					o.logSupervisorAction(supervisor.JournalEntry{
						Timestamp: time.Now(),
						AgentName: "orchestrator",
						TaskID:    task.ID.String(),
						TaskTitle: task.Title,
						Type:      "merge_conflict",
						Summary:   fmt.Sprintf("Severity: %s — Strategy: %s", analysis.Severity, analysis.ResolutionStrategy),
						Details: map[string]string{
							"Resolution Hints": analysis.ResolutionHints,
							"Conflicts":        strings.Join(result.Conflicts, ", "),
						},
						Outcome: fmt.Sprintf("Merge failed — recommended strategy: %s", analysis.ResolutionStrategy),
					})

					if analysis.ResolutionStrategy == "spawn_agent" {
						o.logger.Info("supervisor suggests spawning resolver agent", "task_id", task.ID)
					}
				}
			}
		}

		details := map[string]any{"conflicts": result.Conflicts}
		if err := o.failTask(task, "merge conflicts"); err != nil {
			return err
		}
		o.emit("merge_conflict", map[string]any{"task_id": task.ID, "details": details})
		o.logger.Warn("merge failed with conflicts", "task_id", task.ID, "conflicts", result.Conflicts)
	}

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

// ---------------------------------------------------------------------------
// Public methods for TUI interaction (task processing)
// ---------------------------------------------------------------------------

// HandlePlanApproved creates subtask records from the plan and transitions the
// task to IN_PROGRESS (or TEST_WRITING for TDD plans).
func (o *Orchestrator) HandlePlanApproved(taskID uuid.UUID) error {
	var task model.Task
	if err := o.db.First(&task, "id = ?", taskID).Error; err != nil {
		return fmt.Errorf("handle plan approved: load task: %w", err)
	}

	if task.Status != model.StatusPlanReview {
		return fmt.Errorf("handle plan approved: task %s is in %s, expected plan_review", taskID, task.Status)
	}

	// Parse the plan (full format with TDD exceptions).
	planResult, err := parsePlan(task.Plan)
	if err != nil {
		return fmt.Errorf("handle plan approved: %w", err)
	}
	subtaskPlans := planResult.Subtasks

	// Auto-generate TDD reverse dependencies from tests_for.
	merged := MergeTDDDependencies(subtaskPlans)

	// Create subtask records. We need to track created IDs for dependency mapping.
	createdIDs := make([]uuid.UUID, len(subtaskPlans))
	for i, sp := range subtaskPlans {
		subtaskID := uuid.New()
		createdIDs[i] = subtaskID

		ctx := model.JSONField{
			"agent_type":      sp.AgentType,
			"estimated_files": sp.EstimatedFiles,
		}
		if sp.Phase != "" {
			ctx["phase"] = sp.Phase
		}

		sub := model.Task{
			ID:           subtaskID,
			ProjectID:    task.ProjectID,
			ParentTaskID: &task.ID,
			Title:        sp.Title,
			Description:  sp.Description,
			Status:       model.StatusBacklog,
			Phase:        sp.Phase,
			Context:      ctx,
			Priority:     len(subtaskPlans) - i,
		}

		if err := o.db.Create(&sub).Error; err != nil {
			return fmt.Errorf("handle plan approved: create subtask %d: %w", i, err)
		}
	}

	// Second pass: set dependency IDs (including auto-generated TDD deps).
	for i, sp := range merged {
		if len(sp.Dependencies) == 0 {
			continue
		}
		var depIDs model.JSONArray
		for _, depIdx := range sp.Dependencies {
			if depIdx >= 0 && depIdx < len(createdIDs) {
				depIDs = append(depIDs, createdIDs[depIdx].String())
			}
		}
		if len(depIDs) > 0 {
			if err := o.db.Model(&model.Task{}).Where("id = ?", createdIDs[i]).
				Update("dependency_ids", depIDs).Error; err != nil {
				return fmt.Errorf("handle plan approved: update dependencies for subtask %d: %w", i, err)
			}
		}
	}

	// Third pass: set TestsFor on test-phase subtasks.
	for i, sp := range subtaskPlans {
		if len(sp.TestsFor) > 0 {
			var testsForIDs model.JSONArray
			for _, idx := range sp.TestsFor {
				if idx >= 0 && idx < len(createdIDs) {
					testsForIDs = append(testsForIDs, createdIDs[idx].String())
				}
			}
			if len(testsForIDs) > 0 {
				o.db.Model(&model.Task{}).Where("id = ?", createdIDs[i]).
					Update("tests_for", testsForIDs)
			}
		}
	}

	// Store TDD exceptions on the parent task.
	if len(planResult.TDDExceptions) > 0 {
		exceptionsJSON, _ := json.Marshal(planResult.TDDExceptions)
		var exceptionsField any
		json.Unmarshal(exceptionsJSON, &exceptionsField)
		if task.TDDExceptions == nil {
			task.TDDExceptions = make(model.JSONField)
		}
		task.TDDExceptions["exceptions"] = exceptionsField
	}

	// Build wave schedule from the created subtasks.
	var createdSubtasks []model.Task
	if err := o.db.Where("parent_task_id = ?", task.ID).Find(&createdSubtasks).Error; err != nil {
		o.logger.Warn("handle plan approved: failed to load subtasks for scheduling", "error", err)
	} else if len(createdSubtasks) > 0 {
		schedule := BuildSchedule(createdSubtasks)
		scheduleJSON, marshalErr := json.Marshal(schedule)
		if marshalErr != nil {
			o.logger.Warn("handle plan approved: failed to marshal schedule", "error", marshalErr)
		} else {
			if task.Context == nil {
				task.Context = make(model.JSONField)
			}
			var scheduleField any
			if err := json.Unmarshal(scheduleJSON, &scheduleField); err != nil {
				o.logger.Warn("handle plan approved: failed to unmarshal schedule into context", "error", err)
			} else {
				task.Context["schedule"] = scheduleField
				o.logger.Info("wave schedule computed",
					"task_id", task.ID,
					"groups", len(schedule.Groups),
					"subtasks", len(createdSubtasks))
			}
		}
	}

	// Clear planner agent assignment now that review is complete.
	task.AssignedAgentID = nil

	// Write plan.json to the integration worktree and commit it.
	if task.WorktreeBranch != "" {
		featureName := strings.TrimPrefix(task.WorktreeBranch, "feature/")
		featureDir := o.worktree.FeatureWorktreePath(featureName)
		planJSON, marshalErr := json.MarshalIndent(task.Plan, "", "  ")
		if marshalErr != nil {
			o.logger.Warn("handle plan approved: failed to marshal plan for worktree", "error", marshalErr)
		} else {
			planPath := filepath.Join(featureDir, "plan.json")
			if writeErr := os.WriteFile(planPath, planJSON, 0o644); writeErr != nil {
				o.logger.Warn("handle plan approved: failed to write plan.json to worktree", "error", writeErr)
			} else {
				committed, commitErr := worktree.CommitUnstagedChanges(
					featureDir, "chore: commit plan.json after plan approval")
				if commitErr != nil {
					o.logger.Warn("handle plan approved: failed to commit plan.json", "error", commitErr)
				} else if committed {
					o.logger.Info("handle plan approved: committed plan.json to integration worktree",
						"task_id", task.ID)
				}
			}
		}
	}

	// Determine transition target: TEST_WRITING if plan has test-phase subtasks.
	hasTestPhase := false
	for _, sp := range subtaskPlans {
		if sp.Phase == "test" {
			hasTestPhase = true
			break
		}
	}

	var targetStatus model.TaskStatus
	if hasTestPhase {
		targetStatus = model.StatusTestWriting
	} else {
		targetStatus = model.StatusInProgress
	}

	evt, err := state.TransitionTask(&task, targetStatus, "user", map[string]any{"action": "plan_approved"})
	if err != nil {
		return fmt.Errorf("handle plan approved: transition: %w", err)
	}
	if err := o.db.Save(&task).Error; err != nil {
		return fmt.Errorf("handle plan approved: save task: %w", err)
	}
	if err := o.db.Create(evt).Error; err != nil {
		return fmt.Errorf("handle plan approved: save event: %w", err)
	}

	o.emit("task_updated", &task)
	o.logger.Info("plan approved", "task_id", task.ID, "subtask_count", len(subtaskPlans))
	return nil
}

// HandlePlanRejected clears the plan and transitions back to PLANNING.
func (o *Orchestrator) HandlePlanRejected(taskID uuid.UUID) error {
	var task model.Task
	if err := o.db.First(&task, "id = ?", taskID).Error; err != nil {
		return fmt.Errorf("handle plan rejected: load task: %w", err)
	}

	if task.Status != model.StatusPlanReview {
		return fmt.Errorf("handle plan rejected: task %s is in %s, expected plan_review", taskID, task.Status)
	}

	task.Plan = nil
	task.AssignedAgentID = nil

	evt, err := state.TransitionTask(&task, model.StatusPlanning, "user", map[string]any{"action": "plan_rejected"})
	if err != nil {
		return fmt.Errorf("handle plan rejected: transition: %w", err)
	}
	if err := o.db.Save(&task).Error; err != nil {
		return fmt.Errorf("handle plan rejected: save task: %w", err)
	}
	if err := o.db.Create(evt).Error; err != nil {
		return fmt.Errorf("handle plan rejected: save event: %w", err)
	}

	o.emit("task_updated", &task)
	o.logger.Info("plan rejected", "task_id", task.ID)
	return nil
}

// HandleTestPassed transitions from TESTING_READY to MERGING.
func (o *Orchestrator) HandleTestPassed(taskID uuid.UUID) error {
	var task model.Task
	if err := o.db.First(&task, "id = ?", taskID).Error; err != nil {
		return fmt.Errorf("handle test passed: load task: %w", err)
	}

	if task.Status != model.StatusTestingReady {
		return fmt.Errorf("handle test passed: task %s is in %s, expected testing_ready", taskID, task.Status)
	}

	evt, err := state.TransitionTask(&task, model.StatusMerging, "user", map[string]any{"action": "test_passed"})
	if err != nil {
		return fmt.Errorf("handle test passed: transition: %w", err)
	}
	if err := o.db.Save(&task).Error; err != nil {
		return fmt.Errorf("handle test passed: save task: %w", err)
	}
	if err := o.db.Create(evt).Error; err != nil {
		return fmt.Errorf("handle test passed: save event: %w", err)
	}

	o.emit("task_updated", &task)
	o.logger.Info("test passed, task merging", "task_id", task.ID)
	return nil
}

// HandleTestFailed transitions from TESTING_READY back to IN_PROGRESS.
func (o *Orchestrator) HandleTestFailed(taskID uuid.UUID) error {
	var task model.Task
	if err := o.db.First(&task, "id = ?", taskID).Error; err != nil {
		return fmt.Errorf("handle test failed: load task: %w", err)
	}

	if task.Status != model.StatusTestingReady {
		return fmt.Errorf("handle test failed: task %s is in %s, expected testing_ready", taskID, task.Status)
	}

	task.AssignedAgentID = nil

	evt, err := state.TransitionTask(&task, model.StatusInProgress, "user", map[string]any{"action": "test_failed"})
	if err != nil {
		return fmt.Errorf("handle test failed: transition: %w", err)
	}
	if err := o.db.Save(&task).Error; err != nil {
		return fmt.Errorf("handle test failed: save task: %w", err)
	}
	if err := o.db.Create(evt).Error; err != nil {
		return fmt.Errorf("handle test failed: save event: %w", err)
	}

	o.emit("task_updated", &task)
	o.logger.Info("test failed, task back to in_progress", "task_id", task.ID)
	return nil
}

// HandleTestReviewApproved transitions a task from TEST_REVIEW to IN_PROGRESS.
func (o *Orchestrator) HandleTestReviewApproved(taskID uuid.UUID) error {
	var task model.Task
	if err := o.db.First(&task, "id = ?", taskID).Error; err != nil {
		return fmt.Errorf("handle test review approved: load task: %w", err)
	}

	if task.Status != model.StatusTestReview {
		return fmt.Errorf("handle test review approved: task %s is in %s, expected test_review", taskID, task.Status)
	}

	evt, err := state.TransitionTask(&task, model.StatusInProgress, "user", map[string]any{"action": "test_review_approved"})
	if err != nil {
		return fmt.Errorf("handle test review approved: transition: %w", err)
	}
	if err := o.db.Save(&task).Error; err != nil {
		return fmt.Errorf("handle test review approved: save task: %w", err)
	}
	if err := o.db.Create(evt).Error; err != nil {
		return fmt.Errorf("handle test review approved: save event: %w", err)
	}

	o.emit("task_updated", &task)
	o.logger.Info("test review approved, scheduling implementation", "task_id", task.ID)
	return nil
}

// HandleTestReviewRejected marks rejected test subtasks as REJECTED, clones
// them with feedback, and transitions the parent back to TEST_WRITING.
// After 3 rejection rounds, pauses the task and spawns a diagnostic agent.
func (o *Orchestrator) HandleTestReviewRejected(taskID uuid.UUID, feedback string) error {
	var task model.Task
	if err := o.db.First(&task, "id = ?", taskID).Error; err != nil {
		return fmt.Errorf("handle test review rejected: load task: %w", err)
	}

	if task.Status != model.StatusTestReview {
		return fmt.Errorf("handle test review rejected: task %s is in %s, expected test_review", taskID, task.Status)
	}

	if task.Context == nil {
		task.Context = make(model.JSONField)
	}

	rejectionCount := 0
	if v, ok := task.Context["test_rejection_count"].(float64); ok {
		rejectionCount = int(v)
	}
	rejectionCount++
	task.Context["test_rejection_count"] = float64(rejectionCount)

	feedbackKey := fmt.Sprintf("test_rejection_feedback_%d", rejectionCount)
	task.Context[feedbackKey] = feedback

	if rejectionCount >= 3 {
		evt1, err := state.TransitionTask(&task, model.StatusTestWriting, "user", map[string]any{
			"action": "test_review_rejected_diagnostic",
			"reason": "3 rejection rounds exceeded",
		})
		if err != nil {
			return fmt.Errorf("handle test review rejected: transition to test_writing: %w", err)
		}
		if err := o.db.Create(evt1).Error; err != nil {
			return fmt.Errorf("handle test review rejected: save event1: %w", err)
		}

		evt2, err := state.TransitionTask(&task, model.StatusPaused, "user", map[string]any{
			"action": "test_review_rejected_paused",
			"reason": "3 test rejection rounds exceeded, diagnostic required",
		})
		if err != nil {
			return fmt.Errorf("handle test review rejected: transition to paused: %w", err)
		}
		task.Context["paused_from"] = string(model.StatusTestWriting)
		task.Context["diagnostic_required"] = true

		if err := o.db.Save(&task).Error; err != nil {
			return fmt.Errorf("handle test review rejected: save task: %w", err)
		}
		if err := o.db.Create(evt2).Error; err != nil {
			return fmt.Errorf("handle test review rejected: save event2: %w", err)
		}

		o.emit("task_updated", &task)
		o.logger.Warn("test review rejected 3 times, task paused for diagnostic",
			"task_id", task.ID, "rejection_count", rejectionCount)

		if err := o.spawnDiagnosticAgent(&task); err != nil {
			o.logger.Warn("failed to spawn diagnostic agent", "task_id", task.ID, "error", err)
		}
		return nil
	}

	var doneTestSubtasks []model.Task
	if err := o.db.Where(
		"parent_task_id = ? AND phase = ? AND status = ?",
		task.ID, "test", model.StatusDone,
	).Find(&doneTestSubtasks).Error; err != nil {
		return fmt.Errorf("handle test review rejected: query test subtasks: %w", err)
	}

	for i := range doneTestSubtasks {
		sub := &doneTestSubtasks[i]

		sub.Status = model.StatusRejected
		sub.UpdatedAt = time.Now()
		if err := o.db.Save(sub).Error; err != nil {
			return fmt.Errorf("handle test review rejected: reject subtask %s: %w", sub.ID, err)
		}

		rejectEvt := &model.TaskEvent{
			ID:        uuid.New(),
			TaskID:    sub.ID,
			EventType: "status_change",
			OldValue:  string(model.StatusDone),
			NewValue:  string(model.StatusRejected),
			Details:   model.JSONField{"action": "test_review_rejected", "feedback": feedback},
			Actor:     "user",
			CreatedAt: time.Now(),
		}
		if err := o.db.Create(rejectEvt).Error; err != nil {
			return fmt.Errorf("handle test review rejected: save reject event for %s: %w", sub.ID, err)
		}

		revisionSuffix := fmt.Sprintf(" (revision %d)", rejectionCount)
		newDescription := sub.Description + "\n\n## Rejection Feedback\n\n" + feedback

		var newCtx model.JSONField
		if sub.Context != nil {
			newCtx = make(model.JSONField, len(sub.Context))
			for k, v := range sub.Context {
				newCtx[k] = v
			}
		} else {
			newCtx = make(model.JSONField)
		}

		replacementID := uuid.New()
		replacement := model.Task{
			ID:            replacementID,
			ProjectID:     sub.ProjectID,
			ParentTaskID:  sub.ParentTaskID,
			Title:         sub.Title + revisionSuffix,
			Description:   newDescription,
			Status:        model.StatusBacklog,
			Priority:      sub.Priority,
			DependencyIDs: sub.DependencyIDs,
			Phase:         sub.Phase,
			TestsFor:      sub.TestsFor,
			Context:       newCtx,
		}

		if err := o.db.Create(&replacement).Error; err != nil {
			return fmt.Errorf("handle test review rejected: create replacement for %s: %w", sub.ID, err)
		}

		o.logger.Info("test subtask rejected and replaced",
			"original_id", sub.ID,
			"replacement_id", replacementID,
			"revision", rejectionCount)
	}

	evt, err := state.TransitionTask(&task, model.StatusTestWriting, "user", map[string]any{
		"action":          "test_review_rejected",
		"rejection_count": rejectionCount,
		"feedback":        feedback,
		"subtasks_cloned": len(doneTestSubtasks),
	})
	if err != nil {
		return fmt.Errorf("handle test review rejected: transition to test_writing: %w", err)
	}
	if err := o.db.Save(&task).Error; err != nil {
		return fmt.Errorf("handle test review rejected: save task: %w", err)
	}
	if err := o.db.Create(evt).Error; err != nil {
		return fmt.Errorf("handle test review rejected: save event: %w", err)
	}

	o.emit("task_updated", &task)
	o.logger.Info("test review rejected, back to test writing",
		"task_id", task.ID,
		"rejection_count", rejectionCount,
		"subtasks_cloned", len(doneTestSubtasks))
	return nil
}
