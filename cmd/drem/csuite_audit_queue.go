package main

// drem csuite audit queue — thin HTTP client for /v1/queue.
// Shares the common flag + token + format machinery from
// csuite_audit.go so both subcommands look identical to users.

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
)

// init replaces the placeholder var in csuite_audit.go with the
// real queue implementation. The forward-reference pattern keeps
// the dispatch switch compiling in prior commits and lets the
// queue implementation ship in its own feat commit per the TDD
// discipline documented in plans/csuite-audit-cli.md.
func init() {
	runCsuiteAuditQueue = runCsuiteAuditQueueImpl
}

// runCsuiteAuditQueueImpl serves `drem csuite audit queue`. Flags
// match plan §V1 subcommand surface.
func runCsuiteAuditQueueImpl(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("queue", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var common auditCommonFlags
	registerAuditCommonFlags(fs, &common)

	agent := fs.String("agent", "", "filter by agent (persona name)")
	scope := fs.String("scope", "", "filter by scope: inbox|outbox|quarantine|all")
	stale := fs.String("stale", "", "only entries older than N (duration, e.g. 30m)")

	if err := fs.Parse(args); err != nil {
		return 1
	}

	tok, code := loadCLIToken(common.tokenPath, stderr)
	if code != 0 {
		return code
	}

	q := url.Values{}
	if *agent != "" {
		q.Set("agent", *agent)
	}
	if *scope != "" {
		q.Set("scope", *scope)
	}
	if *stale != "" {
		q.Set("stale", *stale)
	}

	body, err := watcherGET(common.watcherURL, "/v1/queue", q, tok)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	return renderQueue(body, pickFormat(common.format, stdout), stdout, stderr)
}

// renderQueue turns the watcher's JSON body into either a JSON
// stream (verbatim) or a formatted table, writing to stdout. Column
// order matches plan §V1 subcommand surface:
//
//	AGENT SCOPE COUNT OLDEST NEWEST
func renderQueue(body []byte, format string, stdout, stderr io.Writer) int {
	if format == "json" {
		_, _ = stdout.Write(body)
		if len(body) == 0 || body[len(body)-1] != '\n' {
			_, _ = stdout.Write([]byte{'\n'})
		}
		return 0
	}

	var rows []map[string]any
	if err := json.Unmarshal(body, &rows); err != nil {
		fmt.Fprintf(stderr, "error: decode response: %v\n", err)
		return 1
	}

	const header = "AGENT SCOPE COUNT OLDEST NEWEST"
	fmt.Fprintln(stdout, header)
	for _, r := range rows {
		count := 0
		if v, ok := r["count"].(float64); ok {
			count = int(v)
		}
		fmt.Fprintf(stdout, "%s %s %d %s %s\n",
			orDash(strVal(r, "agent")),
			orDash(strVal(r, "scope")),
			count,
			orDash(strVal(r, "oldest")),
			orDash(strVal(r, "newest")),
		)
	}
	return 0
}
