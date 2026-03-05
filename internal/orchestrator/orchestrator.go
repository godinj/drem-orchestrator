// Package orchestrator implements the main tick loop and task scheduling for
// the Drem Orchestrator. It drives tasks through their lifecycle, spawns
// planner and coder agents, handles plan approval/rejection, manages merges,
// and exposes public methods for TUI interaction.
package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/agent"
	"github.com/godinj/drem-orchestrator/internal/memory"
	"github.com/godinj/drem-orchestrator/internal/merge"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/prompt"
	"github.com/godinj/drem-orchestrator/internal/state"
	"github.com/godinj/drem-orchestrator/internal/supervisor"
	"github.com/godinj/drem-orchestrator/internal/worktree"
)

// MaxPlannerRetries is the number of times the orchestrator will retry a
// planner agent before failing the task.
const MaxPlannerRetries = 3

// slugRegexp matches non-alphanumeric characters for feature name derivation.
var slugRegexp = regexp.MustCompile(`[^a-z0-9]+`)

// Event is sent from the orchestrator to the TUI via a channel.
type Event struct {
	Type    string
	Payload any
}

// Orchestrator is the main scheduling loop. It queries the database each tick,
// processes tasks through the state machine, spawns agents, and drives merges.
type Orchestrator struct {
	db         *gorm.DB
	runner     *agent.Runner
	worktree   *worktree.Manager
	merger     *merge.Orchestrator
	memory     *memory.Manager
	supervisor *supervisor.Supervisor // nil disables LLM-powered decisions
	projectID  uuid.UUID
	events     chan<- Event
	tick       time.Duration
	stale      time.Duration
	logger     *slog.Logger
}

// New creates an Orchestrator. The supervisor parameter is optional — pass nil
// to disable LLM-powered decision points and fall back to existing behavior.
func New(
	db *gorm.DB,
	runner *agent.Runner,
	wt *worktree.Manager,
	merger *merge.Orchestrator,
	mem *memory.Manager,
	sup *supervisor.Supervisor,
	projectID uuid.UUID,
	events chan<- Event,
	tickInterval time.Duration,
	staleTimeout time.Duration,
) *Orchestrator {
	return &Orchestrator{
		db:         db,
		runner:     runner,
		worktree:   wt,
		merger:     merger,
		memory:     mem,
		supervisor: sup,
		projectID:  projectID,
		events:     events,
		tick:       tickInterval,
		stale:      staleTimeout,
		logger:     slog.Default().With("component", "orchestrator", "project_id", projectID),
	}
}

// Run starts the main loop. It blocks until ctx is cancelled.
func (o *Orchestrator) Run(ctx context.Context) {
	ticker := time.NewTicker(o.tick)
	defer ticker.Stop()
	o.logger.Info("orchestrator started", "project_id", o.projectID)
	for {
		select {
		case <-ctx.Done():
			o.logger.Info("orchestrator stopping")
			return
		case <-ticker.C:
			o.doTick(ctx)
		}
	}
}

