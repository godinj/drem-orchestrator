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
	"github.com/godinj/drem-orchestrator/internal/container"
	"github.com/godinj/drem-orchestrator/internal/eventbus"
	"github.com/godinj/drem-orchestrator/internal/gitexec"
	"github.com/godinj/drem-orchestrator/internal/gitref"
	"github.com/godinj/drem-orchestrator/internal/memory"
	"github.com/godinj/drem-orchestrator/internal/metrics"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/state"
	"github.com/godinj/drem-orchestrator/internal/supervisor"
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

// defaultTestGateConfig returns a TestGateConfig with sensible defaults.
func defaultTestGateConfig() TestGateConfig {
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
	worktree                    WorktreeManager
	mergeDispatcher             MergeDispatcher // optional override for tests; nil → dispatchMerge
	memory                      *memory.Manager
	bus                         *eventbus.Bus          // nil disables C-Suite event emission
	supervisor                  *supervisor.Supervisor // nil disables LLM-powered decisions
	bugreport                   *bugreport.Service     // nil disables bug report ingestion
	bugreportDir                string                 // path to .drem/bug-reports/ drop directory
	testGate                    TestGateConfig
	projectID                   uuid.UUID
	projectName                 string // human-readable label; pairs with projectID on worker labels (see plans/dual-label-worker-spawn.md)
	events                      chan<- Event
	tick                        time.Duration
	stale                       time.Duration
	tickCount                   int
	contextWarnPct              int
	contextStopPct              int
	contextFixerPct             int // percentage: spawn fixer instead of failing
	subtaskRecovery             SubtaskRecoveryPolicy
	skipConstraintGate          bool                            // bypass constraint gate evaluation
	interactiveSupervisorConfig model.AgentCLIConfig            // model/effort for interactive supervisor sessions
	directClassifierCfg         *agent.DirectClassifierConfig   // nil means use OpenCode subprocess path
	classifierContainerURL      string                          // empty means use inline direct path (rollback-safe)
	classifierContainerToken    string                          // Bearer token for POST /classify
	plannerContainerURL         string                          // POST /plan URL on drem-planner; empty falls back to legacy runner path
	plannerContainerToken       string                          // Bearer token for POST /plan
	directPlanReviewerCfg       *agent.DirectPlanReviewerConfig // nil means use subprocess path for plan review
	directPrepCfg               *agent.DirectPrepConfig         // nil means use OpenCode subprocess path
	directToolAgentCfg          *agent.DirectToolAgentConfig    // nil means use subprocess path for coder/reviewer/fixer
	endpointHealth              *agent.EndpointHealthChecker    // nil means no health checking
	metrics                     *metrics.Store                  // nil-safe: callers nil-check before use
	experimentScheduler         *ExperimentScheduler            // experiment-aware scheduling
	lifecycle                   lifecycleEngine                 // owns task lifecycle advancement; nil keeps legacy tests working
	logger                      *slog.Logger

	// Spawner is the RPC client for the spawner service that owns the Docker
	// socket. When set, task dispatch uses container-based workers via
	// spawner.Client instead of host-side worktree processes. When nil, the
	// orchestrator falls back to the legacy worktree/tmux dispatch path.
	Spawner WorkerSpawner

	// Runtime is the container runtime used for Docker event subscription.
	// Orchestrator subscribes once on Run, filtering events by
	// drem.project=<project>, and dispatches die/OOM events to handleWorkerDeath.
	// When nil, no Docker event subscription is set up.
	Runtime container.Runtime

	// GitrefRegistry is the database-backed branch reference tracker.
	// Every spawnCoder/Reviewer/Fixer/Supervisor call registers the worker's
	// branch here so merge and destroy paths can transition it to merged/
	// deleted without touching the host filesystem.
	GitrefRegistry *gitref.Registry

	// orchURL is the in-cluster base URL that spawned worker containers
	// (most notably the per-task merger) use to POST results back to this
	// orchestrator's /internal/logs endpoint. Populated from the
	// DREM_ORCH_URL env var at startup via SetInternalEndpoints. Empty
	// means "no in-cluster URL" — workers can still run, but merge result
	// ingestion via HTTP will not happen.
	orchURL string

	// agentmonToken is the shared bearer token that agentmon (and any
	// merger container POSTing merge_result records) must present on
	// /internal/logs. Mirrors DREM_AGENTMON_TOKEN from the per-project
	// compose file. Populated via SetInternalEndpoints.
	agentmonToken string

	// sightingProbe lets the stuck-agent reconciler ask "has agentmon's
	// subscription ever observed this container?" before declaring a
	// container-mode agent dead. See plans/agentmon-observability.md.
	// Nil is safe (host-mode legacy) and preserves the original
	// kill-on-stale-DB behaviour. When set, a false return from
	// HasSeen short-circuits the kill so v12–v14-style false positives
	// do not fire when agentmon itself is silently misconfigured.
	sightingProbe ContainerSightingProbe
}

