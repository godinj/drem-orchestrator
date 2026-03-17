package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/godinj/drem-orchestrator/internal/agent"
	"github.com/godinj/drem-orchestrator/internal/constraints"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/state"
	"github.com/godinj/drem-orchestrator/internal/supervisor"
	"github.com/godinj/drem-orchestrator/internal/worktree"
)

// MaxEmptyWorkRetries is the number of times a subtask will be rescheduled
// after an agent completes without committing any changes.
const MaxEmptyWorkRetries = 2

// processAgentResult handles a completed agent process.
func (o *Orchestrator) processAgentResult(comp agent.Completion) error {
	var ag model.Agent
	if err := o.db.First(&ag, "id = ?", comp.AgentID).Error; err != nil {
		return fmt.Errorf("process agent result: load agent: %w", err)
	}

	if ag.CurrentTaskID == nil {
		o.logger.Warn("completed agent has no current task", "agent_id", ag.ID)
		return nil
	}

	var task model.Task
	if err := o.db.First(&task, "id = ?", ag.CurrentTaskID).Error; err != nil {
		return fmt.Errorf("process agent result: load task: %w", err)
	}

	if comp.ReturnCode == 0 {
		return o.onAgentCompleted(&ag, &task)
	}
	return o.onAgentFailed(&ag, &task)
}

