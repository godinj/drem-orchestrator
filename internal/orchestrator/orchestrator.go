// Package orchestrator implements the main tick loop and task scheduling for
// the Drem Orchestrator. It drives tasks through their lifecycle, spawns
// planner and coder agents, handles plan approval/rejection, manages merges,
// and exposes public methods for TUI interaction.
package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/agent"
	"github.com/godinj/drem-orchestrator/internal/bugreport"
	"github.com/godinj/drem-orchestrator/internal/eventbus"
	"github.com/godinj/drem-orchestrator/internal/memory"
	"github.com/godinj/drem-orchestrator/internal/metrics"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/state"
	"github.com/godinj/drem-orchestrator/internal/supervisor"
	"github.com/godinj/drem-orchestrator/internal/worktree"
)

const (
	// MaxPlannerRetries is the number of times the orchestrator will retry a
	// planner agent before failing the task.
	MaxPlannerRetries = 3
	// MaxTotalPlannerSpawns is the hard cap on total planner agents spawned
	// for a single task across ALL planning cycles (replans, retries, etc.).
	// This prevents multiplicative blowup from retry_count resets and
	// reconciliation bypasses. 6 allows 2 full planning cycles with room
	// for 1-2 transient failures.
	MaxTotalPlannerSpawns    = 6
	defaultContextFixerPct   = 85
	fixerEscalatePct         = 80 // fixer agents at this % → stop and escalate to human
	classifierContextStopPct = 70 // classifier agents at this % → stop and park for triage
	maxTestOutputLen         = 5000
	maxGitDiffLen            = 10000
	maxCmdOutputLen          = 5000
	maxTestFailureLen        = 2000
	maxErrorSnippetLen       = 500
	maxSlugLen               = 40
	maxBuildRetries          = 3
	reconcileInterval        = 10               // consistency audit frequency (every N ticks; 0 = disable)
	shortIDLen               = 4                // UUID characters for short display IDs
	maxDisplayNameLen        = 30               // max task title length in supervisor session names
	agentSpawnGracePeriod    = 60 * time.Second // how long to wait after agent spawn before treating it as stuck
)

// slugRegexp matches non-alphanumeric characters for feature name derivation.
var slugRegexp = regexp.MustCompile(`[^a-z0-9]+`)

// Event is sent from the orchestrator to the TUI via a channel.
type Event struct {
	Type    string
	Payload any
}

// TestGateConfig holds configuration for the pre-merge test gate.
// These values are typically loaded from drem.toml or CLAUDE.md.
type TestGateConfig struct {
	TestCommand    string        `toml:"test_command"`    // e.g. "go test ./..."
	CompileCommand string        `toml:"compile_command"` // e.g. "go vet ./..."
	ScopedTests    bool          `toml:"scoped_tests"`    // default true — scope tests to changed packages
	TestTimeout    time.Duration `toml:"test_timeout"`    // default 5m
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
	Output       string    `json:"output"` // truncated to last 5000 chars
	ExitCode     int       `json:"exit_code"`
	RunAt        time.Time `json:"run_at"`
	Duration     float64   `json:"duration_seconds"`
	Command      string    `json:"command"`
	Scoped       bool      `json:"scoped"` // true if ran scoped tests
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
	db                          *gorm.DB
	dbPath                      string
	runner                      *agent.Runner
	worktree                    *worktree.Manager
	merger                      mergerClient
	memory                      *memory.Manager
	bus                         *eventbus.Bus          // nil disables C-Suite event emission
	supervisor                  *supervisor.Supervisor // nil disables LLM-powered decisions
	bugreport                   *bugreport.Service     // nil disables bug report ingestion
	bugreportDir                string                 // path to .drem/bug-reports/ drop directory
	testGate                    TestGateConfig
	projectID                   uuid.UUID
	events                      chan<- Event
	tick                        time.Duration
	stale                       time.Duration
	tickCount                   int
	contextWarnPct              int
	contextStopPct              int
	contextFixerPct             int // percentage: spawn fixer instead of failing
	subtaskRecovery             SubtaskRecoveryPolicy
	skipConstraintGate          bool                 // bypass constraint gate evaluation
	interactiveSupervisorConfig model.AgentCLIConfig // model/effort for interactive supervisor sessions
	metrics                     *metrics.Store       // nil-safe: callers nil-check before use
	experimentScheduler         *ExperimentScheduler // experiment-aware scheduling
	logger                      *slog.Logger
}

