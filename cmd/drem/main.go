// Package main is the entry point for the Drem Orchestrator CLI.
package main

import (
	"fmt"
	"os"
)

func main() {
	cfg, err := LoadConfig("drem.toml")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	_ = cfg // TODO: wire up orchestrator, TUI, etc.
	fmt.Println("drem orchestrator — not yet implemented")
}
