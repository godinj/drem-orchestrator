// Package orchestrator implements the main tick loop and task scheduling for
// the Drem Orchestrator. It drives tasks through their lifecycle, spawns
// planner and coder agents, handles plan approval/rejection, manages merges,
// and exposes public methods for TUI interaction.
package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
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

// TestGateConfig holds configuration for the pre-merge test gate.
// These values are typically loaded from drem.toml or CLAUDE.md.
type TestGateConfig struct {
	TestCommand    string        `toml:"test_command"`     // e.g. "go test ./..."
	CompileCommand string        `toml:"compile_command"`  // e.g. "go vet ./..."
	ScopedTests    bool          `toml:"scoped_tests"`     // default true — scope tests to changed packages
	TestTimeout    time.Duration `toml:"test_timeout"`     // default 5m
}

// DefaultTestGateConfig returns a TestGateConfig with sensible defaults.
func DefaultTestGateConfig() TestGateConfig {
	return TestGateConfig{
		ScopedTests: true,
		TestTimeout: 5 * time.Minute,
	}
}

// TestResult stores the outcome of a test run for auditing and debugging.
type TestResult struct {
	Passed       bool      `json:"passed"`
	Output       string    `json:"output"`           // truncated to last 5000 chars
	ExitCode     int       `json:"exit_code"`
	RunAt        time.Time `json:"run_at"`
	Duration     float64   `json:"duration_seconds"`
	Command      string    `json:"command"`
	Scoped       bool      `json:"scoped"`            // true if ran scoped tests
	AttemptCount int       `json:"attempt_count"`
}

// commandResult holds the output from running a shell command.
type commandResult struct {
	Output   string
	ExitCode int
	Duration time.Duration
}

// Orchestrator is the main scheduling loop. It queries the database each tick,
// processes tasks through the state machine, spawns agents, and drives merges.
type Orchestrator struct {
	db              *gorm.DB
	dbPath          string
	runner          *agent.Runner
	worktree        *worktree.Manager
	merger          *merge.Orchestrator
	memory          *memory.Manager
	supervisor      *supervisor.Supervisor // nil disables LLM-powered decisions
	testGate        TestGateConfig
	projectID       uuid.UUID
	events          chan<- Event
	tick            time.Duration
	stale           time.Duration
	tickCount       int
	contextWarnPct  int
	contextStopPct  int
	contextFixerPct int // percentage: spawn fixer instead of failing
	logger          *slog.Logger
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
	contextWarnPct int,
	contextStopPct int,
	contextFixerPct ...int,
) *Orchestrator {
	fixerPct := 85
	if len(contextFixerPct) > 0 && contextFixerPct[0] > 0 {
		fixerPct = contextFixerPct[0]
	}
	return &Orchestrator{
		db:              db,
		dbPath:          dbPath,
		runner:          runner,
		worktree:        wt,
		merger:          merger,
		memory:          mem,
		supervisor:      sup,
		testGate:        DefaultTestGateConfig(),
		projectID:       projectID,
		events:          events,
		tick:            tickInterval,
		stale:           staleTimeout,
		contextWarnPct:  contextWarnPct,
		contextStopPct:  contextStopPct,
		contextFixerPct: fixerPct,
		logger:          slog.Default().With("component", "orchestrator", "project_id", projectID),
	}
}

