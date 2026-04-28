// Package main is the entry point for the Drem Orchestrator CLI. It wires
// together the database, orchestrator, and Bubble Tea TUI.
//
// After the containerization migration (prompt 21) the CLI no longer hosts
// agents or the dashboard in a tmux session — headless agents run as
// subprocesses or inside containers, and the TUI runs in the user's own
// terminal.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/godinj/drem-orchestrator/internal/agent"
	"github.com/godinj/drem-orchestrator/internal/bugreport"
	"github.com/godinj/drem-orchestrator/internal/container"
	"github.com/godinj/drem-orchestrator/internal/csuite"
	"github.com/godinj/drem-orchestrator/internal/db"
	"github.com/godinj/drem-orchestrator/internal/eventbus"
	"github.com/godinj/drem-orchestrator/internal/memory"
	"github.com/godinj/drem-orchestrator/internal/metrics"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/orchestrator"
	"github.com/godinj/drem-orchestrator/internal/orchhttp"
	"github.com/godinj/drem-orchestrator/internal/ratelimit"
	"github.com/godinj/drem-orchestrator/internal/spawner"
	"github.com/godinj/drem-orchestrator/internal/supervisor"
	"github.com/godinj/drem-orchestrator/internal/taskimport"
	"github.com/godinj/drem-orchestrator/internal/tui"
	"github.com/godinj/drem-orchestrator/pkg/orchclient"
)