// onAgentCompleted handles a successfully completed agent.
func (o *Orchestrator) onAgentCompleted(ag *model.Agent, task *model.Task) error {
	switch ag.AgentType {
	case model.AgentPlanner:
		return o.onPlannerCompleted(ag, task)
	case model.AgentReviewer:
		return o.onReviewerCompleted(ag, task)
	case model.AgentFixer:
		return o.onFixerCompleted(ag, task)
	}

	// Extract memories from agent output.
	output, err := o.runner.GetAgentOutput(ag.ID)
	if err != nil {
		o.logger.Warn("failed to read agent output for memory extraction", "agent_id", ag.ID, "error", err)
	} else if output != "" {
		if _, memErr := o.memory.ExtractMemoriesFromOutput(ag.ID, task.ID, output); memErr != nil {
			o.logger.Warn("memory extraction failed", "agent_id", ag.ID, "error", memErr)
		}
	}

	// Merge agent branch into feature.
	// Subtasks don't carry WorktreeBranch — resolve from the parent task.
	featureBranch := task.WorktreeBranch
	if featureBranch == "" && task.ParentTaskID != nil {
		var parent model.Task
		if err := o.db.Select("worktree_branch").First(&parent, "id = ?", task.ParentTaskID).Error; err == nil {
			featureBranch = parent.WorktreeBranch
		}
	}
	merged := false
	if ag.WorktreeBranch != "" && featureBranch != "" {
		fn := strings.TrimPrefix(featureBranch, "feature/")
		featureDir := o.worktree.FeatureWorktreePath(fn)

		// Check if agent actually committed changes before attempting merge.
		hasCommits, commitErr := worktree.BranchHasNewCommits(featureDir, ag.WorktreeBranch)
		if commitErr != nil {
			o.logger.Warn("failed to check agent commits, proceeding with merge", "agent_id", ag.ID, "error", commitErr)
			hasCommits = true // assume there are commits on error
		}
		if !hasCommits {
			// Agent may have made changes but failed to commit. Rescue them.
			committed, rescueErr := worktree.CommitUnstagedChanges(
				ag.WorktreePath,
				fmt.Sprintf("Auto-commit uncommitted agent work for task: %s", task.Title),
			)
			if rescueErr != nil {
				o.logger.Warn("failed to rescue uncommitted agent work", "agent_id", ag.ID, "error", rescueErr)
			} else if committed {
				o.logger.Info("rescued uncommitted agent work", "agent_id", ag.ID, "task_id", task.ID)
				hasCommits = true
			}
		}
		if !hasCommits {
			return o.onAgentEmptyWork(ag, task, output)
		}

		result, mergeErr := o.merger.MergeAgentIntoFeature(ag.WorktreeBranch, featureDir)
		if mergeErr != nil {
			o.logger.Error("merge agent into feature failed", "agent_id", ag.ID, "error", mergeErr)
		} else if !result.Success {
			o.logger.Error("merge agent into feature had conflicts",
				"agent_id", ag.ID,
				"source", result.SourceBranch,
				"target", result.TargetBranch,
				"conflicts", result.Conflicts)
		} else {
			merged = true
		}
	} else {
		// No branches to merge (e.g. planner-only task); treat as merged.
		merged = true
	}

	if !merged {
		// Merge failed — keep the agent worktree/branch intact so work is not lost.
		// Transition the subtask to failed so it can be retried or manually resolved.
		ag.Status = model.AgentIdle
		ag.CurrentTaskID = nil
		if err := o.db.Save(ag).Error; err != nil {
			return fmt.Errorf("on agent completed: save agent: %w", err)
		}
		evt, err := state.TransitionTask(task, model.StatusFailed, "orchestrator",
			map[string]any{"reason": "merge into feature branch failed, agent branch preserved"})
		if err != nil {
			o.logger.Warn("failed to transition task to failed after merge failure", "task_id", task.ID, "error", err)
		} else {
			if err := o.db.Save(task).Error; err != nil {
				return fmt.Errorf("on agent completed: save task after merge failure: %w", err)
			}
			if err := o.db.Create(evt).Error; err != nil {
				return fmt.Errorf("on agent completed: save merge-failure event: %w", err)
			}
		}
		return nil
	}

	// Merge succeeded — clean up agent worktree.
	if ag.WorktreeBranch != "" {
		if err := o.worktree.RemoveAgentWorktree(ag.WorktreeBranch); err != nil {
			o.logger.Warn("cleanup agent worktree failed", "agent_id", ag.ID, "error", err)
		}
	}

	// Update agent status to IDLE.
	ag.Status = model.AgentIdle
	ag.CurrentTaskID = nil
	if err := o.db.Save(ag).Error; err != nil {
		return fmt.Errorf("on agent completed: save agent: %w", err)
	}

	// Fast-track subtask through states to DONE.
	transitions := []model.TaskStatus{
		model.StatusTestingReady,
		model.StatusMerging,
		model.StatusDone,
	}

	// The subtask might be in IN_PROGRESS; fast-track through the rest.
	for _, target := range transitions {
		if task.Status == target {
			continue // already at or past this state
		}
		evt, err := state.TransitionTask(task, target, "orchestrator", map[string]any{"reason": "auto-fasttrack"})
		if err != nil {
			// If the transition is invalid, skip (state machine protects us).
			o.logger.Debug("fast-track skip", "task_id", task.ID, "from", task.Status, "to", target, "error", err)
			continue
		}
		if err := o.db.Create(evt).Error; err != nil {
			return fmt.Errorf("on agent completed: save event: %w", err)
		}
	}

	if err := o.db.Save(task).Error; err != nil {
		return fmt.Errorf("on agent completed: save task: %w", err)
	}

	o.emit("task_updated", task)
	o.logger.Info("subtask completed", "task_id", task.ID, "agent_id", ag.ID)

	// Check if parent's subtasks are all done.
	if task.ParentTaskID != nil {
		var parent model.Task
		if err := o.db.First(&parent, "id = ?", task.ParentTaskID).Error; err == nil {
			if checkErr := o.checkFeatureCompletion(&parent); checkErr != nil {
				o.logger.Error("check parent completion after subtask done", "parent_id", parent.ID, "error", checkErr)
			}
		}
	}

	return nil
}

