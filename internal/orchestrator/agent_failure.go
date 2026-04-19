package orchestrator

import (
	"context"
	"fmt"
	"strings"

	"github.com/godinj/drem-orchestrator/internal/experiment"
	"github.com/godinj/drem-orchestrator/internal/gitexec"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/state"
	"github.com/godinj/drem-orchestrator/internal/supervisor"
)

// onAgentFailed handles a failed agent. Uses supervisor diagnosis for smart
// retry decisions when available; otherwise planners retry and coders hard-fail.
func (o *Orchestrator) onAgentFailed(ag *model.Agent, task *model.Task) error {
	// Classifier agents have their own failure handling — they stay in
	// CLASSIFYING and get parked for human triage instead of transitioning
	// to FAILED.
	if ag.AgentType == model.AgentClassifier {
		return o.onClassifierFailed(ag, task)
	}

	// Prep agents degrade gracefully — mark prep as failed and let the coder
	// proceed without enrichment on the next dispatch tick.
	if ag.AgentType == model.AgentPrep {
		return o.onPrepFailed(ag, task)
	}

	// Read agent output for error details.
	var output string
	if o.runner != nil {
		var err error
		output, err = o.runner.GetAgentOutput(ag.ID)
		if err != nil {
			o.logger.Warn("failed to read failed agent output", "agent_id", ag.ID, "error", err)
			output = "unknown error"
		}
	} else {
		output = "unknown error"
	}

	// Store error in task context.
	if task.Context == nil {
		task.Context = make(model.JSONField)
	}
	task.Context["last_error"] = truncate(output, maxErrorSnippetLen)

	// Before cleanup, check if the agent's work was already merged into the
	// feature branch (e.g. merge succeeded on a prior attempt but DB update
	// failed). Must check BEFORE worktree removal because RemoveAgentWorktree
	// deletes the branch ref needed by merge-base --is-ancestor.
	earlyAlreadyMerged := false
	if ag.WorktreeBranch != "" {
		if featureDir := o.resolveFeatureWorktree(task); featureDir != "" {
			earlyAlreadyMerged = o.isWorkAlreadyMerged(task, featureDir)
		}
	}

	// Only remove the agent worktree if it has no commits to preserve.
	// If the agent produced work, keep the worktree so it can be retried
	// or manually resolved (consistent with onAgentCompleted merge failure).
	if ag.WorktreeBranch != "" {
		featureDir := o.resolveFeatureWorktree(task)
		hasWork := false
		if featureDir != "" {
			if hasCommits, err := gitexec.BranchHasNewCommits(context.Background(), featureDir, ag.WorktreeBranch); err == nil && hasCommits {
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
	o.publishAgentStatus(task.ID.String(), ag.ID.String(), string(ag.AgentType), string(model.AgentDead))

	// Supervisor-powered failure diagnosis.
	if o.supervisor != nil {
		var diagnosis supervisor.FailureDiagnosis
		diagPrompt := supervisor.FailureDiagnosisPrompt(
			task.Title, task.Description, string(ag.AgentType), output, truncate(output, maxErrorSnippetLen),
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
					if err := o.failTask(task, fmt.Sprintf("agent failed after %d retries (supervisor: %s)", retries, diagnosis.RootCause)); err != nil {
						return err
					}
					o.emit("agent_failed", map[string]any{"task_id": task.ID, "agent_id": ag.ID, "diagnosis": diagnosis.RootCause})
					return nil
				}

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
	// but DB update failed on a prior attempt). Uses the result cached before
	// worktree cleanup because RemoveAgentWorktree deletes the branch ref.
	if earlyAlreadyMerged {
		o.logger.Info("agent failed but work already merged, fast-tracking to done",
			"task_id", task.ID, "agent_id", ag.ID)
		// Fast-track subtask through states to DONE.
		preStatus := string(task.Status)
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
		o.publishTaskTransition(task.ID.String(), preStatus, string(task.Status), "already merged, fast-tracked to done")
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

	// Before retrying or failing, check if work was already merged into the
	// feature branch (e.g. a prior merge succeeded but the retry agent found
	// no new commits to produce). Mirror the pattern in onAgentFailed.
	featureDir := o.resolveFeatureWorktree(task)
	if featureDir != "" && o.isWorkAlreadyMerged(task, featureDir) {
		o.logger.Info("agent produced no commits but work already merged, fast-tracking to done",
			"task_id", task.ID, "agent_id", ag.ID)
		preStatus := string(task.Status)
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
				map[string]any{"reason": "already-merged-on-empty-work"})
			if tErr != nil {
				continue
			}
			if err := o.db.Create(evt).Error; err != nil {
				return fmt.Errorf("on agent empty work: save event: %w", err)
			}
		}
		if err := o.db.Save(task).Error; err != nil {
			return fmt.Errorf("on agent empty work: save task after fast-track: %w", err)
		}
		o.emit("task_updated", task)
		o.publishTaskTransition(task.ID.String(), preStatus, string(task.Status), "already merged, fast-tracked to done")
		return nil
	}

	retries := o.incrementRetryCount(task)

	// Supervisor-powered diagnosis.
	if o.supervisor != nil {
		var diagnosis supervisor.FailureDiagnosis
		diagPrompt := supervisor.FailureDiagnosisPrompt(
			task.Title, task.Description, string(ag.AgentType),
			"Agent completed successfully (exit code 0) but did not commit any changes to the repository.",
			truncate(agentOutput, maxErrorSnippetLen),
		)
		if diagErr := o.supervisor.EvaluateJSON(context.Background(), diagPrompt, &diagnosis); diagErr != nil {
			o.logger.Warn("supervisor empty-work diagnosis failed", "error", diagErr)
		} else {
			task.Context["failure_diagnosis"] = diagnosis.RootCause
			if diagnosis.PromptAdjustment != "" {
				task.Context["prompt_adjustment"] = diagnosis.PromptAdjustment
			}

			// Fast-track if supervisor determines work is already complete.
			if isWorkAlreadyCompleteCategory(diagnosis.Category) {
				task.Context["done_no_work"] = true
				// Fast-track to DONE.
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
						map[string]any{"reason": "already-complete-no-work"})
					if tErr != nil {
						continue
					}
					if err := o.db.Create(evt).Error; err != nil {
						o.logger.Error("empty work fast-track event", "task_id", task.ID, "error", err)
						break
					}
				}
				if err := o.db.Save(task).Error; err != nil {
					return fmt.Errorf("on agent empty work: save task after fast-track: %w", err)
				}
				o.emit("task_updated", task)
				return nil
			}

			if diagnosis.ShouldRetry && retries < MaxEmptyWorkRetries {
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

func isWorkAlreadyCompleteCategory(category string) bool {
	return category == "already_complete" || category == "no_changes_needed" || category == "work_done"
}

// handleAgentMergeFailure handles the case where merging an agent's work into
// the feature branch fails. When a supervisor is available, it diagnoses the
// conflict and may spawn a fixer agent. Without a supervisor, it falls back to
// the current behavior: fail the task and preserve the agent branch.
func (o *Orchestrator) handleAgentMergeFailure(ag *model.Agent, task *model.Task, result *WorktreeMergeResult, featureDir string) error {
	// Supervisor-powered merge conflict diagnosis.
	if o.supervisor != nil && result != nil && len(result.Conflicts) > 0 {
		var analysis supervisor.MergeConflictAnalysis
		diffOutput, _ := gitexec.RunGit(context.Background(), featureDir,
			"diff", result.TargetBranch+"..."+ag.WorktreeBranch,
		)

		mcPrompt := supervisor.MergeConflictPrompt(
			ag.WorktreeBranch, result.TargetBranch,
			result.Conflicts, diffOutput,
		)
		if mcErr := o.supervisor.EvaluateJSON(context.Background(), mcPrompt, &analysis); mcErr != nil {
			o.logger.Warn("supervisor agent merge conflict analysis failed", "task_id", task.ID, "error", mcErr)
		} else {
			if task.Context == nil {
				task.Context = make(model.JSONField)
			}
			task.Context["merge_conflict_severity"] = analysis.Severity
			task.Context["merge_conflict_strategy"] = analysis.ResolutionStrategy
			task.Context["merge_conflict_hints"] = analysis.ResolutionHints

			if analysis.ResolutionStrategy == "spawn_agent" {
				task.Context["merge_conflict_files"] = result.Conflicts
				task.Context["merge_resolution_hints"] = analysis.ResolutionHints

				// Set agent to idle and fail the task.
				ag.Status = model.AgentIdle
				ag.CurrentTaskID = nil
				if err := o.db.Save(ag).Error; err != nil {
					return fmt.Errorf("handle agent merge failure: save agent: %w", err)
				}
				if err := o.failTask(task, "merge conflicts — spawning resolver agent"); err != nil {
					return err
				}
				if _, fixerErr := o.SpawnFixerSession(task.ID); fixerErr != nil {
					o.logger.Warn("failed to spawn fixer for agent merge conflict",
						"task_id", task.ID, "error", fixerErr)
				}

				return nil
			}
		}
	}

	// Default fallback: set agent to idle, fail the task, preserve agent branch.
	ag.Status = model.AgentIdle
	ag.CurrentTaskID = nil
	if err := o.db.Save(ag).Error; err != nil {
		return fmt.Errorf("on agent completed: save agent: %w", err)
	}

	// Record merge conflict metric
	if o.metrics != nil {
		o.metrics.Record(ag.ID, "merge_conflict", 1.0, nil)
	}

	failureReason := "merge into feature branch failed, agent branch preserved"
	publishReason := "merge into feature branch failed"
	if result != nil {
		var parts []string
		if len(result.Conflicts) > 0 {
			parts = append(parts, fmt.Sprintf("conflicts: %s", strings.Join(result.Conflicts, ", ")))
		}
		if result.GitStderr != "" {
			parts = append(parts, fmt.Sprintf("git stderr: %s", result.GitStderr))
		}
		if len(parts) > 0 {
			failureReason = fmt.Sprintf("merge into feature branch failed (%s), agent branch preserved", strings.Join(parts, "; "))
			publishReason = fmt.Sprintf("merge into feature branch failed (%s)", strings.Join(parts, "; "))
		}
	}

	evt, err := state.TransitionTask(task, model.StatusFailed, "orchestrator",
		map[string]any{"reason": failureReason})
	if err != nil {
		o.logger.Warn("failed to transition task to failed after merge failure", "task_id", task.ID, "error", err)
	} else {
		if err := o.db.Save(task).Error; err != nil {
			return fmt.Errorf("on agent completed: save task after merge failure: %w", err)
		}
		if err := o.db.Create(evt).Error; err != nil {
			return fmt.Errorf("on agent completed: save merge-failure event: %w", err)
		}
		o.publishTaskTransition(task.ID.String(), evt.OldValue, evt.NewValue, publishReason)
	}

	// Check if this task belongs to an experiment variant
	exp, expErr := experiment.GetVariantByTaskID(o.db, task.ID)
	if expErr == nil && exp != nil {
		if err := o.handleExperimentVariantFailed(task, exp); err != nil {
			o.logger.Warn("experiment variant failure handling failed", "error", err)
		}
	}

	return nil
}
