// direct_tool_dispatch.go wires coder, reviewer, and fixer roles to the
// DirectToolAgent runner. These methods bypass the Claude Code / OpenCode
// subprocess and call the SGLang HTTP API directly, saving ~20K tokens of
// tool-definition overhead per agent.
//
// A lightweight agent DB record is created for audit trail and duplicate
// dispatch prevention (mirrors processClassifyingTasksDirect). On success,
// completion is funneled back through the existing onAgentCompleted /
// onReviewerCompleted / onFixerCompleted handlers so merge, review, and
// fixer flows stay unchanged.
package orchestrator

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/agent"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/prompt"
	"github.com/godinj/drem-orchestrator/internal/state"
	"github.com/godinj/drem-orchestrator/internal/worktree"
)

// shouldUseDirectToolAgent decides whether the given subtask should be
// dispatched via the direct SGLang HTTP path. The direct path is used when
// the orchestrator has been configured with a DirectToolAgentConfig (via
// SetDirectToolAgentConfig). Once per-role provider resolution is wired
// through the task context, this will also consult the agent's
// EffectiveProvider for finer-grained routing.
func (o *Orchestrator) shouldUseDirectToolAgent(sub *model.Task, agentType model.AgentType) bool {
	if o.directToolAgentCfg == nil {
		return false
	}
	// Provider override in task context: if the agent config was recorded
	// with provider=sglang-direct for this subtask, honor it regardless of
	// the other heuristics. Missing override defaults to direct (cfg set).
	if sub != nil && sub.Context != nil {
		if p, ok := sub.Context["provider"].(string); ok && p != "" {
			return model.ProviderType(p) == model.ProviderSGLangDirect
		}
	}
	_ = agentType
	return true
}