// doTick is a single iteration of the orchestrator loop.
func (o *Orchestrator) doTick(ctx context.Context) {
	_ = ctx // reserved for future use

	// 1. Process BACKLOG tasks -> transition to PLANNING.
	var backlogTasks []model.Task
	if err := o.db.Where("project_id = ? AND status = ? AND parent_task_id IS NULL", o.projectID, model.StatusBacklog).
		Find(&backlogTasks).Error; err != nil {
		o.logger.Error("query backlog tasks", "error", err)
	}
	for i := range backlogTasks {
		if err := o.processBacklog(&backlogTasks[i]); err != nil {
			o.logger.Error("process backlog", "task_id", backlogTasks[i].ID, "error", err)
		}
	}

	// 2. Drain agent completions.
	completions := o.runner.DrainCompletions()
	for _, comp := range completions {
		if err := o.processAgentResult(comp); err != nil {
			o.logger.Error("process agent result", "agent_id", comp.AgentID, "error", err)
		}
	}

	// 2b. Fallback: detect agents stuck as WORKING whose idle signal file
	// exists but was never picked up (e.g. notification hook failed to fire).
	o.recoverStuckAgents()

	// 3. Process PLANNING tasks -> spawn planners or handle plans.
	var planningTasks []model.Task
	if err := o.db.Where("project_id = ? AND status = ? AND parent_task_id IS NULL", o.projectID, model.StatusPlanning).
		Find(&planningTasks).Error; err != nil {
		o.logger.Error("query planning tasks", "error", err)
	}
	for i := range planningTasks {
		if err := o.processPlanning(&planningTasks[i]); err != nil {
			o.logger.Error("process planning", "task_id", planningTasks[i].ID, "error", err)
		}
	}

	// 4. Process IN_PROGRESS parent tasks -> schedule subtasks, check completion.
	var inProgressTasks []model.Task
	if err := o.db.Where("project_id = ? AND status = ? AND parent_task_id IS NULL", o.projectID, model.StatusInProgress).
		Find(&inProgressTasks).Error; err != nil {
		o.logger.Error("query in_progress tasks", "error", err)
	}
	for i := range inProgressTasks {
		if err := o.scheduleSubtasks(&inProgressTasks[i]); err != nil {
			o.logger.Error("schedule subtasks", "task_id", inProgressTasks[i].ID, "error", err)
		}
		if err := o.checkFeatureCompletion(&inProgressTasks[i]); err != nil {
			o.logger.Error("check feature completion", "task_id", inProgressTasks[i].ID, "error", err)
		}
	}

	// 5. Process MERGING tasks -> execute merges.
	var mergingTasks []model.Task
	if err := o.db.Where("project_id = ? AND status = ?", o.projectID, model.StatusMerging).
		Find(&mergingTasks).Error; err != nil {
		o.logger.Error("query merging tasks", "error", err)
	}
	for i := range mergingTasks {
		if err := o.executeMerge(&mergingTasks[i]); err != nil {
			o.logger.Error("execute merge", "task_id", mergingTasks[i].ID, "error", err)
		}
	}

	// 6. Handle PAUSED tasks -> stop agents.
	var pausedTasks []model.Task
	if err := o.db.Where("project_id = ? AND status = ?", o.projectID, model.StatusPaused).
		Find(&pausedTasks).Error; err != nil {
		o.logger.Error("query paused tasks", "error", err)
	}
	for i := range pausedTasks {
		if err := o.handlePaused(&pausedTasks[i]); err != nil {
			o.logger.Error("handle paused", "task_id", pausedTasks[i].ID, "error", err)
		}
	}

	// 7. Cleanup stale agents.
	if err := o.runner.CleanupStaleAgents(o.stale); err != nil {
		o.logger.Error("cleanup stale agents", "error", err)
	}
}

// ---------------------------------------------------------------------------
// Tick helpers
// ---------------------------------------------------------------------------

// recoverStuckAgents finds agents marked WORKING in the DB whose idle signal
// file exists, meaning the agent finished but the notification hook never
// fired (or the monitor goroutine missed it). For each such agent, it
// synthesizes a completion event so the normal processing pipeline picks it up.
func (o *Orchestrator) recoverStuckAgents() {
	var agents []model.Agent
	if err := o.db.Where("project_id = ? AND status = ?", o.projectID, model.AgentWorking).
		Find(&agents).Error; err != nil {
		o.logger.Error("recover stuck agents: query", "error", err)
		return
	}

	for _, ag := range agents {
		idleSignal := filepath.Join(ag.WorktreePath, ".claude", "agent-idle")
		if _, err := os.Stat(idleSignal); err != nil {
			continue // signal file doesn't exist — agent is genuinely working
		}

		o.logger.Info("recovering stuck agent (idle signal found)", "agent_id", ag.ID, "type", ag.AgentType)

		if ag.CurrentTaskID == nil {
			continue
		}

		var task model.Task
		if err := o.db.First(&task, "id = ?", ag.CurrentTaskID).Error; err != nil {
			o.logger.Error("recover stuck agent: load task", "agent_id", ag.ID, "error", err)
			continue
		}

		if err := o.onAgentCompleted(&ag, &task); err != nil {
			o.logger.Error("recover stuck agent: on completed", "agent_id", ag.ID, "error", err)
		}
	}
}