// onPlannerCompleted handles a successfully completed planner agent.
func (o *Orchestrator) onPlannerCompleted(ag *model.Agent, task *model.Task) error {
	// Mark agent as idle immediately — it has exited regardless of whether
	// it produced a valid plan. This prevents orphaned WORKING agents in DB
	// when the early-return paths below clear task.AssignedAgentID and
	// trigger a retry spawn in the same tick.
	ag.Status = model.AgentIdle
	ag.CurrentTaskID = nil
	if err := o.db.Save(ag).Error; err != nil {
		return fmt.Errorf("on planner completed: save agent: %w", err)
	}

	// Read plan.json from the agent's worktree.
	planPath := filepath.Join(ag.WorktreePath, "plan.json")
	planData, err := os.ReadFile(planPath)
	if err != nil {
		o.logger.Warn("planner produced no plan.json, will retry", "task_id", task.ID, "agent_id", ag.ID, "error", err)
		task.AssignedAgentID = nil
		o.incrementRetryCount(task)
		return o.db.Save(task).Error
	}

	// Parse plan JSON.
	var rawPlan struct {
		Subtasks []model.SubtaskPlan `json:"subtasks"`
	}
	if err := json.Unmarshal(planData, &rawPlan); err != nil {
		o.logger.Warn("planner plan.json parse failed, will retry", "task_id", task.ID, "error", err)
		task.AssignedAgentID = nil
		o.incrementRetryCount(task)
		return o.db.Save(task).Error
	}

	if len(rawPlan.Subtasks) == 0 {
		o.logger.Warn("planner produced empty plan, will retry", "task_id", task.ID)
		task.AssignedAgentID = nil
		o.incrementRetryCount(task)
		return o.db.Save(task).Error
	}

	// Store plan on the task.
	planJSON, err := json.Marshal(rawPlan.Subtasks)
	if err != nil {
		return fmt.Errorf("on planner completed: marshal plan: %w", err)
	}
	var planField model.JSONField
	if err := json.Unmarshal(planJSON, &planField); err != nil {
		// JSONField is map[string]any; wrap the array in a map.
		task.Plan = model.JSONField{"subtasks": rawPlan.Subtasks}
	} else {
		task.Plan = model.JSONField{"subtasks": rawPlan.Subtasks}
	}

	// Validate the plan before transitioning to plan_review.
	planResult, parseErr := parsePlan(task.Plan)
	if parseErr != nil {
		o.logger.Warn("plan validation: failed to parse stored plan", "task_id", task.ID, "error", parseErr)
	} else {
		validation := ValidatePlan(planResult.Subtasks, planResult.TDDExceptions)

		// Check plan against project constraints (grandfathered files, ceilings).
		constraintCfg, cfgErr := constraints.LoadConfig(ag.WorktreePath)
		if cfgErr != nil {
			o.logger.Warn("constraint config load failed", "task_id", task.ID, "error", cfgErr)
		} else if constraintCfg != nil {
			// Use feature worktree for near-limit checks (integration branch has latest state).
			featureDir := ""
			if task.WorktreeBranch != "" {
				fn := strings.TrimPrefix(task.WorktreeBranch, "feature/")
				featureDir = o.worktree.FeatureWorktreePath(fn)
			}
			constraintResult := ValidatePlanConstraints(planResult.Subtasks, constraintCfg, featureDir)
			// Merge constraint warnings into the existing validation result.
			validation.Warnings = append(validation.Warnings, constraintResult.Warnings...)
			validation.Errors = append(validation.Errors, constraintResult.Errors...)
			if len(constraintResult.Errors) > 0 {
				validation.Valid = false
			}
		}

		// Store validation result in task context for TUI display.
		if task.Context == nil {
			task.Context = make(model.JSONField)
		}
		task.Context["plan_validation"] = map[string]any{
			"valid":    validation.Valid,
			"warnings": validation.Warnings,
			"errors":   validation.Errors,
		}
		if !validation.Valid {
			o.logger.Warn("plan validation failed, will retry",
				"task_id", task.ID, "errors", validation.Errors)
			task.AssignedAgentID = nil
			o.incrementRetryCount(task)
			return o.db.Save(task).Error
		}
		if len(validation.Warnings) > 0 {
			o.logger.Info("plan validation warnings",
				"task_id", task.ID, "warnings", validation.Warnings)
		}
	}

	// Clean up planner agent worktree.
	if ag.WorktreeBranch != "" {
		if err := o.worktree.RemoveAgentWorktree(ag.WorktreeBranch); err != nil {
			o.logger.Warn("cleanup planner worktree failed", "agent_id", ag.ID, "error", err)
		}
	}

	// Transition to PLAN_REVIEW. Keep AssignedAgentID so the TUI can still
	// jump to the agent's tmux session for plan review. The assignment is
	// cleared when the plan is approved or rejected.
	evt, err := state.TransitionTask(task, model.StatusPlanReview, "orchestrator", nil)
	if err != nil {
		return fmt.Errorf("on planner completed: transition to plan_review: %w", err)
	}
	if err := o.db.Save(task).Error; err != nil {
		return fmt.Errorf("on planner completed: save task: %w", err)
	}
	if err := o.db.Create(evt).Error; err != nil {
		return fmt.Errorf("on planner completed: save event: %w", err)
	}

	o.emit("plan_ready", map[string]any{"task_id": task.ID, "subtask_count": len(rawPlan.Subtasks)})
	o.logger.Info("plan ready for review", "task_id", task.ID, "subtasks", len(rawPlan.Subtasks))
	return nil
}

