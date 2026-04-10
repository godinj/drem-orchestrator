// Package main is the entry point for the Drem Orchestrator CLI. It wires
// together the database, orchestrator, tmux manager, and Bubble Tea TUI.
//
// The dashboard runs inside a tmux session to support interactive supervisor
// and shell sessions (switch-client). Headless agents (planners, coders,
// reviewers, fixers) run as direct subprocesses — no tmux overhead.
package main

import (
	"context"
	"errors"
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
	"github.com/godinj/drem-orchestrator/internal/csuite"
	"github.com/godinj/drem-orchestrator/internal/db"
	"github.com/godinj/drem-orchestrator/internal/eventbus"
	"github.com/godinj/drem-orchestrator/internal/memory"
	"github.com/godinj/drem-orchestrator/internal/merge"
	"github.com/godinj/drem-orchestrator/internal/metrics"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/orchestrator"
	"github.com/godinj/drem-orchestrator/internal/ratelimit"
	"github.com/godinj/drem-orchestrator/internal/supervisor"
	"github.com/godinj/drem-orchestrator/internal/taskimport"
	tmuxpkg "github.com/godinj/drem-orchestrator/internal/tmux"
	"github.com/godinj/drem-orchestrator/internal/tui"
	"github.com/godinj/drem-orchestrator/internal/worktree"
)

func main() {
	// Handle subcommands that bypass the flag-based TUI entry point.
	if len(os.Args) > 1 && os.Args[1] == "cli" {
		runCLI()
		return
	}

	// Parse flags.
	configPath := flag.String("config", "drem.toml", "config file path")
	repoPath := flag.String("repo", "", "bare repo path (required)")
	importPath := flag.String("import", "", "import tasks from a Markdown file")
	flag.Parse()

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

	sessionName := "󱇯 dash " + projectName

	// Resolve tmux config file path relative to bare repo.
	tmuxConfPath := filepath.Join(cfg.BareRepoPath, cfg.TmuxConfigFile)

	// Tmux options used by both outer and inner invocations.
	tmuxOpts := []tmuxpkg.Option{
		tmuxpkg.WithSocket(cfg.TmuxSocket),
		tmuxpkg.WithConfigFile(tmuxConfPath),
	}

	// Self-respawn: if DREM_SESSION is not set, we are the outer invocation.
	// Create the tmux session with ourselves as the dashboard command, then
	// attach (replacing this process).
	if os.Getenv("DREM_SESSION") != sessionName {
		exe, err := os.Executable()
		if err != nil {
			log.Fatalf("resolve executable: %v", err)
		}

		// Build the command that tmux will run in the dashboard window.
		// It re-invokes drem with the same flags, plus DREM_SESSION set.
		dashCmd := fmt.Sprintf("DREM_SESSION='%s' %s --config %s --repo %s",
			sessionName, exe, *configPath, cfg.BareRepoPath)

		tmux := tmuxpkg.NewManager(sessionName, tmuxOpts...)
		if err := tmux.EnsureSession(dashCmd); err != nil {
			if !errors.Is(err, tmuxpkg.ErrDashboardRespawned) {
				log.Fatalf("tmux: %v", err)
			}
			// Dashboard was respawned — fall through to attach/switch.
		}

		// Replace this process with tmux attach (or switch-client if
		// already inside tmux).
		if err := tmux.Attach(); err != nil {
			log.Fatalf("tmux attach: %v", err)
		}
		return // unreachable after successful Exec
	}

	// Inner invocation: running inside the tmux session. Init DB/TUI normally.

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

	// Init components.
	tmux := tmuxpkg.NewManager(sessionName, tmuxOpts...)
	wt := worktree.NewManager(cfg.BareRepoPath, cfg.DefaultBranch)

	// Migrate old-layout worktrees to grouped layout.
	if err := wt.MigrateToGroupedLayout(); err != nil {
		slog.Warn("worktree layout migration failed", "error", err)
	}
	wt.MigrateAgentPaths(database)

	runner := agent.NewRunner(database, tmux, wt, cfg.ClaudeBin, cfg.OpenCodeBin, cfg.MaxConcurrentAgents, cfg.Agents.ForAgentType)
	runner.SetDispatchLimiter(ratelimit.New(cfg.MaxDispatchRate, cfg.DispatchWindow))
	runner.SetMetricsRecorder(metrics.NewStore(database))
	runner.SetOpenCodeContextWindow(cfg.OpenCodeContextWindow)
	mergeOrch := merge.NewOrchestrator(wt, database)
	mergeOrch.TestCommand = cfg.TestCommand
	merger := merge.NewMergeQueue(mergeOrch, wt)
	mem := memory.NewManager(database)

	var sup *supervisor.Supervisor
	if cfg.SupervisorEnabled {
		sup = supervisor.New(cfg.ClaudeBin, cfg.SupervisorTimeout, cfg.Agents.SupervisorCLIConfig())
	}

	// Create bug report drop directory and service.
	bugReportDir := filepath.Join(cfg.BareRepoPath, ".drem", "bug-reports")
	if err := os.MkdirAll(bugReportDir, 0o755); err != nil {
		log.Fatalf("create bug report directory: %v", err)
	}
	bugReportSvc := bugreport.New(database)

	orchEvents := make(chan orchestrator.Event, 100)
	orch := orchestrator.NewWithExperimentScheduling(database, cfg.DatabasePath, runner, wt, merger, mem, sup, project.ID, orchEvents, cfg.TickInterval, cfg.StaleTimeout, cfg.ContextWarnPercent, cfg.ContextStopPercent, bugReportSvc, bugReportDir, cfg.MaxConcurrentAgents, cfg.ContextFixerPercent)
	orch.SetInteractiveSupervisorConfig(cfg.Agents.InteractiveSupervisorCLIConfig())

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

	// Start orchestrator in background.
	ctx, cancel := context.WithCancel(context.Background())
	go orch.Run(ctx)

	// Start C-Suite dashboard poller backed by disk state files.
	csuiteSource := csuite.NewDiskSnapshotSource("")
	csuitePoller := csuite.NewPoller(csuiteSource, csuite.DefaultPollInterval, log.Default())
	go csuitePoller.Start(ctx)

	// Create C-Suite store for messaging operations (compose, list, detail).
	csuiteStore := csuite.NewStore(database)

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
				tui.NewModel(database, orch, tmux, project.ID, tuiEvents, cfg.LogPath, bugreportSvc, csuitePoller.Snapshots(), csuiteStore),
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