// processBacklog transitions a task from BACKLOG to PLANNING.
func (o *Orchestrator) processBacklog(task *model.Task) error {
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

		// If agent is dead or idle (finished without plan), clear and maybe retry.
		if ag.Status == model.AgentDead || ag.Status == model.AgentIdle {
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
	featureDir := filepath.Join(o.worktree.BareRepoPath, task.WorktreeBranch)
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
	if ag.AgentType == model.AgentPlanner {
		return o.onPlannerCompleted(ag, task)
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
	if ag.WorktreeBranch != "" && task.WorktreeBranch != "" {
		featureDir := filepath.Join(o.worktree.BareRepoPath, task.WorktreeBranch)
		if _, mergeErr := o.merger.MergeAgentIntoFeature(ag.WorktreeBranch, featureDir); mergeErr != nil {
			o.logger.Error("merge agent into feature failed", "agent_id", ag.ID, "error", mergeErr)
		}
	}

	// Clean up agent worktree.
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
		model.StatusManualTesting,
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

	// Clean up agent worktree.
	if ag.WorktreeBranch != "" {
		if err := o.worktree.RemoveAgentWorktree(ag.WorktreeBranch); err != nil {
			o.logger.Warn("cleanup failed agent worktree failed", "agent_id", ag.ID, "error", err)
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
			// Supervisor says don't retry — fall through to default behavior.
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

	// Coder/researcher failure: transition to FAILED.
	if err := o.failTask(task, "agent exited with non-zero code"); err != nil {
		return err
	}
	o.emit("agent_failed", map[string]any{"task_id": task.ID, "agent_id": ag.ID})
	return nil
}

// scheduleSubtasks looks for BACKLOG subtasks of the parent that have their
// dependencies met and spawns agents for them.
func (o *Orchestrator) scheduleSubtasks(parent *model.Task) error {
	var subtasks []model.Task
	if err := o.db.Where("parent_task_id = ? AND status = ?", parent.ID, model.StatusBacklog).
		Order("priority DESC").
		Find(&subtasks).Error; err != nil {
		return fmt.Errorf("schedule subtasks: query: %w", err)
	}

	for i := range subtasks {
		sub := &subtasks[i]

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

		// Create agent worktree.
		featureName := strings.TrimPrefix(parent.WorktreeBranch, "feature/")
		wtInfo, err := o.worktree.CreateAgentWorktree(featureName)
		if err != nil {
			o.logger.Error("create agent worktree failed", "subtask_id", sub.ID, "error", err)
			continue
		}

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
			WorktreePath: wtInfo.Path,
			Comments:     subComments,
			ParentCtx:    parentCtx,
		})

		// Spawn agent.
		ag, err := o.runner.SpawnAgent(sub, featureName, agentType, agentPrompt)
		if err != nil {
			o.logger.Error("spawn agent for subtask failed", "subtask_id", sub.ID, "error", err)
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

// checkFeatureCompletion checks whether all subtasks of a parent are DONE and
// transitions the parent accordingly.
func (o *Orchestrator) checkFeatureCompletion(parent *model.Task) error {
	var subtasks []model.Task
	if err := o.db.Where("parent_task_id = ?", parent.ID).Find(&subtasks).Error; err != nil {
		return fmt.Errorf("check feature completion: query: %w", err)
	}

	if len(subtasks) == 0 {
		return nil
	}

	allDone := true
	anyFailed := false
	for _, sub := range subtasks {
		if sub.Status != model.StatusDone {
			allDone = false
		}
		if sub.Status == model.StatusFailed {
			anyFailed = true
		}
	}

	if allDone && parent.Status == model.StatusInProgress {
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
	} else if anyFailed && parent.Status == model.StatusInProgress {
		if err := o.failTask(parent, "one or more subtasks failed"); err != nil {
			return err
		}
	}

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
// Public methods for TUI interaction
// ---------------------------------------------------------------------------

// HandlePlanApproved creates subtask records from the plan and transitions the
// task to IN_PROGRESS.
func (o *Orchestrator) HandlePlanApproved(taskID uuid.UUID) error {
	var task model.Task
	if err := o.db.First(&task, "id = ?", taskID).Error; err != nil {
		return fmt.Errorf("handle plan approved: load task: %w", err)
	}

	if task.Status != model.StatusPlanReview {
		return fmt.Errorf("handle plan approved: task %s is in %s, expected plan_review", taskID, task.Status)
	}

	// Parse the plan.
	subtaskPlans, err := parsePlan(task.Plan)
	if err != nil {
		return fmt.Errorf("handle plan approved: %w", err)
	}

	// Create subtask records. We need to track created IDs for dependency mapping.
	createdIDs := make([]uuid.UUID, len(subtaskPlans))
	for i, sp := range subtaskPlans {
		subtaskID := uuid.New()
		createdIDs[i] = subtaskID

		ctx := model.JSONField{
			"agent_type":      sp.AgentType,
			"estimated_files": sp.EstimatedFiles,
		}

		sub := model.Task{
			ID:           subtaskID,
			ProjectID:    task.ProjectID,
			ParentTaskID: &task.ID,
			Title:        sp.Title,
			Description:  sp.Description,
			Status:       model.StatusBacklog,
			Context:      ctx,
			Priority:     len(subtaskPlans) - i, // higher priority for earlier items
		}

		if err := o.db.Create(&sub).Error; err != nil {
			return fmt.Errorf("handle plan approved: create subtask %d: %w", i, err)
		}
	}

	// Second pass: set dependency IDs now that all subtask UUIDs are known.
	// The plan uses 0-based indices to reference other subtasks.
	for i, sp := range subtaskPlans {
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

	// Clear planner agent assignment now that review is complete.
	task.AssignedAgentID = nil

	// Transition task to IN_PROGRESS.
	evt, err := state.TransitionTask(&task, model.StatusInProgress, "user", map[string]any{"action": "plan_approved"})
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

// HandleStartTesting transitions from TESTING_READY to MANUAL_TESTING.
func (o *Orchestrator) HandleStartTesting(taskID uuid.UUID) error {
	var task model.Task
	if err := o.db.First(&task, "id = ?", taskID).Error; err != nil {
		return fmt.Errorf("handle start testing: load task: %w", err)
	}

	if task.Status != model.StatusTestingReady {
		return fmt.Errorf("handle start testing: task %s is in %s, expected testing_ready", taskID, task.Status)
	}

	evt, err := state.TransitionTask(&task, model.StatusManualTesting, "user", map[string]any{"action": "start_testing"})
	if err != nil {
		return fmt.Errorf("handle start testing: transition: %w", err)
	}
	if err := o.db.Save(&task).Error; err != nil {
		return fmt.Errorf("handle start testing: save task: %w", err)
	}
	if err := o.db.Create(evt).Error; err != nil {
		return fmt.Errorf("handle start testing: save event: %w", err)
	}

	o.emit("task_updated", &task)
	o.logger.Info("testing started", "task_id", task.ID)
	return nil
}

// HandleTestPassed transitions from MANUAL_TESTING to MERGING.
func (o *Orchestrator) HandleTestPassed(taskID uuid.UUID) error {
	var task model.Task
	if err := o.db.First(&task, "id = ?", taskID).Error; err != nil {
		return fmt.Errorf("handle test passed: load task: %w", err)
	}

	if task.Status != model.StatusManualTesting {
		return fmt.Errorf("handle test passed: task %s is in %s, expected manual_testing", taskID, task.Status)
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

// HandleTestFailed transitions from MANUAL_TESTING back to PLANNING so the
// planner agent can read user comments and create new subtasks to address
// the feedback.
func (o *Orchestrator) HandleTestFailed(taskID uuid.UUID) error {
	var task model.Task
	if err := o.db.First(&task, "id = ?", taskID).Error; err != nil {
		return fmt.Errorf("handle test failed: load task: %w", err)
	}

	if task.Status != model.StatusManualTesting {
		return fmt.Errorf("handle test failed: task %s is in %s, expected manual_testing", taskID, task.Status)
	}

	// Clear the existing plan so the planner re-plans with user feedback.
	task.Plan = nil
	task.AssignedAgentID = nil

	evt, err := state.TransitionTask(&task, model.StatusPlanning, "user", map[string]any{"action": "test_failed"})
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
	o.logger.Info("test failed, task back to planning", "task_id", task.ID)
	return nil
}

// AddComment creates a new comment on a task. Only allowed for human-gate statuses.
func (o *Orchestrator) AddComment(taskID uuid.UUID, author, body string) error {
	var task model.Task
	if err := o.db.First(&task, "id = ?", taskID).Error; err != nil {
		return fmt.Errorf("add comment: load task: %w", err)
	}
	if !task.Status.IsHumanGate() {
		return fmt.Errorf("add comment: task %s is in %s, comments only allowed in human-gate statuses", taskID, task.Status)
	}
	comment := model.TaskComment{
		TaskID: taskID,
		Author: author,
		Body:   body,
	}
	if err := o.db.Create(&comment).Error; err != nil {
		return fmt.Errorf("add comment: %w", err)
	}
	o.logger.Info("comment added", "task_id", taskID, "author", author)
	return nil
}

// DeleteComment deletes a comment by ID.
func (o *Orchestrator) DeleteComment(commentID uuid.UUID) error {
	if err := o.db.Delete(&model.TaskComment{}, "id = ?", commentID).Error; err != nil {
		return fmt.Errorf("delete comment: %w", err)
	}
	o.logger.Info("comment deleted", "comment_id", commentID)
	return nil
}

// GetComments returns all comments for a task ordered by creation time.
func (o *Orchestrator) GetComments(taskID uuid.UUID) ([]model.TaskComment, error) {
	var comments []model.TaskComment
	if err := o.db.Where("task_id = ?", taskID).Order("created_at asc").Find(&comments).Error; err != nil {
		return nil, fmt.Errorf("get comments: %w", err)
	}
	return comments, nil
}

// PauseTask pauses a task and stops its agents.
func (o *Orchestrator) PauseTask(taskID uuid.UUID) error {
	var task model.Task
	if err := o.db.First(&task, "id = ?", taskID).Error; err != nil {
		return fmt.Errorf("pause task: load task: %w", err)
	}

	// Store previous status so we can resume later.
	if task.Context == nil {
		task.Context = make(model.JSONField)
	}
	task.Context["paused_from"] = string(task.Status)

	evt, err := state.TransitionTask(&task, model.StatusPaused, "user", map[string]any{"action": "pause"})
	if err != nil {
		return fmt.Errorf("pause task: transition: %w", err)
	}
	if err := o.db.Save(&task).Error; err != nil {
		return fmt.Errorf("pause task: save task: %w", err)
	}
	if err := o.db.Create(evt).Error; err != nil {
		return fmt.Errorf("pause task: save event: %w", err)
	}

	o.emit("task_updated", &task)
	o.logger.Info("task paused", "task_id", task.ID)
	return nil
}

// ResumeTask resumes a paused task to its previous status.
func (o *Orchestrator) ResumeTask(taskID uuid.UUID) error {
	var task model.Task
	if err := o.db.First(&task, "id = ?", taskID).Error; err != nil {
		return fmt.Errorf("resume task: load task: %w", err)
	}

	if task.Status != model.StatusPaused {
		return fmt.Errorf("resume task: task %s is in %s, expected paused", taskID, task.Status)
	}

	// Determine the status to resume to.
	resumeTo := model.StatusBacklog
	if task.Context != nil {
		if prev, ok := task.Context["paused_from"].(string); ok {
			parsed, err := model.ParseTaskStatus(prev)
			if err == nil {
				resumeTo = parsed
			}
		}
	}

	// Validate the resume transition is allowed from PAUSED.
	evt, err := state.TransitionTask(&task, resumeTo, "user", map[string]any{"action": "resume"})
	if err != nil {
		// If the original status isn't reachable from PAUSED, fall back to BACKLOG.
		evt, err = state.TransitionTask(&task, model.StatusBacklog, "user", map[string]any{"action": "resume", "fallback": true})
		if err != nil {
			return fmt.Errorf("resume task: transition: %w", err)
		}
	}

	if err := o.db.Save(&task).Error; err != nil {
		return fmt.Errorf("resume task: save task: %w", err)
	}
	if err := o.db.Create(evt).Error; err != nil {
		return fmt.Errorf("resume task: save event: %w", err)
	}

	o.emit("task_updated", &task)
	o.logger.Info("task resumed", "task_id", task.ID, "status", task.Status)
	return nil
}

// RetryTask transitions a FAILED task back to BACKLOG.
func (o *Orchestrator) RetryTask(taskID uuid.UUID) error {
	var task model.Task
	if err := o.db.First(&task, "id = ?", taskID).Error; err != nil {
		return fmt.Errorf("retry task: load task: %w", err)
	}

	if task.Status != model.StatusFailed {
		return fmt.Errorf("retry task: task %s is in %s, expected failed", taskID, task.Status)
	}

	// Reset retry count.
	if task.Context != nil {
		delete(task.Context, "retry_count")
		delete(task.Context, "last_error")
	}

	evt, err := state.TransitionTask(&task, model.StatusBacklog, "user", map[string]any{"action": "retry"})
	if err != nil {
		return fmt.Errorf("retry task: transition: %w", err)
	}
	if err := o.db.Save(&task).Error; err != nil {
		return fmt.Errorf("retry task: save task: %w", err)
	}
	if err := o.db.Create(evt).Error; err != nil {
		return fmt.Errorf("retry task: save event: %w", err)
	}

	o.emit("task_updated", &task)
	o.logger.Info("task retried", "task_id", task.ID)
	return nil
}

// CreateTask creates a new task in BACKLOG.
func (o *Orchestrator) CreateTask(title, description string, priority int) (*model.Task, error) {
	task := &model.Task{
		ID:          uuid.New(),
		ProjectID:   o.projectID,
		Title:       title,
		Description: description,
		Status:      model.StatusBacklog,
		Priority:    priority,
	}

	if err := o.db.Create(task).Error; err != nil {
		return nil, fmt.Errorf("create task: %w", err)
	}

	o.emit("task_created", task)
	o.logger.Info("task created", "task_id", task.ID, "title", title)
	return task, nil
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// emit sends an event to the TUI channel without blocking.
func (o *Orchestrator) emit(eventType string, payload any) {
	select {
	case o.events <- Event{Type: eventType, Payload: payload}:
	default:
		o.logger.Warn("event channel full, dropping event", "type", eventType)
	}
}

// failTask transitions a task to FAILED and persists the change.
func (o *Orchestrator) failTask(task *model.Task, reason string) error {
	if task.Context == nil {
		task.Context = make(model.JSONField)
	}
	task.Context["failure_reason"] = reason

	evt, err := state.TransitionTask(task, model.StatusFailed, "orchestrator", map[string]any{"reason": reason})
	if err != nil {
		return fmt.Errorf("fail task: transition: %w", err)
	}
	if err := o.db.Save(task).Error; err != nil {
		return fmt.Errorf("fail task: save task: %w", err)
	}
	if err := o.db.Create(evt).Error; err != nil {
		return fmt.Errorf("fail task: save event: %w", err)
	}

	o.emit("task_failed", map[string]any{"task_id": task.ID, "reason": reason})
	o.logger.Warn("task failed", "task_id", task.ID, "reason", reason)
	return nil
}

// incrementRetryCount bumps the retry counter stored in task.Context and
// returns the new count. The task is NOT saved to DB — the caller must do that.
func (o *Orchestrator) incrementRetryCount(task *model.Task) int {
	if task.Context == nil {
		task.Context = make(model.JSONField)
	}
	count := 0
	if v, ok := task.Context["retry_count"].(float64); ok {
		count = int(v)
	}
	count++
	task.Context["retry_count"] = float64(count)
	return count
}

// taskFeatureName derives a slug-based feature name from a task.
func taskFeatureName(task *model.Task) string {
	slug := strings.ToLower(task.Title)
	slug = slugRegexp.ReplaceAllString(slug, "-")
	slug = strings.Trim(slug, "-")
	if len(slug) > 40 {
		slug = slug[:40]
	}
	return fmt.Sprintf("%s-%s", task.ID.String()[:8], slug)
}

// truncate returns s truncated to maxLen characters.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}

// planEntry is an intermediate struct for parsing plans from JSON that may
// include dependency indices.
type planEntry struct {
	Title          string   `json:"title"`
	Description    string   `json:"description"`
	AgentType      string   `json:"agent_type"`
	EstimatedFiles []string `json:"estimated_files"`
	Files          []string `json:"files"`
	Dependencies   []int    `json:"dependencies"`
	Priority       int      `json:"priority"`
}

// parsePlan extracts subtask plans from a task's Plan JSONField.
func parsePlan(planField model.JSONField) ([]planEntry, error) {
	if planField == nil {
		return nil, fmt.Errorf("parse plan: plan is nil")
	}

	// The plan is stored as {"subtasks": [...]}.
	subtasksRaw, ok := planField["subtasks"]
	if !ok {
		return nil, fmt.Errorf("parse plan: no subtasks key in plan")
	}

	// Marshal back to JSON and unmarshal into planEntry slice.
	b, err := json.Marshal(subtasksRaw)
	if err != nil {
		return nil, fmt.Errorf("parse plan: marshal subtasks: %w", err)
	}

	var entries []planEntry
	if err := json.Unmarshal(b, &entries); err != nil {
		return nil, fmt.Errorf("parse plan: unmarshal subtasks: %w", err)
	}

	// Normalize: use "files" as fallback for "estimated_files".
	for i := range entries {
		if len(entries[i].EstimatedFiles) == 0 && len(entries[i].Files) > 0 {
			entries[i].EstimatedFiles = entries[i].Files
		}
		if entries[i].AgentType == "" {
			entries[i].AgentType = string(model.AgentCoder)
		}
	}

	return entries, nil
}