func main() {
	// Handle subcommands that bypass the flag-based TUI entry point.
	if len(os.Args) > 1 && os.Args[1] == "cli" {
		runCLI()
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "project" {
		runProject(os.Args[2:])
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "csuite" {
		runCsuite()
		return
	}

	// Parse flags. Env var fallbacks let the containerized orch image run
	// without a baked drem.toml; the per-project compose template sets
	// DREM_BARE_REPO / DREM_CONFIG / DREM_ORCH_URL / DREM_HEADLESS.
	configPath := flag.String("config", envOr("DREM_CONFIG", "drem.toml"), "config file path")
	repoPath := flag.String("repo", os.Getenv("DREM_BARE_REPO"), "bare repo path (required)")
	importPath := flag.String("import", "", "import tasks from a Markdown file")
	// orchURL lets the dashboard point at a remote or alternate orchestrator
	// HTTP API. Empty falls back to http://127.0.0.1:<cfg.OrchHTTPPort>.
	// See docs/prd-containerization.md and internal/tui/datasource.go.
	orchURL := flag.String("orch-url", os.Getenv("DREM_ORCH_URL"), "orchestrator HTTP API URL (default http://127.0.0.1:<orch_http_port>)")
	headless := flag.Bool("headless", os.Getenv("DREM_HEADLESS") != "", "run orchestrator + HTTP API without the TUI (container mode)")
	tuiOnly := flag.Bool("tui-only", os.Getenv("DREM_TUI_ONLY") != "", "launch the TUI as a pure HTTP client against --orch-url; does not spawn a local orchestrator or HTTP API")
	flag.Parse()

	if *headless && *tuiOnly {
		log.Fatal("--headless and --tui-only are mutually exclusive")
	}
	if *tuiOnly && *orchURL == "" {
		log.Fatal("--tui-only requires --orch-url (or DREM_ORCH_URL) pointing at a running orchestrator")
	}

	// Load config.
	cfg, err := LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if *repoPath != "" {
		cfg.BareRepoPath = *repoPath
	}
	if cfg.BareRepoPath == "" {
		log.Fatal("--repo is required: path to bare git repo")
	}

	// Validate model capabilities before starting.
	if result := cfg.ValidateModelCapabilities(); result.HasErrors() {
		log.Fatalf("model capability validation failed:\n%s", result.Summary())
	} else if len(result.Warnings) > 0 {
		log.Printf("model capability warnings:\n%s", result.Summary())
	}

	// Derive session name.
	projectName := filepath.Base(cfg.BareRepoPath)
	projectName = strings.TrimSuffix(projectName, ".git")

	// Handle --import: create tasks from Markdown file and exit.
	if *importPath != "" {
		database, err := db.Init(cfg.DatabasePath)
		if err != nil {
			log.Fatalf("database: %v", err)
		}

		var project model.Project
		result := database.Where("bare_repo_path = ?", cfg.BareRepoPath).First(&project)
		if result.Error != nil {
			project = model.Project{
				Name:          projectName,
				BareRepoPath:  cfg.BareRepoPath,
				DefaultBranch: cfg.DefaultBranch,
			}
			if err := database.Create(&project).Error; err != nil {
				log.Fatalf("create project: %v", err)
			}
		}

		f, err := os.Open(*importPath)
		if err != nil {
			log.Fatalf("open import file: %v", err)
		}
		defer f.Close()

		n, err := taskimport.Import(f, database, project.ID)
		if err != nil {
			log.Fatalf("import: %v", err)
		}
		fmt.Printf("Imported %d tasks from %s\n", n, *importPath)
		return
	}

	// projectName is the human-readable project label. Previously only used
	// for session naming; it is now also threaded into the orchestrator as
	// o.projectName so worker spawns can emit drem.project=<name> alongside
	// drem.project_id=<UUID>. See plans/dual-label-worker-spawn.md.

	// Redirect logging to file so it doesn't corrupt the TUI.
	logFile, err := os.OpenFile(cfg.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		log.Fatalf("open log file: %v", err)
	}
	defer logFile.Close()
	slog.SetDefault(slog.New(slog.NewTextHandler(logFile, nil)))
	log.SetOutput(logFile)

	// Init database.
	database, err := db.Init(cfg.DatabasePath)
	if err != nil {
		log.Fatalf("database: %v", err)
	}

	// Get or create project.
	var project model.Project
	result := database.Where("bare_repo_path = ?", cfg.BareRepoPath).First(&project)
	if result.Error != nil {
		project = model.Project{
			Name:          projectName,
			BareRepoPath:  cfg.BareRepoPath,
			DefaultBranch: cfg.DefaultBranch,
		}
		if err := database.Create(&project).Error; err != nil {
			log.Fatalf("create project: %v", err)
		}
	}

	// Init components. The host-mode worktree manager is built behind
	// orchestrator.NewHostManager so main.go holds no direct import of the
	// host worktree package. The tmux dashboard session manager is retired;
	// headless and interactive agents no longer share a tmux server with the
	// dashboard. Pass a nil TmuxSessionManager to the runner — supervisor
	// and shell sessions now live inside containerized workers and are
	// surfaced through the orchestrator's HTTP API.
	host := orchestrator.NewHostManager(cfg.BareRepoPath, cfg.DefaultBranch)

	// Migrate old-layout worktrees to grouped layout.
	if err := host.MigrateToGroupedLayout(); err != nil {
		slog.Warn("worktree layout migration failed", "error", err)
	}
	host.MigrateAgentPaths(database)

	wt := host.AsInterface()
	runner := agent.NewRunner(database, nil, host.AsAgentWorktreeManager(), cfg.ClaudeBin, cfg.OpenCodeBin, cfg.MaxConcurrentAgents, cfg.Agents.ForAgentType)
	runner.SetCodexBin(cfg.CodexBin)
	runner.SetDispatchLimiter(ratelimit.New(cfg.MaxDispatchRate, cfg.DispatchWindow))
	runner.SetMetricsRecorder(metrics.NewStore(database))
	runner.SetOpenCodeContextWindow(cfg.OpenCodeContextWindow)
	// The feature-into-main merge runs in a merger container (see
	// internal/orchestrator/merge_dispatch.go). The agent-into-feature merge
	// runs in-process via Orchestrator.mergeAgentBranchIntoFeature, which
	// uses the worktree.Manager's MergeBranch/RebaseBranchOnto primitives.
	// The TestCommand previously set on internal/merge now belongs on the
	// merger container; tracked separately in the merger deployment config.
	_ = cfg.TestCommand
	mem := memory.NewManager(database)

	var sup *supervisor.Supervisor
	if cfg.SupervisorEnabled {
		supervisorBin := cfg.ClaudeBin
		if cfg.Agents.SupervisorCLIConfig().EffectiveProvider() == model.ProviderCodex {
			supervisorBin = cfg.CodexBin
		}
		sup = supervisor.New(supervisorBin, cfg.SupervisorTimeout, cfg.Agents.SupervisorCLIConfig())
	}

	// Create bug report drop directory and service.
	bugReportDir := filepath.Join(cfg.BareRepoPath, ".drem", "bug-reports")
	if err := os.MkdirAll(bugReportDir, 0o755); err != nil {
		log.Fatalf("create bug report directory: %v", err)
	}
	bugReportSvc := bugreport.New(database)

	orchEvents := make(chan orchestrator.Event, 100)
	orch := orchestrator.NewWithExperimentScheduling(database, cfg.DatabasePath, runner, wt, mem, sup, project.ID, projectName, orchEvents, cfg.TickInterval, cfg.StaleTimeout, cfg.ContextWarnPercent, cfg.ContextStopPercent, bugReportSvc, bugReportDir, cfg.MaxConcurrentAgents, cfg.ContextFixerPercent)
	orch.SetInteractiveSupervisorConfig(cfg.Agents.InteractiveSupervisorCLIConfig())

	// Enable direct SGLang classifier when configured or auto-detected.
	// Auto-enable when the classifier provider is "opencode" and model contains "sglang",
	// or when the direct flag is explicitly set.
	classifierDirect := cfg.Agents.Classifier.Direct || (cfg.Agents.Classifier.Provider == "opencode" && strings.Contains(cfg.Agents.Classifier.Model, "sglang"))
	if classifierDirect {
		dcfg := agent.DefaultDirectClassifierConfig()
		// If the classifier has a model override, use it for the direct path too.
		if cfg.Agents.Classifier.Model != "" && !strings.Contains(cfg.Agents.Classifier.Model, "sglang") {
			dcfg.Model = cfg.Agents.Classifier.Model
		}
		// Route through gq proxy when [direct_tool_agent].endpoint is set.
		if cfg.DirectToolAgent.Endpoint != "" {
			dcfg.Endpoint = cfg.DirectToolAgent.Endpoint
		}
		orch.SetDirectClassifierConfig(&dcfg)
	}

	// Route classify jobs to the warm drem-classifier container when
	// DREM_CLASSIFIER_URL is set or the [agents.classifier].endpoint key
	// is populated in drem.toml. Empty value keeps the inline direct path
	// as a rollback-safe default. See plans/warm-direct-classifier.md.
	classifierURL := os.Getenv("DREM_CLASSIFIER_URL")
	if classifierURL == "" {
		classifierURL = cfg.Agents.Classifier.Endpoint
	}
	if classifierURL != "" {
		orch.SetClassifierContainerEndpoint(classifierURL, os.Getenv("DREM_AGENTMON_TOKEN"))
	}

	// Route plan jobs to the warm drem-planner container when
	// DREM_PLANNER_URL is set or the [agents.planner].endpoint key is
	// populated in drem.toml. Empty keeps the legacy runner.SpawnAgent
	// path as the rollback-safe default. See plans/warm-planner-pivot.md.
	plannerURL := os.Getenv("DREM_PLANNER_URL")
	if plannerURL == "" {
		plannerURL = cfg.Agents.Planner.Endpoint
	}
	if plannerURL != "" {
		orch.SetPlannerContainerEndpoint(plannerURL, os.Getenv("DREM_AGENTMON_TOKEN"))
	}

	// Wire the container-mode worker spawner. When DREM_SPAWNER_SOCKET is set
	// (standard in the orch container per the compose template) or the
	// default socket exists on disk, connect to the spawner service and set
	// orch.Spawner so container-path dispatch (subtask_scheduling.go,
	// session_spawning.go, test_execution.go, reconcile_containers.go,
	// merge_dispatch.go) takes effect. Without this, o.Spawner stays nil and
	// every site falls through to the legacy runner.SpawnAgent host-subprocess
	// path — which fails inside the orch container because `claude` isn't on
	// $PATH. See plans/phase-3.5-subtask-dispatch-migration.md.
	//
	// Absent socket keeps the legacy path as the rollback-safe default for
	// local dev on a host where drem runs directly against a host sqlite DB.
	spawnerSock := os.Getenv("DREM_SPAWNER_SOCKET")
	if spawnerSock == "" {
		spawnerSock = "/var/run/drem/spawner.sock"
	}
	if _, err := os.Stat(spawnerSock); err == nil {
		orch.SetSpawner(spawner.NewClient(spawnerSock))
		slog.Info("spawner client wired", "socket", spawnerSock)

		// Container-mode deploys MUST also have a container.Runtime
		// wired so orchestrator.watchDockerEvents runs (see
		// orchestrator.Run, which guards the event-watch goroutine on
		// o.Runtime != nil). Before this wiring landed, watchDockerEvents
		// never ran in containerised deploys — the fourth observability
		// gap discovered during v13–v14 triage (see
		// plans/agentmon-observability.md). DREM_IN_CONTAINER=1 (set by
		// the orch container image) is the opt-in signal; host-mode dev
		// keeps the legacy behaviour where Runtime stays nil and
		// watchDockerEvents does not start.
		if os.Getenv("DREM_IN_CONTAINER") == "1" {
			rt, rterr := container.NewDockerRuntime()
			if rterr != nil {
				slog.Warn("docker runtime: could not connect; watchDockerEvents will stay disabled",
					"error", rterr)
			} else {
				orch.SetRuntime(rt)
				slog.Info("docker runtime wired for watchDockerEvents")
			}
		}
	} else {
		slog.Info("spawner socket not present; container dispatch disabled, falling back to legacy runner",
			"socket", spawnerSock, "error", err)
	}

	// Enable direct SGLang prep agent. Prep is the read-only recon role that
	// reuses the classifier's SGLang server but with a larger token budget for
	// tool loops. Auto-enable when the prep role is explicitly set to direct,
	// when the prep provider is "opencode" with an sglang model, or when the
	// classifier already runs direct (the prep role piggybacks on the same
	// SGLang endpoint by default).
	prepDirect := cfg.Agents.Prep.Direct ||
		(cfg.Agents.Prep.Provider == "opencode" && strings.Contains(cfg.Agents.Prep.Model, "sglang")) ||
		classifierDirect
	if prepDirect {
		pcfg := agent.DirectPrepConfig{DirectToolAgentConfig: agent.DefaultDirectToolAgentConfig()}
		// Prep does tool loops, so allow more output tokens than the classifier.
		// The DirectToolAgent default (2048) is reasonable; raise to 4096 to
		// give the model room for the final PrepOutput JSON after several
		// iterations of tool calls.
		pcfg.MaxTokens = 4096
		// If the prep role has its own model override, use it. Otherwise fall
		// back to the classifier's model so both roles target the same server.
		switch {
		case cfg.Agents.Prep.Model != "" && !strings.Contains(cfg.Agents.Prep.Model, "sglang"):
			pcfg.Model = cfg.Agents.Prep.Model
		case cfg.Agents.Classifier.Model != "" && !strings.Contains(cfg.Agents.Classifier.Model, "sglang"):
			pcfg.Model = cfg.Agents.Classifier.Model
		}
		// Route through gq proxy when [direct_tool_agent].endpoint is set.
		if cfg.DirectToolAgent.Endpoint != "" {
			pcfg.Endpoint = cfg.DirectToolAgent.Endpoint
		}
		orch.SetDirectPrepConfig(&pcfg)
	}

	// Enable direct SGLang tool-calling agent path for coder/reviewer/fixer
	// roles when configured. Explicit opt-in via [direct_tool_agent].enabled,
	// or auto-enable when any of the coder/reviewer/fixer providers resolve
	// to "sglang-direct".
	if dta := buildDirectToolAgentConfig(cfg); dta != nil {
		orch.SetDirectToolAgentConfig(dta)
	}

	// Wire test gate config from drem.toml so test_command is respected.
	scopedTests := true
	if cfg.ScopedTests != nil {
		scopedTests = *cfg.ScopedTests
	}
	orch.SetTestGateConfig(orchestrator.TestGateConfig{
		TestCommand:    cfg.TestCommand,
		CompileCommand: cfg.CompileCommand,
		ScopedTests:    scopedTests,
		TestTimeout:    cfg.TestTimeout,
	})
	// When drem.toml leaves test_command empty, infer it from the main
	// worktree (go.mod → "go test ./...", package.json → "npm test",
	// etc.). Prevents drem-merger crash-looping with
	// `missing required flags: [--test-cmd]` for Go projects registered
	// before test_command was documented in drem.toml. The fail-close
	// guard in dispatchMerge catches the remaining non-Go, non-Node,
	// non-Python, non-Rust cases. See
	// plans/bug-h-merger-crash-on-v17-advance.md (Option A).
	if cfg.TestCommand == "" {
		if mainWT, mainErr := wt.MainWorktreePath(); mainErr == nil {
			orch.ApplyTestCommandInference(mainWT)
		} else {
			slog.Debug("test command inference skipped: no main worktree path",
				"error", mainErr)
		}
	}
	orch.SetSkipConstraintGate(cfg.SkipConstraintGate)

	// Plumb the in-cluster orchestrator URL and agentmon shared token
	// through so dispatchMerge can pass --orch-url and --agentmon-token
	// to every spawned merger container. These env vars are set by the
	// per-project compose template (see
	// internal/projects/templates/project-compose.yml.tmpl). Outside the
	// container (TUI-only, local dev) both are empty and the merger is
	// not used.
	orch.SetInternalEndpoints(
		os.Getenv("DREM_ORCH_URL"),
		os.Getenv("DREM_AGENTMON_TOKEN"),
	)

	// Wire event bus so task transitions produce events for C-Suite agents.
	if bus, err := eventbus.New(filepath.Join(os.Getenv("HOME"), ".drem-csuite", "csuite.db")); err != nil {
		log.Printf("warning: event bus unavailable: %v", err)
	} else {
		orch.SetEventBus(bus)
		defer bus.Close()
	}

	// Init bug report service.
	bugreportSvc := bugreport.New(database)

	// Bridge orchestrator events to TUI-local Event type.
	tuiEvents := make(chan tui.Event, 100)
	go func() {
		for e := range orchEvents {
			tuiEvents <- tui.Event{Type: e.Type, Payload: e.Payload}
		}
		close(tuiEvents)
	}()

	// Start orchestrator in background. Skipped in --tui-only mode so the
	// TUI runs as a pure client against the remote orchestrator HTTP API
	// (e.g. the containerized drem-orch in the per-project compose).
	ctx, cancel := context.WithCancel(context.Background())

	// Bug E W3.1 + W3.2: always-on observability hooks.
	//
	// pprof listener: gated on DREM_PPROF=1 and bound to 127.0.0.1:6060
	// (override via DREM_PPROF_ADDR) so a distroless orch can serve a
	// runtime profile to an operator who docker exec-curls it from the
	// host. Off by default — the env gate keeps the profile surface
	// opt-in. See internal/orchhttp/pprof.go.
	//
	// Goroutine-dump signal handler: a SIGUSR1 writes runtime.Stack
	// into /tmp/drem-goroutines-<unix_ts>.log so the host operator can
	// `docker kill --signal=USR1 drem-orchestrator-orch-1` when orch
	// hangs, then read the dump via a /tmp bind-mount. See
	// internal/orchhttp/goroutinedump.go.
	if addr, _, err := orchhttp.StartPprofListener(ctx); err != nil {
		slog.Warn("pprof listener disabled", "error", err)
	} else if addr != "" {
		slog.Info("pprof listener ready", "addr", addr)
	}
	if err := orchhttp.InstallGoroutineDumpHandler(ctx, "/tmp"); err != nil {
		slog.Warn("SIGUSR1 goroutine dump handler disabled", "error", err)
	} else {
		slog.Info("SIGUSR1 goroutine dump handler installed", "dir", "/tmp")
	}

	if !*tuiOnly {
		go orch.Run(ctx)

		// Start orchestrator HTTP API (read-only public endpoints + agentmon
		// ingestion). See docs/prd-containerization.md — Kyle and the TUI both
		// call this API. A nil log streamer means GET /logs returns 503 until
		// agentmon is containerized and wired through.
		startOrchHTTP(ctx, cfg, database, project.Name, orch)
	}

	// Start C-Suite dashboard poller backed by disk state files.
	csuiteSource := csuite.NewDiskSnapshotSource("")
	csuitePoller := csuite.NewPoller(csuiteSource, csuite.DefaultPollInterval, log.Default())
	go csuitePoller.Start(ctx)

	// Create C-Suite store for messaging operations (compose, list, detail).
	csuiteStore := csuite.NewStore(database)

	// Build the orchestrator HTTP data source used by the TUI for tasks,
	// workers, events, and logs. The dashboard runs colocated with the
	// orchestrator today, so the default points at local loopback; the
	// --orch-url flag overrides for remote/test setups.
	resolvedOrchURL := tui.ResolveOrchURL(*orchURL, cfg.OrchHTTPPort)
	dataSource := tui.NewHTTPDataSource(resolvedOrchURL, project.Name)
	slog.Info("TUI data source configured", "url", resolvedOrchURL, "project", project.Name)

	// Phase 3 of the orch API gate-mutation pivot: the TUI's gate actions
	// (approve / reject / pass / fail / answer) route through pkg/orchclient
	// over HTTP instead of calling the in-process orchestrator directly.
	// This closes the same double-writer escape hatch Phase 2 is closing
	// for `drem cli`. The in-process orch is still constructed (above) and
	// still runs its reconciler/scheduler via orch.Run(ctx) below; we just
	// hand the TUI an adapter that POSTs gate mutations to the orch's HTTP
	// API. Non-gate methods (pause/resume/retry/comment/spawn/…) fall
	// through to the in-process orch via WithFallback — those are out of
	// scope for Phase 3 and stay functional.
	//
	// See plans/orch-api-gate-mutations.md "Phase 3" and
	// internal/tui/http_orchestrator.go.
	tuiOrch := tui.NewHTTPOrchestrator(orchclient.New(resolvedOrchURL), project.Name).WithFallback(orch)

	// Register our own signal handler so we control shutdown — not
	// Bubble Tea.  WithoutSignalHandler() prevents Bubble Tea from
	// converting SIGTERM into a QuitMsg that silently exits the TUI.
	//
	// Only SIGINT and SIGTERM trigger graceful shutdown (quit TUI + exit).
	// SIGHUP is caught separately and IGNORED — tmux sends SIGHUP when a
	// session is destroyed, and we must not let it kill the TUI or process.
	sigShutdown := make(chan os.Signal, 1)
	signal.Notify(sigShutdown, syscall.SIGINT, syscall.SIGTERM)

	sigHup := make(chan os.Signal, 1)
	signal.Notify(sigHup, syscall.SIGHUP)
	go func() {
		for range sigHup {
			slog.Info("SIGHUP received and ignored (tmux session event)")
		}
	}()

	// shutdownRequested is closed when a real shutdown signal (SIGINT/SIGTERM)
	// arrives, signalling the TUI restart loop to stop.
	shutdownRequested := make(chan struct{})

	// Headless mode: orchestrator + HTTP API only, no TUI. Used by the
	// containerized drem-orch image (DREM_HEADLESS=1 in the per-project
	// compose). Block on shutdown signal and let deferred cancel() tear
	// everything down.
	if *headless {
		slog.Info("headless mode: TUI disabled, orchestrator + HTTP API running", "project", project.Name)
		<-sigShutdown
		slog.Info("received shutdown signal, stopping orchestrator")
		cancel()
		return
	}

	// TUI restart loop: if p.Run() returns unexpectedly (no shutdown signal),
	// create a fresh Program and restart. This makes the TUI resilient to
	// transient Bubble Tea exits (PTY errors, races, etc.).
	var (
		pMu sync.Mutex
		p   *tea.Program
	)
	go func() {
		for {
			prog := tea.NewProgram(
				tui.NewModel(database, dataSource, tuiOrch, nil, project.ID, tuiEvents, cfg.LogPath, bugreportSvc, csuitePoller.Snapshots(), csuiteStore),
				tea.WithAltScreen(),
				tea.WithoutSignalHandler(),
			)
			pMu.Lock()
			p = prog
			pMu.Unlock()

			slog.Info("TUI starting")

			_, err := prog.Run()

			// Check whether this was a requested shutdown.
			select {
			case <-shutdownRequested:
				slog.Info("TUI exited after shutdown request")
				return
			default:
			}

			// Unexpected exit — log diagnostics and restart.
			if err != nil {
				slog.Warn("TUI exited unexpectedly, restarting in 1s", "error", err)
			} else {
				slog.Warn("TUI p.Run() returned nil error unexpectedly, restarting in 1s")
			}

			// Brief pause before restart to avoid tight loops if the TTY
			// is permanently unavailable.
			select {
			case <-shutdownRequested:
				return
			case <-time.After(1 * time.Second):
			}
		}
	}()

	// Shutdown listener: when a real shutdown signal arrives, quit the TUI
	// gracefully (if running) so it restores the terminal, then proceed
	// to cleanup.
	<-sigShutdown
	slog.Info("received shutdown signal, stopping orchestrator")
	close(shutdownRequested)
	pMu.Lock()
	if p != nil {
		p.Quit()
	}
	pMu.Unlock()

	// Give the TUI goroutine a moment to notice shutdownRequested and exit
	// p.Run() cleanly. Then proceed to cleanup regardless.
	time.Sleep(100 * time.Millisecond)

	// Cleanup.
	cancel()
}

// envOr returns the value of env var key, or def when the var is unset or empty.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// buildDirectToolAgentConfig resolves the runtime configuration for the
// direct SGLang tool-calling agent path. Returns nil when the feature is
// not enabled.
//
// The direct path is enabled when either:
//  1. [direct_tool_agent].enabled is set to true in drem.toml, or
//  2. any of the coder/reviewer/fixer agents have provider = "sglang-direct".
//
// TOML values in [direct_tool_agent] override the defaults from
// agent.DefaultDirectToolAgentConfig(). The per-role agent Model is NOT
// used here — the direct path shares a single served model name across all
// roles via the [direct_tool_agent].model key.
func buildDirectToolAgentConfig(cfg Config) *agent.DirectToolAgentConfig {
	roleDirect := cfg.Agents.Coder.Provider == string(model.ProviderSGLangDirect) ||
		cfg.Agents.Reviewer.Provider == string(model.ProviderSGLangDirect) ||
		cfg.Agents.Fixer.Provider == string(model.ProviderSGLangDirect)
	if !cfg.DirectToolAgent.Enabled && !roleDirect {
		return nil
	}
	resolved := agent.DefaultDirectToolAgentConfig()
	if cfg.DirectToolAgent.Endpoint != "" {
		resolved.Endpoint = cfg.DirectToolAgent.Endpoint
	}
	if cfg.DirectToolAgent.Model != "" {
		resolved.Model = cfg.DirectToolAgent.Model
	}
	if cfg.DirectToolAgent.MaxTokens > 0 {
		resolved.MaxTokens = cfg.DirectToolAgent.MaxTokens
	}
	if cfg.DirectToolAgent.Temperature != 0 {
		resolved.Temperature = cfg.DirectToolAgent.Temperature
	}
	if cfg.DirectToolAgent.Timeout > 0 {
		resolved.Timeout = cfg.DirectToolAgent.Timeout
	}
	if cfg.DirectToolAgent.MaxIterations > 0 {
		resolved.MaxIterations = cfg.DirectToolAgent.MaxIterations
	}
	if cfg.DirectToolAgent.BashTimeout > 0 {
		resolved.BashTimeout = cfg.DirectToolAgent.BashTimeout
	}
	if cfg.DirectToolAgent.ContextLimit > 0 {
		resolved.ContextLimit = cfg.DirectToolAgent.ContextLimit
	}
	return &resolved
}