// processCoderDirect launches a coder agent via the direct SGLang tool-call
// loop for a single subtask. The agent runs in a background goroutine so that
// scheduleSubtasks can dispatch multiple coders per tick instead of blocking
// on each one serially. On completion (success or failure), the result is
// sent to the runner's completion channel for the next tick to drain.
func (o *Orchestrator) processCoderDirect(sub *model.Task, parent *model.Task) error {
	if o.directToolAgentCfg == nil {
		// Fall through — caller (subtask_scheduling) will use subprocess path.
		return nil
	}

	// Circuit breaker: skip dispatch when LLM endpoint is unreachable.
	if o.endpointHealth != nil && !o.endpointHealth.IsHealthy() {
		o.logger.Warn("direct coder: LLM endpoint unhealthy, skipping dispatch",
			"subtask_id", sub.ID, "status", o.endpointHealth.Status())
		return nil
	}

	featureDir := o.resolveCoderWorkDir(parent)
	if featureDir == "" {
		o.logger.Debug("direct coder: no workdir resolved, deferring dispatch",
			"subtask_id", sub.ID)
		return nil
	}
	toolCfg := *o.directToolAgentCfg
	toolCfg.WorkDir = featureDir
	o.applyContextThresholds(&toolCfg)

	agentID := uuid.New()

	// Trace logging: write tool-call history to the feature worktree so we
	// can post-mortem diagnose agent behavior in production.
	tracePath := fmt.Sprintf("%s/agent-trace-%s.jsonl", featureDir, agentID.String()[:8])
	traceFile, traceErr := os.OpenFile(tracePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if traceErr == nil {
		toolCfg.TraceWriter = traceFile
	}

	// Wire heartbeat callback so the stale-agent reaper doesn't kill us.
	db := o.db
	toolCfg.OnIteration = func(iteration int, tokensIn, tokensOut int) {
		now := time.Now()
		db.Model(&model.Agent{}).Where("id = ?", agentID).Updates(map[string]any{
			"heartbeat_at": &now,
			"tokens_in":    tokensIn,
			"tokens_out":   tokensOut,
		})
	}
	now := time.Now()
	ag := &model.Agent{
		ID:            agentID,
		ProjectID:     sub.ProjectID,
		AgentType:     model.AgentCoder,
		Name:          fmt.Sprintf("direct-coder-%s", sub.ID.String()[:4]),
		Status:        model.AgentWorking,
		CurrentTaskID: &sub.ID,
		WorktreePath:  featureDir,
		Provider:      string(model.ProviderSGLangDirect),
		ModelID:       toolCfg.Model,
		HeartbeatAt:   &now,
	}
	if err := o.db.Create(ag).Error; err != nil {
		return fmt.Errorf("direct coder: create agent record: %w", err)
	}

	// Fast-track subtask: BACKLOG -> PLANNING -> PLAN_REVIEW -> IN_PROGRESS.
	// Without this, the subtask stays in BACKLOG and scheduleSubtasks re-selects
	// it on the next tick, spawning duplicate agents for the same work.
	fastTrack := []model.TaskStatus{
		model.StatusPlanning,
		model.StatusPlanReview,
		model.StatusInProgress,
	}
	for _, target := range fastTrack {
		evt, tErr := state.TransitionTask(sub, target, "orchestrator",
			map[string]any{"reason": "direct-coder-dispatch"})
		if tErr != nil {
			continue
		}
		if err := o.db.Create(evt).Error; err != nil {
			o.logger.Error("direct coder: save transition event", "subtask_id", sub.ID, "error", err)
			break
		}
	}

	sub.AssignedAgentID = &ag.ID
	if err := o.db.Save(sub).Error; err != nil {
		return fmt.Errorf("direct coder: save subtask: %w", err)
	}

	parentCtx := map[string]any{
		"parent_title":       parent.Title,
		"parent_description": parent.Description,
		"feature_branch":     parent.WorktreeBranch,
	}
	systemPrompt := prompt.GenerateDirectCoder(prompt.Opts{
		Task:         sub,
		AgentType:    model.AgentCoder,
		WorktreePath: featureDir,
		ParentCtx:    parentCtx,
	})

	// Frontload file content: read estimated_files and include their content
	// in the user message so G4 doesn't waste iterations exploring the codebase.
	// This is the #1 reason agents fail — they spend all 20 iterations reading
	// files and never get to writing code.
	userMessage := sub.Description
	if userMessage == "" {
		userMessage = sub.Title
	}
	if sub.Context != nil {
		if files := extractFileList(sub.Context["estimated_files"]); len(files) > 0 {
			var fileBuf strings.Builder
			fileBuf.WriteString("\n\n## Source files (already read for you — do NOT re-read these)\n\n")
			totalSize := 0
			for _, f := range files {
				fullPath := f
				if !strings.HasPrefix(f, "/") {
					fullPath = featureDir + "/" + f
				}
				content, err := os.ReadFile(fullPath)
				if err != nil {
					continue
				}
				// Cap per-file at 4K and total at 16K to stay within context budget
				snippet := string(content)
				if len(snippet) > 4096 {
					snippet = snippet[:4096] + "\n...[truncated]"
				}
				if totalSize+len(snippet) > 16384 {
					break
				}
				totalSize += len(snippet)
				fmt.Fprintf(&fileBuf, "### %s\n```go\n%s\n```\n\n", f, snippet)
			}
			if totalSize > 0 {
				userMessage += fileBuf.String()
			}
		}
	}

	o.publishAgentStatus(sub.ID.String(), ag.ID.String(), string(model.AgentCoder), string(model.AgentWorking))
	o.logger.Info("direct coder: launching async tool agent", "subtask_id", sub.ID, "agent_id", ag.ID)

	runAndComplete := func() {
		// Close trace file when agent completes.
		if traceFile != nil {
			defer traceFile.Close()
		}
		// Panic recovery: this goroutine runs outside any caller's defer
		// chain. An unhandled panic in RunDirectToolAgent (nil deref, index
		// out of range on malformed model responses, etc.) would crash the
		// entire orchestrator process. Convert panics into synthetic failure
		// completions so the agent record is properly closed out and dispatch
		// continues for the remaining subtasks.
		defer func() {
			if r := recover(); r != nil {
				o.logger.Error("direct coder: goroutine panic recovered",
					"subtask_id", sub.ID, "agent_id", ag.ID, "panic", r)
				if o.endpointHealth != nil {
					o.endpointHealth.RecordFailure()
				}
				comp := agent.Completion{AgentID: agentID, ReturnCode: 1}
				if o.runner != nil {
					o.runner.SendCompletion(comp)
				}
			}
		}()

		result, runErr := agent.RunDirectToolAgent(toolCfg, systemPrompt, userMessage, agent.ToolsForRole("coder"), "")
		if result != nil {
			ag.TokensIn = result.TokensIn
			ag.TokensOut = result.TokensOut
			persistDirectAgentContext(ag, result)
			if saveErr := o.db.Save(ag).Error; saveErr != nil {
				o.logger.Warn("direct coder: save tokens", "agent_id", ag.ID, "error", saveErr)
			}
		}

		comp := agent.Completion{AgentID: agentID, ReturnCode: 0}
		if runErr != nil {
			o.logger.Error("direct coder: tool agent failed", "subtask_id", sub.ID, "agent_id", ag.ID, "error", runErr)
			comp.ReturnCode = 1
			if o.endpointHealth != nil {
				o.endpointHealth.RecordFailure()
			}
		} else if o.endpointHealth != nil {
			o.endpointHealth.RecordSuccess()
		}
		// Funnel through runner's completion channel so the next tick
		// processes the result via the standard processAgentResult path.
		if o.runner != nil {
			o.runner.SendCompletion(comp)
		} else {
			// No runner (test environment) — process inline.
			if err := o.processAgentResult(comp); err != nil {
				o.logger.Error("direct coder: inline processAgentResult", "agent_id", agentID, "error", err)
			}
		}
	}

	if o.runner != nil {
		// Run in background goroutine — unblocks the tick loop for more dispatches.
		go runAndComplete()
	} else {
		// No runner (test environment) — run synchronously to avoid races.
		runAndComplete()
	}

	return nil
}

// processReviewerDirect runs a reviewer agent via the direct SGLang tool-call
// loop for the given task. Reviewer tools are read-only (read, grep, glob,
// bash). On success, onReviewerCompleted parses review.json and stores it
// in the task context.
func (o *Orchestrator) processReviewerDirect(task *model.Task) error {
	if o.directToolAgentCfg == nil {
		return nil
	}

	worktreePath := o.resolveReviewerWorkDir(task)
	toolCfg := *o.directToolAgentCfg
	toolCfg.WorkDir = worktreePath
	o.applyContextThresholds(&toolCfg)

	agentID := uuid.New()

	// Wire heartbeat callback so the stale-agent reaper doesn't kill us.
	db := o.db
	toolCfg.OnIteration = func(iteration int, tokensIn, tokensOut int) {
		now := time.Now()
		db.Model(&model.Agent{}).Where("id = ?", agentID).Updates(map[string]any{
			"heartbeat_at": &now,
			"tokens_in":    tokensIn,
			"tokens_out":   tokensOut,
		})
	}

	now := time.Now()
	ag := &model.Agent{
		ID:            agentID,
		ProjectID:     task.ProjectID,
		AgentType:     model.AgentReviewer,
		Name:          fmt.Sprintf("direct-reviewer-%s", task.ID.String()[:4]),
		Status:        model.AgentWorking,
		CurrentTaskID: &task.ID,
		WorktreePath:  worktreePath,
		Provider:      string(model.ProviderSGLangDirect),
		ModelID:       toolCfg.Model,
		HeartbeatAt:   &now,
	}
	if err := o.db.Create(ag).Error; err != nil {
		return fmt.Errorf("direct reviewer: create agent record: %w", err)
	}

	reviewMode, planJSON, gitDiff := o.buildReviewerContext(task, worktreePath)
	systemPrompt := prompt.GenerateDirectReviewer(prompt.Opts{
		Task:         task,
		AgentType:    model.AgentReviewer,
		WorktreePath: worktreePath,
		ReviewMode:   reviewMode,
		PlanJSON:     planJSON,
		GitDiff:      gitDiff,
	})
	userMessage := fmt.Sprintf("Review the %s and produce review.json.", reviewMode)

	o.publishAgentStatus(task.ID.String(), ag.ID.String(), string(model.AgentReviewer), string(model.AgentWorking))
	o.logger.Info("direct reviewer: running tool agent", "task_id", task.ID, "agent_id", ag.ID, "mode", reviewMode)

	result, runErr := agent.RunDirectToolAgent(toolCfg, systemPrompt, userMessage, agent.ToolsForRole("reviewer"), "")
	if result != nil {
		ag.TokensIn = result.TokensIn
		ag.TokensOut = result.TokensOut
		persistDirectAgentContext(ag, result)
		if saveErr := o.db.Save(ag).Error; saveErr != nil {
			o.logger.Warn("direct reviewer: save tokens", "agent_id", ag.ID, "error", saveErr)
		}
	}
	if runErr != nil {
		o.logger.Error("direct reviewer: tool agent failed", "task_id", task.ID, "agent_id", ag.ID, "error", runErr)
		if failErr := o.onAgentFailed(ag, task); failErr != nil {
			o.logger.Error("direct reviewer: onAgentFailed", "agent_id", ag.ID, "error", failErr)
		}
		return nil
	}

	if err := o.onReviewerCompleted(ag, task); err != nil {
		o.logger.Error("direct reviewer: onReviewerCompleted", "agent_id", ag.ID, "error", err)
		return err
	}
	return nil
}

// processFixerDirect runs a fixer agent via the direct SGLang tool-call loop.
// Diagnosis, affected files, and suggested fix are pulled from task context
// (same fields read by SpawnFixerSession). On success, onFixerCompleted is
// invoked to mark the agent idle.
func (o *Orchestrator) processFixerDirect(task *model.Task) error {
	if o.directToolAgentCfg == nil {
		return nil
	}

	worktreePath := o.resolveReviewerWorkDir(task)
	toolCfg := *o.directToolAgentCfg
	toolCfg.WorkDir = worktreePath
	o.applyContextThresholds(&toolCfg)

	agentID := uuid.New()

	// Wire heartbeat callback so the stale-agent reaper doesn't kill us.
	db := o.db
	toolCfg.OnIteration = func(iteration int, tokensIn, tokensOut int) {
		now := time.Now()
		db.Model(&model.Agent{}).Where("id = ?", agentID).Updates(map[string]any{
			"heartbeat_at": &now,
			"tokens_in":    tokensIn,
			"tokens_out":   tokensOut,
		})
	}

	now := time.Now()
	ag := &model.Agent{
		ID:            agentID,
		ProjectID:     task.ProjectID,
		AgentType:     model.AgentFixer,
		Name:          fmt.Sprintf("direct-fixer-%s", task.ID.String()[:4]),
		Status:        model.AgentWorking,
		CurrentTaskID: &task.ID,
		WorktreePath:  worktreePath,
		Provider:      string(model.ProviderSGLangDirect),
		ModelID:       toolCfg.Model,
		HeartbeatAt:   &now,
	}
	if err := o.db.Create(ag).Error; err != nil {
		return fmt.Errorf("direct fixer: create agent record: %w", err)
	}

	diagnosis, suggestedFix, affectedFiles := extractFixerContext(task)

	systemPrompt := prompt.GenerateDirectFixer(prompt.Opts{
		Task:          task,
		AgentType:     model.AgentFixer,
		WorktreePath:  worktreePath,
		Diagnosis:     diagnosis,
		AffectedFiles: affectedFiles,
		SuggestedFix:  suggestedFix,
	})
	userMessage := diagnosis
	if userMessage == "" {
		userMessage = task.Description
	}

	o.publishAgentStatus(task.ID.String(), ag.ID.String(), string(model.AgentFixer), string(model.AgentWorking))
	o.logger.Info("direct fixer: running tool agent", "task_id", task.ID, "agent_id", ag.ID)

	result, runErr := agent.RunDirectToolAgent(toolCfg, systemPrompt, userMessage, agent.ToolsForRole("fixer"), "")
	if result != nil {
		ag.TokensIn = result.TokensIn
		ag.TokensOut = result.TokensOut
		persistDirectAgentContext(ag, result)
		if saveErr := o.db.Save(ag).Error; saveErr != nil {
			o.logger.Warn("direct fixer: save tokens", "agent_id", ag.ID, "error", saveErr)
		}
	}
	if runErr != nil {
		o.logger.Error("direct fixer: tool agent failed", "task_id", task.ID, "agent_id", ag.ID, "error", runErr)
		if failErr := o.onAgentFailed(ag, task); failErr != nil {
			o.logger.Error("direct fixer: onAgentFailed", "agent_id", ag.ID, "error", failErr)
		}
		return nil
	}

	if err := o.onFixerCompleted(ag, task); err != nil {
		o.logger.Error("direct fixer: onFixerCompleted", "agent_id", ag.ID, "error", err)
		return err
	}
	return nil
}

// resolveCoderWorkDir returns the feature integration worktree for a coder
// subtask, creating it if it doesn't exist. Falls back to the main worktree
// if the parent has no branch.
func (o *Orchestrator) resolveCoderWorkDir(parent *model.Task) string {
	if parent != nil && parent.WorktreeBranch != "" {
		fn := strings.TrimPrefix(parent.WorktreeBranch, "feature/")
		if o.worktree != nil {
			// Ensure the integration worktree actually exists on disk.
			// FeatureWorktreePath only computes the path — if the worktree
			// was never created or was cleaned up, agents dispatch into a
			// non-existent directory and every tool call fails silently.
			wt, err := o.worktree.CreateFeature(fn)
			if err != nil {
				o.logger.Warn("direct coder: failed to ensure feature worktree, falling back to main",
					"feature", fn, "error", err)
			} else {
				return wt.Path
			}
		}
	}
	// No feature branch on parent — do NOT fall back to master.
	// Multiple concurrent agents writing to master clobber each other's files.
	if o.worktree == nil {
		return ""
	}
	o.logger.Warn("direct coder: no feature branch on parent, skipping (would fall back to master)")
	return ""
}

// resolveReviewerWorkDir returns the integration worktree path for a reviewer
// or fixer task, creating it if it doesn't exist. Falls back to the main
// worktree when no feature branch is set yet (e.g. plan review on a task
// before its integration worktree exists).
func (o *Orchestrator) resolveReviewerWorkDir(task *model.Task) string {
	if o.worktree == nil {
		return ""
	}
	branch := task.WorktreeBranch
	if branch == "" && task.ParentTaskID != nil {
		var parent model.Task
		if err := o.db.Select("worktree_branch").First(&parent, "id = ?", task.ParentTaskID).Error; err == nil {
			branch = parent.WorktreeBranch
		}
	}
	if branch != "" {
		fn := strings.TrimPrefix(branch, "feature/")
		// Ensure the integration worktree exists on disk (same fix as
		// resolveCoderWorkDir — FeatureWorktreePath only computes the
		// path string without creating anything).
		wt, err := o.worktree.CreateFeature(fn)
		if err != nil {
			o.logger.Warn("direct reviewer: failed to ensure feature worktree, falling back to main",
				"feature", fn, "error", err)
		} else {
			return wt.Path
		}
	}
	mainWT, err := o.worktree.MainWorktreePath()
	if err != nil {
		return ""
	}
	return mainWT
}

// buildReviewerContext assembles plan JSON (for plan review) or git diff
// (for feature review) from the task and worktree. Mirrors SpawnReviewerSession.
func (o *Orchestrator) buildReviewerContext(task *model.Task, worktreePath string) (mode, planJSON, gitDiff string) {
	if task.Status == model.StatusPlanReview {
		mode = "plan"
		if task.Plan != nil {
			if data, err := json.MarshalIndent(task.Plan, "", "  "); err == nil {
				planJSON = string(data)
			}
		}
		return
	}
	mode = "feature"
	if worktreePath == "" || o.worktree == nil {
		return
	}
	fullDiff, err := worktree.RunGit(
		[]string{"diff", o.worktree.DefaultBranch + "...HEAD"},
		worktreePath,
	)
	if err == nil && fullDiff != "" {
		gitDiff = fullDiff
		return
	}
	statDiff, statErr := worktree.RunGit(
		[]string{"diff", o.worktree.DefaultBranch + "...HEAD", "--stat"},
		worktreePath,
	)
	if statErr == nil {
		gitDiff = statDiff
	}
	return
}

// extractFixerContext pulls diagnosis, suggested fix, and affected files from
// task.Context. Accepts both []any (JSON round-trip) and []string (Go literal
// from tests) for affected_files.
func extractFixerContext(task *model.Task) (diagnosis, suggestedFix string, affectedFiles []string) {
	if task.Context == nil {
		return
	}
	if d, ok := task.Context["failure_diagnosis"].(string); ok {
		diagnosis = d
	}
	if diagnosis == "" {
		if d, ok := task.Context["diagnosis"].(string); ok {
			diagnosis = d
		}
	}
	if diagnosis == "" {
		if d, ok := task.Context["failure_reason"].(string); ok {
			diagnosis = d
		}
	}
	if sf, ok := task.Context["suggested_fix"].(string); ok {
		suggestedFix = sf
	}
	if af, ok := task.Context["affected_files"].([]any); ok {
		for _, f := range af {
			if s, ok := f.(string); ok {
				affectedFiles = append(affectedFiles, s)
			}
		}
	}
	if af, ok := task.Context["affected_files"].([]string); ok {
		affectedFiles = append(affectedFiles, af...)
	}
	return
}

// applyContextThresholds copies the orchestrator's contextWarnPct/contextStopPct
// onto the per-run tool config so the in-loop monitor in RunDirectToolAgent
// uses the same thresholds as the subprocess-agent monitor in context_monitor.go.
// The model's context window size (ContextLimit) is left to the cfg author —
// it's a model attribute, not an orchestrator policy. When ContextLimit is 0
// these thresholds have no effect (monitor is disabled).
func (o *Orchestrator) applyContextThresholds(toolCfg *agent.DirectToolAgentConfig) {
	if toolCfg == nil {
		return
	}
	if o.contextWarnPct > 0 {
		toolCfg.ContextWarnPct = o.contextWarnPct
	}
	if o.contextStopPct > 0 {
		toolCfg.ContextStopPct = o.contextStopPct
	}
}

// persistDirectAgentContext writes context-monitor results onto the agent
// record's Config so downstream consumers (TUI, monitoring, escalation
// logic in context_monitor.go) can see the direct-tool agent's final
// context usage just like they do for subprocess agents. Skips writing
// when monitoring was disabled (FinalContextPct == 0 with no StopReason)
// to avoid populating the field with a misleading zero.
func persistDirectAgentContext(ag *model.Agent, result *agent.DirectToolAgentResult) {
	if ag == nil || result == nil {
		return
	}
	if result.FinalContextPct == 0 && result.StopReason == "" {
		return
	}
	if ag.Config == nil {
		ag.Config = make(model.JSONField)
	}
	ag.Config["context_used_pct"] = float64(result.FinalContextPct)
	if result.StopReason != "" {
		ag.Config["stop_reason"] = result.StopReason
	}
	ag.FinalContextPct = result.FinalContextPct
	if result.StopReason == "context_limit" {
		ag.ExitReason = model.ExitReasonContextLimit
	}
}

// extractFileList converts a task.Context["estimated_files"] value into a
// string slice. Accepts []any (JSON round-trip) or []string.
func extractFileList(v any) []string {
	if v == nil {
		return nil
	}
	switch files := v.(type) {
	case []any:
		out := make([]string, 0, len(files))
		for _, f := range files {
			if s, ok := f.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return files
	}
	return nil
}
