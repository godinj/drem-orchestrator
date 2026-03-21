package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/state"
	"github.com/godinj/drem-orchestrator/internal/supervisor"
	"github.com/godinj/drem-orchestrator/internal/worktree"
)

// handleAgentMergeFailure handles the case where merging an agent's work into
// the feature branch fails. When a supervisor is available, it diagnoses the
// conflict and may spawn a fixer agent. Without a supervisor, it falls back to
// the current behavior: fail the task and preserve the agent branch.
func (o *Orchestrator) handleAgentMergeFailure(ag *model.Agent, task *model.Task, result *worktree.MergeResult, featureDir string) error {
	// Supervisor-powered merge conflict diagnosis.
	if o.supervisor != nil && result != nil && len(result.Conflicts) > 0 {
		var analysis supervisor.MergeConflictAnalysis
		diffOutput, _ := worktree.RunGit([]string{
			"diff", result.TargetBranch + "..." + ag.WorktreeBranch,
		}, featureDir)

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

				o.logSupervisorAction(supervisor.JournalEntry{
					Timestamp: time.Now(),
					AgentName: ag.Name,
					TaskID:    task.ID.String(),
					TaskTitle: task.Title,
					Type:      "merge_conflict",
					Summary:   fmt.Sprintf("Severity: %s — Strategy: %s", analysis.Severity, analysis.ResolutionStrategy),
					Details: map[string]string{
						"Resolution Hints": analysis.ResolutionHints,
						"Conflicts":        strings.Join(result.Conflicts, ", "),
					},
					Outcome: "Spawning resolver agent for agent-to-feature merge conflict",
				})
				return nil
			}

			// Non-spawn strategy — log and fall through to default.
			o.logSupervisorAction(supervisor.JournalEntry{
				Timestamp: time.Now(),
				AgentName: ag.Name,
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
		}
	}

	// Default fallback: set agent to idle, fail the task, preserve agent branch.
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

// depthScoreThreshold is the minimum depth score (0.0–1.0) a plan must achieve
// to proceed to human review without supervisor diagnosis. Plans scoring below
// this threshold trigger a supervisor plan depth review. Initial value: 0.5.
const depthScoreThreshold = 0.5

// checkPlanDepthGate evaluates the plan's depth score and, if below threshold,
// requests a supervisor diagnosis. The diagnosis is stored as a system comment
// and in the task context. This is advisory only — the plan always proceeds to
// human review regardless of the supervisor outcome.
func (o *Orchestrator) checkPlanDepthGate(task *model.Task, scores map[string]any) {
	depthScore, ok := scores["depth"].(float64)
	if !ok {
		return // no depth score available
	}

	if depthScore >= depthScoreThreshold {
		return // depth is acceptable
	}

	if o.supervisor == nil {
		o.logger.Warn("plan depth score below threshold but no supervisor configured",
			"task_id", task.ID, "depth_score", depthScore)
		return
	}

	// Serialize plan for the supervisor prompt.
	planJSON := ""
	if task.Plan != nil {
		if data, err := json.MarshalIndent(task.Plan, "", "  "); err == nil {
			planJSON = string(data)
		}
	}

	var review supervisor.PlanDepthReview
	prompt := supervisor.PlanDepthReviewPrompt(task.Title, task.Description, planJSON, depthScore)

	if err := o.supervisor.EvaluateJSON(context.Background(), prompt, &review); err != nil {
		o.logger.Warn("supervisor plan depth review failed, continuing",
			"task_id", task.ID, "error", err)
		return
	}

	// Store the review in task context.
	if task.Context == nil {
		task.Context = make(model.JSONField)
	}
	task.Context["depth_review"] = map[string]any{
		"assessment":       review.Assessment,
		"shallow_areas":    review.ShallowAreas,
		"recommendations":  review.Recommendations,
		"rejection_reason": review.RejectionReason,
	}

	// Add supervisor diagnosis as a system comment on the task.
	if review.RejectionReason != "" {
		comment := model.TaskComment{
			TaskID: task.ID,
			Author: "system",
			Body:   fmt.Sprintf("[Depth Review] Score: %.0f%% — %s", depthScore*100, review.RejectionReason),
		}
		if err := o.db.Create(&comment).Error; err != nil {
			o.logger.Warn("failed to create depth review comment", "task_id", task.ID, "error", err)
		}
	}

	o.logSupervisorAction(supervisor.JournalEntry{
		Timestamp: time.Now(),
		AgentName: "orchestrator",
		TaskID:    task.ID.String(),
		TaskTitle: task.Title,
		Type:      "depth_review",
		Summary:   fmt.Sprintf("Plan depth score %.0f%% below threshold %.0f%%", depthScore*100, depthScoreThreshold*100),
		Details: map[string]string{
			"Assessment":       review.Assessment,
			"Rejection Reason": review.RejectionReason,
		},
		Outcome: "Depth review completed, plan proceeds to human review",
	})

	o.logger.Info("plan depth review completed",
		"task_id", task.ID, "depth_score", depthScore, "assessment", review.Assessment)
}

// checkDepthConstraintFailures requests a supervisor diagnosis for
// depth-specific constraint failures. The caller must check for depth failures
// and pass the pre-formatted constraint report string. The diagnosis is stored
// as a system comment and in the task context. This is advisory only — normal
// constraint failure handling continues regardless.
func (o *Orchestrator) checkDepthConstraintFailures(task *model.Task, constraintReport, featureDir string) {
	if o.supervisor == nil {
		return
	}

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

	o.logSupervisorAction(supervisor.JournalEntry{
		Timestamp: time.Now(),
		AgentName: "orchestrator",
		TaskID:    task.ID.String(),
		TaskTitle: task.Title,
		Type:      "depth_constraint_diagnosis",
		Summary:   diagnosis.RejectionReason,
		Details: map[string]string{
			"Root Cause":     diagnosis.RootCause,
			"Recommendation": diagnosis.Recommendation,
		},
		Outcome: "Depth constraint diagnosis completed",
	})

	o.logger.Info("depth constraint diagnosis completed",
		"task_id", task.ID, "rejection_reason", diagnosis.RejectionReason)
}

// getChangedFilesDiff returns the diff of changed files between the worktree
// HEAD and the given base branch. Returns empty string on error.
func getChangedFilesDiff(worktreeDir, baseBranch string) (string, error) {
	output, err := worktree.RunGit([]string{"diff", baseBranch + "...HEAD"}, worktreeDir)
	if err != nil {
		return "", fmt.Errorf("get changed files diff: %w", err)
	}
	// Truncate to avoid overly large diffs in prompts.
	if len(output) > maxGitDiffLen {
		output = output[:maxGitDiffLen] + "\n... (truncated)"
	}
	return output, nil
}
