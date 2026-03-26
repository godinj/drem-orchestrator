package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/godinj/drem-orchestrator/internal/cli"
	"github.com/godinj/drem-orchestrator/internal/db"
)

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

	if err := cli.Run(database, cliArgs, os.Stdout, jsonMode, nil); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
