package orchestrator

import (
	"context"
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
