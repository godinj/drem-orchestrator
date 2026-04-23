package main

// drem csuite — parent subcommand for csuite-scoped operations.
//
// Today this routes only to `audit`; future work from
// plans/csuite-audit-cli.md §Namespace placement adds approve/reject/
// status siblings when the orch-API gate commands lift under the same
// namespace. Kept as a thin dispatch so each subgroup (audit, ...)
// lives in its own file without fighting over the subcommand switch.

import (
	"fmt"
	"io"
	"os"
)

// runCsuite handles `drem csuite <subgroup> ...`. Called from main.go
// when os.Args[1] == "csuite". Exits the process via os.Exit and does
// not return.
func runCsuite() {
	args := os.Args[2:]
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: drem csuite <subgroup> [flags] [args]")
		fmt.Fprintln(os.Stderr, "subgroups: audit, send")
		os.Exit(1)
	}
	code := dispatchCsuite(args, os.Stdout, os.Stderr)
	os.Exit(code)
}

// dispatchCsuite is the testable core of runCsuite. It receives args
// (without the leading binary name and "csuite") and returns an exit
// code. All output goes through the supplied writers so tests can
// capture it without hooking os.Stdout/Stderr.
func dispatchCsuite(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: drem csuite <subgroup> [flags] [args]")
		fmt.Fprintln(stderr, "subgroups: audit, send")
		return 1
	}
	switch args[0] {
	case "audit":
		return runCsuiteAudit(args[1:], stdout, stderr)
	case "send":
		return runCsuiteSend(args[1:], os.Stdin, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown csuite subgroup %q\n", args[0])
		fmt.Fprintln(stderr, "subgroups: audit, send")
		return 1
	}
}
