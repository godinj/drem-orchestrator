package main

// drem csuite audit — thin HTTP client for the csuite-watcher's
// /v1/deliveries and /v1/queue endpoints. Stateless; the watcher's
// ledger is the only storage (plan §Goals: single source of truth).
//
// Auth is bearer-over-file: the CLI reads ~/.drem/csuite-watcher.token
// (or the path in --token) and attaches Authorization: Bearer <tok>
// to every request. Missing or unreadable token files exit 2 with a
// diagnostic — the CLI never auto-generates the file.
//
// Output is table-on-TTY, JSON-otherwise. --format explicitly
// overrides. See plan §V1 subcommand surface for the exact column
// order.

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// defaultAuditTokenPath is the operator-facing default token
// location. Mirrors cmd/csuite-watcher's DefaultAuditTokenPath so
// the CLI and the watcher agree on where the token lives.
const defaultAuditTokenPath = "~/.drem/csuite-watcher.token"

// defaultWatcherURL is used when neither --watcher-url nor a TOML
// override is supplied. Matches the compose template's published
// port for the bridge HTTP surface.
const defaultWatcherURL = "http://127.0.0.1:8080"

// listLimitCap mirrors the watcher's auditListCap. Kept client-side
// so users who over-specify --limit see a clear message rather than
// silent clamping.
const listLimitCap = 500

// defaultListLimit is the CLI's client-side default when --limit is
// not supplied. Matches the watcher's auditListDefault so the CLI
// and the server agree on the baseline page size.
const defaultListLimit = 50

// runCsuiteAudit handles `drem csuite audit <subcommand> ...`. Args
// exclude the leading "csuite audit". Exit codes follow the plan:
// 0 success, 1 generic error, 2 token file missing/unreadable.
func runCsuiteAudit(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: drem csuite audit <list|queue> [flags]")
		return 1
	}
	switch args[0] {
	case "list":
		return runCsuiteAuditList(args[1:], stdout, stderr)
	case "queue":
		return runCsuiteAuditQueue(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown audit subcommand %q\n", args[0])
		fmt.Fprintln(stderr, "subcommands: list, queue")
		return 1
	}
}

// auditCommonFlags holds the flags present on every
// `drem csuite audit *` subcommand.
type auditCommonFlags struct {
	watcherURL string
	tokenPath  string
	format     string
}

// registerAuditCommonFlags binds --watcher-url, --token, and --format
// to fs. Keeps the three common flags identical across list/queue so
// users don't have to relearn them per subcommand.
func registerAuditCommonFlags(fs *flag.FlagSet, c *auditCommonFlags) {
	fs.StringVar(&c.watcherURL, "watcher-url", defaultWatcherURL, "watcher HTTP URL (default http://127.0.0.1:8080)")
	fs.StringVar(&c.tokenPath, "token", defaultAuditTokenPath, "path to the bearer-token file (default ~/.drem/csuite-watcher.token)")
	fs.StringVar(&c.format, "format", "", "output format: table or json (default table on TTY, json otherwise)")
}

// runCsuiteAuditList implements `drem csuite audit list`.
func runCsuiteAuditList(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var common auditCommonFlags
	registerAuditCommonFlags(fs, &common)

	from := fs.String("from", "", "filter by sender persona")
	to := fs.String("to", "", "filter by recipient persona")
	status := fs.String("status", "", "filter by status: delivered|quarantined|failed|all")
	typ := fs.String("type", "", "filter by message type: observation|request|report|decision")
	since := fs.String("since", "", "filter to entries newer than N (duration like 1h or date like 2026-04-21)")
	limit := fs.Int("limit", defaultListLimit, "maximum rows to return (capped at 500)")
	offset := fs.Int("offset", 0, "pagination offset")

	if err := fs.Parse(args); err != nil {
		return 1
	}

	tok, code := loadCLIToken(common.tokenPath, stderr)
	if code != 0 {
		return code
	}

	if *limit > listLimitCap {
		fmt.Fprintf(stderr, "--limit %d exceeds cap of %d; clamping\n", *limit, listLimitCap)
		*limit = listLimitCap
	}

	q := url.Values{}
	if *from != "" {
		q.Set("from", *from)
	}
	if *to != "" {
		q.Set("to", *to)
	}
	if *status != "" {
		q.Set("status", *status)
	}
	if *typ != "" {
		q.Set("type", *typ)
	}
	if *since != "" {
		q.Set("since", *since)
	}
	q.Set("limit", strconv.Itoa(*limit))
	if *offset > 0 {
		q.Set("offset", strconv.Itoa(*offset))
	}

	body, err := watcherGET(common.watcherURL, "/v1/deliveries", q, tok)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	return renderDeliveries(body, pickFormat(common.format, stdout), stdout, stderr)
}

