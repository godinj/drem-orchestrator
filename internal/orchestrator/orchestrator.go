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
	StaleSubtasksReset     int
	OrphanedSubtasksFixed  int
	EmptyFeaturesFailed    int
	OrphanWorktreesCleaned int
	StuckAgentsRecovered   int
}

// ReapOrphanedSessions kills tmux agent sessions that have no active agent
// and whose process has exited. This is manual-only to avoid destroying
// worktrees before merges complete. Returns the number of sessions reaped.
func (o *Orchestrator) ReapOrphanedSessions() (int, error) {
	return o.runner.ReapOrphanedSessions()
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

	if n, err := o.reconcileStuckAgents(); err != nil {
		return 0, fmt.Errorf("reconcile stuck agents: %w", err)
	} else {
		r.StuckAgentsRecovered = n
	}

	total := r.StaleSubtasksReset + r.OrphanedSubtasksFixed + r.EmptyFeaturesFailed + r.OrphanWorktreesCleaned + r.StuckAgentsRecovered
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

// isWorkAlreadyMerged checks whether the agent branch's commits are
// already reachable from the feature branch HEAD. Returns true if the
// work has been merged (even if the subtask status says failed).
func (o *Orchestrator) isWorkAlreadyMerged(subtask *model.Task, featureWorktree string) bool {
	if subtask.AssignedAgentID == nil {
		return false
	}

	var ag model.Agent
	if err := o.db.First(&ag, "id = ?", subtask.AssignedAgentID).Error; err != nil {
		return false
	}

	if ag.WorktreeBranch == "" {
		return false
	}

	// Check if agent branch tip is an ancestor of feature HEAD.
	_, err := worktree.RunGit(
		[]string{"merge-base", "--is-ancestor", ag.WorktreeBranch, "HEAD"},
		featureWorktree,
	)
	return err == nil // exit code 0 means it IS an ancestor
}

// resolveFeatureWorktree resolves the feature integration worktree path
// for a subtask by looking up its parent task's WorktreeBranch.
func (o *Orchestrator) resolveFeatureWorktree(subtask *model.Task) string {
	if subtask.ParentTaskID == nil {
		return ""
	}
	var parent model.Task
	if err := o.db.Select("worktree_branch").First(&parent, "id = ?", subtask.ParentTaskID).Error; err != nil {
		return ""
	}
	if parent.WorktreeBranch == "" {
		return ""
	}
	fn := strings.TrimPrefix(parent.WorktreeBranch, "feature/")
	return o.worktree.FeatureWorktreePath(fn)
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

		// Before resetting, check if work is already merged.
		if featureBranch != "" {
			fn := strings.TrimPrefix(featureBranch, "feature/")
			featureDir := o.worktree.FeatureWorktreePath(fn)
			if o.isWorkAlreadyMerged(sub, featureDir) {
				o.logger.Info("reconcile: work already merged, fast-tracking to done",
					"subtask_id", sub.ID, "agent_id", ag.ID)
				// Fast-track to done since work is already in the feature branch.
				transitions := []model.TaskStatus{
					model.StatusTestingReady,
					model.StatusMerging,
					model.StatusDone,
				}
				for _, target := range transitions {
					if sub.Status == target {
						continue
					}
					evt, tErr := state.TransitionTask(sub, target, "orchestrator",
						map[string]any{"reason": "reconcile-already-merged"})
					if tErr != nil {
						continue
					}
					if err := o.db.Create(evt).Error; err != nil {
						o.logger.Error("reconcile: save event", "subtask_id", sub.ID, "error", err)
						break
					}
				}
				if err := o.db.Save(sub).Error; err != nil {
					o.logger.Error("reconcile: save subtask", "subtask_id", sub.ID, "error", err)
				}
				// Clean up agent worktree since work is merged.
				if ag.WorktreeBranch != "" {
					if err := o.worktree.RemoveAgentWorktree(ag.WorktreeBranch); err != nil {
						o.logger.Warn("reconcile: cleanup agent worktree", "agent_id", ag.ID, "error", err)
					}
				}
				fixed++
				continue
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
			// Merge failed — keep the agent worktree/branch intact so work
			// is not lost (consistent with onAgentCompleted behavior).
			if err := o.failTask(sub, "reconcile: agent work could not be merged into feature branch (agent branch preserved)"); err != nil {
				o.logger.Error("reconcile: fail subtask", "subtask_id", sub.ID, "error", err)
			}
			// Leave agent worktree intact for manual resolution or retry.
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

// reconcileStuckAgents finds IN_PROGRESS subtasks whose agent tmux
// sessions are dead but no completion was ever received. This catches
// agents that exited without triggering the monitor goroutine.
func (o *Orchestrator) reconcileStuckAgents() (int, error) {
	var subtasks []model.Task
	if err := o.db.Where(
		"project_id = ? AND status = ? AND parent_task_id IS NOT NULL AND assigned_agent_id IS NOT NULL",
		o.projectID, model.StatusInProgress,
	).Find(&subtasks).Error; err != nil {
		return 0, err
	}

	// Build a set of agent IDs that the runner considers active.
	runningAgents := o.runner.GetRunningAgents()
	runningSet := make(map[uuid.UUID]bool, len(runningAgents))
	for _, ra := range runningAgents {
		runningSet[ra.AgentID] = true
	}

	fixed := 0
	for i := range subtasks {
		sub := &subtasks[i]

		var ag model.Agent
		if err := o.db.First(&ag, "id = ?", sub.AssignedAgentID).Error; err != nil {
			continue
		}

		// Only act on agents that are still marked as working in the DB.
		if ag.Status != model.AgentWorking {
			continue
		}

		// Skip agents that the runner still considers active.
		if runningSet[ag.ID] {
			continue
		}

		// Agent is NOT in the runner's running map AND DB status is working.
		o.logger.Warn("detected dead agent session without completion",
			"agent_id", ag.ID, "task", sub.Title, "session", ag.TmuxSession)

		// Check if the agent branch has commits.
		featureDir := o.resolveFeatureWorktree(sub)
		hasCommits := false
		if featureDir != "" && ag.WorktreeBranch != "" {
			var err error
			hasCommits, err = worktree.BranchHasNewCommits(featureDir, ag.WorktreeBranch)
			if err != nil {
				o.logger.Warn("reconcile stuck: failed to check commits",
					"agent_id", ag.ID, "error", err)
			}
		}

		if hasCommits {
			// Route through the normal completion path.
			o.logger.Info("reconcile stuck: agent has commits, sending completion",
				"agent_id", ag.ID, "task", sub.Title)
			if err := o.processAgentResult(agent.Completion{
				AgentID:    ag.ID,
				ReturnCode: 0,
			}); err != nil {
				o.logger.Error("reconcile stuck: process completion",
					"agent_id", ag.ID, "error", err)
			}
		} else {
			// No work produced — mark agent dead, subtask failed.
			ag.Status = model.AgentDead
			ag.CurrentTaskID = nil
			if err := o.db.Save(&ag).Error; err != nil {
				o.logger.Error("reconcile stuck: save agent", "agent_id", ag.ID, "error", err)
				continue
			}
			if err := o.failTask(sub, "agent session died without producing commits"); err != nil {
				o.logger.Error("reconcile stuck: fail subtask", "subtask_id", sub.ID, "error", err)
			}
		}
		fixed++
	}
	return fixed, nil
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

// SpawnReviewerSession spawns a reviewer agent for the given task.
// The task must be in plan_review or testing_ready status.
// Returns the tmux session name so the TUI can focus it.
func (o *Orchestrator) SpawnReviewerSession(taskID uuid.UUID) (string, error) {
	var task model.Task
	if err := o.db.First(&task, "id = ?", taskID).Error; err != nil {
		return "", fmt.Errorf("spawn reviewer: find task: %w", err)
	}

	// Validate status.
	if task.Status != model.StatusPlanReview && task.Status != model.StatusTestingReady {
		return "", fmt.Errorf("spawn reviewer: task must be in plan_review or testing_ready, got %s", task.Status)
	}

	// Check for existing working reviewer on the same task.
	var existing model.Agent
	err := o.db.Where("current_task_id = ? AND agent_type = ? AND status = ?",
		taskID, model.AgentReviewer, model.AgentWorking).First(&existing).Error
	if err == nil {
		// Already a working reviewer — return its session.
		o.logger.Info("reviewer already running for task", "task_id", taskID, "agent_id", existing.ID)
		return existing.TmuxSession, nil
	}

	// Resolve integration worktree.
	worktreePath := o.resolveIntegrationWorktree(&task)
	if worktreePath == "" {
		return "", fmt.Errorf("spawn reviewer: no integration worktree found for task %s", taskID)
	}

	// Load project.
	var project model.Project
	if err := o.db.First(&project, "id = ?", o.projectID).Error; err != nil {
		return "", fmt.Errorf("spawn reviewer: load project: %w", err)
	}

	// Determine review mode and build context.
	var reviewMode, planJSON, gitDiff string
	if task.Status == model.StatusPlanReview {
		reviewMode = "plan"
		if task.Plan != nil {
			if data, err := json.MarshalIndent(task.Plan, "", "  "); err == nil {
				planJSON = string(data)
			}
		}
	} else {
		reviewMode = "feature"
		// Get diff of integration branch vs default branch.
		diff, err := worktree.RunGit(
			[]string{"diff", o.worktree.DefaultBranch + "...HEAD", "--stat"},
			worktreePath,
		)
		if err == nil {
			// Also get the full diff (limited size).
			fullDiff, _ := worktree.RunGit(
				[]string{"diff", o.worktree.DefaultBranch + "...HEAD"},
				worktreePath,
			)
			if fullDiff != "" {
				gitDiff = fullDiff
			} else {
				gitDiff = diff
			}
		}
	}

	// Generate prompt.
	comments, _ := o.GetComments(task.ID)
	reviewerPrompt := prompt.Generate(prompt.Opts{
		Task:         &task,
		Project:      &project,
		AgentType:    model.AgentReviewer,
		WorktreePath: worktreePath,
		Comments:     comments,
		ReviewMode:   reviewMode,
		PlanJSON:     planJSON,
		GitDiff:      gitDiff,
	})

	// Spawn via runner.
	ag, err := o.runner.SpawnAgentInWorktree(&task, worktreePath, model.AgentReviewer, reviewerPrompt)
	if err != nil {
		return "", fmt.Errorf("spawn reviewer: %w", err)
	}

	o.emit("reviewer_spawned", map[string]any{"task_id": taskID, "agent_id": ag.ID, "mode": reviewMode})
	o.logger.Info("reviewer spawned", "task_id", taskID, "agent_id", ag.ID, "mode", reviewMode)
	return ag.TmuxSession, nil
}

// SpawnFixerSession spawns a fixer agent for the given task.
// The task should be in a status where a fix is applicable (in_progress, failed,
// testing_ready). Returns the tmux session name so the TUI can focus it.
func (o *Orchestrator) SpawnFixerSession(taskID uuid.UUID) (string, error) {
	var task model.Task
	if err := o.db.First(&task, "id = ?", taskID).Error; err != nil {
		return "", fmt.Errorf("spawn fixer: find task: %w", err)
	}

	// Validate status.
	switch task.Status {
	case model.StatusInProgress, model.StatusFailed, model.StatusTestingReady:
		// OK
	default:
		return "", fmt.Errorf("spawn fixer: task must be in in_progress, failed, or testing_ready, got %s", task.Status)
	}

	// Resolve integration worktree.
	worktreePath := o.resolveIntegrationWorktree(&task)
	if worktreePath == "" {
		return "", fmt.Errorf("spawn fixer: no integration worktree found for task %s", taskID)
	}

	// Check for any agent (reviewer/fixer) working in the same integration worktree.
	var busy model.Agent
	err := o.db.Where("worktree_path = ? AND status = ? AND agent_type IN ?",
		worktreePath, model.AgentWorking, []model.AgentType{model.AgentReviewer, model.AgentFixer}).
		First(&busy).Error
	if err == nil {
		return "", fmt.Errorf("spawn fixer: integration worktree is occupied by %s agent %s", busy.AgentType, busy.ID)
	}

	// Load project.
	var project model.Project
	if err := o.db.First(&project, "id = ?", o.projectID).Error; err != nil {
		return "", fmt.Errorf("spawn fixer: load project: %w", err)
	}

	// Extract diagnosis from task context.
	var diagnosis, suggestedFix string
	var affectedFiles []string
	if task.Context != nil {
		if d, ok := task.Context["failure_diagnosis"].(string); ok {
			diagnosis = d
		}
		if d, ok := task.Context["failure_reason"].(string); ok && diagnosis == "" {
			diagnosis = d
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
	}

	// Generate prompt.
	comments, _ := o.GetComments(task.ID)
	fixerPrompt := prompt.Generate(prompt.Opts{
		Task:          &task,
		Project:       &project,
		AgentType:     model.AgentFixer,
		WorktreePath:  worktreePath,
		Comments:      comments,
		Diagnosis:     diagnosis,
		AffectedFiles: affectedFiles,
		SuggestedFix:  suggestedFix,
	})

	// Spawn via runner.
	ag, err := o.runner.SpawnAgentInWorktree(&task, worktreePath, model.AgentFixer, fixerPrompt)
	if err != nil {
		return "", fmt.Errorf("spawn fixer: %w", err)
	}

	o.emit("fixer_spawned", map[string]any{"task_id": taskID, "agent_id": ag.ID})
	o.logger.Info("fixer spawned", "task_id", taskID, "agent_id", ag.ID)
	return ag.TmuxSession, nil
}

// resolveIntegrationWorktree returns the integration worktree path for a task.
// For parent tasks, it derives from WorktreeBranch. For subtasks, it looks up
// the parent task. Returns empty string if not found.
func (o *Orchestrator) resolveIntegrationWorktree(task *model.Task) string {
	branch := task.WorktreeBranch
	if branch == "" && task.ParentTaskID != nil {
		var parent model.Task
		if err := o.db.Select("worktree_branch").First(&parent, "id = ?", task.ParentTaskID).Error; err != nil {
			return ""
		}
		branch = parent.WorktreeBranch
	}
	if branch == "" {
		return ""
	}
	fn := strings.TrimPrefix(branch, "feature/")
	path := o.worktree.FeatureWorktreePath(fn)
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		return ""
	}
	return path
}

// ---------------------------------------------------------------------------
// Public methods for TUI interaction
// ---------------------------------------------------------------------------

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
		JournalDir:    o.journalDir(),
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
	shortID := taskID.String()[:4]
	title := strings.ReplaceAll(task.Title, "/", "-")
	title = truncate(title, 30)
	sessionName := fmt.Sprintf("%s/supervisor - %s %s", o.runner.TmuxSessionName(), title, shortID)
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

	o.logSupervisorAction(supervisor.JournalEntry{
		Timestamp: time.Now(),
		AgentName: "supervisor",
		TaskID:    taskID.String(),
		TaskTitle: task.Title,
		Type:      "on_demand_session",
		Summary:   "Interactive supervisor session spawned",
		Details: map[string]string{
			"Status":  string(task.Status),
			"Branch":  task.WorktreeBranch,
			"Session": sessionName,
		},
		Outcome: "Session started — supervisor will document findings in this journal",
	})

	o.logger.Info("supervisor session spawned", "task_id", taskID, "session", sessionName)
	return sessionName, nil
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// journalDir returns the path to the supervisor journal directory.
func (o *Orchestrator) journalDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "git", "drem-orchestrator.git", "journals")
}

// logSupervisorAction writes a supervisor intervention to the journal directory.
// Errors are logged but do not propagate — journaling is best-effort.
func (o *Orchestrator) logSupervisorAction(entry supervisor.JournalEntry) {
	if err := supervisor.WriteJournalEntry(o.journalDir(), entry); err != nil {
		o.logger.Warn("failed to write supervisor journal", "error", err)
	}
}

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
	IsTest         bool     `json:"is_test,omitempty"`
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
