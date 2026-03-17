package main

import (
	"fmt"
	"os"

	"github.com/godinj/drem-orchestrator/internal/constraints"
)

func main() {
	// Use current directory as worktree root.
	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	cfg, err := constraints.LoadConfig(wd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error loading constraints: %v\n", err)
		os.Exit(1)
	}
	if cfg == nil {
		fmt.Println("No .drem/constraints.toml found, nothing to check.")
		os.Exit(0)
	}

	report, err := constraints.Evaluate(cfg, wd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error evaluating constraints: %v\n", err)
		os.Exit(1)
	}

	fmt.Print(constraints.FormatReport(report))

	if report.Failed > 0 {
		os.Exit(1)
	}
}
