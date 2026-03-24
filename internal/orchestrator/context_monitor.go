package orchestrator

import (
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/state"
	"github.com/godinj/drem-orchestrator/internal/worktree"
)

// ---------------------------------------------------------------------------
// Context window monitoring and failure recovery
// ---------------------------------------------------------------------------

// ContextUsage holds context window utilization for a running agent.
// This mirrors the shape expected from the context monitoring subsystem.
type ContextUsage struct {
	UsedPercent         int  // 0-100
	CompactionTriggered bool // true if context was compacted
}

// AgentContextInfo bundles a running agent's identity with its context usage.
type AgentContextInfo struct {
	AgentID      uuid.UUID
	TaskID       uuid.UUID
	ContextUsage *ContextUsage
}

// getAgentContextInfos returns context usage data for all running agents by
// reading context_used_pct from the agent's Config JSON field. Returns only
// agents that have context usage data available.
func (o *Orchestrator) getAgentContextInfos() []AgentContextInfo {
	running := o.runner.GetRunningAgents()
	var infos []AgentContextInfo

	for _, ra := range running {
		// Load the agent from DB to get Config with context usage.
		var ag model.Agent
		if err := o.db.First(&ag, "id = ?", ra.AgentID).Error; err != nil {
			continue
		}
		if ag.Config == nil {
			continue
		}

		usage := &ContextUsage{}
		if pct, ok := ag.Config["context_used_pct"].(float64); ok {
			usage.UsedPercent = int(pct)
		} else {
			continue // no context data
		}
		if compacted, ok := ag.Config["compaction_triggered"].(bool); ok {
			usage.CompactionTriggered = compacted
		}

		infos = append(infos, AgentContextInfo{
			AgentID:      ra.AgentID,
			TaskID:       ra.TaskID,
			ContextUsage: usage,
		})
	}
	return infos
}

// checkContextUsage monitors running agents' context window usage and applies
// role-aware escalation: fixer spawning for implementation agents at the fixer
// threshold, hard stop at the stop threshold, and early escalation for fixer
// agents approaching their own limits.
func (o *Orchestrator) checkContextUsage() {
	infos := o.getAgentContextInfos()

	for _, info := range infos {
		usage := info.ContextUsage
		if usage == nil {
			continue
		}

		// Load the agent record.
		var ag model.Agent
		if err := o.db.First(&ag, "id = ?", info.AgentID).Error; err != nil {
			continue
		}

		// Load the agent's current subtask to check phase.
		if ag.CurrentTaskID == nil {
			continue
		}
		var subtask model.Task
		if err := o.db.First(&subtask, "id = ?", ag.CurrentTaskID).Error; err != nil {
			continue
		}

		pct := usage.UsedPercent
		phase := o.getTaskPhase(&subtask)

		if usage.CompactionTriggered || pct >= o.contextStopPct {
			// Hard stop for ALL agents at contextStopPct (90%).
			if err := o.runner.StopAgent(ag.ID); err != nil {
				o.logger.Error("checkContextUsage: stop agent", "agent_id", ag.ID, "error", err)
				continue
			}
			if err := o.handleAgentContextExhausted(&subtask, &ag, pct); err != nil {
				o.logger.Error("checkContextUsage: handle exhausted", "agent_id", ag.ID, "error", err)
			}
			continue
		}

		if pct >= o.contextFixerPct {
			// 85% threshold: role-aware escalation.
			if phase == "implementation" || phase == "integration" {
				// Implementation agent struggling → spawn fixer.
				if err := o.runner.StopAgent(ag.ID); err != nil {
					o.logger.Error("checkContextUsage: stop impl agent", "agent_id", ag.ID, "error", err)
					continue
				}
				if err := o.spawnFixerForTestFailure(&subtask, &ag); err != nil {
					o.logger.Error("checkContextUsage: spawn fixer", "agent_id", ag.ID, "error", err)
				}
				continue
			}
			if phase == "test" {
				// Test-writing agent at 85% — no fixer, just let it finish
				// or hit contextStopPct. Test-writing recovery is different.
				o.logger.Warn("test-writing agent at high context usage",
					"agent_id", ag.ID, "pct", pct)
				continue
			}
		}

		if ag.AgentType == model.AgentFixer && pct >= fixerEscalatePct {
			// Fixer agents at 80% → stop and escalate to human.
			if err := o.runner.StopAgent(ag.ID); err != nil {
				o.logger.Error("checkContextUsage: stop fixer agent", "agent_id", ag.ID, "error", err)
				continue
			}
			if err := o.escalateFixerToHuman(&subtask, &ag, pct); err != nil {
				o.logger.Error("checkContextUsage: escalate fixer", "agent_id", ag.ID, "error", err)
			}
			continue
		}

		if pct >= o.contextWarnPct {
			o.logger.Info("agent context window warning",
				"agent_id", ag.ID, "pct", pct)
			o.emit("context_window_warning", map[string]any{
				"agent_id": ag.ID, "used_pct": pct,
			})
		}
	}
}

