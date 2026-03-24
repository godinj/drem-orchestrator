package orchestrator

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/godinj/drem-orchestrator/internal/constraints"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/state"
	"github.com/godinj/drem-orchestrator/internal/supervisor"
	"github.com/godinj/drem-orchestrator/internal/worktree"
)

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
		// Quick fix tasks: flag for human review on merge failure, no fixer agent.
		if task.Category.IsQuickFix() {
			task.NeedsHumanReview = true
			if err := o.failTask(task, "quick fix merge failed — flagged for human review"); err != nil {
				return err
			}
			o.emit("quickfix_merge_failed", map[string]any{"task_id": task.ID, "conflicts": result.Conflicts})
			return nil
		}

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

					if analysis.ResolutionStrategy == "spawn_agent" {
						o.logger.Info("spawning resolver agent for merge conflict", "task_id", task.ID)
						// Store conflict context for the fixer agent.
						task.Context["merge_conflict_files"] = result.Conflicts
						task.Context["merge_resolution_hints"] = analysis.ResolutionHints
						// Fail the task (required state transition) then spawn fixer.
						if err := o.failTask(task, "merge conflicts — spawning resolver agent"); err != nil {
							return err
						}
						if _, fixerErr := o.SpawnFixerSession(task.ID); fixerErr != nil {
							o.logger.Warn("failed to spawn fixer for merge conflict",
								"task_id", task.ID, "error", fixerErr)
						}
						o.emit("merge_conflict", map[string]any{
							"task_id":       task.ID,
							"details":       map[string]any{"conflicts": result.Conflicts},
							"fixer_spawned": true,
						})
						return nil
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

// transitionQuickFixToMerging transitions a completed quick fix task from
// IN_PROGRESS to MERGING. It runs constraint checks and fast-tracks through
// the TESTING_READY state (quickfix tasks skip human test review).
func (o *Orchestrator) transitionQuickFixToMerging(task *model.Task) error {
	// Run constraint checks on the feature worktree.
	if task.WorktreeBranch != "" {
		fn := strings.TrimPrefix(task.WorktreeBranch, "feature/")
		featureDir := o.worktree.FeatureWorktreePath(fn)

		constraintCfg, cfgErr := constraints.LoadConfig(featureDir)
		if cfgErr != nil {
			o.logger.Warn("quickfix constraint config load failed",
				"task_id", task.ID, "error", cfgErr)
		} else if constraintCfg != nil {
			report, evalErr := constraints.Evaluate(constraintCfg, featureDir)
			if evalErr != nil {
				o.logger.Warn("quickfix constraint evaluation failed",
					"task_id", task.ID, "error", evalErr)
			} else if report.Failed > 0 {
				o.logger.Warn("quickfix constraint violations, flagging for human review",
					"task_id", task.ID, "failed", report.Failed)

				task.NeedsHumanReview = true
				if task.Context == nil {
					task.Context = make(model.JSONField)
				}
				task.Context["constraint_violations"] = constraints.FormatReport(report)
				if err := o.db.Save(task).Error; err != nil {
					return fmt.Errorf("transition quickfix to merging: save constraint violations: %w", err)
				}

				o.emit("quickfix_constraint_failed", map[string]any{
					"task_id":    task.ID,
					"failed":     report.Failed,
					"violations": constraints.FormatReport(report),
				})
				return nil
			}
		}
	}

	// Fast-track: in_progress → testing_ready → merging.
	evt1, err := state.TransitionTask(task, model.StatusTestingReady, "orchestrator",
		map[string]any{"reason": "quickfix-fast-track"})
	if err != nil {
		return fmt.Errorf("transition quickfix to merging: to testing_ready: %w", err)
	}
	if err := o.db.Save(task).Error; err != nil {
		return fmt.Errorf("transition quickfix to merging: save testing_ready: %w", err)
	}
	if err := o.db.Create(evt1).Error; err != nil {
		return fmt.Errorf("transition quickfix to merging: save testing_ready event: %w", err)
	}

	evt2, err := state.TransitionTask(task, model.StatusMerging, "orchestrator",
		map[string]any{"reason": "quickfix-fast-track"})
	if err != nil {
		return fmt.Errorf("transition quickfix to merging: to merging: %w", err)
	}
	if err := o.db.Save(task).Error; err != nil {
		return fmt.Errorf("transition quickfix to merging: save merging: %w", err)
	}
	if err := o.db.Create(evt2).Error; err != nil {
		return fmt.Errorf("transition quickfix to merging: save merging event: %w", err)
	}

	o.emit("quickfix_merging", map[string]any{"task_id": task.ID})
	o.logger.Info("quickfix transitioning to merging", "task_id", task.ID)
	return nil
}
