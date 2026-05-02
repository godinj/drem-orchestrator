package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/godinj/drem-orchestrator/internal/cli"
	"github.com/godinj/drem-orchestrator/internal/db"
	"github.com/godinj/drem-orchestrator/internal/tui"
	"github.com/godinj/drem-orchestrator/pkg/orchclient"
)

// runCLI handles the "drem cli" subcommand. It loads config, opens the
// database for read-only subcommands (tasks, agents, failures, stats,
// experiment, etc.), and constructs an *orchclient.Client pointed at
// the containerized orchestrator's HTTP API for the five gate
// mutations (approve, reject, answer, pass, fail).
//
// Gate commands no longer open the DB directly or spin up a second
// in-process orchestrator — see plans/orch-api-gate-mutations.md §1
// for the double-writer foot-gun this closes.
//
// This function calls os.Exit and does not return.
func runCLI() {
	// Find --config flag in os.Args before the subcommand args.
	configPath := "drem.toml"
	configExplicit := false
	var cliArgs []string
	args := os.Args[2:] // skip binary name and "cli"

	// Extract --json, --config, and --orch-url from the args.
	jsonMode := false
	orchURL := os.Getenv("DREM_ORCH_URL")
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--json":
			jsonMode = true
		case args[i] == "--config" && i+1 < len(args):
			configExplicit = true
			configPath = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--config="):
			configExplicit = true
			configPath = strings.TrimPrefix(args[i], "--config=")
		case args[i] == "--orch-url" && i+1 < len(args):
			orchURL = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--orch-url="):
			orchURL = strings.TrimPrefix(args[i], "--orch-url=")
		default:
			cliArgs = append(cliArgs, args[i])
		}
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		os.Exit(1)
	}
	if shouldWarnImplicitStatsDB(configExplicit, cfg, cliArgs) {
		fmt.Fprintln(os.Stderr, "warning: drem cli stats is reading the implicit ./drem.db fallback. For live operations, use dremctl --orch-url ... --project ... status or drem cli --config <active project config> stats.")
	}

	database, err := db.Init(cfg.DatabasePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "database: %v\n", err)
		os.Exit(1)
	}

	// Construct an HTTP client aimed at the orchestrator container. The
	// five gate subcommands POST against this; read-only subcommands
	// (tasks, agents, failures, stats, ...) continue to use the DB
	// handle above — they are still considered safe because they do
	// not write. Migrating them to HTTP is Phase 4 scope.
	resolvedURL := tui.ResolveOrchURL(orchURL, cfg.OrchHTTPPort)
	gateClient := orchclient.New(resolvedURL)

	project := projectNameFromBareRepo(cfg.BareRepoPath)

	if err := cli.Run(database, cliArgs, os.Stdout, jsonMode, gateClient, project, cli.WithDBPath(cfg.DatabasePath)); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// projectNameFromBareRepo mirrors the derivation in main.go: the
// project's server-visible name is the bare-repo directory's basename
// with a trailing ".git" stripped. Kept local here so cli_cmd.go does
// not drag main.go's large set of imports into its path.
func projectNameFromBareRepo(bareRepoPath string) string {
	base := filepath.Base(bareRepoPath)
	return strings.TrimSuffix(base, ".git")
}

func shouldWarnImplicitStatsDB(configExplicit bool, cfg Config, cliArgs []string) bool {
	if configExplicit || len(cliArgs) == 0 || cliArgs[0] != "stats" {
		return false
	}
	return filepath.Clean(cfg.DatabasePath) == filepath.Clean(DefaultConfig().DatabasePath)
}