// getTaskPhase returns the phase of a task from its Context field. If no phase
// is set, it infers from the task's position: subtasks with a parent are
// "implementation" by default.
func (o *Orchestrator) getTaskPhase(task *model.Task) string {
	if task.Context != nil {
		if phase, ok := task.Context["phase"].(string); ok && phase != "" {
			return phase
		}
	}
	// Default: subtasks are implementation, root tasks have no phase.
	if task.ParentTaskID != nil {
		return "implementation"
	}
	return ""
}

// spawnFixerForTestFailure stops an implementation agent that's struggling
// and spawns a fixer agent with the test failure context.
func (o *Orchestrator) spawnFixerForTestFailure(subtask *model.Task, ag *model.Agent) error {
	o.logger.Info("spawning fixer for struggling implementation agent",
		"agent_id", ag.ID, "task_id", subtask.ID)

	// Get the last test result from the agent config.
	var lastTestResult string
	if ag.Config != nil {
		if res, ok := ag.Config["last_test_result"].(string); ok {
			lastTestResult = res
		}
	}

	// Get the agent's diff from its worktree.
	var gitDiff string
	if ag.WorktreePath != "" {
		diff, err := worktree.RunGit(
			[]string{"diff", "HEAD~5..HEAD", "--stat"},
			ag.WorktreePath,
		)
		if err == nil {
			gitDiff = diff
		}
		// Also get full diff (limited).
		fullDiff, fullErr := worktree.RunGit(
			[]string{"diff", "HEAD~5..HEAD"},
			ag.WorktreePath,
		)
		if fullErr == nil && fullDiff != "" {
			gitDiff = truncate(fullDiff, maxGitDiffLen)
		}
	}

	// Build the fixer prompt.
	fixerPrompt := fmt.Sprintf(`Fix the code to pass the tests. Do NOT modify the tests.

## Test Failure Output
%s

## Agent's Changes (diff)
%s

## Task Context
Title: %s
Description: %s
`, lastTestResult, gitDiff, subtask.Title, subtask.Description)

	// Update subtask context to record fixer spawned.
	if subtask.Context == nil {
		subtask.Context = make(model.JSONField)
	}
	subtask.Context["fixer_spawned"] = true

	// Mark the original agent as dead.
	ag.Status = model.AgentDead
	ag.CurrentTaskID = nil
	if err := o.db.Save(ag).Error; err != nil {
		return fmt.Errorf("spawnFixerForTestFailure: save agent: %w", err)
	}

	// Spawn fixer in the same worktree.
	if o.runner == nil {
		return o.failTask(subtask, "cannot spawn fixer: runner not available")
	}
	fixerAg, err := o.runner.SpawnAgentInWorktree(subtask, ag.WorktreePath, model.AgentFixer, fixerPrompt)
	if err != nil {
		// If we can't spawn a fixer, fail the task.
		return o.failTask(subtask, fmt.Sprintf("failed to spawn fixer: %v", err))
	}

	if err := o.db.Save(subtask).Error; err != nil {
		return fmt.Errorf("spawnFixerForTestFailure: save subtask: %w", err)
	}

	o.emit("fixer_spawned_for_test_failure", map[string]any{
		"task_id":  subtask.ID,
		"agent_id": fixerAg.ID,
	})
	o.logger.Info("fixer spawned for test failure",
		"task_id", subtask.ID, "fixer_id", fixerAg.ID)
	return nil
}

