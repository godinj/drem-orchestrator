package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/godinj/drem-orchestrator/internal/cli"
	"github.com/godinj/drem-orchestrator/internal/db"
	"github.com/godinj/drem-orchestrator/internal/orchestrator"
	"github.com/godinj/drem-orchestrator/internal/tui"
)

// gateCommands is the set of CLI subcommands that require a live orchestrator.
var gateCommands = map[string]bool{
	"approve": true,
	"reject":  true,
	"answer":  true,
	"pass":    true,
	"fail":    true,
}

// runCLI handles the "drem cli" subcommand. It loads config, opens the
// database, and delegates to the cli package. This function calls
// os.Exit and does not return.
func runCLI() {
	// Find --config flag in os.Args before the subcommand args.
	configPath := "drem.toml"
	var cliArgs []string
	args := os.Args[2:] // skip binary name and "cli"

	// Extract --json and --config from the args.
	jsonMode := false
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--json":
			jsonMode = true
		case args[i] == "--config" && i+1 < len(args):
			configPath = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--config="):
			configPath = strings.TrimPrefix(args[i], "--config=")
		default:
			cliArgs = append(cliArgs, args[i])
		}
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		os.Exit(1)
	}

	database, err := db.Init(cfg.DatabasePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "database: %v\n", err)
		os.Exit(1)
	}

	// If the subcommand is a gate command, create a minimal orchestrator
	// so the handler methods (approve, reject, answer, pass, fail) can
	// execute state transitions against the database. The host-mode
	// worktree manager is built via orchestrator.NewHostWorktreeManager,
	// so this file does not need to know about the worktree implementation.
	var orch tui.TUIOrchestrator
	if len(cliArgs) > 0 && gateCommands[cliArgs[0]] {
		var wt orchestrator.WorktreeManager
		if cfg.BareRepoPath != "" {
			wt = orchestrator.NewHostWorktreeManager(cfg.BareRepoPath, cfg.DefaultBranch)
		}
		orch = orchestrator.NewForCLI(database, wt)
	}

	if err := cli.Run(database, cliArgs, os.Stdout, jsonMode, orch, cli.WithDBPath(cfg.DatabasePath)); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
