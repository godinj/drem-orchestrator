// Package main is the entry point for the Drem Orchestrator CLI. It wires
// together the database, orchestrator, tmux manager, and Bubble Tea TUI.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/godinj/drem-orchestrator/internal/db"
	"github.com/godinj/drem-orchestrator/internal/agent"
	"github.com/godinj/drem-orchestrator/internal/memory"
	"github.com/godinj/drem-orchestrator/internal/merge"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/orchestrator"
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

	// Init database.
	database, err := db.Init(cfg.DatabasePath)
	if err != nil {
		log.Fatalf("database: %v", err)
	}

	// Get or create project.
	projectName := filepath.Base(cfg.BareRepoPath)
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
	tmux := tmuxpkg.NewManager("drem-" + projectName)
	if err := tmux.EnsureSession(); err != nil {
		log.Fatalf("tmux: %v", err)
	}

	wt := worktree.NewManager(cfg.BareRepoPath, cfg.DefaultBranch)
	runner := agent.NewRunner(database, tmux, wt, cfg.ClaudeBin, cfg.MaxConcurrentAgents)
	merger := merge.NewOrchestrator(wt, database)
	mem := memory.NewManager(database)

	events := make(chan orchestrator.Event, 100)
	orch := orchestrator.New(database, runner, wt, merger, mem, project.ID, events, cfg.TickInterval, cfg.StaleTimeout)

	// Start orchestrator in background.
	ctx, cancel := context.WithCancel(context.Background())
	go orch.Run(ctx)

	// Start TUI (blocks until quit).
	p := tea.NewProgram(
		tui.NewModel(database, orch, tmux, project.ID, events),
		tea.WithAltScreen(),
	)
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "tui error: %v\n", err)
	}

	// Cleanup.
	cancel()
}