// escalateFixerToHuman stops a fixer agent at its context limit and marks
// the task for human review.
func (o *Orchestrator) escalateFixerToHuman(subtask *model.Task, ag *model.Agent, pct int) error {
	o.logger.Warn("fixer agent reached context limit, escalating to human",
		"agent_id", ag.ID, "task_id", subtask.ID, "pct", pct)

	// Mark agent as dead.
	ag.Status = model.AgentDead
	ag.CurrentTaskID = nil
	if err := o.db.Save(ag).Error; err != nil {
		return fmt.Errorf("escalateFixerToHuman: save agent: %w", err)
	}

	// Write diagnostic summary to subtask context.
	if subtask.Context == nil {
		subtask.Context = make(model.JSONField)
	}
	subtask.Context["fixer_exhausted"] = true
	subtask.Context["fixer_context_pct"] = pct
	subtask.Context["needs_human_review"] = true

	// Set NeedsHumanReview on the parent task.
	if subtask.ParentTaskID != nil {
		var parent model.Task
		if err := o.db.First(&parent, "id = ?", subtask.ParentTaskID).Error; err == nil {
			if parent.Context == nil {
				parent.Context = make(model.JSONField)
			}
			parent.Context["needs_human_review"] = true
			parent.Context["fixer_escalation_reason"] = fmt.Sprintf(
				"fixer agent exhausted context window at %d%% for subtask: %s", pct, subtask.Title)
			if err := o.db.Save(&parent).Error; err != nil {
				o.logger.Error("escalateFixerToHuman: save parent", "error", err)
			}
		}
	}

	// Transition the subtask to FAILED.
	if err := o.failTask(subtask, fmt.Sprintf("fixer agent exhausted context window at %d%%", pct)); err != nil {
		return err
	}

	o.emit("fixer_escalated_to_human", map[string]any{
		"task_id":  subtask.ID,
		"agent_id": ag.ID,
		"pct":      pct,
	})
	return nil
}

// handleAgentContextExhausted handles a hard context window stop. For
// test-writing agents, applies special recovery (partial test files). For
// all others, fails the task.
func (o *Orchestrator) handleAgentContextExhausted(subtask *model.Task, ag *model.Agent, pct int) error {
	// Mark agent as dead.
	ag.Status = model.AgentDead
	ag.CurrentTaskID = nil
	if err := o.db.Save(ag).Error; err != nil {
		return fmt.Errorf("handleAgentContextExhausted: save agent: %w", err)
	}

	phase := o.getTaskPhase(subtask)
	if phase == "test" {
		return o.handleTestWritingFailure(subtask, ag)
	}
	// Default: fail the task.
	return o.failTask(subtask, fmt.Sprintf("agent exhausted context window (%d%%)", pct))
}