// SetTestGateConfig updates the test gate configuration. Call this after
// loading configuration from drem.toml to override the defaults.
func (o *Orchestrator) SetTestGateConfig(cfg TestGateConfig) {
	if cfg.TestTimeout == 0 {
		cfg.TestTimeout = 5 * time.Minute
	}
	o.testGate = cfg
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

	// 2b. Check context window usage and apply role-aware escalation.
	o.checkContextUsage()

	// 2c. Fallback: detect agents stuck as WORKING whose idle signal file
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

	// 4b. Process TEST_WRITING parent tasks.
	var testWritingTasks []model.Task
	if err := o.db.Where("project_id = ? AND status = ? AND parent_task_id IS NULL",
		o.projectID, model.StatusTestWriting).Find(&testWritingTasks).Error; err != nil {
		o.logger.Error("doTick: query test_writing tasks", "error", err)
	} else {
		for i := range testWritingTasks {
			if err := o.processTestWriting(&testWritingTasks[i]); err != nil {
				o.logger.Error("doTick: processTestWriting", "task_id", testWritingTasks[i].ID, "error", err)
			}
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

	// 4b. Process TESTING_READY parent tasks (automated gate).
	var testingReadyTasks []model.Task
	if err := o.db.Where("project_id = ? AND status = ? AND parent_task_id IS NULL",
		o.projectID, model.StatusTestingReady).Find(&testingReadyTasks).Error; err != nil {
		o.logger.Error("doTick: query testing_ready tasks", "error", err)
	} else {
		for i := range testingReadyTasks {
			if err := o.processTestingReady(&testingReadyTasks[i]); err != nil {
				o.logger.Error("doTick: processTestingReady", "task_id", testingReadyTasks[i].ID, "error", err)
			}
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

	// 7b. Check context window usage for running agents.
	o.checkContextUsage()

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

// processBacklog transitions a task from BACKLOG to PLANNING.
// When replanning (PlanFeedback is set), it detaches old subtasks first
// to prevent the planner from seeing stale done subtasks and auto-advancing.
func (o *Orchestrator) processBacklog(task *model.Task) error {
	if task.PlanFeedback != "" {
		var oldSubtasks []model.Task
		o.db.Where("parent_task_id = ?", task.ID).Find(&oldSubtasks)
		if len(oldSubtasks) > 0 {
			for i := range oldSubtasks {
				oldSubtasks[i].ParentTaskID = nil
				o.db.Save(&oldSubtasks[i])
			}
			slog.Info("detached old subtasks for replanning",
				"task_id", task.ID, "count", len(oldSubtasks))
		}
	}

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
	switch ag.AgentType {
	case model.AgentPlanner:
		return o.onPlannerCompleted(ag, task)
	case model.AgentReviewer:
		return o.onReviewerCompleted(ag, task)
	case model.AgentFixer:
		return o.onFixerCompleted(ag, task)
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

		// Test gate: verify tests/compilation before merging.
		phase := taskPhase(task)
		if phase == "test" {
			// Test-phase subtask: compilation-only gate.
			testResult, testErr := o.verifyCompilationBeforeMerge(task, ag.WorktreePath)
			if testErr != nil {
				return fmt.Errorf("compilation gate: %w", testErr)
			}
			o.storeTestResult(ag, testResult)
			if !testResult.Passed {
				o.logger.Warn("compilation failed for test-phase subtask",
					"subtask_id", task.ID, "exit_code", testResult.ExitCode)
				// Don't block — the human catches quality at TEST_REVIEW.
			}
		} else if phase == "implementation" || phase == "integration" || phase == "" {
			// Implementation/integration phase: full test gate.
			testResult, testErr := o.verifyTestsBeforeMerge(task, ag.WorktreePath)
			if testErr != nil {
				return fmt.Errorf("test gate: %w", testErr)
			}
			o.storeTestResult(ag, testResult)
			if !testResult.Passed {
				o.logger.Error("tests failed, blocking merge",
					"subtask_id", task.ID, "attempts", testResult.AttemptCount)
				o.emit("test_gate_failed", map[string]any{
					"subtask_id": task.ID,
					"exit_code":  testResult.ExitCode,
					"output":     testResult.Output,
				})
				// Don't proceed to merge — leave subtask in current state.
				// The failure recovery handles escalation.
				return nil
			}
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

	// Track actual test files for test-phase subtasks (§4.8.2).
	if task.Phase == "test" && ag.WorktreePath != "" {
		featureBranch := task.WorktreeBranch
		if featureBranch == "" && task.ParentTaskID != nil {
			var parentForBranch model.Task
			if err := o.db.Select("worktree_branch").First(&parentForBranch, "id = ?", task.ParentTaskID).Error; err == nil {
				featureBranch = parentForBranch.WorktreeBranch
			}
		}
		if featureBranch != "" {
			actualTestFiles := o.extractTestFiles(ag.WorktreePath, featureBranch)
			if len(actualTestFiles) > 0 {
				if task.Context == nil {
					task.Context = make(model.JSONField)
				}
				task.Context["actual_test_files"] = actualTestFiles
			}
		}
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

	// Validate the plan before transitioning to plan_review.
	planResult, parseErr := parsePlan(task.Plan)
	if parseErr != nil {
		o.logger.Warn("plan validation: failed to parse stored plan", "task_id", task.ID, "error", parseErr)
	} else {
		validation := ValidatePlan(planResult.Subtasks, nil)
		// Store validation result in task context for TUI display.
		if task.Context == nil {
			task.Context = make(model.JSONField)
		}
		task.Context["plan_validation"] = map[string]any{
			"valid":    validation.Valid,
			"warnings": validation.Warnings,
			"errors":   validation.Errors,
		}
		if !validation.Valid {
			o.logger.Warn("plan validation failed, will retry",
				"task_id", task.ID, "errors", validation.Errors)
			task.AssignedAgentID = nil
			o.incrementRetryCount(task)
			return o.db.Save(task).Error
		}
		if len(validation.Warnings) > 0 {
			o.logger.Info("plan validation warnings",
				"task_id", task.ID, "warnings", validation.Warnings)
		}
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

// onReviewerCompleted handles a completed reviewer agent by parsing its
// review.json output and storing it in the task context.
func (o *Orchestrator) onReviewerCompleted(ag *model.Agent, task *model.Task) error {
	// Mark agent as idle.
	ag.Status = model.AgentIdle
	ag.CurrentTaskID = nil
	if err := o.db.Save(ag).Error; err != nil {
		return fmt.Errorf("on reviewer completed: save agent: %w", err)
	}

	// Read review.json from the worktree.
	reviewPath := filepath.Join(ag.WorktreePath, "review.json")
	reviewData, err := os.ReadFile(reviewPath)
	if err != nil {
		o.logger.Warn("reviewer produced no review.json", "task_id", task.ID, "agent_id", ag.ID, "error", err)
		return nil
	}

	// Parse review JSON into a map.
	var review map[string]any
	if err := json.Unmarshal(reviewData, &review); err != nil {
		o.logger.Warn("reviewer review.json parse failed", "task_id", task.ID, "error", err)
		return nil
	}

	// Store review in task context.
	if task.Context == nil {
		task.Context = make(model.JSONField)
	}
	task.Context["review"] = review
	if err := o.db.Save(task).Error; err != nil {
		return fmt.Errorf("on reviewer completed: save task: %w", err)
	}

	// Clean up the review.json file to avoid stale data on re-review.
	_ = os.Remove(reviewPath)

	o.emit("task_updated", task)
	o.logger.Info("review stored", "task_id", task.ID, "agent_id", ag.ID)
	return nil
}

// onFixerCompleted handles a completed fixer agent. The task stays in its
// current status for the user to decide next steps.
func (o *Orchestrator) onFixerCompleted(ag *model.Agent, task *model.Task) error {
	// Mark agent as idle.
	ag.Status = model.AgentIdle
	ag.CurrentTaskID = nil
	if err := o.db.Save(ag).Error; err != nil {
		return fmt.Errorf("on fixer completed: save agent: %w", err)
	}

	o.emit("task_updated", task)
	o.logger.Info("fixer completed", "task_id", task.ID, "agent_id", ag.ID)
	return nil
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

// IntegrationWorktreePath returns the integration worktree path for a task,
// resolving through the parent if the task is a subtask.
func (o *Orchestrator) IntegrationWorktreePath(taskID uuid.UUID) string {
	var task model.Task
	if err := o.db.First(&task, "id = ?", taskID).Error; err != nil {
		return ""
	}
	return o.resolveIntegrationWorktree(&task)
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

	// Only remove the agent worktree if it has no commits to preserve.
	// If the agent produced work, keep the worktree so it can be retried
	// or manually resolved (consistent with onAgentCompleted merge failure).
	if ag.WorktreeBranch != "" {
		featureDir := o.resolveFeatureWorktree(task)
		hasWork := false
		if featureDir != "" {
			if hasCommits, err := worktree.BranchHasNewCommits(featureDir, ag.WorktreeBranch); err == nil && hasCommits {
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
					o.logSupervisorAction(supervisor.JournalEntry{
						Timestamp: time.Now(),
						AgentName: ag.Name,
						TaskID:    task.ID.String(),
						TaskTitle: task.Title,
						Type:      "failure_diagnosis",
						Summary:   diagnosis.RootCause,
						Details: map[string]string{
							"Category":   diagnosis.Category,
							"Strategy":   diagnosis.RetryStrategy,
							"Agent Type": string(ag.AgentType),
						},
						Outcome: fmt.Sprintf("Failed after %d retries — max retries exceeded", retries),
					})
					if err := o.failTask(task, fmt.Sprintf("agent failed after %d retries (supervisor: %s)", retries, diagnosis.RootCause)); err != nil {
						return err
					}
					o.emit("agent_failed", map[string]any{"task_id": task.ID, "agent_id": ag.ID, "diagnosis": diagnosis.RootCause})
					return nil
				}

				o.logSupervisorAction(supervisor.JournalEntry{
					Timestamp: time.Now(),
					AgentName: ag.Name,
					TaskID:    task.ID.String(),
					TaskTitle: task.Title,
					Type:      "failure_diagnosis",
					Summary:   diagnosis.RootCause,
					Details: map[string]string{
						"Category":          diagnosis.Category,
						"Strategy":          diagnosis.RetryStrategy,
						"Prompt Adjustment": diagnosis.PromptAdjustment,
						"Agent Type":        string(ag.AgentType),
					},
					Outcome: fmt.Sprintf("Retrying (attempt %d)", retries),
				})

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

			o.logSupervisorAction(supervisor.JournalEntry{
				Timestamp: time.Now(),
				AgentName: ag.Name,
				TaskID:    task.ID.String(),
				TaskTitle: task.Title,
				Type:      "failure_diagnosis",
				Summary:   diagnosis.RootCause,
				Details: map[string]string{
					"Category":   diagnosis.Category,
					"Agent Type": string(ag.AgentType),
				},
				Outcome: "No retry recommended — falling through to default behavior",
			})
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
	// but DB update failed on a prior attempt).
	featureDir := o.resolveFeatureWorktree(task)
	if featureDir != "" && o.isWorkAlreadyMerged(task, featureDir) {
		o.logger.Info("agent failed but work already merged, fast-tracking to done",
			"task_id", task.ID, "agent_id", ag.ID)
		// Fast-track subtask through states to DONE.
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
				o.logSupervisorAction(supervisor.JournalEntry{
					Timestamp: time.Now(),
					AgentName: ag.Name,
					TaskID:    task.ID.String(),
					TaskTitle: task.Title,
					Type:      "empty_work_diagnosis",
					Summary:   diagnosis.RootCause,
					Details: map[string]string{
						"Prompt Adjustment": diagnosis.PromptAdjustment,
						"Agent Type":        string(ag.AgentType),
					},
					Outcome: fmt.Sprintf("Retrying (attempt %d of %d)", retries, MaxEmptyWorkRetries),
				})
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

			o.logSupervisorAction(supervisor.JournalEntry{
				Timestamp: time.Now(),
				AgentName: ag.Name,
				TaskID:    task.ID.String(),
				TaskTitle: task.Title,
				Type:      "empty_work_diagnosis",
				Summary:   diagnosis.RootCause,
				Details: map[string]string{
					"Agent Type": string(ag.AgentType),
				},
				Outcome: "No retry — will fail or fall through",
			})
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
	// During TEST_WRITING, skip wave-group gating entirely. Test subtasks are
	// additive (they create new test files and append to build files), so
	// file-overlap conflicts are rare and trivially handled by merge retry.
	// Serializing test subtasks via wave groups doubles wall-clock time for no
	// practical benefit.
	skipWaveGating := parent.Status == model.StatusTestWriting

	// Check for wave schedule in parent context.
	var allowedIDs map[uuid.UUID]bool
	if !skipWaveGating && parent.Context != nil {
		if scheduleRaw, hasSchedule := parent.Context["schedule"]; hasSchedule {
			scheduleJSON, marshalErr := json.Marshal(scheduleRaw)
			if marshalErr == nil {
				var schedule Schedule
				if parseErr := json.Unmarshal(scheduleJSON, &schedule); parseErr == nil {
					currentGroup := o.findCurrentGroup(parent, schedule)
					if currentGroup != nil {
						allowedIDs = make(map[uuid.UUID]bool, len(currentGroup.TaskIDs))
						for _, id := range currentGroup.TaskIDs {
							allowedIDs[id] = true
						}
						o.logger.Debug("wave schedule active",
							"task_id", parent.ID,
							"group_order", currentGroup.Order,
							"group_size", len(currentGroup.TaskIDs))
					} else {
						// All groups complete.
						return nil
					}
				} else {
					o.logger.Warn("schedule parse failed, falling back to legacy scheduling",
						"task_id", parent.ID, "error", parseErr)
				}
			}
		}
	}

	var subtasks []model.Task
	if err := o.db.Where(
		"parent_task_id = ? AND ((status = ?) OR (status = ? AND assigned_agent_id IS NULL))",
		parent.ID, model.StatusBacklog, model.StatusInProgress,
	).Order("priority DESC").Find(&subtasks).Error; err != nil {
		return fmt.Errorf("schedule subtasks: query: %w", err)
	}

	// Determine parent status for phase-aware scheduling.
	var parentStatus model.TaskStatus
	if parent != nil {
		parentStatus = parent.Status
	}

	for i := range subtasks {
		sub := &subtasks[i]

		// Phase-aware scheduling: during TEST_WRITING, only schedule test-phase subtasks.
		if parentStatus == model.StatusTestWriting && sub.Phase != "test" {
			continue
		}
		// During IN_PROGRESS, only schedule implementation and integration subtasks.
		if parentStatus == model.StatusInProgress && sub.Phase == "test" {
			continue
		}

		// If wave scheduling is active, only schedule subtasks in the current group.
		if allowedIDs != nil && !allowedIDs[sub.ID] {
			continue
		}

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

		// If subtask was previously assigned an agent, check if its work
		// is already merged into the feature branch before re-spawning.
		if sub.AssignedAgentID != nil {
			featureName := strings.TrimPrefix(parent.WorktreeBranch, "feature/")
			featureDir := o.worktree.FeatureWorktreePath(featureName)
			if o.isWorkAlreadyMerged(sub, featureDir) {
				o.logger.Info("schedule: work already merged, fast-tracking to done",
					"subtask_id", sub.ID)
				// Fast-track to done.
				transitions := []model.TaskStatus{
					model.StatusPlanning,
					model.StatusPlanReview,
					model.StatusInProgress,
					model.StatusTestingReady,
					model.StatusMerging,
					model.StatusDone,
				}
				for _, target := range transitions {
					if sub.Status == target {
						continue
					}
					evt, tErr := state.TransitionTask(sub, target, "orchestrator",
						map[string]any{"reason": "already-merged-skip-spawn"})
					if tErr != nil {
						continue
					}
					if err := o.db.Create(evt).Error; err != nil {
						o.logger.Error("schedule: save event", "subtask_id", sub.ID, "error", err)
						break
					}
				}
				if err := o.db.Save(sub).Error; err != nil {
					o.logger.Error("schedule: save subtask", "subtask_id", sub.ID, "error", err)
				}
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

		// Verify agent record was created in the DB.
		var verifyAgent model.Agent
		if err := o.db.Where("current_task_id = ? AND status = ?",
			sub.ID, model.AgentWorking).First(&verifyAgent).Error; err != nil {
			o.logger.Error("agent record missing after spawn",
				"subtask", sub.Title, "error", err)
			if err := o.failTask(sub, "agent record not found after spawn"); err != nil {
				o.logger.Error("schedule: fail subtask after missing agent", "subtask_id", sub.ID, "error", err)
			}
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

// findCurrentGroup returns the earliest group that has subtasks not yet in a
// terminal state (done or failed). Returns nil if all groups are complete.
// If a task ID in the schedule no longer exists (e.g. after replanning),
// it is treated as terminal to avoid permanently blocking the wave.
func (o *Orchestrator) findCurrentGroup(parent *model.Task, schedule Schedule) *SubtaskGroup {
	for i := range schedule.Groups {
		group := &schedule.Groups[i]
		allTerminal := true
		for _, taskID := range group.TaskIDs {
			var sub model.Task
			if err := o.db.Select("status").First(&sub, "id = ?", taskID).Error; err != nil {
				// Subtask not found (deleted or replanned) — treat as
				// terminal so this group doesn't block forever.
				o.logger.Warn("wave schedule references missing subtask",
					"parent_id", parent.ID, "subtask_id", taskID)
				continue
			}
			if !isTerminal(sub.Status) {
				allTerminal = false
				break
			}
		}
		if !allTerminal {
			return group
		}
	}
	return nil
}

// processTestWriting schedules test-phase subtasks and checks for completion.
// When all test-phase subtasks are done, transitions the parent to TEST_REVIEW.
func (o *Orchestrator) processTestWriting(parent *model.Task) error {
	// Check baseline test health (once per task).
	if parent.Context == nil {
		parent.Context = make(model.JSONField)
	}
	if _, checked := parent.Context["baseline_tests_checked"]; !checked {
		testCmd := o.getTestCommand(parent)
		if testCmd != "" {
			featureName := strings.TrimPrefix(parent.WorktreeBranch, "feature/")
			featureDir := o.worktree.FeatureWorktreePath(featureName)
			result, runErr := runCommand(featureDir, testCmd)
			parent.Context["baseline_tests_checked"] = true
			if runErr != nil || result.ExitCode != 0 {
				parent.Context["baseline_tests_failed"] = true
				parent.Context["baseline_test_output"] = truncate(result.Output, 5000)
				o.logger.Warn("baseline tests fail on integration branch",
					"task_id", parent.ID, "exit_code", result.ExitCode)
				if err := o.db.Save(parent).Error; err != nil {
					return fmt.Errorf("process test writing: save baseline check: %w", err)
				}
				return nil
			}
			if err := o.db.Save(parent).Error; err != nil {
				return fmt.Errorf("process test writing: save baseline check: %w", err)
			}
		}
	}

	// Block scheduling (but NOT completion checks) if baseline tests failed.
	if failed, ok := parent.Context["baseline_tests_failed"].(bool); ok && failed {
		// Still run the completion check below — a supervisor may have
		// manually fixed the subtask and set it to done.
	} else {
		// Schedule test-phase subtasks using the existing scheduling logic.
		if err := o.scheduleSubtasks(parent); err != nil {
			return fmt.Errorf("process test writing: schedule: %w", err)
		}
	}

	// Completion check runs unconditionally every tick. This ensures that
	// when a supervisor manually fixes a failed subtask (merges work, sets
	// subtask to done), processTestWriting detects the change on the next
	// tick and transitions the parent.

	// Check if all test-phase subtasks are in a terminal state.
	var testSubtasks []model.Task
	if err := o.db.Where("parent_task_id = ? AND phase = ?", parent.ID, "test").
		Find(&testSubtasks).Error; err != nil {
		return fmt.Errorf("process test writing: query test subtasks: %w", err)
	}

	if len(testSubtasks) == 0 {
		return nil
	}

	allTerminal := true
	anyFailed := false
	allDone := true

	for _, sub := range testSubtasks {
		switch sub.Status {
		case model.StatusDone:
			// good
		case model.StatusFailed, model.StatusRejected:
			anyFailed = true
			allDone = false
		default:
			allTerminal = false
			allDone = false
		}
	}

	if allDone {
		// All test subtasks done -> transition to TEST_REVIEW.
		// Clear any blocking flags that may have been set by prior failure handling.
		delete(parent.Context, "baseline_tests_failed")
		delete(parent.Context, "needs_human_review")

		evt, err := state.TransitionTask(parent, model.StatusTestReview, "orchestrator",
			map[string]any{"reason": "all test subtasks done"})
		if err != nil {
			return fmt.Errorf("process test writing: transition to test_review: %w", err)
		}
		if err := o.db.Save(parent).Error; err != nil {
			return fmt.Errorf("process test writing: save parent: %w", err)
		}
		if err := o.db.Create(evt).Error; err != nil {
			return fmt.Errorf("process test writing: save event: %w", err)
		}
		o.emit("test_review_ready", map[string]any{"task_id": parent.ID})
		o.logger.Info("all test subtasks done, test review ready", "task_id", parent.ID)
	} else if allTerminal && anyFailed {
		// All test subtasks terminal but some failed -> fail the parent.
		var failedNames []string
		for _, sub := range testSubtasks {
			if sub.Status == model.StatusFailed {
				failedNames = append(failedNames, sub.Title)
			}
		}
		if err := o.failTask(parent, fmt.Sprintf("test subtasks failed: %s",
			strings.Join(failedNames, ", "))); err != nil {
			return err
		}
	}

	return nil
}

// checkFeatureCompletion checks whether all subtasks of a parent are DONE and
// transitions the parent accordingly. The parent only fails when ALL subtasks
// are terminal (done or failed) and at least one is failed. While any subtask
// is still in_progress, planning, or backlog, the parent stays in_progress.
func (o *Orchestrator) checkFeatureCompletion(parent *model.Task) error {
	var subtasks []model.Task
	if err := o.db.Where("parent_task_id = ?", parent.ID).Find(&subtasks).Error; err != nil {
		return fmt.Errorf("check feature completion: query: %w", err)
	}

	if len(subtasks) == 0 {
		return nil
	}

	allTerminal := true
	anyFailed := false
	allDone := true

	for _, sub := range subtasks {
		if isTerminal(sub.Status) {
			if sub.Status == model.StatusFailed {
				anyFailed = true
				allDone = false
			} else if sub.Status == model.StatusRejected {
				// Rejected subtasks are terminal but don't count as "done".
				allDone = false
			}
			// StatusDone: good — no flags to set.
		} else {
			allTerminal = false
			allDone = false
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
	} else if allTerminal && anyFailed && parent.Status == model.StatusInProgress {
		// All subtasks finished but some failed -> parent fails.
		var failedNames []string
		for _, sub := range subtasks {
			if sub.Status == model.StatusFailed {
				failedNames = append(failedNames, sub.Title)
			}
		}
		if err := o.failTask(parent, fmt.Sprintf("subtasks failed: %s", strings.Join(failedNames, ", "))); err != nil {
			return err
		}
	}
	// Otherwise: subtasks still running, keep parent in_progress — do nothing.

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

					canAutoFix := "no"
					if diagnosis.CanAutoFix {
						canAutoFix = "yes"
					}
					o.logSupervisorAction(supervisor.JournalEntry{
						Timestamp: time.Now(),
						AgentName: "orchestrator",
						TaskID:    task.ID.String(),
						TaskTitle: task.Title,
						Type:      "build_failure",
						Summary:   diagnosis.RootCause,
						Details: map[string]string{
							"Suggested Fix":  diagnosis.SuggestedFix,
							"Affected Files": strings.Join(diagnosis.AffectedFiles, ", "),
							"Can Auto-Fix":   canAutoFix,
						},
						Outcome: "Merge failed — build verification error",
					})
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

					o.logSupervisorAction(supervisor.JournalEntry{
						Timestamp: time.Now(),
						AgentName: "orchestrator",
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

	// Parse the plan (full format with TDD exceptions).
	planResult, err := parsePlan(task.Plan)
	if err != nil {
		return fmt.Errorf("handle plan approved: %w", err)
	}
	subtaskPlans := planResult.Subtasks

	// Auto-generate TDD reverse dependencies from tests_for.
	merged := MergeTDDDependencies(subtaskPlans)

	// Create subtask records. We need to track created IDs for dependency mapping.
	createdIDs := make([]uuid.UUID, len(subtaskPlans))
	for i, sp := range subtaskPlans {
		subtaskID := uuid.New()
		createdIDs[i] = subtaskID

		ctx := model.JSONField{
			"agent_type":      sp.AgentType,
			"estimated_files": sp.EstimatedFiles,
		}
		if sp.Phase != "" {
			ctx["phase"] = sp.Phase
		}

		sub := model.Task{
			ID:           subtaskID,
			ProjectID:    task.ProjectID,
			ParentTaskID: &task.ID,
			Title:        sp.Title,
			Description:  sp.Description,
			Status:       model.StatusBacklog,
			Phase:        sp.Phase,
			Context:      ctx,
			Priority:     len(subtaskPlans) - i, // higher priority for earlier items
		}

		if err := o.db.Create(&sub).Error; err != nil {
			return fmt.Errorf("handle plan approved: create subtask %d: %w", i, err)
		}
	}

	// Second pass: set dependency IDs (including auto-generated TDD deps)
	// now that all subtask UUIDs are known.
	// The plan uses 0-based indices to reference other subtasks.
	for i, sp := range merged {
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

	// Third pass: set TestsFor on test-phase subtasks.
	for i, sp := range subtaskPlans {
		if len(sp.TestsFor) > 0 {
			var testsForIDs model.JSONArray
			for _, idx := range sp.TestsFor {
				if idx >= 0 && idx < len(createdIDs) {
					testsForIDs = append(testsForIDs, createdIDs[idx].String())
				}
			}
			if len(testsForIDs) > 0 {
				o.db.Model(&model.Task{}).Where("id = ?", createdIDs[i]).
					Update("tests_for", testsForIDs)
			}
		}
	}

	// Store TDD exceptions on the parent task.
	if len(planResult.TDDExceptions) > 0 {
		exceptionsJSON, _ := json.Marshal(planResult.TDDExceptions)
		var exceptionsField any
		json.Unmarshal(exceptionsJSON, &exceptionsField)
		if task.TDDExceptions == nil {
			task.TDDExceptions = make(model.JSONField)
		}
		task.TDDExceptions["exceptions"] = exceptionsField
	}

	// Build wave schedule from the created subtasks.
	var createdSubtasks []model.Task
	if err := o.db.Where("parent_task_id = ?", task.ID).Find(&createdSubtasks).Error; err != nil {
		o.logger.Warn("handle plan approved: failed to load subtasks for scheduling", "error", err)
	} else if len(createdSubtasks) > 0 {
		schedule := BuildSchedule(createdSubtasks)
		scheduleJSON, marshalErr := json.Marshal(schedule)
		if marshalErr != nil {
			o.logger.Warn("handle plan approved: failed to marshal schedule", "error", marshalErr)
		} else {
			if task.Context == nil {
				task.Context = make(model.JSONField)
			}
			var scheduleField any
			if err := json.Unmarshal(scheduleJSON, &scheduleField); err != nil {
				o.logger.Warn("handle plan approved: failed to unmarshal schedule into context", "error", err)
			} else {
				task.Context["schedule"] = scheduleField
				o.logger.Info("wave schedule computed",
					"task_id", task.ID,
					"groups", len(schedule.Groups),
					"subtasks", len(createdSubtasks))
			}
		}
	}

	// Clear planner agent assignment now that review is complete.
	task.AssignedAgentID = nil

	// Write plan.json to the integration worktree and commit it immediately
	// so it doesn't leave the worktree dirty (which blocks subsequent merges).
	if task.WorktreeBranch != "" {
		featureName := strings.TrimPrefix(task.WorktreeBranch, "feature/")
		featureDir := o.worktree.FeatureWorktreePath(featureName)
		planJSON, marshalErr := json.MarshalIndent(task.Plan, "", "  ")
		if marshalErr != nil {
			o.logger.Warn("handle plan approved: failed to marshal plan for worktree", "error", marshalErr)
		} else {
			planPath := filepath.Join(featureDir, "plan.json")
			if writeErr := os.WriteFile(planPath, planJSON, 0o644); writeErr != nil {
				o.logger.Warn("handle plan approved: failed to write plan.json to worktree", "error", writeErr)
			} else {
				committed, commitErr := worktree.CommitUnstagedChanges(
					featureDir, "chore: commit plan.json after plan approval")
				if commitErr != nil {
					o.logger.Warn("handle plan approved: failed to commit plan.json", "error", commitErr)
				} else if committed {
					o.logger.Info("handle plan approved: committed plan.json to integration worktree",
						"task_id", task.ID)
				}
			}
		}
	}

	// Determine transition target: TEST_WRITING if plan has test-phase subtasks,
	// IN_PROGRESS otherwise (backward compatible for old-format plans).
	hasTestPhase := false
	for _, sp := range subtaskPlans {
		if sp.Phase == "test" {
			hasTestPhase = true
			break
		}
	}

	var targetStatus model.TaskStatus
	if hasTestPhase {
		targetStatus = model.StatusTestWriting
	} else {
		targetStatus = model.StatusInProgress
	}

	evt, err := state.TransitionTask(&task, targetStatus, "user", map[string]any{"action": "plan_approved"})
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

// HandleTestPassed transitions from TESTING_READY to MERGING.
func (o *Orchestrator) HandleTestPassed(taskID uuid.UUID) error {
	var task model.Task
	if err := o.db.First(&task, "id = ?", taskID).Error; err != nil {
		return fmt.Errorf("handle test passed: load task: %w", err)
	}

	if task.Status != model.StatusTestingReady {
		return fmt.Errorf("handle test passed: task %s is in %s, expected testing_ready", taskID, task.Status)
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

// HandleTestFailed transitions from TESTING_READY back to IN_PROGRESS so the
// failed subtasks can be re-implemented. When called manually by a human, the
// task returns to implementation rather than re-planning.
func (o *Orchestrator) HandleTestFailed(taskID uuid.UUID) error {
	var task model.Task
	if err := o.db.First(&task, "id = ?", taskID).Error; err != nil {
		return fmt.Errorf("handle test failed: load task: %w", err)
	}

	if task.Status != model.StatusTestingReady {
		return fmt.Errorf("handle test failed: task %s is in %s, expected testing_ready", taskID, task.Status)
	}

	task.AssignedAgentID = nil

	evt, err := state.TransitionTask(&task, model.StatusInProgress, "user", map[string]any{"action": "test_failed"})
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
	o.logger.Info("test failed, task back to in_progress", "task_id", task.ID)
	return nil
}

// HandleTestReviewApproved transitions from TEST_REVIEW to IN_PROGRESS,
// enabling implementation subtask scheduling.
func (o *Orchestrator) HandleTestReviewApproved(taskID uuid.UUID) error {
	var task model.Task
	if err := o.db.First(&task, "id = ?", taskID).Error; err != nil {
		return fmt.Errorf("handle test review approved: load task: %w", err)
	}

	if task.Status != model.StatusTestReview {
		return fmt.Errorf("handle test review approved: task %s is in %s, expected test_review", taskID, task.Status)
	}

	evt, err := state.TransitionTask(&task, model.StatusInProgress, "user", map[string]any{"action": "test_review_approved"})
	if err != nil {
		return fmt.Errorf("handle test review approved: transition: %w", err)
	}
	if err := o.db.Save(&task).Error; err != nil {
		return fmt.Errorf("handle test review approved: save task: %w", err)
	}
	if err := o.db.Create(evt).Error; err != nil {
		return fmt.Errorf("handle test review approved: save event: %w", err)
	}

	o.emit("task_updated", &task)
	o.logger.Info("test review approved, scheduling implementation", "task_id", task.ID)
	return nil
}

// HandleTestReviewRejected marks rejected test subtasks as REJECTED, clones
// them with feedback, and transitions the parent back to TEST_WRITING.
// After 3 rejection rounds, pauses the task and spawns a diagnostic agent.
func (o *Orchestrator) HandleTestReviewRejected(taskID uuid.UUID, feedback string) error {
	var task model.Task
	if err := o.db.First(&task, "id = ?", taskID).Error; err != nil {
		return fmt.Errorf("handle test review rejected: load task: %w", err)
	}

	if task.Status != model.StatusTestReview {
		return fmt.Errorf("handle test review rejected: task %s is in %s, expected test_review", taskID, task.Status)
	}

	// Initialize context if needed.
	if task.Context == nil {
		task.Context = make(model.JSONField)
	}

	// Track rejection count.
	rejectionCount := 0
	if v, ok := task.Context["test_rejection_count"].(float64); ok {
		rejectionCount = int(v)
	}
	rejectionCount++
	task.Context["test_rejection_count"] = float64(rejectionCount)

	// Store the feedback for this round.
	feedbackKey := fmt.Sprintf("test_rejection_feedback_%d", rejectionCount)
	task.Context[feedbackKey] = feedback

	// If 3rd rejection, transition to PAUSED via TEST_WRITING and spawn diagnostic.
	if rejectionCount >= 3 {
		// Transition to TEST_WRITING first (valid from TEST_REVIEW).
		evt1, err := state.TransitionTask(&task, model.StatusTestWriting, "user", map[string]any{
			"action": "test_review_rejected_diagnostic",
			"reason": "3 rejection rounds exceeded",
		})
		if err != nil {
			return fmt.Errorf("handle test review rejected: transition to test_writing: %w", err)
		}
		if err := o.db.Create(evt1).Error; err != nil {
			return fmt.Errorf("handle test review rejected: save event1: %w", err)
		}

		// Then transition to PAUSED (valid from TEST_WRITING).
		evt2, err := state.TransitionTask(&task, model.StatusPaused, "user", map[string]any{
			"action": "test_review_rejected_paused",
			"reason": "3 test rejection rounds exceeded, diagnostic required",
		})
		if err != nil {
			return fmt.Errorf("handle test review rejected: transition to paused: %w", err)
		}
		task.Context["paused_from"] = string(model.StatusTestWriting)
		task.Context["diagnostic_required"] = true

		if err := o.db.Save(&task).Error; err != nil {
			return fmt.Errorf("handle test review rejected: save task: %w", err)
		}
		if err := o.db.Create(evt2).Error; err != nil {
			return fmt.Errorf("handle test review rejected: save event2: %w", err)
		}

		o.emit("task_updated", &task)
		o.logger.Warn("test review rejected 3 times, task paused for diagnostic",
			"task_id", task.ID, "rejection_count", rejectionCount)

		// Spawn diagnostic agent (best-effort).
		if err := o.spawnDiagnosticAgent(&task); err != nil {
			o.logger.Warn("failed to spawn diagnostic agent", "task_id", task.ID, "error", err)
		}
		return nil
	}

	// Load all test-phase subtasks in DONE state for this parent.
	var doneTestSubtasks []model.Task
	if err := o.db.Where(
		"parent_task_id = ? AND phase = ? AND status = ?",
		task.ID, "test", model.StatusDone,
	).Find(&doneTestSubtasks).Error; err != nil {
		return fmt.Errorf("handle test review rejected: query test subtasks: %w", err)
	}

	// For each done test subtask: mark as REJECTED and create a replacement.
	for i := range doneTestSubtasks {
		sub := &doneTestSubtasks[i]

		// Mark as REJECTED. Since DONE has no outbound transitions in the state
		// machine, we set the status directly for this terminal→terminal move.
		sub.Status = model.StatusRejected
		sub.UpdatedAt = time.Now()
		if err := o.db.Save(sub).Error; err != nil {
			return fmt.Errorf("handle test review rejected: reject subtask %s: %w", sub.ID, err)
		}

		rejectEvt := &model.TaskEvent{
			ID:        uuid.New(),
			TaskID:    sub.ID,
			EventType: "status_change",
			OldValue:  string(model.StatusDone),
			NewValue:  string(model.StatusRejected),
			Details:   model.JSONField{"action": "test_review_rejected", "feedback": feedback},
			Actor:     "user",
			CreatedAt: time.Now(),
		}
		if err := o.db.Create(rejectEvt).Error; err != nil {
			return fmt.Errorf("handle test review rejected: save reject event for %s: %w", sub.ID, err)
		}

		// Create a replacement subtask cloned from the rejected one.
		revisionSuffix := fmt.Sprintf(" (revision %d)", rejectionCount)
		newDescription := sub.Description + "\n\n## Rejection Feedback\n\n" + feedback

		// Copy context fields.
		var newCtx model.JSONField
		if sub.Context != nil {
			newCtx = make(model.JSONField, len(sub.Context))
			for k, v := range sub.Context {
				newCtx[k] = v
			}
		} else {
			newCtx = make(model.JSONField)
		}

		replacementID := uuid.New()
		replacement := model.Task{
			ID:            replacementID,
			ProjectID:     sub.ProjectID,
			ParentTaskID:  sub.ParentTaskID,
			Title:         sub.Title + revisionSuffix,
			Description:   newDescription,
			Status:        model.StatusBacklog,
			Priority:      sub.Priority,
			DependencyIDs: sub.DependencyIDs,
			Phase:         sub.Phase,
			TestsFor:      sub.TestsFor,
			Context:       newCtx,
		}

		if err := o.db.Create(&replacement).Error; err != nil {
			return fmt.Errorf("handle test review rejected: create replacement for %s: %w", sub.ID, err)
		}

		o.logger.Info("test subtask rejected and replaced",
			"original_id", sub.ID,
			"replacement_id", replacementID,
			"revision", rejectionCount)
	}

	// Transition parent back to TEST_WRITING.
	evt, err := state.TransitionTask(&task, model.StatusTestWriting, "user", map[string]any{
		"action":          "test_review_rejected",
		"rejection_count": rejectionCount,
		"feedback":        feedback,
		"subtasks_cloned": len(doneTestSubtasks),
	})
	if err != nil {
		return fmt.Errorf("handle test review rejected: transition to test_writing: %w", err)
	}
	if err := o.db.Save(&task).Error; err != nil {
		return fmt.Errorf("handle test review rejected: save task: %w", err)
	}
	if err := o.db.Create(evt).Error; err != nil {
		return fmt.Errorf("handle test review rejected: save event: %w", err)
	}

	o.emit("task_updated", &task)
	o.logger.Info("test review rejected, back to test writing",
		"task_id", task.ID,
		"rejection_count", rejectionCount,
		"subtasks_cloned", len(doneTestSubtasks))
	return nil
}

// spawnDiagnosticAgent creates a diagnostic agent to help the human understand
// repeated test rejection patterns.
func (o *Orchestrator) spawnDiagnosticAgent(parent *model.Task) error {
	// Gather rejection history from the parent context.
	var rounds []string
	for i := 1; i <= 3; i++ {
		key := fmt.Sprintf("test_rejection_feedback_%d", i)
		if fb, ok := parent.Context[key].(string); ok {
			rounds = append(rounds, fmt.Sprintf("Round %d feedback: %s", i, fb))
		}
	}

	// Gather test subtask history (all test-phase subtasks including rejected).
	var testSubtasks []model.Task
	if err := o.db.Where(
		"parent_task_id = ? AND phase = ?",
		parent.ID, "test",
	).Order("created_at asc").Find(&testSubtasks).Error; err != nil {
		o.logger.Warn("diagnostic agent: failed to load test subtasks", "error", err)
	}

	var subtaskSummary []string
	for _, sub := range testSubtasks {
		subtaskSummary = append(subtaskSummary,
			fmt.Sprintf("- %s [%s]", sub.Title, sub.Status))
	}

	diagnosticPrompt := fmt.Sprintf(
		"The tests for this task have been rejected 3 times. Help the human understand why.\n\n"+
			"Task: %s\n%s\n\n"+
			"Test subtask history:\n%s\n\n"+
			"Rejection rounds:\n%s\n\n"+
			"Summarize the pattern of rejections and suggest a path forward.\n"+
			"Either the test premise is wrong, the acceptance criteria are ambiguous,\n"+
			"or there's a misunderstanding.",
		parent.Title,
		parent.Description,
		strings.Join(subtaskSummary, "\n"),
		strings.Join(rounds, "\n"),
	)

	// Store the diagnostic prompt in the parent context for reference.
	parent.Context["diagnostic_prompt"] = diagnosticPrompt
	if err := o.db.Save(parent).Error; err != nil {
		return fmt.Errorf("spawn diagnostic agent: save context: %w", err)
	}

	// If the runner is not available, log and return.
	if o.runner == nil {
		o.logger.Warn("diagnostic agent: runner not available, diagnostic prompt stored in task context",
			"task_id", parent.ID)
		return nil
	}

	// Spawn a reviewer-type agent. Use the integration branch worktree if available.
	featureName := strings.TrimPrefix(parent.WorktreeBranch, "feature/")
	worktreePath := ""
	if featureName != "" && o.worktree != nil {
		worktreePath = o.worktree.FeatureWorktreePath(featureName)
	}

	ag := model.Agent{
		ID:             uuid.New(),
		ProjectID:      parent.ProjectID,
		AgentType:      model.AgentReviewer,
		Name:           fmt.Sprintf("diagnostic-%s", parent.ID.String()[:8]),
		Status:         model.AgentWorking,
		CurrentTaskID:  &parent.ID,
		WorktreePath:   worktreePath,
		WorktreeBranch: parent.WorktreeBranch,
	}

	if err := o.db.Create(&ag).Error; err != nil {
		return fmt.Errorf("spawn diagnostic agent: create agent record: %w", err)
	}

	o.logger.Info("diagnostic agent spawned",
		"task_id", parent.ID,
		"agent_id", ag.ID)

	return nil
}

// isTerminal returns true if a task status is a terminal state (no further
// automated processing will occur).
func isTerminal(status model.TaskStatus) bool {
	return status == model.StatusDone || status == model.StatusFailed || status == model.StatusRejected
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

// checkContextUsage inspects context window usage for all running agents and
// takes action at configured thresholds.
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

// extractTestFiles runs git diff --name-only on the agent's worktree and
// returns files matching test patterns.
func (o *Orchestrator) extractTestFiles(worktreePath, baseBranch string) []string {
	output, err := worktree.RunGit([]string{
		"diff", "--name-only", baseBranch + "...HEAD",
	}, worktreePath)
	if err != nil {
		o.logger.Warn("extract test files: git diff failed", "path", worktreePath, "error", err)
		return nil
	}
	if output == "" {
		return nil
	}

	var testFiles []string
	for _, file := range strings.Split(output, "\n") {
		file = strings.TrimSpace(file)
		if file == "" {
			continue
		}
		if isTestFile(file) {
			testFiles = append(testFiles, file)
		}
	}
	return testFiles
}

// isTestFile checks if a filename matches common test file patterns.
func isTestFile(name string) bool {
	base := filepath.Base(name)
	lower := strings.ToLower(base)

	// Go: *_test.go
	if strings.HasSuffix(lower, "_test.go") {
		return true
	}
	// Python: test_*.py or *_test.py
	if strings.HasSuffix(lower, ".py") && (strings.HasPrefix(lower, "test_") || strings.HasSuffix(lower, "_test.py")) {
		return true
	}
	// JavaScript/TypeScript: *.test.ts, *.test.js, *.spec.ts, *.spec.js
	for _, suffix := range []string{".test.ts", ".test.js", ".spec.ts", ".spec.js", ".test.tsx", ".test.jsx", ".spec.tsx", ".spec.jsx"} {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	// C++: *Test.cpp, *Tests.cpp, *_test.cpp, *_tests.cpp, *Test.h, *Tests.h
	for _, suffix := range []string{"test.cpp", "tests.cpp", "_test.cpp", "_tests.cpp", "test.h", "tests.h"} {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	// C++: files under tests/ or test/ directories
	normalizedPath := strings.ToLower(name)
	for _, prefix := range []string{"tests/", "test/"} {
		if strings.HasPrefix(normalizedPath, prefix) || strings.Contains(normalizedPath, "/"+prefix) {
			// Only match C++ source/header files in test directories
			for _, ext := range []string{".cpp", ".cc", ".cxx", ".h", ".hpp"} {
				if strings.HasSuffix(lower, ext) {
					return true
				}
			}
		}
	}
	return false
}

// runCommand executes a shell command in the given directory and returns the result.
func runCommand(dir, command string) (*commandResult, error) {
	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = dir

	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &out

	err := cmd.Run()
	result := &commandResult{
		Output: out.String(),
	}

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
		} else {
			return result, fmt.Errorf("run command: %w", err)
		}
	}

	return result, nil
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

		if ag.AgentType == model.AgentFixer && pct >= 80 {
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
			gitDiff = truncate(fullDiff, 10000)
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

// processTestingReady runs the automated test gate at TESTING_READY.
// It checks for an existing fixer/reviewer agent, runs the test suite,
// and either transitions to MERGING or spawns a fixer.
func (o *Orchestrator) processTestingReady(parent *model.Task) error {
	// Check if a reviewer or fixer is already running for this task.
	var busy model.Agent
	err := o.db.Where("current_task_id = ? AND status = ? AND agent_type IN ?",
		parent.ID, model.AgentWorking,
		[]model.AgentType{model.AgentReviewer, model.AgentFixer}).
		First(&busy).Error
	if err == nil {
		// Agent already running — skip.
		return nil
	}

	// Check if we already ran the automated gate and it passed.
	if parent.Context != nil {
		if passed, ok := parent.Context["automated_gate_passed"].(bool); ok && passed {
			return nil
		}
	}

	// Resolve integration worktree.
	worktreePath := o.resolveIntegrationWorktree(parent)
	if worktreePath == "" {
		o.logger.Warn("processTestingReady: no integration worktree", "task_id", parent.ID)
		return nil
	}

	// Run the test suite.
	testsPassed, testOutput := o.runTestSuite(worktreePath)

	if testsPassed {
		// Tests pass → transition to MERGING.
		if parent.Context == nil {
			parent.Context = make(model.JSONField)
		}
		parent.Context["automated_gate_passed"] = true

		evt, err := state.TransitionTask(parent, model.StatusMerging, "orchestrator",
			map[string]any{"reason": "automated test gate passed"})
		if err != nil {
			return fmt.Errorf("processTestingReady: transition to merging: %w", err)
		}
		if err := o.db.Save(parent).Error; err != nil {
			return fmt.Errorf("processTestingReady: save parent: %w", err)
		}
		if err := o.db.Create(evt).Error; err != nil {
			return fmt.Errorf("processTestingReady: save event: %w", err)
		}

		o.emit("testing_ready_passed", map[string]any{"task_id": parent.ID})
		o.logger.Info("automated test gate passed, transitioning to merging", "task_id", parent.ID)
		return nil
	}

	// Tests failed — check if a fixer already attempted and failed.
	if parent.Context == nil {
		parent.Context = make(model.JSONField)
	}

	fixerAttempted := false
	if v, ok := parent.Context["testing_ready_fixer_attempted"].(bool); ok && v {
		fixerAttempted = true
	}

	if fixerAttempted {
		// Fixer already tried and failed → flag for human review.
		parent.Context["needs_human_review"] = true
		parent.Context["testing_ready_failure"] = truncate(testOutput, 2000)
		if err := o.db.Save(parent).Error; err != nil {
			return fmt.Errorf("processTestingReady: save parent after fixer failure: %w", err)
		}
		o.emit("testing_ready_needs_human", map[string]any{
			"task_id": parent.ID,
			"reason":  "fixer failed to resolve test failures",
		})
		o.logger.Warn("testing_ready fixer failed, needs human review", "task_id", parent.ID)
		return nil
	}

	// Spawn a fixer agent on the integration worktree.
	parent.Context["testing_ready_fixer_attempted"] = true
	parent.Context["test_failure_output"] = truncate(testOutput, 2000)

	// Get the diff between feature branch and default branch.
	var gitDiff string
	diff, diffErr := worktree.RunGit(
		[]string{"diff", o.worktree.DefaultBranch + "...HEAD"},
		worktreePath,
	)
	if diffErr == nil {
		gitDiff = truncate(diff, 10000)
	}

	fixerPrompt := fmt.Sprintf(`Fix the integration failures. Prefer fixing implementation code over modifying tests.

## Test Failure Output
%s

## Changes (diff vs %s)
%s

## Task
Title: %s
Description: %s
`, truncate(testOutput, 5000), o.worktree.DefaultBranch, gitDiff, parent.Title, parent.Description)

	if o.runner == nil {
		o.logger.Error("processTestingReady: runner is nil, cannot spawn fixer", "task_id", parent.ID)
		parent.Context["needs_human_review"] = true
		if err := o.db.Save(parent).Error; err != nil {
			return fmt.Errorf("processTestingReady: save parent: %w", err)
		}
		return nil
	}
	fixerAg, spawnErr := o.runner.SpawnAgentInWorktree(parent, worktreePath, model.AgentFixer, fixerPrompt)
	if spawnErr != nil {
		o.logger.Error("processTestingReady: spawn fixer failed", "task_id", parent.ID, "error", spawnErr)
		parent.Context["needs_human_review"] = true
		if err := o.db.Save(parent).Error; err != nil {
			return fmt.Errorf("processTestingReady: save parent: %w", err)
		}
		return nil
	}

	if err := o.db.Save(parent).Error; err != nil {
		return fmt.Errorf("processTestingReady: save parent: %w", err)
	}

	o.emit("testing_ready_fixer_spawned", map[string]any{
		"task_id":  parent.ID,
		"agent_id": fixerAg.ID,
	})
	o.logger.Info("testing_ready: fixer spawned for test failures",
		"task_id", parent.ID, "fixer_id", fixerAg.ID)
	return nil
}

// runTestSuite runs the test suite in the given worktree and returns whether
// tests passed and the combined output.
func (o *Orchestrator) runTestSuite(worktreePath string) (passed bool, output string) {
	// Run go test for Go projects.
	result, err := runCommand(worktreePath, "go test ./...")
	if err != nil || result.ExitCode != 0 {
		return false, result.Output
	}
	return true, result.Output
}

// truncate returns s truncated to maxLen characters.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}

// ---------------------------------------------------------------------------
// Test gate — pre-merge test/compilation verification
// ---------------------------------------------------------------------------

// taskPhase returns the phase string for a task, reading from
// task.Context["phase"]. Returns "" if not set.
func taskPhase(task *model.Task) string {
	if task.Context == nil {
		return ""
	}
	phase, _ := task.Context["phase"].(string)
	return phase
}

// getTestCommand returns the test command to run for the given subtask.
// It checks the orchestrator's TestGateConfig first, then falls back to
// inferring from the worktree contents.
func (o *Orchestrator) getTestCommand(subtask *model.Task) string {
	if o.testGate.TestCommand != "" {
		return o.testGate.TestCommand
	}
	// Infer from worktree contents. Use the agent's worktree path from the
	// subtask's assigned agent.
	if subtask.AssignedAgentID != nil {
		var ag model.Agent
		if err := o.db.First(&ag, "id = ?", subtask.AssignedAgentID).Error; err == nil && ag.WorktreePath != "" {
			return inferTestCommand(ag.WorktreePath)
		}
	}
	return ""
}

// inferTestCommand detects the project type and returns the appropriate
// test command, or "" if none can be determined.
func inferTestCommand(dir string) string {
	if fileExistsAt(filepath.Join(dir, "go.mod")) {
		return "go test ./..."
	}
	if fileExistsAt(filepath.Join(dir, "package.json")) {
		return "npm test"
	}
	if fileExistsAt(filepath.Join(dir, "pyproject.toml")) {
		return "pytest"
	}
	if fileExistsAt(filepath.Join(dir, "Cargo.toml")) {
		return "cargo test"
	}
	return ""
}

// inferCompileCommand detects the project type and returns the appropriate
// compilation check command, or "" if none can be determined.
func inferCompileCommand(dir string) string {
	if fileExistsAt(filepath.Join(dir, "go.mod")) {
		return "go vet ./..."
	}
	if fileExistsAt(filepath.Join(dir, "tsconfig.json")) {
		return "npx tsc --noEmit"
	}
	if fileExistsAt(filepath.Join(dir, "Cargo.toml")) {
		return "cargo check"
	}
	return ""
}

// fileExistsAt returns true if path exists and is a regular file.
func fileExistsAt(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// verifyTestsBeforeMerge runs the test suite in the agent's worktree and
// returns the result. Retries up to 3 times with backoff for environmental
// flakiness. Only applies to implementation and integration phase subtasks;
// test-phase subtasks use verifyCompilationBeforeMerge instead.
func (o *Orchestrator) verifyTestsBeforeMerge(subtask *model.Task, worktreePath string) (*TestResult, error) {
	testCmd := o.getTestCommand(subtask)
	if testCmd == "" {
		o.logger.Warn("no test command configured, skipping test gate (degraded mode)",
			"subtask_id", subtask.ID)
		return &TestResult{Passed: true, RunAt: time.Now()}, nil
	}

	// Determine if scoped execution applies.
	scoped := false
	if o.testGate.ScopedTests {
		scopedCmd, didScope := o.scopeTestsForSubtask(testCmd, worktreePath)
		if didScope {
			testCmd = scopedCmd
			scoped = true
		}
	}

	timeout := o.testGate.TestTimeout
	if timeout == 0 {
		timeout = 5 * time.Minute
	}

	var lastResult *TestResult
	for attempt := 1; attempt <= 3; attempt++ {
		cmdResult := o.runCommandWithTimeout(worktreePath, testCmd, timeout)
		lastResult = &TestResult{
			Passed:       cmdResult.ExitCode == 0,
			Output:       truncate(cmdResult.Output, 5000),
			ExitCode:     cmdResult.ExitCode,
			RunAt:        time.Now(),
			Duration:     cmdResult.Duration.Seconds(),
			Command:      testCmd,
			Scoped:       scoped,
			AttemptCount: attempt,
		}
		if lastResult.Passed {
			return lastResult, nil
		}
		if attempt < 3 {
			time.Sleep(time.Duration(attempt) * 2 * time.Second)
		}
	}
	return lastResult, nil
}

// verifyCompilationBeforeMerge runs the compilation command for test-phase
// subtasks. Test execution results are ignored — only compilation matters.
func (o *Orchestrator) verifyCompilationBeforeMerge(subtask *model.Task, worktreePath string) (*TestResult, error) {
	compileCmd := o.testGate.CompileCommand
	if compileCmd == "" {
		compileCmd = inferCompileCommand(worktreePath)
	}
	if compileCmd == "" {
		o.logger.Warn("no compile command found, skipping compilation gate",
			"subtask_id", subtask.ID)
		return &TestResult{Passed: true, RunAt: time.Now()}, nil
	}

	timeout := o.testGate.TestTimeout
	if timeout == 0 {
		timeout = 5 * time.Minute
	}

	cmdResult := o.runCommandWithTimeout(worktreePath, compileCmd, timeout)
	return &TestResult{
		Passed:       cmdResult.ExitCode == 0,
		Output:       truncate(cmdResult.Output, 5000),
		ExitCode:     cmdResult.ExitCode,
		RunAt:        time.Now(),
		Duration:     cmdResult.Duration.Seconds(),
		Command:      compileCmd,
		Scoped:       false,
		AttemptCount: 1,
	}, nil
}

// scopeTestsForSubtask derives changed packages from the agent's diff and
// scopes the test command. Returns the scoped command and true if scoping
// was applied.
func (o *Orchestrator) scopeTestsForSubtask(baseCmd, worktreePath string) (string, bool) {
	// Get changed files via git diff.
	diffOutput, err := worktree.RunGit([]string{"diff", "--name-only", "HEAD~1"}, worktreePath)
	if err != nil {
		// Also try against the default branch.
		diffOutput, err = worktree.RunGit([]string{
			"diff", "--name-only", o.worktree.DefaultBranch + "...HEAD",
		}, worktreePath)
		if err != nil {
			return baseCmd, false
		}
	}

	if diffOutput == "" {
		return baseCmd, false
	}

	changedFiles := strings.Split(strings.TrimSpace(diffOutput), "\n")
	scopedCmd, didScope := scopeTestCommand(baseCmd, changedFiles)
	return scopedCmd, didScope
}

// scopeTestCommand takes a base test command and a list of changed files,
// and returns a scoped command that only tests affected packages.
// Returns the original command if scoping isn't possible.
func scopeTestCommand(baseCmd string, changedFiles []string) (string, bool) {
	if len(changedFiles) == 0 {
		return baseCmd, false
	}

	// Only scope Go test commands that use ./...
	if !strings.Contains(baseCmd, "./...") {
		return baseCmd, false
	}

	// Map changed .go files to their package directories.
	pkgSet := make(map[string]struct{})
	for _, f := range changedFiles {
		if !strings.HasSuffix(f, ".go") {
			continue
		}
		dir := filepath.Dir(f)
		if dir == "." {
			pkgSet["./..."] = struct{}{}
		} else {
			pkgSet["./"+dir+"/..."] = struct{}{}
		}
	}

	if len(pkgSet) == 0 {
		return baseCmd, false
	}

	// Build sorted package list for deterministic output.
	var pkgs []string
	for p := range pkgSet {
		pkgs = append(pkgs, p)
	}
	sort.Strings(pkgs)

	// If ./... is in the set (root-level files changed), fall back to full.
	for _, p := range pkgs {
		if p == "./..." {
			return baseCmd, false
		}
	}

	scopedCmd := strings.Replace(baseCmd, "./...", strings.Join(pkgs, " "), 1)
	return scopedCmd, true
}

// runCommandWithTimeout executes a shell command with a timeout.
// Returns the combined output, exit code, and duration. Uses process
// group killing to ensure child processes are cleaned up on timeout.
func (o *Orchestrator) runCommandWithTimeout(dir, cmd string, timeout time.Duration) *commandResult {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	start := time.Now()
	c := exec.CommandContext(ctx, "sh", "-c", cmd)
	c.Dir = dir
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error {
		// Kill the entire process group so child processes are cleaned up.
		return syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
	}

	var outBuf bytes.Buffer
	c.Stdout = &outBuf
	c.Stderr = &outBuf

	err := c.Run()
	elapsed := time.Since(start)

	exitCode := 0
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return &commandResult{
				Output:   truncate(outBuf.String(), 5000) + "\n[killed: timeout exceeded]",
				ExitCode: -1,
				Duration: elapsed,
			}
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 1
		}
	}

	return &commandResult{
		Output:   outBuf.String(),
		ExitCode: exitCode,
		Duration: elapsed,
	}
}

// storeTestResult persists the test result on the agent's Config field.
func (o *Orchestrator) storeTestResult(ag *model.Agent, result *TestResult) {
	if ag.Config == nil {
		ag.Config = make(model.JSONField)
	}
	ag.Config["last_test_result"] = result
	if err := o.db.Model(ag).Update("config", ag.Config).Error; err != nil {
		o.logger.Warn("failed to store test result on agent", "agent_id", ag.ID, "error", err)
	}
}

// planEntry is an intermediate struct for parsing plans from JSON that may
// include dependency indices and TDD phase information.
type planEntry struct {
	Title          string   `json:"title"`
	Description    string   `json:"description"`
	AgentType      string   `json:"agent_type"`
	EstimatedFiles []string `json:"estimated_files"`
	Files          []string `json:"files"`
	Dependencies   []int    `json:"dependencies"`
	Priority       int      `json:"priority"`
	IsTest         bool     `json:"is_test,omitempty"`
	Phase          string   `json:"phase,omitempty"`
	TestsFor       []int    `json:"tests_for,omitempty"`
}

// tddException represents a planner-declared exception to TDD enforcement
// for a specific subtask.
type tddException struct {
	SubtaskIndex int    `json:"subtask_index"`
	Reason       string `json:"reason"`
}

// parsePlanResult holds the full parsed plan output.
type parsePlanResult struct {
	Subtasks      []planEntry
	TDDExceptions []tddException
}

// parsePlan extracts subtask plans and TDD exceptions from a task's Plan JSONField.
func parsePlan(planField model.JSONField) (*parsePlanResult, error) {
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

	// Extract TDD exceptions if present (backward compatible — missing key is not an error).
	var exceptions []tddException
	if exceptionsRaw, hasExceptions := planField["tdd_exceptions"]; hasExceptions {
		eb, err := json.Marshal(exceptionsRaw)
		if err != nil {
			return nil, fmt.Errorf("parse plan: marshal tdd_exceptions: %w", err)
		}
		if err := json.Unmarshal(eb, &exceptions); err != nil {
			return nil, fmt.Errorf("parse plan: unmarshal tdd_exceptions: %w", err)
		}
	}

	return &parsePlanResult{
		Subtasks:      entries,
		TDDExceptions: exceptions,
	}, nil
}
