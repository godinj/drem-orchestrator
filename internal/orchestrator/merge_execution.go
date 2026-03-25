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

// dispatchMerges queries all MERGING tasks and calls executeMerge for each.
func (o *Orchestrator) dispatchMerges() {
	var mergingTasks []model.Task
	if err := o.db.Where("project_id = ? AND status = ?", o.projectID, model.StatusMerging).
		Find(&mergingTasks).Error; err != nil {
		o.logger.Error("query merging tasks", "error", err)
		return
	}
	for i := range mergingTasks {
		if err := o.executeMerge(&mergingTasks[i]); err != nil {
			o.logger.Error("execute merge", "task_id", mergingTasks[i].ID, "error", err)
		}
	}
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
		// Quick fix tasks: flag for human review on merge failure, no fixer agent.
		if task.Category.IsQuickFix() {
			task.NeedsHumanReview = true
			if err := o.failTask(task, "quick fix merge failed — flagged for human review"); err != nil {
				return err
			}
			o.emit("quickfix_merge_failed", map[string]any{"task_id": task.ID, "conflicts": result.Conflicts})
			return nil
		}

		// Transient failure (no real conflicts): retry with exponential backoff.
		if len(result.Conflicts) == 0 {
			attemptState := LoadMergeAttemptState(task)
			attemptState.Increment()
			attemptState.Save(task)

			policy := DefaultMergeRetryPolicy()
			if policy.Exhausted(attemptState.AttemptCount()) {
				reason := fmt.Sprintf("merge failed after %d attempts", attemptState.AttemptCount())
				if err := o.failTask(task, reason); err != nil {
					return err
				}
				o.emit("merge_retries_exhausted", map[string]any{"task_id": task.ID, "attempts": attemptState.AttemptCount()})
				return nil
			}

			// Stay in MERGING for next tick to retry.
			if err := o.db.Save(task).Error; err != nil {
				return fmt.Errorf("execute merge: save retry state: %w", err)
			}
			o.logger.Info("merge transient failure, will retry",
				"task_id", task.ID,
				"attempt", attemptState.AttemptCount(),
				"next_delay", policy.Delay(attemptState.AttemptCount()))
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

		o.logger.Warn("merge conflict classification",
			"task_id", task.ID,
			"trivial", result.TrivialCount,
			"non_trivial", result.NonTrivialCount)

		reason := fmt.Sprintf("merge conflicts (%d trivial, %d non-trivial):\n%s",
			result.TrivialCount, result.NonTrivialCount, result.ClassifiedDetails)

		details := map[string]any{
			"conflicts":          result.Conflicts,
			"classified_details": result.ClassifiedDetails,
		}
		if err := o.failTask(task, reason); err != nil {
			return err
		}
		o.emit("merge_conflict", map[string]any{"task_id": task.ID, "details": details})
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

// mergerClient abstracts the merge operations used by the orchestrator.
// Defined at the consumption site (per architecture rule) so tests can
// provide stubs without importing the merge package.
type mergerClient interface {
	MergeFeatureIntoMain(task *model.Task) (*worktree.MergeResult, error)
	MergeAgentIntoFeature(agentBranch, featureWorktree string) (*worktree.MergeResult, error)
}

// contextKeyMergeAttemptCount is the task.Context key for merge retry tracking.
const contextKeyMergeAttemptCount = "merge_attempt_count"

// MergeAttemptState provides typed access to merge retry tracking fields
// stored in task.Context. It reads and writes the merge_attempt_count field,
// replacing stringly-typed map access with a structured API.
type MergeAttemptState struct {
	attemptCount int
}

// LoadMergeAttemptState reads the current merge attempt state from a task's
// Context map. Returns a zero-valued state if no prior attempts exist.
func LoadMergeAttemptState(task *model.Task) MergeAttemptState {
	if task.Context == nil {
		return MergeAttemptState{}
	}
	raw, ok := task.Context[contextKeyMergeAttemptCount]
	if !ok {
		return MergeAttemptState{}
	}
	switch v := raw.(type) {
	case float64:
		return MergeAttemptState{attemptCount: int(v)}
	case int:
		return MergeAttemptState{attemptCount: v}
	default:
		return MergeAttemptState{}
	}
}

// AttemptCount returns the number of merge attempts made so far.
func (s MergeAttemptState) AttemptCount() int {
	return s.attemptCount
}

// Increment bumps the attempt count by one.
func (s *MergeAttemptState) Increment() {
	s.attemptCount++
}

// Save writes the attempt state back into the task's Context map,
// initializing the map if nil.
func (s MergeAttemptState) Save(task *model.Task) {
	if task.Context == nil {
		task.Context = make(model.JSONField)
	}
	task.Context[contextKeyMergeAttemptCount] = s.attemptCount
}

// checkDepthConstraintFailures checks for depth-type constraint failures and
// requests advisory supervisor diagnosis, stored as a system comment.
func (o *Orchestrator) checkDepthConstraintFailures(task *model.Task, report *constraints.Report, featureDir string) {
	if report == nil || o.supervisor == nil {
		return
	}

	// Check for depth-type constraint failures.
	hasDepthFailure := false
	for _, r := range report.Results {
		if !r.Passed && r.Type == "depth" {
			hasDepthFailure = true
			break
		}
	}

	if !hasDepthFailure {
		return
	}

	constraintReport := constraints.FormatReport(report)
	// Get the diff for context.
	diff := ""
	if o.worktree != nil {
		diffOutput, err := getChangedFilesDiff(featureDir, o.worktree.DefaultBranch)
		if err == nil {
			diff = diffOutput
		}
	}

	var diagnosis supervisor.DepthConstraintDiagnosis
	prompt := supervisor.DepthConstraintDiagnosisPrompt(task.Title, constraintReport, diff)

	if err := o.supervisor.EvaluateJSON(context.Background(), prompt, &diagnosis); err != nil {
		o.logger.Warn("supervisor depth constraint diagnosis failed, continuing",
			"task_id", task.ID, "error", err)
		return
	}

	// Store the diagnosis in task context.
	if task.Context == nil {
		task.Context = make(model.JSONField)
	}
	task.Context["depth_diagnosis"] = map[string]any{
		"violations":       diagnosis.Violations,
		"root_cause":       diagnosis.RootCause,
		"recommendation":   diagnosis.Recommendation,
		"rejection_reason": diagnosis.RejectionReason,
	}

	// Add supervisor diagnosis as a system comment on the task.
	if diagnosis.RejectionReason != "" {
		comment := model.TaskComment{
			TaskID: task.ID,
			Author: "system",
			Body:   fmt.Sprintf("[Depth Constraint Failure] %s", diagnosis.RejectionReason),
		}
		if err := o.db.Create(&comment).Error; err != nil {
			o.logger.Warn("failed to create depth diagnosis comment", "task_id", task.ID, "error", err)
		}
	}

	o.logger.Info("depth constraint diagnosis completed",
		"task_id", task.ID, "rejection_reason", diagnosis.RejectionReason)
}