// ContainerSightingProbe is the hook the orchestrator's stuck-agent
// reconciler consults before killing a container-mode agent. The
// production implementation is an *agentmon.DockerSource via its
// HasSeen method; tests pass a fake. The interface lives in this
// package (not agentmon) so adding the hook does not introduce a
// new internal/* import to internal/orchestrator — constitution
// constraints pin that package's internal imports at 17, shrink-only.
type ContainerSightingProbe interface {
	// HasSeen returns true if the subscription backing this probe has
	// observed any lifecycle event for containerID since Run began,
	// OR if the subscription has demonstrable recent traffic (the
	// agentmon-is-alive fallback). A false return means the probe
	// is unable to confirm the container exists through its live
	// event stream — the exact situation that caused the v12–v14
	// false-positive kills.
	HasSeen(containerID string) bool
}

// SetContainerSightingProbe installs the agentmon HasSeen probe that
// reconcileStuckAgents consults before killing a container-mode agent.
// Pass nil (the default) to keep the legacy behaviour where the
// reconciler trusts the spawner's ListWorkers + DB heartbeats alone.
//
// The expected production wiring builds the probe from the agentmon
// DockerSource that the orch container spawns via SetRuntime, so
// orch and agentmon share a single event subscription.
func (o *Orchestrator) SetContainerSightingProbe(p ContainerSightingProbe) {
	o.sightingProbe = p
}

// New creates an Orchestrator. sup/bugSvc are optional (nil disables).
// maxConcurrent is used for experiment-aware scheduling. projectName is the
// human-readable project label; it pairs with projectID on worker labels so
// drem.project/drem.project_id stay in lockstep (plans/dual-label-worker-spawn.md).
// The merger parameter that previously preceded mem is retired: the
// feature-into-main merge path runs via dispatchMerge against the merger
// container (merge_dispatch.go); tests override via SetMergeDispatcher.
func New(
	db *gorm.DB,
	dbPath string,
	runner *agent.Runner,
	wt WorktreeManager,
	mem *memory.Manager,
	sup *supervisor.Supervisor,
	projectID uuid.UUID,
	projectName string,
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
	o := &Orchestrator{
		db:              db,
		dbPath:          dbPath,
		runner:          runner,
		worktree:        wt,
		memory:          mem,
		supervisor:      sup,
		bugreport:       bugSvc,
		bugreportDir:    bugDir,
		testGate:        defaultTestGateConfig(),
		projectID:       projectID,
		projectName:     projectName,
		events:          events,
		tick:            tickInterval,
		stale:           staleTimeout,
		contextWarnPct:  contextWarnPct,
		contextStopPct:  contextStopPct,
		contextFixerPct: fixerPct,
		logger:          slog.Default().With("component", "orchestrator", "project_id", projectID),
	}
	o.lifecycle = newOrchestratorLifecycleEngine(o)
	return o
}

// SetTestGateConfig updates the test gate configuration. Call this after
// loading configuration from drem.toml to override the defaults.
func (o *Orchestrator) SetTestGateConfig(cfg TestGateConfig) {
	if cfg.TestTimeout == 0 {
		cfg.TestTimeout = 5 * time.Minute
	}
	o.testGate = cfg
}