// onReviewerCompleted handles a completed reviewer agent by parsing its
// review.json output and storing it in the task context.
func (o *Orchestrator) onReviewerCompleted(ag *model.Agent, task *model.Task) error {
	// Mark agent as idle.
	ag.Status = model.AgentIdle
	ag.CurrentTaskID = nil
	if err := o.db.Save(ag).Error; err != nil {
		return fmt.Errorf("on reviewer completed: save agent: %w", err)
	}

	// Read review.json from the worktree.
	reviewPath := filepath.Join(ag.WorktreePath, "review.json")
	reviewData, err := os.ReadFile(reviewPath)
	if err != nil {
		o.logger.Warn("reviewer produced no review.json", "task_id", task.ID, "agent_id", ag.ID, "error", err)
		return nil
	}

	// Parse review JSON into a map.
	var review map[string]any
	if err := json.Unmarshal(reviewData, &review); err != nil {
		o.logger.Warn("reviewer review.json parse failed", "task_id", task.ID, "error", err)
		return nil
	}

	// Store review in task context.
	if task.Context == nil {
		task.Context = make(model.JSONField)
	}
	task.Context["review"] = review
	if err := o.db.Save(task).Error; err != nil {
		return fmt.Errorf("on reviewer completed: save task: %w", err)
	}

	// Clean up the review.json file to avoid stale data on re-review.
	_ = os.Remove(reviewPath)

	o.emit("task_updated", task)
	o.logger.Info("review stored", "task_id", task.ID, "agent_id", ag.ID)
	return nil
}

// onFixerCompleted handles a completed fixer agent. The task stays in its
// current status for the user to decide next steps.
func (o *Orchestrator) onFixerCompleted(ag *model.Agent, task *model.Task) error {
	// Mark agent as idle.
	ag.Status = model.AgentIdle
	ag.CurrentTaskID = nil
	if err := o.db.Save(ag).Error; err != nil {
		return fmt.Errorf("on fixer completed: save agent: %w", err)
	}

	o.emit("task_updated", task)
	o.logger.Info("fixer completed", "task_id", task.ID, "agent_id", ag.ID)
	return nil
}