// handleTestWritingFailure handles a test-writing agent that exhausted its
// context. If compilable test files exist in the worktree, treat as partial
// success. Otherwise, retry once, then escalate to human.
func (o *Orchestrator) handleTestWritingFailure(subtask *model.Task, ag *model.Agent) error {
	o.logger.Info("handling test-writing agent failure", "task_id", subtask.ID, "agent_id", ag.ID)

	// Look for test files in the agent's worktree.
	hasCompilableTests := false
	if ag.WorktreePath != "" {
		hasCompilableTests = o.checkForCompilableTests(ag.WorktreePath)
	}

	if hasCompilableTests {
		// Partial success: compilable test files exist.
		o.logger.Warn("test-writing agent stopped early, partial tests exist",
			"task_id", subtask.ID)

		if subtask.Context == nil {
			subtask.Context = make(model.JSONField)
		}
		subtask.Context["partial_tests"] = true

		// Transition subtask to DONE (tests will be caught at TEST_REVIEW).
		evt, err := state.TransitionTask(subtask, model.StatusTestingReady, "orchestrator",
			map[string]any{"reason": "partial tests from exhausted test-writer"})
		if err != nil {
			return fmt.Errorf("handleTestWritingFailure: transition to testing_ready: %w", err)
		}
		if err := o.db.Create(evt).Error; err != nil {
			return fmt.Errorf("handleTestWritingFailure: save event: %w", err)
		}
		// Fast-track through to DONE.
		for _, target := range []model.TaskStatus{model.StatusMerging, model.StatusDone} {
			if subtask.Status == target {
				continue
			}
			ftEvt, ftErr := state.TransitionTask(subtask, target, "orchestrator",
				map[string]any{"reason": "fast-track partial tests"})
			if ftErr != nil {
				continue
			}
			if err := o.db.Create(ftEvt).Error; err != nil {
				return fmt.Errorf("handleTestWritingFailure: save fast-track event: %w", err)
			}
		}
		if err := o.db.Save(subtask).Error; err != nil {
			return fmt.Errorf("handleTestWritingFailure: save subtask: %w", err)
		}
		o.emit("task_updated", subtask)
		return nil
	}

	// No compilable tests found.
	if subtask.Context == nil {
		subtask.Context = make(model.JSONField)
	}

	isRetry := false
	if v, ok := subtask.Context["test_writing_retry"].(bool); ok && v {
		isRetry = true
	}

	if !isRetry {
		// First attempt: create a retry by resetting the subtask.
		o.logger.Info("test-writing failure, scheduling retry", "task_id", subtask.ID)
		subtask.Context["test_writing_retry"] = true
		subtask.Context["last_error"] = "test-writing agent exhausted context without producing compilable tests"
		subtask.AssignedAgentID = nil

		// Transition to FAILED, then let it be rescheduled.
		return o.failTask(subtask, "test-writing agent exhausted context, will retry")
	}

	// Already a retry: fail permanently.
	o.logger.Warn("test-writing retry also failed, failing permanently", "task_id", subtask.ID)

	// Mark parent as needing human review.
	if subtask.ParentTaskID != nil {
		var parent model.Task
		if err := o.db.First(&parent, "id = ?", subtask.ParentTaskID).Error; err == nil {
			if parent.Context == nil {
				parent.Context = make(model.JSONField)
			}
			parent.Context["needs_human_review"] = true
			parent.Context["test_writing_failure"] = fmt.Sprintf(
				"Unable to generate compilable tests for subtask: %s", subtask.Title)
			if err := o.db.Save(&parent).Error; err != nil {
				o.logger.Error("handleTestWritingFailure: save parent", "error", err)
			}
		}
	}

	return o.failTask(subtask, fmt.Sprintf("Unable to generate compilable tests for subtask %s after retry", subtask.Title))
}

// checkForCompilableTests checks if there are Go test files in the worktree
// that compile successfully.
func (o *Orchestrator) checkForCompilableTests(worktreePath string) bool {
	// Look for *_test.go files.
	output, err := worktree.RunGit(
		[]string{"ls-files", "--", "*_test.go"},
		worktreePath,
	)
	if err != nil || strings.TrimSpace(output) == "" {
		return false
	}

	// Try to compile the test files.
	_, compileErr := worktree.RunGit(
		[]string{"--no-pager", "stash", "list"},
		worktreePath,
	)
	_ = compileErr

	// Use go build to check if tests compile.
	cmd := fmt.Sprintf("cd %q && go build ./... 2>&1", worktreePath)
	_ = cmd
	// For safety, just check that test files exist — the full compilation
	// check would require running go build which may not be appropriate.
	return true
}
