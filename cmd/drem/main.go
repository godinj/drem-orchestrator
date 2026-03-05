// Package main is the entry point for the Drem Orchestrator CLI. It wires
// together the database, orchestrator, tmux manager, and Bubble Tea TUI.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/godinj/drem-orchestrator/internal/agent"
	"github.com/godinj/drem-orchestrator/internal/db"
	"github.com/godinj/drem-orchestrator/internal/memory"
	"github.com/godinj/drem-orchestrator/internal/merge"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/orchestrator"
	"github.com/godinj/drem-orchestrator/internal/supervisor"
	tmuxpkg "github.com/godinj/drem-orchestrator/internal/tmux"
	"github.com/godinj/drem-orchestrator/internal/tui"
	"github.com/godinj/drem-orchestrator/internal/worktree"
)

func main() {
	// Parse flags.
	configPath := flag.String("config", "drem.toml", "config file path")
	repoPath := flag.String("repo", "", "bare repo path (required)")
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
	sessionName := "󱇯 dash " + projectName

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

		tmux := tmuxpkg.NewManager(sessionName)
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
	tmux := tmuxpkg.NewManager(sessionName)
	wt := worktree.NewManager(cfg.BareRepoPath, cfg.DefaultBranch)
	runner := agent.NewRunner(database, tmux, wt, cfg.ClaudeBin, cfg.MaxConcurrentAgents)
	merger := merge.NewOrchestrator(wt, database)
	mem := memory.NewManager(database)

	var sup *supervisor.Supervisor
	if cfg.SupervisorEnabled {
		sup = supervisor.New(cfg.ClaudeBin, cfg.SupervisorTimeout)
	}

	events := make(chan orchestrator.Event, 100)
	orch := orchestrator.New(database, runner, wt, merger, mem, sup, project.ID, events, cfg.TickInterval, cfg.StaleTimeout)

	// Start orchestrator in background.
	ctx, cancel := context.WithCancel(context.Background())
	go orch.Run(ctx)

	// Start TUI (blocks until quit).
	p := tea.NewProgram(
		tui.NewModel(database, orch, tmux, project.ID, events, cfg.LogPath),
		tea.WithAltScreen(),
	)
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "tui error: %v\n", err)
	}

	// Cleanup.
	cancel()
}