// onAgentFailed handles a failed agent. When a supervisor is configured, it
// performs LLM-powered failure diagnosis to decide whether to retry (and with
// what prompt adjustments). Without a supervisor, planners retry up to
// MaxPlannerRetries and coders/researchers hard-fail.
func (o *Orchestrator) onAgentFailed(ag *model.Agent, task *model.Task) error {
	// Read agent output for error details.
	output, err := o.runner.GetAgentOutput(ag.ID)
	if err != nil {
		o.logger.Warn("failed to read failed agent output", "agent_id", ag.ID, "error", err)
		output = "unknown error"
	}

	// Store error in task context.
	if task.Context == nil {
		task.Context = make(model.JSONField)
	}
	task.Context["last_error"] = truncate(output, 500)

	// Only remove the agent worktree if it has no commits to preserve.
	// If the agent produced work, keep the worktree so it can be retried
	// or manually resolved (consistent with onAgentCompleted merge failure).
	if ag.WorktreeBranch != "" {
		featureDir := o.resolveFeatureWorktree(task)
		hasWork := false
		if featureDir != "" {
			if hasCommits, err := worktree.BranchHasNewCommits(featureDir, ag.WorktreeBranch); err == nil && hasCommits {
				hasWork = true
			}
		}
		if hasWork {
			o.logger.Info("preserving failed agent worktree with commits",
				"agent_id", ag.ID, "branch", ag.WorktreeBranch)
		} else {
			if err := o.worktree.RemoveAgentWorktree(ag.WorktreeBranch); err != nil {
				o.logger.Warn("cleanup failed agent worktree failed", "agent_id", ag.ID, "error", err)
			}
		}
	}

	// Update agent status to DEAD.
	ag.Status = model.AgentDead
	ag.CurrentTaskID = nil
	if err := o.db.Save(ag).Error; err != nil {
		return fmt.Errorf("on agent failed: save agent: %w", err)
	}

	// Supervisor-powered failure diagnosis.
	if o.supervisor != nil {
		var diagnosis supervisor.FailureDiagnosis
		diagPrompt := supervisor.FailureDiagnosisPrompt(
			task.Title, task.Description, string(ag.AgentType), output, truncate(output, 500),
		)
		if diagErr := o.supervisor.EvaluateJSON(context.Background(), diagPrompt, &diagnosis); diagErr != nil {
			o.logger.Warn("supervisor failure diagnosis failed, falling back", "error", diagErr)
		} else {
			task.Context["failure_diagnosis"] = diagnosis.RootCause
			task.Context["failure_category"] = diagnosis.Category

			if diagnosis.ShouldRetry {
				task.AssignedAgentID = nil
				if diagnosis.PromptAdjustment != "" {
					task.Context["prompt_adjustment"] = diagnosis.PromptAdjustment
				}
				retries := o.incrementRetryCount(task)
				maxRetries := MaxPlannerRetries
				if diagnosis.MaxAdditionalRetries > 0 {
					maxRetries = retries + diagnosis.MaxAdditionalRetries
				}
				if retries >= maxRetries {
					o.logSupervisorAction(supervisor.JournalEntry{
						Timestamp: time.Now(),
						AgentName: ag.Name,
						TaskID:    task.ID.String(),
						TaskTitle: task.Title,
						Type:      "failure_diagnosis",
						Summary:   diagnosis.RootCause,
						Details: map[string]string{
							"Category":   diagnosis.Category,
							"Strategy":   diagnosis.RetryStrategy,
							"Agent Type": string(ag.AgentType),
						},
						Outcome: fmt.Sprintf("Failed after %d retries — max retries exceeded", retries),
					})
					if err := o.failTask(task, fmt.Sprintf("agent failed after %d retries (supervisor: %s)", retries, diagnosis.RootCause)); err != nil {
						return err
					}
					o.emit("agent_failed", map[string]any{"task_id": task.ID, "agent_id": ag.ID, "diagnosis": diagnosis.RootCause})
					return nil
				}

				o.logSupervisorAction(supervisor.JournalEntry{
					Timestamp: time.Now(),
					AgentName: ag.Name,
					TaskID:    task.ID.String(),
					TaskTitle: task.Title,
					Type:      "failure_diagnosis",
					Summary:   diagnosis.RootCause,
					Details: map[string]string{
						"Category":          diagnosis.Category,
						"Strategy":          diagnosis.RetryStrategy,
						"Prompt Adjustment": diagnosis.PromptAdjustment,
						"Agent Type":        string(ag.AgentType),
					},
					Outcome: fmt.Sprintf("Retrying (attempt %d)", retries),
				})

				// For planners, stay in PLANNING. For coders/researchers,
				// stay in current parent status (IN_PROGRESS) to be rescheduled.
				if err := o.db.Save(task).Error; err != nil {
					return fmt.Errorf("on agent failed: save task after supervisor retry: %w", err)
				}
				o.emit("agent_retrying", map[string]any{
					"task_id":   task.ID,
					"agent_id":  ag.ID,
					"retries":   retries,
					"diagnosis": diagnosis.RootCause,
				})
				o.logger.Info("supervisor recommends retry", "task_id", task.ID, "retries", retries, "strategy", diagnosis.RetryStrategy)
				return nil
			}

			o.logSupervisorAction(supervisor.JournalEntry{
				Timestamp: time.Now(),
				AgentName: ag.Name,
				TaskID:    task.ID.String(),
				TaskTitle: task.Title,
				Type:      "failure_diagnosis",
				Summary:   diagnosis.RootCause,
				Details: map[string]string{
					"Category":   diagnosis.Category,
					"Agent Type": string(ag.AgentType),
				},
				Outcome: "No retry recommended — falling through to default behavior",
			})
		}
	}

	if ag.AgentType == model.AgentPlanner {
		// Planner failure: clear assignment and stay in PLANNING for retry.
		task.AssignedAgentID = nil
		retries := o.incrementRetryCount(task)
		if retries >= MaxPlannerRetries {
			if err := o.failTask(task, "planner failed after max retries"); err != nil {
				return err
			}
			o.emit("planner_failed", map[string]any{"task_id": task.ID, "error": "max retries exceeded"})
			return nil
		}
		if err := o.db.Save(task).Error; err != nil {
			return fmt.Errorf("on agent failed: save task: %w", err)
		}
		o.emit("planner_failed", map[string]any{"task_id": task.ID, "retries": retries})
		return nil
	}

	// Before failing, check if work was already merged (e.g. merge succeeded
	// but DB update failed on a prior attempt).
	featureDir := o.resolveFeatureWorktree(task)
	if featureDir != "" && o.isWorkAlreadyMerged(task, featureDir) {
		o.logger.Info("agent failed but work already merged, fast-tracking to done",
			"task_id", task.ID, "agent_id", ag.ID)
		// Fast-track subtask through states to DONE.
		transitions := []model.TaskStatus{
			model.StatusTestingReady,
			model.StatusMerging,
			model.StatusDone,
		}
		for _, target := range transitions {
			if task.Status == target {
				continue
			}
			evt, tErr := state.TransitionTask(task, target, "orchestrator",
				map[string]any{"reason": "already-merged-on-failure"})
			if tErr != nil {
				continue
			}
			if err := o.db.Create(evt).Error; err != nil {
				return fmt.Errorf("on agent failed: save event: %w", err)
			}
		}
		if err := o.db.Save(task).Error; err != nil {
			return fmt.Errorf("on agent failed: save task: %w", err)
		}
		o.emit("task_updated", task)
		return nil
	}

	// Coder/researcher failure: transition to FAILED.
	if err := o.failTask(task, "agent exited with non-zero code"); err != nil {
		return err
	}
	o.emit("agent_failed", map[string]any{"task_id": task.ID, "agent_id": ag.ID})
	return nil
}