// ApplyTestCommandInference populates TestGateConfig.TestCommand from
// inferTestCommand(projectDir) when the currently-configured value is
// empty. projectDir is typically the main worktree path (the checkout of
// the default branch) so well-known build-system files (go.mod,
// package.json, pyproject.toml, Cargo.toml) are discoverable.
//
// This is the startup-time complement to Bug H's fail-close guard in
// dispatchMerge: for Go projects that leave test_command unset in
// drem.toml, inference supplies "go test ./..." before the first merge
// attempt so the guard does not trip. For non-Go projects where
// inference returns empty, the guard takes over and refuses to spawn
// the merger with a first-class failure_reason.
//
// Safe to call when the project dir is empty or unknown — the inference
// helper returns "" and TestCommand stays unset. Safe to call multiple
// times — later calls with a non-empty config value win.
// See plans/bug-h-merger-crash-on-v17-advance.md (Option A).
func (o *Orchestrator) ApplyTestCommandInference(projectDir string) {
	if o.testGate.TestCommand != "" || projectDir == "" {
		return
	}
	inferred := inferTestCommand(projectDir)
	if inferred == "" {
		o.logger.Info("test command inference found no known project markers",
			"project_dir", projectDir,
			"hint", "set test_command in drem.toml if this project has tests")
		return
	}
	o.testGate.TestCommand = inferred
	o.logger.Info("test command inferred from project markers",
		"project_dir", projectDir, "test_command", inferred)
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

// SetDirectClassifierConfig enables the direct SGLang API classifier path.
// When set, CLASSIFYING tasks are handled by calling the SGLang API directly
// instead of spawning an OpenCode subprocess. Pass nil to disable.
func (o *Orchestrator) SetDirectClassifierConfig(cfg *agent.DirectClassifierConfig) {
	o.directClassifierCfg = cfg
	if cfg != nil {
		o.logger.Info("direct classifier enabled", "endpoint", cfg.Endpoint, "model", cfg.Model)
	}
}

// SetClassifierContainerEndpoint enables the warm drem-classifier container
// path. When url is non-empty the orchestrator POSTs classify jobs to it
// instead of calling agent.RunDirectClassifier inline; this isolates
// classifier failure modes from the orch process (see plans/warm-direct-
// classifier.md). token is forwarded as "Authorization: Bearer <token>"
// and must match DREM_AGENTMON_TOKEN on the classifier container. Passing
// an empty url falls back to the inline path for rollback safety.
func (o *Orchestrator) SetClassifierContainerEndpoint(url, token string) {
	o.classifierContainerURL = url
	o.classifierContainerToken = token
	if url != "" {
		o.logger.Info("classifier container enabled", "endpoint", url, "auth", token != "")
	}
}

// SetPlannerContainerEndpoint enables the warm drem-planner container path.
// When url is non-empty the orchestrator POSTs plan jobs to it instead of
// spawning per-task planner containers; see plans/warm-planner-pivot.md.
// token is forwarded as "Authorization: Bearer <token>" and must match the
// planner container's DREM_AGENTMON_TOKEN. Passing empty URL falls back to
// the legacy runner.SpawnAgent path for rollback safety.
func (o *Orchestrator) SetPlannerContainerEndpoint(url, token string) {
	o.plannerContainerURL = url
	o.plannerContainerToken = token
	if url != "" {
		o.logger.Info("planner container enabled", "endpoint", url, "auth", token != "")
	}
}

// SetDirectPlanReviewerConfig enables the direct SGLang plan reviewer path for PLAN_REVIEW tasks. Pass nil to disable.
func (o *Orchestrator) SetDirectPlanReviewerConfig(cfg *agent.DirectPlanReviewerConfig) {
	o.directPlanReviewerCfg = cfg
	if cfg != nil {
		o.logger.Info("direct plan reviewer enabled", "endpoint", cfg.Endpoint, "model", cfg.Model)
	}
}

// SetDirectToolAgentConfig enables the direct SGLang API tool-calling agent
// path for coder, reviewer, and fixer roles. When set (and the per-role
// provider resolves to ProviderSGLangDirect), those agents bypass the
// Claude Code / OpenCode subprocess and call the SGLang HTTP API directly
// via RunDirectToolAgent. Pass nil to disable.
func (o *Orchestrator) SetDirectToolAgentConfig(cfg *agent.DirectToolAgentConfig) {
	o.directToolAgentCfg = cfg
	if cfg != nil {
		o.logger.Info("direct tool agent enabled", "endpoint", cfg.Endpoint, "model", cfg.Model)
		// Initialize the endpoint health checker from the tool agent endpoint.
		o.endpointHealth = agent.NewEndpointHealthChecker(cfg.Endpoint, o.logger)
		o.logger.Info("endpoint health checker enabled", "probe_url", cfg.Endpoint)
	}
}

// SetSpawner configures the WorkerSpawner used by container-based task
// dispatch. When set, the orchestrator routes coder/reviewer/fixer/supervisor
// spawns through the spawner RPC in addition to (not replacing) the legacy
// worktree dispatch, and subscribes to Docker events via Runtime. Pass nil
// to disable.
func (o *Orchestrator) SetSpawner(s WorkerSpawner) {
	o.Spawner = s
}

// SetRuntime configures the container.Runtime used for Docker event
// subscription. Must be set alongside SetSpawner for the full container path.
func (o *Orchestrator) SetRuntime(rt container.Runtime) {
	o.Runtime = rt
}

// SetGitrefRegistry configures the gitref branch registry. Must be set
// alongside SetSpawner for the container path's branch lifecycle tracking.
func (o *Orchestrator) SetGitrefRegistry(reg *gitref.Registry) {
	o.GitrefRegistry = reg
}

// SetEventBus connects the orchestrator to the C-Suite event bus. When set,
// every task status transition and agent status change is published as an event
// with delivery records for all known C-Suite agents. Pass nil to disable.
func (o *Orchestrator) SetEventBus(bus *eventbus.Bus) {
	o.bus = bus
}

// SetInternalEndpoints configures the in-cluster URL and shared bearer
// token that spawned worker containers (the per-task merger in
// particular) need in order to POST merge_result records back to
// /internal/logs. Both values are typically plumbed in from the
// DREM_ORCH_URL and DREM_AGENTMON_TOKEN env vars on the orchestrator
// container. Safe to call multiple times; empty strings reset the
// corresponding field.
func (o *Orchestrator) SetInternalEndpoints(orchURL, agentmonToken string) {
	o.orchURL = orchURL
	o.agentmonToken = agentmonToken
}

// NewWithExperimentScheduling creates an Orchestrator with experiment-aware
// scheduling. Normal tasks pause while experiments run; the agent pool is
// partitioned across variants. See New for the merger/projectName contract.
func NewWithExperimentScheduling(
	db *gorm.DB,
	dbPath string,
	runner *agent.Runner,
	wt WorktreeManager,
	mem *memory.Manager,
	sup *supervisor.Supervisor,
	projectID uuid.UUID,
	projectName string,
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
	orch := New(db, dbPath, runner, wt, mem, sup, projectID, projectName, events, tickInterval, staleTimeout, contextWarnPct, contextStopPct, bugSvc, bugDir, contextFixerPct...)
	orch.experimentScheduler = NewExperimentScheduler(db, maxConcurrent)
	return orch
}

// Run starts the main loop. It blocks until ctx is cancelled.
func (o *Orchestrator) Run(ctx context.Context) {
	// Startup cleanup: clear stale agent assignments left from previous runs.
	o.cleanupOrphanedAssignments()

	// Generate repo map for the default branch worktree at startup.
	go o.worktree.GenerateRepoMapForMain()

	// Container-mode startup: reconcile in-flight workers against the
	// spawner's live list, then launch the Docker event watcher in parallel
	// with the tick loop. Shutdown waits on both to unwind cleanly.
	eventsDone := make(chan struct{})
	if o.Spawner != nil && o.Runtime != nil {
		if err := o.reconcileOnStartup(ctx); err != nil {
			o.logger.Error("reconcile on startup", "error", err)
		}
		go func() {
			defer close(eventsDone)
			if err := o.watchDockerEvents(ctx); err != nil {
				o.logger.Error("watch docker events", "error", err)
			}
		}()
	} else {
		close(eventsDone)
	}

	ticker := time.NewTicker(o.tick)
	defer ticker.Stop()
	o.logger.Info("orchestrator started", "project_id", o.projectID)
	for {
		select {
		case <-ctx.Done():
			o.logger.Info("orchestrator stopping, waiting for event watcher")
			<-eventsDone
			o.logger.Info("orchestrator stopped")
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
	if o.lifecycle != nil {
		if _, err := o.lifecycle.Tick(ctx, TickScope{
			ProjectID: o.projectID,
			Now:       time.Now(),
		}); err != nil {
			o.logger.Error("lifecycle tick", "error", err)
		}
		return
	}
	o.doTickLegacy(ctx)
}

func (o *Orchestrator) doTickLegacy(ctx context.Context) {
	_ = ctx
	// -1. Check for operator signal files (e.g. reset-circuit).
	o.checkSignalFiles()
	// 0. Ingest any pending bug reports from the drop directory.
	o.ingestBugReports()
	// 0b. Process CLASSIFYING tasks -> spawn classifier agents or call API directly.
	if o.directClassifierCfg != nil {
		o.processClassifyingTasksDirect()
	} else {
		o.processClassifyingTasks()
	}
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
	_, err := gitexec.RunGit(
		context.Background(), featureWorktree,
		"merge-base", "--is-ancestor", ag.WorktreeBranch, "HEAD",
	)
	return err == nil // exit code 0 means it IS an ancestor
}

// resolveFeatureWorktree resolves the feature integration worktree path
// for a task. Top-level tasks carry WorktreeBranch on the task row itself;
// subtasks inherit it from their parent. Mirrors the branch-resolution
// logic in agent_results.go so the reconciler path agrees with the normal
// completion path.
func (o *Orchestrator) resolveFeatureWorktree(task *model.Task) string {
	// Prefer the task's own branch when set. This covers both top-level
	// tasks (which always own their branch) and subtasks whose branch has
	// been materialised onto the task row.
	if task.WorktreeBranch != "" {
		fn := strings.TrimPrefix(task.WorktreeBranch, "feature/")
		return o.worktree.FeatureWorktreePath(fn)
	}
	// Fall back to the parent task's branch for subtasks that only carry
	// the branch via parent_task_id.
	if task.ParentTaskID == nil {
		return ""
	}
	var parent model.Task
	if err := o.db.Select("worktree_branch").First(&parent, "id = ?", task.ParentTaskID).Error; err != nil {
		return ""
	}
	if parent.WorktreeBranch == "" {
		return ""
	}
	fn := strings.TrimPrefix(parent.WorktreeBranch, "feature/")
	return o.worktree.FeatureWorktreePath(fn)
}

func (o *Orchestrator) featureBranchForTask(task *model.Task) string {
	if task.WorktreeBranch != "" {
		return task.WorktreeBranch
	}
	if task.ParentTaskID == nil {
		return ""
	}
	var parent model.Task
	if err := o.db.Select("worktree_branch").First(&parent, "id = ?", task.ParentTaskID).Error; err != nil {
		return ""
	}
	return parent.WorktreeBranch
}

func (o *Orchestrator) featureBranchHasChanges(task *model.Task, featureDir string) bool {
	if featureDir == "" || o.worktree == nil {
		return false
	}
	changed, err := gitexec.GetChangedFiles(context.Background(), featureDir, o.worktree.DefaultBranchName())
	if err != nil {
		o.logger.Warn("feature branch change check failed", "task_id", task.ID, "error", err)
		return false
	}
	return len(changed) > 0
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

		// Container-mode agents don't share a host-visible worktree, so the
		// idle-signal-file heuristic is moot for them. A container agent
		// carries its container ID in TmuxSession (not a tmux session name)
		// and typically has no WorktreePath set. Skip to avoid accidentally
		// matching a stale .claude/agent-idle file if a host path was ever
		// recorded. Container-mode stuck detection is handled by
		// reconcileStuckAgents via the spawner's ListWorkers result.
		if ag.TmuxSession != "" && !isLegacyTmuxSession(ag.TmuxSession) {
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

// isTerminal returns true if a task status is a terminal state (no further
// automated processing will occur).
func isTerminal(status model.TaskStatus) bool {
	return status == model.StatusDone || status == model.StatusFailed || status == model.StatusRejected || status == model.StatusCancelled
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
