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

// reconcileInterval controls how often the consistency audit runs inside
// doTick (every N ticks). Set to 0 to disable periodic reconciliation.
const reconcileInterval = 10

// Orchestrator is the main scheduling loop. It queries the database each tick,
// processes tasks through the state machine, spawns agents, and drives merges.
type Orchestrator struct {
	db         *gorm.DB
	dbPath     string
	runner     *agent.Runner
	worktree   *worktree.Manager
	merger     *merge.Orchestrator
	memory     *memory.Manager
	supervisor *supervisor.Supervisor // nil disables LLM-powered decisions
	projectID  uuid.UUID
	events     chan<- Event
	tick       time.Duration
	stale      time.Duration
	tickCount  int
	logger     *slog.Logger
}

// New creates an Orchestrator. The supervisor parameter is optional — pass nil
// to disable LLM-powered decision points and fall back to existing behavior.
func New(
	db *gorm.DB,
	dbPath string,
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
		dbPath:     dbPath,
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
	// Root tasks with unmet dependencies remain in BACKLOG (pending).
	var backlogTasks []model.Task
	if err := o.db.Where("project_id = ? AND status = ? AND parent_task_id IS NULL", o.projectID, model.StatusBacklog).
		Find(&backlogTasks).Error; err != nil {
		o.logger.Error("query backlog tasks", "error", err)
	}
	for i := range backlogTasks {
		task := &backlogTasks[i]
		if len(task.DependencyIDs) > 0 {
			met, err := DependenciesMet(o.db, task.DependencyIDs)
			if err != nil {
				o.logger.Error("check root task dependencies", "task_id", task.ID, "error", err)
				continue
			}
			if !met {
				continue
			}
		}
		if err := o.processBacklog(task); err != nil {
			o.logger.Error("process backlog", "task_id", task.ID, "error", err)
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

	// 8. Periodic consistency audit.
	o.tickCount++
	if reconcileInterval > 0 && o.tickCount%reconcileInterval == 0 {
		if fixes, err := o.Reconcile(); err != nil {
			o.logger.Error("reconcile", "error", err)
		} else if fixes > 0 {
			o.logger.Info("reconcile applied fixes", "count", fixes)
		}
	}
}

// ---------------------------------------------------------------------------
// Consistency audit
// ---------------------------------------------------------------------------

// ReconcileResult describes the fixes applied by a single Reconcile run.
type ReconcileResult struct {
	StaleSubtasksReset      int
	OrphanedSubtasksFixed   int
	EmptyFeaturesFailed     int
	OrphanWorktreesCleaned  int
}

// Reconcile audits the project for state inconsistencies and corrects them.
// It is called periodically from doTick and can also be invoked on demand
// from the TUI. Returns the number of fixes applied.
func (o *Orchestrator) Reconcile() (int, error) {
	var r ReconcileResult

	if n, err := o.reconcileStaleSubtasks(); err != nil {
		return 0, fmt.Errorf("reconcile stale subtasks: %w", err)
	} else {
		r.StaleSubtasksReset = n
	}

	if n, err := o.reconcileOrphanedSubtasks(); err != nil {
		return 0, fmt.Errorf("reconcile orphaned subtasks: %w", err)
	} else {
		r.OrphanedSubtasksFixed = n
	}

	if n, err := o.reconcileEmptyFeatures(); err != nil {
		return 0, fmt.Errorf("reconcile empty features: %w", err)
	} else {
		r.EmptyFeaturesFailed = n
	}

	if n, err := o.reconcileOrphanWorktrees(); err != nil {
		return 0, fmt.Errorf("reconcile orphan worktrees: %w", err)
	} else {
		r.OrphanWorktreesCleaned = n
	}

	total := r.StaleSubtasksReset + r.OrphanedSubtasksFixed + r.EmptyFeaturesFailed + r.OrphanWorktreesCleaned
	if total > 0 {
		o.emit("reconcile", r)
	}
	return total, nil
}

// reconcileStaleSubtasks finds subtasks marked DONE whose parent is still
// IN_PROGRESS and verifies the subtask's agent actually contributed commits
// to the feature branch. Subtasks that are DONE but have no corresponding
// work are reset to BACKLOG for rescheduling.
func (o *Orchestrator) reconcileStaleSubtasks() (int, error) {
	// Find IN_PROGRESS parents with at least one DONE subtask.
	var parents []model.Task
	if err := o.db.Where(
		"project_id = ? AND status = ? AND parent_task_id IS NULL",
		o.projectID, model.StatusInProgress,
	).Find(&parents).Error; err != nil {
		return 0, err
	}

	fixed := 0
	for _, parent := range parents {
		if parent.WorktreeBranch == "" {
			continue
		}

		var subs []model.Task
		if err := o.db.Where("parent_task_id = ? AND status = ?", parent.ID, model.StatusDone).
			Find(&subs).Error; err != nil {
			continue
		}

		fn := strings.TrimPrefix(parent.WorktreeBranch, "feature/")
		featureDir := o.worktree.FeatureWorktreePath(fn)

		// Get the set of files changed on the feature branch. If empty,
		// every DONE subtask is suspect.
		changedFiles, err := worktree.GetChangedFiles(featureDir, o.worktree.DefaultBranch)
		if err != nil {
			continue
		}
		if len(changedFiles) > 0 {
			// Feature branch has changes — subtasks plausibly contributed.
			continue
		}

		// Feature branch has no changes but subtasks claim to be done.
		for i := range subs {
			sub := &subs[i]
			o.logger.Warn("reconcile: resetting done subtask with no feature changes",
				"subtask_id", sub.ID, "parent_id", parent.ID)

			// Force status back to backlog (bypasses state machine since
			// DONE is terminal and has no valid outbound transitions).
			sub.Status = model.StatusBacklog
			sub.AssignedAgentID = nil
			sub.UpdatedAt = time.Now()
			if sub.Context == nil {
				sub.Context = make(model.JSONField)
			}
			sub.Context["reconciled"] = true
			sub.Context["reconcile_reason"] = "subtask was done but feature branch has no changes"
			if err := o.db.Save(sub).Error; err != nil {
				o.logger.Error("reconcile: save subtask", "subtask_id", sub.ID, "error", err)
				continue
			}
			fixed++
		}
	}
	return fixed, nil
}

// reconcileOrphanedSubtasks finds IN_PROGRESS subtasks whose assigned agent
// is idle or dead — meaning the agent finished but the completion signal was
// lost before the subtask could be transitioned. For each orphaned subtask,
// it attempts to merge any remaining agent work into the feature branch and
// fast-tracks the subtask to DONE. If the agent branch has no mergeable work
// and the feature branch is empty, the subtask is reset to BACKLOG.
func (o *Orchestrator) reconcileOrphanedSubtasks() (int, error) {
	var subtasks []model.Task
	if err := o.db.Where(
		"project_id = ? AND status = ? AND parent_task_id IS NOT NULL AND assigned_agent_id IS NOT NULL",
		o.projectID, model.StatusInProgress,
	).Find(&subtasks).Error; err != nil {
		return 0, err
	}

	fixed := 0
	for i := range subtasks {
		sub := &subtasks[i]

		var ag model.Agent
		if err := o.db.First(&ag, "id = ?", sub.AssignedAgentID).Error; err != nil {
			// Agent record missing — reset subtask for rescheduling.
			o.logger.Warn("reconcile: assigned agent not found, resetting subtask",
				"subtask_id", sub.ID, "agent_id", sub.AssignedAgentID)
			sub.Status = model.StatusBacklog
			sub.AssignedAgentID = nil
			sub.UpdatedAt = time.Now()
			if err := o.db.Save(sub).Error; err != nil {
				o.logger.Error("reconcile: save subtask", "subtask_id", sub.ID, "error", err)
			}
			fixed++
			continue
		}

		// Only act if the agent is no longer actively working.
		if ag.Status == model.AgentWorking || ag.Status == model.AgentBlocked {
			continue
		}

		o.logger.Info("reconcile: processing orphaned in_progress subtask",
			"subtask_id", sub.ID, "agent_id", ag.ID, "agent_status", ag.Status)

		// Resolve the feature branch from the parent task.
		featureBranch := ""
		if sub.ParentTaskID != nil {
			var parent model.Task
			if err := o.db.Select("worktree_branch").First(&parent, "id = ?", sub.ParentTaskID).Error; err == nil {
				featureBranch = parent.WorktreeBranch
			}
		}

		// Attempt to merge agent work if the branch still exists.
		merged := false
		if ag.WorktreeBranch != "" && featureBranch != "" {
			fn := strings.TrimPrefix(featureBranch, "feature/")
			featureDir := o.worktree.FeatureWorktreePath(fn)

			// Ensure the feature worktree is clean before merge attempts.
			// Leftover changes (e.g. plan.json) block MergeAgentIntoFeature.
			if committed, cErr := worktree.CommitUnstagedChanges(
				featureDir, "Auto-commit uncommitted feature worktree changes (reconcile)",
			); cErr != nil {
				o.logger.Warn("reconcile: failed to clean feature worktree", "feature", featureBranch, "error", cErr)
			} else if committed {
				o.logger.Info("reconcile: committed leftover changes in feature worktree", "feature", featureBranch)
			}

			hasCommits, err := worktree.BranchHasNewCommits(featureDir, ag.WorktreeBranch)
			if err != nil {
				// Branch likely already cleaned up — assume merge happened.
				merged = true
			} else if hasCommits {
				result, mergeErr := o.merger.MergeAgentIntoFeature(ag.WorktreeBranch, featureDir)
				if mergeErr != nil {
					o.logger.Error("reconcile: merge agent into feature failed",
						"subtask_id", sub.ID, "agent_id", ag.ID, "error", mergeErr)
				} else if result.Success {
					merged = true
					if err := o.worktree.RemoveAgentWorktree(ag.WorktreeBranch); err != nil {
						o.logger.Warn("reconcile: cleanup agent worktree", "agent_id", ag.ID, "error", err)
					}
				} else {
					o.logger.Error("reconcile: merge had conflicts",
						"subtask_id", sub.ID, "conflicts", result.Conflicts)
				}
			} else {
				// No commits on agent branch — already merged or empty work.
				merged = true
			}
		} else {
			merged = true
		}

		if !merged {
			if err := o.failTask(sub, "reconcile: agent work could not be merged into feature branch"); err != nil {
				o.logger.Error("reconcile: fail subtask", "subtask_id", sub.ID, "error", err)
			}
			fixed++
			continue
		}

		// Clean up the agent record if it still references this subtask.
		if ag.CurrentTaskID != nil && *ag.CurrentTaskID == sub.ID {
			ag.CurrentTaskID = nil
			if ag.Status == model.AgentDead {
				ag.Status = model.AgentIdle
			}
			if err := o.db.Save(&ag).Error; err != nil {
				o.logger.Error("reconcile: save agent", "agent_id", ag.ID, "error", err)
			}
		}

		// Fast-track subtask to DONE.
		transitions := []model.TaskStatus{
			model.StatusTestingReady,
			model.StatusManualTesting,
			model.StatusMerging,
			model.StatusDone,
		}
		for _, target := range transitions {
			if sub.Status == target {
				continue
			}
			evt, err := state.TransitionTask(sub, target, "orchestrator",
				map[string]any{"reason": "reconcile-fasttrack"})
			if err != nil {
				o.logger.Debug("reconcile fast-track skip",
					"subtask_id", sub.ID, "from", sub.Status, "to", target, "error", err)
				continue
			}
			if err := o.db.Create(evt).Error; err != nil {
				o.logger.Error("reconcile: save event", "subtask_id", sub.ID, "error", err)
				break
			}
		}

		if err := o.db.Save(sub).Error; err != nil {
			o.logger.Error("reconcile: save subtask", "subtask_id", sub.ID, "error", err)
			continue
		}
		fixed++
	}
	return fixed, nil
}

// reconcileEmptyFeatures finds parent tasks in TESTING_READY whose feature
// branch has no file changes relative to the default branch and fails them.
func (o *Orchestrator) reconcileEmptyFeatures() (int, error) {
	var tasks []model.Task
	if err := o.db.Where(
		"project_id = ? AND status = ? AND parent_task_id IS NULL",
		o.projectID, model.StatusTestingReady,
	).Find(&tasks).Error; err != nil {
		return 0, err
	}

	fixed := 0
	for i := range tasks {
		task := &tasks[i]
		if task.WorktreeBranch == "" {
			continue
		}

		fn := strings.TrimPrefix(task.WorktreeBranch, "feature/")
		featureDir := o.worktree.FeatureWorktreePath(fn)

		changed, err := worktree.GetChangedFiles(featureDir, o.worktree.DefaultBranch)
		if err != nil {
			continue
		}
		if len(changed) > 0 {
			continue
		}

		o.logger.Warn("reconcile: failing testing_ready task with empty feature branch",
			"task_id", task.ID)
		if task.Context == nil {
			task.Context = make(model.JSONField)
		}
		task.Context["empty_feature"] = true
		task.Context["reconciled"] = true
		if err := o.failTask(task, "feature branch has no changes (detected by reconcile)"); err != nil {
			o.logger.Error("reconcile: fail empty feature task", "task_id", task.ID, "error", err)
			continue
		}
		fixed++
	}
	return fixed, nil
}

// reconcileOrphanWorktrees finds agent worktrees in each feature directory
// that have no commits ahead of the feature branch and no corresponding
// WORKING agent in the database, and removes them.
func (o *Orchestrator) reconcileOrphanWorktrees() (int, error) {
	// Collect all WORKING agent branches.
	var workingAgents []model.Agent
	if err := o.db.Where("project_id = ? AND status = ?", o.projectID, model.AgentWorking).
		Find(&workingAgents).Error; err != nil {
		return 0, err
	}
	activeBranches := make(map[string]bool, len(workingAgents))
	for _, ag := range workingAgents {
		activeBranches[ag.WorktreeBranch] = true
	}

	// Find all feature parents to scan their worktree directories.
	var parents []model.Task
	if err := o.db.Where(
		"project_id = ? AND parent_task_id IS NULL AND worktree_branch != ''",
		o.projectID,
	).Find(&parents).Error; err != nil {
		return 0, err
	}

	cleaned := 0
	for _, parent := range parents {
		fn := strings.TrimPrefix(parent.WorktreeBranch, "feature/")
		featureDir := o.worktree.FeatureWorktreePath(fn)

		agentWorktrees, err := o.worktree.ListAgentWorktrees(fn)
		if err != nil {
			continue
		}

		for _, awt := range agentWorktrees {
			if activeBranches[awt.Branch] {
				continue // agent is actively working
			}

			// Check if the worktree has commits.
			hasCommits, err := worktree.BranchHasNewCommits(featureDir, awt.Branch)
			if err != nil {
				continue
			}
			if hasCommits {
				continue // has real work, leave it
			}

			o.logger.Info("reconcile: removing orphan empty worktree",
				"branch", awt.Branch, "feature", parent.WorktreeBranch)
			if err := o.worktree.RemoveAgentWorktree(awt.Branch); err != nil {
				o.logger.Warn("reconcile: remove orphan worktree", "branch", awt.Branch, "error", err)
				continue
			}
			cleaned++
		}
	}
	return cleaned, nil
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

		// If agent is dead or idle (finished without plan), clean up its
		// worktree, clear assignment, and maybe retry.
		if ag.Status == model.AgentDead || ag.Status == model.AgentIdle {
			if ag.WorktreeBranch != "" {
				if err := o.worktree.RemoveAgentWorktree(ag.WorktreeBranch); err != nil {
					o.logger.Warn("cleanup dead planner worktree failed", "agent_id", ag.ID, "error", err)
				}
			}
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
	featureDir := o.worktree.FeatureWorktreePath(featureName)
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

// MaxEmptyWorkRetries is the number of times a subtask will be rescheduled
// after an agent completes without committing any changes.
const MaxEmptyWorkRetries = 2

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

// scheduleSubtasks looks for BACKLOG subtasks — and IN_PROGRESS subtasks
// whose agent has been cleared (e.g. after empty-work retry) — of the parent
// that have their dependencies met and spawns agents for them.
func (o *Orchestrator) scheduleSubtasks(parent *model.Task) error {
	var subtasks []model.Task
	if err := o.db.Where(
		"parent_task_id = ? AND ((status = ?) OR (status = ? AND assigned_agent_id IS NULL))",
		parent.ID, model.StatusBacklog, model.StatusInProgress,
	).Order("priority DESC").Find(&subtasks).Error; err != nil {
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

		// Use the feature integration worktree for prompt generation context.
		// The actual agent worktree is created inside SpawnAgent.
		featureName := strings.TrimPrefix(parent.WorktreeBranch, "feature/")
		featureDir := o.worktree.FeatureWorktreePath(featureName)

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
			WorktreePath: featureDir,
			Comments:     subComments,
			ParentCtx:    parentCtx,
		})

		// Spawn agent (creates worktree internally).
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
		// Verify the feature branch actually has changes before declaring
		// testing ready. If all subtasks "completed" without producing commits,
		// fail the parent so the user can replan.
		if parent.WorktreeBranch != "" {
			fn := strings.TrimPrefix(parent.WorktreeBranch, "feature/")
			featureDir := o.worktree.FeatureWorktreePath(fn)
			// Check if the feature branch has any file changes relative to
			// the default branch.
			changed, changeErr := worktree.GetChangedFiles(featureDir, o.worktree.DefaultBranch)
			if changeErr != nil {
				o.logger.Warn("failed to check feature branch changes", "task_id", parent.ID, "error", changeErr)
			} else if len(changed) == 0 {
				o.logger.Warn("all subtasks done but feature branch has no changes, failing parent", "task_id", parent.ID)
				if parent.Context == nil {
					parent.Context = make(model.JSONField)
				}
				parent.Context["empty_feature"] = true
				return o.failTask(parent, "all subtasks completed but no changes were committed to the feature branch")
			}
		}

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

// HandleTestFailed transitions from MANUAL_TESTING (or TESTING_READY) back
// to PLANNING so the planner agent can read user comments and create new
// subtasks to address the feedback.
func (o *Orchestrator) HandleTestFailed(taskID uuid.UUID) error {
	var task model.Task
	if err := o.db.First(&task, "id = ?", taskID).Error; err != nil {
		return fmt.Errorf("handle test failed: load task: %w", err)
	}

	if task.Status != model.StatusManualTesting && task.Status != model.StatusTestingReady {
		return fmt.Errorf("handle test failed: task %s is in %s, expected manual_testing or testing_ready", taskID, task.Status)
	}

	// If still in testing_ready, transition through manual_testing first.
	if task.Status == model.StatusTestingReady {
		evt, err := state.TransitionTask(&task, model.StatusManualTesting, "user", map[string]any{"action": "start_testing"})
		if err != nil {
			return fmt.Errorf("handle test failed: start testing: %w", err)
		}
		if err := o.db.Save(&task).Error; err != nil {
			return fmt.Errorf("handle test failed: save start testing: %w", err)
		}
		if err := o.db.Create(evt).Error; err != nil {
			return fmt.Errorf("handle test failed: save start event: %w", err)
		}
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

// DeletePlanStep removes a single step from a task's plan by index.
// Only valid for tasks in plan_review state.
func (o *Orchestrator) DeletePlanStep(taskID uuid.UUID, stepIndex int) error {
	var task model.Task
	if err := o.db.First(&task, "id = ?", taskID).Error; err != nil {
		return fmt.Errorf("delete plan step: load task: %w", err)
	}
	if task.Status != model.StatusPlanReview {
		return fmt.Errorf("delete plan step: task %s is in %s, expected plan_review", taskID, task.Status)
	}
	if task.Plan == nil {
		return fmt.Errorf("delete plan step: task %s has no plan", taskID)
	}
	subtasksRaw, ok := task.Plan["subtasks"]
	if !ok {
		return fmt.Errorf("delete plan step: no subtasks key in plan")
	}
	items, ok := subtasksRaw.([]any)
	if !ok || stepIndex < 0 || stepIndex >= len(items) {
		return fmt.Errorf("delete plan step: index %d out of range", stepIndex)
	}

	// Remove the step.
	items = append(items[:stepIndex], items[stepIndex+1:]...)
	task.Plan["subtasks"] = items

	if err := o.db.Save(&task).Error; err != nil {
		return fmt.Errorf("delete plan step: save task: %w", err)
	}
	o.emit("task_updated", &task)
	o.logger.Info("plan step deleted", "task_id", taskID, "step_index", stepIndex)
	return nil
}

// DeleteSubtask removes a subtask and stops its agent if one is running.
func (o *Orchestrator) DeleteSubtask(subtaskID uuid.UUID) error {
	var sub model.Task
	if err := o.db.First(&sub, "id = ?", subtaskID).Error; err != nil {
		return fmt.Errorf("delete subtask: load: %w", err)
	}

	// Stop the assigned agent if it's running.
	if sub.AssignedAgentID != nil {
		agentID := *sub.AssignedAgentID
		// StopAgent is best-effort — the agent may already be dead.
		if err := o.runner.StopAgent(agentID); err != nil {
			o.logger.Debug("stop agent during subtask delete (may be already stopped)", "agent_id", agentID, "error", err)
			// StopAgent failed (agent not in running map) — kill tmux session
			// directly for idle/dead agents that still have one.
			var ag model.Agent
			if dbErr := o.db.First(&ag, "id = ?", agentID).Error; dbErr == nil && ag.TmuxSession != "" {
				_ = o.runner.TmuxManager().KillAgentSession(ag.TmuxSession)
			}
		}
		// Mark agent as dead in DB regardless.
		o.db.Model(&model.Agent{}).Where("id = ?", agentID).Update("status", model.AgentDead)
	}

	// Delete associated comments and events.
	o.db.Where("task_id = ?", subtaskID).Delete(&model.TaskComment{})
	o.db.Where("task_id = ?", subtaskID).Delete(&model.TaskEvent{})

	// Delete the subtask itself.
	if err := o.db.Delete(&sub).Error; err != nil {
		return fmt.Errorf("delete subtask: %w", err)
	}

	o.emit("task_updated", nil)
	o.logger.Info("subtask deleted", "subtask_id", subtaskID, "agent_id", sub.AssignedAgentID)
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

// SpawnSupervisorSession creates an interactive Claude session in a tmux
// session for on-demand supervisor work on a task. The session runs in the
// task's integration worktree with a system prompt containing task context.
// Returns the tmux session name so the TUI can switch to it.
func (o *Orchestrator) SpawnSupervisorSession(taskID uuid.UUID) (string, error) {
	var task model.Task
	if err := o.db.First(&task, "id = ?", taskID).Error; err != nil {
		return "", fmt.Errorf("spawn supervisor: find task: %w", err)
	}

	// Determine the working directory. Prefer the task's integration worktree;
	// fall back to the default branch worktree.
	cwd := filepath.Join(o.worktree.BareRepoPath, o.worktree.DefaultBranch)
	if task.WorktreeBranch != "" {
		candidate := filepath.Join(o.worktree.BareRepoPath, task.WorktreeBranch, "integration")
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			cwd = candidate
		}
	}

	// Gather subtask info for context.
	var subtasks []model.Task
	o.db.Where("parent_task_id = ?", taskID).Find(&subtasks)
	stInfos := make([]supervisor.SubtaskInfo, len(subtasks))
	for i, st := range subtasks {
		stInfos[i] = supervisor.SubtaskInfo{
			ID:     st.ID.String(),
			Title:  st.Title,
			Status: string(st.Status),
			Branch: st.WorktreeBranch,
		}
	}

	// Build the system prompt with full orchestration context.
	prompt := supervisor.OnDemandPrompt(supervisor.OnDemandOpts{
		TaskTitle:     task.Title,
		TaskDesc:      task.Description,
		TaskID:        taskID.String(),
		Status:        string(task.Status),
		Branch:        task.WorktreeBranch,
		DBPath:        o.dbPath,
		BareRepoPath:  o.worktree.BareRepoPath,
		DefaultBranch: o.worktree.DefaultBranch,
		Subtasks:      stInfos,
	})

	// Write prompt to a temp file in the worktree.
	claudeDir := filepath.Join(cwd, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		return "", fmt.Errorf("spawn supervisor: mkdir .claude: %w", err)
	}
	promptPath := filepath.Join(claudeDir, "supervisor-prompt.md")
	if err := os.WriteFile(promptPath, []byte(prompt), 0o644); err != nil {
		return "", fmt.Errorf("spawn supervisor: write prompt: %w", err)
	}

	// Build session name under the dashboard's namespace.
	shortID := taskID.String()[:8]
	sessionName := fmt.Sprintf("%s/supervisor %s", o.runner.TmuxSessionName(), shortID)
	sessionName = strings.ReplaceAll(sessionName, ".", "-")
	sessionName = strings.ReplaceAll(sessionName, ":", "-")

	// Kill any existing supervisor session for this task.
	tmuxMgr := o.runner.TmuxManager()
	_ = tmuxMgr.KillAgentSession(sessionName)

	// Build the claude command.
	claudeBin := o.runner.ClaudeBin()
	cmd := fmt.Sprintf("%s --dangerously-skip-permissions \"$(cat %s)\"", claudeBin, promptPath)

	// Create the tmux session.
	if err := tmuxMgr.CreateAgentSession(sessionName, cmd, cwd); err != nil {
		return "", fmt.Errorf("spawn supervisor: create session: %w", err)
	}

	o.logger.Info("supervisor session spawned", "task_id", taskID, "session", sessionName)
	return sessionName, nil
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