// onAgentEmptyWork handles the case where an agent exited successfully but
// made no commits. It retries (with supervisor diagnosis when available) or
// fails the subtask so the parent can be replanned.
func (o *Orchestrator) onAgentEmptyWork(ag *model.Agent, task *model.Task, agentOutput string) error {
	o.logger.Warn("agent completed without making changes", "agent_id", ag.ID, "task_id", task.ID)

	// Clean up agent worktree — nothing to preserve.
	if ag.WorktreeBranch != "" {
		if err := o.worktree.RemoveAgentWorktree(ag.WorktreeBranch); err != nil {
			o.logger.Warn("cleanup empty agent worktree failed", "agent_id", ag.ID, "error", err)
		}
	}

	// Mark agent as idle (it completed normally, just produced nothing).
	ag.Status = model.AgentIdle
	ag.CurrentTaskID = nil
	if err := o.db.Save(ag).Error; err != nil {
		return fmt.Errorf("on agent empty work: save agent: %w", err)
	}

	if task.Context == nil {
		task.Context = make(model.JSONField)
	}
	task.Context["empty_work"] = true
	task.Context["last_error"] = "agent completed without committing any changes"

	retries := o.incrementRetryCount(task)

	// Supervisor-powered diagnosis.
	if o.supervisor != nil {
		var diagnosis supervisor.FailureDiagnosis
		diagPrompt := supervisor.FailureDiagnosisPrompt(
			task.Title, task.Description, string(ag.AgentType),
			"Agent completed successfully (exit code 0) but did not commit any changes to the repository.",
			truncate(agentOutput, 500),
		)
		if diagErr := o.supervisor.EvaluateJSON(context.Background(), diagPrompt, &diagnosis); diagErr != nil {
			o.logger.Warn("supervisor empty-work diagnosis failed", "error", diagErr)
		} else {
			task.Context["failure_diagnosis"] = diagnosis.RootCause
			if diagnosis.PromptAdjustment != "" {
				task.Context["prompt_adjustment"] = diagnosis.PromptAdjustment
			}

			if diagnosis.ShouldRetry && retries < MaxEmptyWorkRetries {
				o.logSupervisorAction(supervisor.JournalEntry{
					Timestamp: time.Now(),
					AgentName: ag.Name,
					TaskID:    task.ID.String(),
					TaskTitle: task.Title,
					Type:      "empty_work_diagnosis",
					Summary:   diagnosis.RootCause,
					Details: map[string]string{
						"Prompt Adjustment": diagnosis.PromptAdjustment,
						"Agent Type":        string(ag.AgentType),
					},
					Outcome: fmt.Sprintf("Retrying (attempt %d of %d)", retries, MaxEmptyWorkRetries),
				})
				task.AssignedAgentID = nil
				if err := o.db.Save(task).Error; err != nil {
					return fmt.Errorf("on agent empty work: save task for retry: %w", err)
				}
				o.emit("agent_retrying", map[string]any{
					"task_id": task.ID, "reason": "no commits", "retries": retries,
				})
				o.logger.Info("retrying subtask after empty work", "task_id", task.ID, "retries", retries)
				return nil
			}

			o.logSupervisorAction(supervisor.JournalEntry{
				Timestamp: time.Now(),
				AgentName: ag.Name,
				TaskID:    task.ID.String(),
				TaskTitle: task.Title,
				Type:      "empty_work_diagnosis",
				Summary:   diagnosis.RootCause,
				Details: map[string]string{
					"Agent Type": string(ag.AgentType),
				},
				Outcome: "No retry — will fail or fall through",
			})
		}
	}

	// Retry without supervisor if under limit.
	if retries < MaxEmptyWorkRetries {
		task.AssignedAgentID = nil
		if err := o.db.Save(task).Error; err != nil {
			return fmt.Errorf("on agent empty work: save task for retry: %w", err)
		}
		o.emit("agent_retrying", map[string]any{
			"task_id": task.ID, "reason": "no commits", "retries": retries,
		})
		o.logger.Info("retrying subtask after empty work (no supervisor)", "task_id", task.ID, "retries", retries)
		return nil
	}

	// Max retries exceeded — fail the subtask.
	if err := o.failTask(task, "agent completed without making any changes"); err != nil {
		return err
	}
	o.emit("agent_failed", map[string]any{"task_id": task.ID, "agent_id": ag.ID, "reason": "no commits"})
	return nil
}