// loadCLIToken reads and trims a token file, returning the token
// string and a zero exit code on success. On any failure it prints
// a diagnostic referencing the expected path to stderr and returns
// exit code 2 (the plan's "token file missing/unreadable" contract).
func loadCLIToken(path string, stderr io.Writer) (string, int) {
	expanded := expandTildePath(path)
	data, err := os.ReadFile(expanded) //nolint:gosec // user-supplied path
	if err != nil {
		fmt.Fprintf(stderr, "error: read token file %q: %v\n", expanded, err)
		fmt.Fprintln(stderr, "create the file with 0600 perms; the watcher reads it on startup")
		return "", 2
	}
	tok := strings.TrimSpace(string(data))
	if tok == "" {
		fmt.Fprintf(stderr, "error: token file %q is empty\n", expanded)
		return "", 2
	}
	return tok, 0
}

// expandTildePath replaces a leading "~" with the user's home dir.
// Kept local so this file has no dependency on main.go's expandTilde.
func expandTildePath(p string) string {
	if !strings.HasPrefix(p, "~") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, p[1:])
}

// watcherGET issues an authenticated GET against path, decoding the
// response as raw JSON bytes. Status codes outside 2xx surface as an
// error whose message includes the status and a truncated body.
func watcherGET(baseURL, path string, q url.Values, token string) ([]byte, error) {
	u, err := url.Parse(strings.TrimRight(baseURL, "/") + path)
	if err != nil {
		return nil, fmt.Errorf("parse url: %w", err)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", path, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("read %s response: %w", path, err)
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("GET %s: status %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}

// pickFormat resolves the effective output format. Empty f means
// "no explicit --format": pick table on TTY, json otherwise. If f is
// explicit (table/json) it wins unconditionally.
func pickFormat(f string, stdout io.Writer) string {
	switch f {
	case "table", "json":
		return f
	}
	if isTerminal(stdout) {
		return "table"
	}
	return "json"
}

// isTerminal reports whether w is a TTY. Only *os.File with a valid
// fd goes through the stat check; any other writer (bytes.Buffer,
// io.Discard, pipes in tests) returns false so the TTY-less default
// kicks in.
//
// The detection uses the os.ModeCharDevice bit — a terminal is a
// character device. Pipes, regular files, and sockets do not carry
// that bit, which is exactly the "non-TTY → JSON" behaviour the
// plan mandates. Keeps the CLI free of a golang.org/x/term
// dependency that the repo does not already pull in.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// renderDeliveries turns the watcher's JSON body into either a JSON
// stream (verbatim) or a formatted table, writing to stdout. Errors
// from decode/format go to stderr and return exit code 1.
func renderDeliveries(body []byte, format string, stdout, stderr io.Writer) int {
	if format == "json" {
		_, _ = stdout.Write(body)
		// Make sure output ends in a newline so shell pipelines see
		// a clean line-terminated message when the server elides it.
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

	// Column order pinned by plan §V1 subcommand surface:
	// TIME FROM TO TYPE PRIO SUBJECT STATUS ID
	const header = "TIME FROM TO TYPE PRIO SUBJECT STATUS ID"
	fmt.Fprintln(stdout, header)
	for _, r := range rows {
		id := strVal(r, "id")
		if len(id) > 8 {
			id = id[:8]
		}
		subject := strVal(r, "subject")
		if len(subject) > 40 {
			subject = subject[:37] + "..."
		}
		fmt.Fprintf(stdout, "%s %s %s %s %s %s %s %s\n",
			orDash(strVal(r, "delivered_at")),
			orDash(strVal(r, "from")),
			orDash(strVal(r, "to")),
			orDash(strVal(r, "type")),
			orDash(strVal(r, "priority")),
			orDash(subject),
			orDash(strVal(r, "status")),
			orDash(id),
		)
	}
	return 0
}

// strVal fetches the string value of m[k]. Missing or non-string
// values return "" so the caller can decide how to render them.
func strVal(m map[string]any, k string) string {
	v, ok := m[k]
	if !ok {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// orDash returns "-" for empty strings. Matches the plan's table
// example where absent fields render as "-" for visual alignment.
func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// runCsuiteAuditQueue implements `drem csuite audit queue`. The
// queue-specific implementation is in csuite_audit_queue.go to keep
// each subcommand's parsing + rendering in its own file.
//
// Declared here as a forward reference so runCsuiteAudit can dispatch
// without a circular import; the body lives in the sibling file.
var runCsuiteAuditQueue = func(args []string, stdout, stderr io.Writer) int {
	fmt.Fprintln(stderr, "queue subcommand not yet implemented")
	return 1
}
