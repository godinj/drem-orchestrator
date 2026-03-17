// Command ctxmon provides standalone context window monitoring for Claude Code
// agents. It is used by the /swarm skill to configure and query monitoring in
// agent worktrees.
//
// Usage:
//
//	ctxmon <command> [dir]
//
// Commands:
//
//	setup    Create .claude/ with context monitoring files
//	status   Read and report context window usage (JSON)
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/godinj/drem-orchestrator/internal/ctxmon"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	cmd := os.Args[1]
	dir := "."
	if len(os.Args) >= 3 {
		dir = os.Args[2]
	}

	switch cmd {
	case "setup":
		if err := ctxmon.SetupDir(dir); err != nil {
			fmt.Fprintf(os.Stderr, "ctxmon setup: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Context monitoring configured in %s/.claude/\n", dir)

	case "status":
		usagePath := ctxmon.UsageFilePath(dir)
		u, err := ctxmon.ReadUsageFile(usagePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ctxmon status: %v\n", err)
			os.Exit(1)
		}
		if u == nil {
			fmt.Fprintln(os.Stderr, "ctxmon status: no usage data yet")
			os.Exit(2)
		}

		// Check for compaction signal.
		signalPath := ctxmon.CompactionSignalPath(dir)
		if _, err := os.Stat(signalPath); err == nil {
			u.CompactionTriggered = true
		}

		data, err := json.MarshalIndent(u, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "ctxmon status: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(data))

	default:
		fmt.Fprintf(os.Stderr, "ctxmon: unknown command %q\n", cmd)
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `Usage: ctxmon <command> [dir]

Commands:
  setup    Create .claude/ with context monitoring files
  status   Read and report context window usage (JSON)

The dir argument defaults to "." if not specified.`)
}