// New creates an Orchestrator. The supervisor parameter is optional — pass nil
// to disable LLM-powered decision points and fall back to existing behavior.
// The bugSvc parameter is optional — pass nil to disable bug report ingestion.
// maxConcurrent is used for experiment-aware scheduling.
func New(
	db *gorm.DB,
	dbPath string,
	runner *agent.Runner,
	wt *worktree.Manager,
	merger mergerClient,
	mem *memory.Manager,
	sup *supervisor.Supervisor,
	projectID uuid.UUID,
	events chan<- Event,
	tickInterval time.Duration,
	staleTimeout time.Duration,
	contextWarnPct int,
	contextStopPct int,
	bugSvc *bugreport.Service,
	bugDir string,
	contextFixerPct ...int,
) *Orchestrator {
	fixerPct := defaultContextFixerPct
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
		bugreport:       bugSvc,
		bugreportDir:    bugDir,
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

// NewWithExperimentScheduling creates an Orchestrator with experiment-aware
// scheduling enabled. When experiments are active, normal tasks are paused
// and the agent pool is partitioned across experiment variants.
func NewWithExperimentScheduling(
	db *gorm.DB,
	dbPath string,
	runner *agent.Runner,
	wt *worktree.Manager,
	merger mergerClient,
	mem *memory.Manager,
	sup *supervisor.Supervisor,
	projectID uuid.UUID,
	events chan<- Event,
	tickInterval time.Duration,
	staleTimeout time.Duration,
	contextWarnPct int,
	contextStopPct int,
	bugSvc *bugreport.Service,
	bugDir string,
	maxConcurrent int,
	contextFixerPct ...int,
) *Orchestrator {
	orch := New(db, dbPath, runner, wt, merger, mem, sup, projectID, events, tickInterval, staleTimeout, contextWarnPct, contextStopPct, bugSvc, bugDir, contextFixerPct...)
	orch.experimentScheduler = NewExperimentScheduler(db, maxConcurrent)
	return orch
}

// SetTestGateConfig updates the test gate configuration. Call this after
// loading configuration from drem.toml to override the defaults.
func (o *Orchestrator) SetTestGateConfig(cfg TestGateConfig) {
	if cfg.TestTimeout == 0 {
		cfg.TestTimeout = 5 * time.Minute
	}
	o.testGate = cfg
}

// SetInteractiveSupervisorConfig sets the model/effort flags used when
// spawning interactive supervisor sessions via tmux.
func (o *Orchestrator) SetInteractiveSupervisorConfig(cfg model.AgentCLIConfig) {
	o.interactiveSupervisorConfig = cfg
}

// SetSkipConstraintGate disables the constraint gate, allowing tasks to
// transition without passing constitution checks. Use temporarily when
// master has pre-existing violations that block all merges.
func (o *Orchestrator) SetSkipConstraintGate(skip bool) {
	o.skipConstraintGate = skip
	if skip {
		o.logger.Warn("constraint gate BYPASSED — tasks will skip constitution checks")
	}
}

// SetEventBus connects the orchestrator to the C-Suite event bus. When set,
// every task status transition and agent status change is published as an event
// with delivery records for all known C-Suite agents. Pass nil to disable.
func (o *Orchestrator) SetEventBus(bus *eventbus.Bus) {
	o.bus = bus
}

// Run starts the main loop. It blocks until ctx is cancelled.
func (o *Orchestrator) Run(ctx context.Context) {
	// Startup cleanup: clear stale agent assignments left from previous runs.
	o.cleanupOrphanedAssignments()

	// Generate repo map for the default branch worktree at startup.
	go o.worktree.GenerateRepoMapForMain()

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

// ingestBugReports scans the bug report drop directory and inserts valid
// reports into the database. After ingestion, newly inserted reports are
// classified and promoted into tasks. Errors are logged and do not halt the tick.
// doTick is a single iteration of the orchestrator loop.
func (o *Orchestrator) doTick(ctx context.Context) {
	_ = ctx
	// 0. Ingest any pending bug reports from the drop directory.
	o.ingestBugReports()
	// 0b. Process CLASSIFYING tasks -> spawn classifier agents.
	o.processClassifyingTasks()
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
		task := &inProgressTasks[i]
		if task.Category.IsQuickFix() {
			// Quick fix tasks are top-level with no subtasks.
			// Agent completion is handled by processAgentResult (step 2).
			if task.AssignedAgentID == nil {
				// Check if this is an empty-work retry (agent completed
				// without commits and onAgentEmptyWork cleared the agent
				// for respawn). In that case, respawn a new agent instead
				// of transitioning to merging.
				needsRetry := false
				if task.Context != nil {
					if _, ok := task.Context["empty_work"]; ok {
						needsRetry = true
					}
				}
				if needsRetry {
					if err := o.respawnQuickFixAgent(task); err != nil {
						o.logger.Error("quickfix respawn", "task_id", task.ID, "error", err)
					}
				} else {
					if err := o.transitionQuickFixToMerging(task); err != nil {
						o.logger.Error("quickfix to merging", "task_id", task.ID, "error", err)
					}
				}
			}
			continue
		}
		if err := o.scheduleSubtasks(task); err != nil {
			o.logger.Error("schedule subtasks", "task_id", task.ID, "error", err)
		}
		if err := o.checkFeatureCompletion(task); err != nil {
			o.logger.Error("check feature completion", "task_id", task.ID, "error", err)
		}
	}

	// 4b. Catch-all: dispatch backlog subtasks for non-terminal parents with pending subtasks.
	o.dispatchPendingSubtasks()

	// 4c. Process TESTING_READY parent tasks (automated gate).
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
	o.dispatchMerges()

	// 5b. Handle NEEDS_CLARIFICATION tasks.
	// Human gate — no automated processing. The TUI handles user input
	// via HandleClarificationAnswer(). Nothing to do here; the case is
	// present so the orchestrator does not log warnings about unhandled statuses.

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

func (o *Orchestrator) recoverStuckAgents() {
	var agents []model.Agent
	if err := o.db.Where("project_id = ? AND status = ?", o.projectID, model.AgentWorking).
		Find(&agents).Error; err != nil {
		o.logger.Error("recover stuck agents: query", "error", err)
		return
	}

	now := time.Now()
	for _, ag := range agents {
		// OpenCode agents don't use .claude/agent-idle — they're monitored
		// by process exit. Skip to avoid false idle detection from stale files.
		if ag.Provider == string(model.ProviderOpenCode) {
			continue
		}

		// Grace period: skip agents that were recently spawned. This prevents
		// a race where a stale idle signal file (from a previous agent in the
		// same worktree) causes a freshly-spawned agent to be immediately
		// treated as stuck before it has time to begin work.
		if ag.CreatedAt.After(now.Add(-agentSpawnGracePeriod)) {
			continue
		}

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

func (o *Orchestrator) processTestWriting(parent *model.Task) error {
	if parent.Context == nil {
		parent.Context = make(model.JSONField)
	}
	if _, checked := parent.Context["baseline_tests_checked"]; !checked {
		if testCmd := o.getTestCommand(parent); testCmd != "" {
			featureName := strings.TrimPrefix(parent.WorktreeBranch, "feature/")
			featureDir := o.worktree.FeatureWorktreePath(featureName)
			result, runErr := runCommand(featureDir, testCmd)
			parent.Context["baseline_tests_checked"] = true
			if runErr != nil || result.ExitCode != 0 {
				parent.Context["baseline_tests_failed"] = true
				parent.Context["baseline_test_output"] = truncate(result.Output, maxTestOutputLen)
				o.logger.Warn("baseline tests fail on integration branch", "task_id", parent.ID, "exit_code", result.ExitCode)
			}
			if err := o.db.Save(parent).Error; err != nil {
				return fmt.Errorf("process test writing: save baseline check: %w", err)
			}
			if runErr != nil || result.ExitCode != 0 {
				return nil
			}
		}
	}

	if failed, ok := parent.Context["baseline_tests_failed"].(bool); !ok || !failed {
		if err := o.scheduleSubtasks(parent, "test"); err != nil {
			return fmt.Errorf("process test writing: schedule: %w", err)
		}
	}

	var testSubtasks []model.Task
	if err := o.db.Where("parent_task_id = ? AND phase = ?", parent.ID, "test").
		Find(&testSubtasks).Error; err != nil {
		return fmt.Errorf("process test writing: query test subtasks: %w", err)
	}

	switch o.subtaskRecovery.Evaluate(parent, len(testSubtasks)) {
	case RecoveryReplan:
		// Cap test_writing replans at 1. After that, flag for human review
		// instead of spinning through more planning cycles.
		replanCount := 0
		if v, ok := parent.Context["test_replan_count"].(float64); ok {
			replanCount = int(v)
		}
		if replanCount >= 1 {
			parent.Context["needs_human_review"] = true
			parent.Context["review_reason"] = "repeated empty test subtasks after replan"
			return o.failTask(parent, "repeated empty test subtasks — needs human review")
		}
		parent.Context["test_replan_count"] = float64(replanCount + 1)

		// Clear the plan so processPlanning spawns a new planner instead of
		// auto-advancing the same stale plan to PLAN_REVIEW.
		replanMsg := "Previous plan produced no test-phase subtasks. Re-plan with explicit test subtasks for each implementation subtask."
		parent.Plan = nil
		parent.PlanFeedback = replanMsg
		parent.AssignedAgentID = nil
		parent.Context["replan_directive"] = replanMsg
		// NOTE: Do NOT reset retry_count on replan. The global
		// total_planner_spawns cap prevents runaway spawning, and
		// per-cycle retries use the existing retry_count which accumulates.

		// Detach old subtasks to prevent duplicates and stale data.
		var oldSubtasks []model.Task
		o.db.Where("parent_task_id = ?", parent.ID).Find(&oldSubtasks)
		for i := range oldSubtasks {
			oldSubtasks[i].ParentTaskID = nil
			o.db.Save(&oldSubtasks[i])
		}
		if len(oldSubtasks) > 0 {
			o.logger.Info("detached old subtasks for replan",
				"task_id", parent.ID, "count", len(oldSubtasks))
		}

		if evt, err := state.TransitionTask(parent, model.StatusPlanning, "orchestrator", map[string]any{"reason": "empty test subtasks, replanning"}); err != nil {
			return fmt.Errorf("process test writing: replan transition: %w", err)
		} else if saveErr := o.db.Save(parent).Error; saveErr != nil {
			return fmt.Errorf("process test writing: save replan: %w", saveErr)
		} else {
			_ = o.db.Create(evt).Error
		}
		o.emit("task_replan", map[string]any{"task_id": parent.ID})
		return nil
	case RecoveryFail:
		return o.failTask(parent, fmt.Sprintf("no test subtasks after %d recovery attempts", defaultMaxEmptyChecks))
	default:
		if err := o.db.Save(parent).Error; err != nil {
			return fmt.Errorf("process test writing: save recovery state: %w", err)
		}
	}
	allTerminal, anyFailed, allDone := true, false, true
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

		// Run full constraint evaluation on the integration worktree before
		// allowing transition to test_review, with retry/backoff gating.
		if parent.WorktreeBranch != "" && !o.skipConstraintGate {
			blocked, err := o.evaluateConstraintGate(parent)
			if err != nil {
				return err
			}
			if blocked {
				return nil
			}
		}

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
	o.publishTaskTransition(task.ID.String(), evt.OldValue, evt.NewValue, reason)
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
	if len(slug) > maxSlugLen {
		slug = slug[:maxSlugLen]
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
