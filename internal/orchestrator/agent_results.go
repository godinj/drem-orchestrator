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
	"github.com/godinj/drem-orchestrator/internal/experiment"
	"github.com/godinj/drem-orchestrator/internal/gitexec"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/state"
	"github.com/google/uuid"
)

// MaxEmptyWorkRetries is the maximum reschedule count after an agent commits nothing.
const MaxEmptyWorkRetries = 2

// processAgentResult handles a completed agent process.
func (o *Orchestrator) processAgentResult(comp agent.Completion) error {
	var ag model.Agent
	if err := o.db.First(&ag, "id = ?", comp.AgentID).Error; err != nil {
		return fmt.Errorf("process agent result: load agent: %w", err)
	}

	// Store exit info in agent Config before routing to success/failure handlers.
	if comp.ExitInfo != nil {
		if ag.Config == nil {
			ag.Config = make(model.JSONField)
		}
		ag.Config["exit_reason"] = comp.ExitInfo.ExitReason
		ag.Config["exit_last_tool"] = comp.ExitInfo.LastTool
		ag.Config["exit_summary"] = comp.ExitInfo.ExitSummary
	}

	// Populate completion fields (CompletedAt, ExitReason, TotalCostUSD, FinalContextPct)
	populateAgentCompletionFields(&ag, comp)

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
	case model.AgentClassifier:
		return o.onClassifierCompleted(ag, task)
	case model.AgentPrep:
		return o.onPrepCompleted(ag, task)
	}

	// Extract memories from agent output. Direct tool agents have no
	// subprocess output file, so skip when the runner is not configured
	// (also covers tests that omit the runner).
	var output string
	if o.runner != nil {
		var err error
		output, err = o.runner.GetAgentOutput(ag.ID)
		if err != nil {
			o.logger.Warn("failed to read agent output for memory extraction", "agent_id", ag.ID, "error", err)
		} else if output != "" && o.memory != nil {
			if _, memErr := o.memory.ExtractMemoriesFromOutput(ag.ID, task.ID, output); memErr != nil {
				o.logger.Warn("memory extraction failed", "agent_id", ag.ID, "error", memErr)
			}
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
	var mergeResult *WorktreeMergeResult
	if ag.WorktreeBranch != "" && featureBranch != "" {
		fn := strings.TrimPrefix(featureBranch, "feature/")
		featureDir := o.worktree.FeatureWorktreePath(fn)

		// Check if agent actually committed changes before attempting merge.
		hasCommits, commitErr := gitexec.BranchHasNewCommits(context.Background(), featureDir, ag.WorktreeBranch)
		if commitErr != nil {
			o.logger.Warn("failed to check agent commits, proceeding with merge", "agent_id", ag.ID, "error", commitErr)
			hasCommits = true // assume there are commits on error
		}
		if !hasCommits {
			// Agent may have made changes but failed to commit. Rescue them.
			committed, rescueErr := gitexec.CommitUnstagedChanges(
				context.Background(),
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

		// Resolve diagnostic refs before merge for structured failure events.
		mergeBase, _ := gitexec.RunGit(context.Background(), featureDir, "merge-base", "HEAD", ag.WorktreeBranch)
		featureHEAD, _ := gitexec.RunGit(context.Background(), featureDir, "rev-parse", "HEAD")
		agentHEAD, _ := gitexec.RunGit(context.Background(), featureDir, "rev-parse", ag.WorktreeBranch)

		result, mergeErr := o.mergeAgentBranchIntoFeature(context.Background(), ag.WorktreeBranch, featureDir)
		if mergeErr != nil {
			o.logger.Error("merge agent into feature failed", "agent_id", ag.ID, "error", mergeErr)
		} else if !result.Success {
			mergeResult = result
			o.logger.Error("merge agent into feature had conflicts",
				"agent_id", ag.ID,
				"source", result.SourceBranch,
				"target", result.TargetBranch,
				"conflicts", result.Conflicts,
				"git_stderr", result.GitStderr,
				"git_command", result.GitCommand)

			// Emit structured merge failure event with full diagnostics.
			o.emit("merge_failed", map[string]any{
				"task_id":        task.ID,
				"agent_id":       ag.ID,
				"agent_branch":   ag.WorktreeBranch,
				"feature_branch": featureBranch,
				"conflicts":      result.Conflicts,
				"git_stderr":     result.GitStderr,
				"git_command":    result.GitCommand,
				"merge_base":     mergeBase,
				"feature_head":   featureHEAD,
				"agent_head":     agentHEAD,
			})
		} else {
			merged = true
		}
	} else {
		// No branches to merge (e.g. planner-only task); treat as merged.
		merged = true
	}

	if !merged {
		// Merge failed — delegate to handleAgentMergeFailure which may invoke
		// supervisor diagnosis and spawn a fixer agent for trivial conflicts.
		fn := strings.TrimPrefix(featureBranch, "feature/")
		mergeFeatureDir := o.worktree.FeatureWorktreePath(fn)
		return o.handleAgentMergeFailure(ag, task, mergeResult, mergeFeatureDir)
	}

	// After merge succeeded, check constraints and compute step scores.
	var implScorePassed, implScoreFailed int
	var implChangedFiles []string
	if merged && featureBranch != "" && !o.skipConstraintGate {
		fn := strings.TrimPrefix(featureBranch, "feature/")
		featureDir := o.worktree.FeatureWorktreePath(fn)

		constraintCfg, cfgErr := constraints.LoadConfig(featureDir)
		if cfgErr != nil {
			o.logger.Warn("constraint config load failed after merge",
				"agent_id", ag.ID, "error", cfgErr)
		} else if constraintCfg != nil {
			changedFiles, chErr := gitexec.GetChangedFiles(context.Background(), featureDir, o.worktree.DefaultBranchName())
			if chErr != nil {
				o.logger.Warn("failed to get changed files for constraint check",
					"agent_id", ag.ID, "error", chErr)
			} else if len(changedFiles) > 0 {
				implChangedFiles = changedFiles
				// Use delta evaluation: compare feature branch against master
				// baseline so pre-existing violations don't block agents whose
				// work is clean. Only NEW or WORSENED violations fail the gate.
				mainDir, mainErr := o.worktree.MainWorktreePath()
				if mainErr != nil {
					o.logger.Warn("failed to resolve main worktree for delta eval",
						"agent_id", ag.ID, "error", mainErr)
				} else {
					delta, evalErr := constraints.EvaluateDelta(constraintCfg, featureDir, mainDir)
					if evalErr != nil {
						o.logger.Warn("constraint evaluation failed", "agent_id", ag.ID, "error", evalErr)
					} else if delta.Skipped {
						o.logger.Info("constraint delta skipped (baseline unavailable)",
							"agent_id", ag.ID, "reason", delta.SkipReason)
						if delta.FeatureReport != nil {
							implScorePassed = delta.FeatureReport.Passed
							implScoreFailed = 0 // don't penalize when baseline is unavailable
						}
					} else {
						implScorePassed = delta.FeatureReport.Passed
						// Only count NEW violations (not pre-existing ones)
						implScoreFailed = len(delta.Comparison.NewViolations) + len(delta.Comparison.Worsened)
						if delta.Comparison.Dominated {
							violations := append(delta.Comparison.NewViolations, delta.Comparison.Worsened...)
							o.logger.Warn("new constraint violations after agent merge",
								"agent_id", ag.ID, "task_id", task.ID,
								"new", len(delta.Comparison.NewViolations),
								"worsened", len(delta.Comparison.Worsened),
								"violations", violations)
							if task.Context == nil {
								task.Context = make(model.JSONField)
							}
							task.Context["constraint_violations"] = violations
							ag.Status = model.AgentIdle
							ag.CurrentTaskID = nil
							if err := o.db.Save(ag).Error; err != nil {
								return fmt.Errorf("on agent completed: save agent after constraint fail: %w", err)
							}
							evt, err := state.TransitionTask(task, model.StatusFailed, "orchestrator",
								map[string]any{"reason": "new constraint violations after merge", "violations": violations})
							if err != nil {
								o.logger.Warn("failed to transition task after constraint violation", "task_id", task.ID, "error", err)
								return nil
							}
							if err := o.db.Save(task).Error; err != nil {
								return fmt.Errorf("on agent completed: save task after constraint fail: %w", err)
							}
							if err := o.db.Create(evt).Error; err != nil {
								return fmt.Errorf("on agent completed: save constraint-fail event: %w", err)
							}
							o.publishTaskTransition(task.ID.String(), evt.OldValue, evt.NewValue, "new constraint violations after merge")
							return nil
						}
						if len(delta.Comparison.NewViolations) == 0 && len(delta.Comparison.Worsened) == 0 {
							o.logger.Info("constraint check passed (pre-existing violations only, no regressions)",
								"agent_id", ag.ID, "task_id", task.ID,
								"feature_failed", delta.FeatureReport.Failed)
						}
					}
				}
			}
		}
		if task.Context == nil {
			task.Context = make(model.JSONField)
		}
		task.Context["scores"] = scoreImplGate(implScorePassed, implScoreFailed, implChangedFiles, "")
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
	preStatus := string(task.Status)
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
	o.publishTaskTransition(task.ID.String(), preStatus, string(task.Status), "subtask completed, fast-tracked to done")
	o.logger.Info("subtask completed", "task_id", task.ID, "agent_id", ag.ID)

	// Check if parent's subtasks are all done.
	if task.ParentTaskID != nil {
		var parent model.Task
		if err := o.db.First(&parent, "id = ?", task.ParentTaskID).Error; err == nil {
			if checkErr := o.checkFeatureCompletion(&parent); checkErr != nil {
				o.logger.Error("check parent completion after subtask done", "parent_id", parent.ID, "error", checkErr)
			}
			// Also advance test_writing parents — checkFeatureCompletion
			// only handles in_progress parents.
			if parent.Status == model.StatusTestWriting {
				if checkErr := o.processTestWriting(&parent); checkErr != nil {
					o.logger.Error("process test writing after subtask done",
						"parent_id", parent.ID, "error", checkErr)
				}
			}
		}
	}

	// Check if this task belongs to an experiment variant
	if task.ParentTaskID == nil {
		exp, expErr := experiment.GetVariantByTaskID(o.db, task.ID)
		if expErr == nil && exp != nil {
			if err := o.handleExperimentVariantCompleted(task, exp); err != nil {
				o.logger.Warn("experiment variant completion handling failed", "error", err)
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

		// Check estimated_files reference real paths in the worktree.
		{
			checkDir := ""
			if task.WorktreeBranch != "" {
				fn := strings.TrimPrefix(task.WorktreeBranch, "feature/")
				checkDir = o.worktree.FeatureWorktreePath(fn)
			}
			if checkDir == "" {
				checkDir = ag.WorktreePath
			}
			fileResult := ValidatePlanFileExistence(planResult.Subtasks, checkDir)
			validation.Warnings = append(validation.Warnings, fileResult.Warnings...)
			validation.Errors = append(validation.Errors, fileResult.Errors...)
			if len(fileResult.Errors) > 0 {
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

		// Compute step scores for the plan review gate.
		scores := scorePlanGate(planResult.Subtasks, planResult.TDDExceptions, validation)
		task.Context["scores"] = scores

		// Depth score gate: if depth score is below threshold, request
		// supervisor diagnosis before human review (advisory only).
		o.checkPlanDepthGate(task, scores)
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
	o.publishTaskTransition(task.ID.String(), evt.OldValue, evt.NewValue, "plan ready for review")
	o.logger.Info("plan ready for review", "task_id", task.ID, "subtasks", len(rawPlan.Subtasks))
	return nil
}

// onReviewerCompleted handles a completed reviewer agent by parsing its review.json.
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

// synthesizeCompletion sends a synthetic success completion through processAgentResult.
func (o *Orchestrator) synthesizeCompletion(agentID uuid.UUID) error {
	return o.processAgentResult(agent.Completion{AgentID: agentID, ReturnCode: 0})
}

// populateAgentCompletionFields populates the agent's completion fields:
// - CompletedAt: set to current time
// - ExitReason: mapped from ExitInfo or set to default
// - TotalCostUSD: read from agent Config
// - FinalContextPct: read from agent Config
func populateAgentCompletionFields(ag *model.Agent, comp agent.Completion) {
	// Set CompletedAt to current time
	now := time.Now()
	ag.CompletedAt = &now

	// Map ExitReason from ExitInfo or use default
	if comp.ExitInfo != nil {
		switch comp.ExitInfo.ExitReason {
		case "success":
			ag.ExitReason = model.ExitReasonSuccess
		case "error":
			ag.ExitReason = model.ExitReasonError
		case "context_limit":
			ag.ExitReason = model.ExitReasonContextLimit
		case "killed":
			ag.ExitReason = model.ExitReasonKilled
		case "timeout":
			ag.ExitReason = model.ExitReasonTimeout
		default:
			ag.ExitReason = model.ExitReasonDefault
		}
	} else {
		ag.ExitReason = model.ExitReasonDefault
	}

	// Extract TotalCostUSD from Config if available
	if ag.Config != nil {
		if cost, ok := ag.Config["total_cost_usd"]; ok {
			if costFloat, ok := cost.(float64); ok {
				ag.TotalCostUSD = costFloat
			}
		}
	}

	// Extract FinalContextPct from Config if available
	if ag.Config != nil {
		if pct, ok := ag.Config["context_used_pct"]; ok {
			if pctInt, ok := pct.(float64); ok {
				ag.FinalContextPct = int(pctInt)
			}
		}
	}

	// Extract TokensIn from Config if available
	if ag.Config != nil {
		if tokIn, ok := ag.Config["tokens_in"]; ok {
			switch v := tokIn.(type) {
			case float64:
				ag.TokensIn = int(v)
			case int:
				ag.TokensIn = v
			}
		}
	}

	// Extract TokensOut from Config if available
	if ag.Config != nil {
		if tokOut, ok := ag.Config["tokens_out"]; ok {
			switch v := tokOut.(type) {
			case float64:
				ag.TokensOut = int(v)
			case int:
				ag.TokensOut = v
			}
		}
	}
}
