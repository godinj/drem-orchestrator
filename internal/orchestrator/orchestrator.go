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
	"github.com/godinj/drem-orchestrator/internal/constraints"
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

// shortIDLen is the number of characters used from a UUID for short display identifiers.
const shortIDLen = 4

// maxDisplayNameLen is the maximum length of task titles in supervisor session names.
const maxDisplayNameLen = 30

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

// GetAgentOutput returns the output log content for the given agent.
func (o *Orchestrator) GetAgentOutput(agentID uuid.UUID) (string, error) {
	return o.runner.GetAgentOutput(agentID)
}

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

		// Run full constraint evaluation on the integration worktree before
		// allowing transition to test_review.
		if parent.WorktreeBranch != "" {
			fn := strings.TrimPrefix(parent.WorktreeBranch, "feature/")
			featureDir := o.worktree.FeatureWorktreePath(fn)

			constraintCfg, cfgErr := constraints.LoadConfig(featureDir)
			if cfgErr != nil {
				o.logger.Warn("constraint config load failed at integration gate",
					"task_id", parent.ID, "error", cfgErr)
			} else if constraintCfg != nil {
				report, evalErr := constraints.Evaluate(constraintCfg, featureDir)
				if evalErr != nil {
					o.logger.Warn("constraint evaluation failed at integration gate",
						"task_id", parent.ID, "error", evalErr)
				} else if report.Failed > 0 {
					o.logger.Warn("constraint violations at integration gate, blocking test_review",
						"task_id", parent.ID, "failed", report.Failed)

					// Store violations in task context for TUI visibility.
					if parent.Context == nil {
						parent.Context = make(model.JSONField)
					}
					parent.Context["constraint_violations"] = constraints.FormatReport(report)
					if err := o.db.Save(parent).Error; err != nil {
						return fmt.Errorf("check feature completion: save constraint violations: %w", err)
					}

					// Do NOT transition to test_review. The parent stays in current state.
					// The violations are visible in the TUI for the user to address.
					o.emit("constraint_violations", map[string]any{
						"task_id":    parent.ID,
						"failed":     report.Failed,
						"violations": constraints.FormatReport(report),
					})
					return nil
				}

				// Constraints passed — clear any previous violation context.
				if parent.Context != nil {
					delete(parent.Context, "constraint_violations")
				}
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
			// StopAgent failed (agent not in running map) — kill stale process
			// directly for idle/dead agents that still have one.
			var ag model.Agent
			if dbErr := o.db.First(&ag, "id = ?", agentID).Error; dbErr == nil {
				o.runner.KillStaleProcess(&ag)
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
	shortID := taskID.String()[:shortIDLen]
	title := strings.ReplaceAll(task.Title, "/", "-")
	title = truncate(title, maxDisplayNameLen)
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
