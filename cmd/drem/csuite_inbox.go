package main

// drem csuite inbox — operator-inbox management companion.
//
// Phase 4 of plans/drem-csuite-send-cli.md. Sibling subgroup to
// `drem csuite send` that lets the operator browse, read, and archive
// the reply files that land in `<CsuiteHomeRoot>/operator/inbox/`
// when a persona addresses `to: operator` (Phase 1 ClassOperator
// routing at commit c823f2f).
//
// Three sub-subcommands:
//
//   list    [--json] [--archived]          — enumerate messages
//   read    <index|path> [--with-frontmatter|--json]
//                                           — print one message body
//   archive <index|path>                    — move to .archive/
//
// Sort order: ascending by `sent_at:` frontmatter, mtime fallback. A
// 1-based index is assigned AFTER the sort so the operator's natural
// reading order ("index 1 = oldest unread") matches the printed list.
//
// Formatting reuses Phase 3's formatReply (see csuite_send_format.go)
// so `drem csuite inbox read` output is byte-for-byte identical to
// `drem csuite send --wait` — one fewer mental model for the operator.
//
// DREM_CSUITE_HOME override is honoured via resolveCsuiteHomeRoot so
// tests (and power users with a non-default location) can point the
// subcommand at an arbitrary tree without touching $HOME.

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// inboxTopicMaxChars caps the topic/first-body-line column in the
// default `list` output so each row fits on one terminal line. Pairs
// with `send`'s topicMaxChars (60 runes) — the list column is a touch
// narrower (40) to leave room for filename + timestamp on the same row.
const inboxTopicMaxChars = 40

// bodyExcerptMaxChars caps the `body_excerpt` field in the --json
// payload. 200 chars is enough for the operator's automation to
// disambiguate messages without dumping the full body into every row.
const bodyExcerptMaxChars = 200

// inboxEntry is the parsed form of a single operator-inbox file.
// Frontmatter == nil means the file exists but had no (or malformed)
// frontmatter — still surfaced in `list` so the operator can archive
// it rather than silently dropped.
type inboxEntry struct {
	Index       int
	Path        string
	Frontmatter map[string]any
	Body        string
	ModTime     time.Time
}

// runCsuiteInbox dispatches drem csuite inbox <subcmd> [args]. args is
// everything after the "inbox" token.
func runCsuiteInbox(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printInboxUsage(stdout)
		return 0
	}
	switch args[0] {
	case "-h", "--help", "help":
		printInboxUsage(stdout)
		return 0
	case "list":
		return runCsuiteInboxList(args[1:], stdout, stderr)
	case "read":
		return runCsuiteInboxRead(args[1:], stdout, stderr)
	case "archive":
		return runCsuiteInboxArchive(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown inbox subcommand %q\n", args[0])
		printInboxUsage(stderr)
		return 2
	}
}

// runCsuiteInboxList implements `drem csuite inbox list [--json] [--archived]`.
func runCsuiteInboxList(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("inbox list", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var (
		asJSON   bool
		archived bool
	)
	fs.BoolVar(&asJSON, "json", false, "emit JSON array instead of tabular output")
	fs.BoolVar(&archived, "archived", false, "list files under .archive/ instead of the live inbox")

	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "error: unexpected positional argument %q\n", fs.Arg(0))
		return 2
	}

	dir, err := resolveInboxDir(archived)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	entries, err := listInboxEntries(dir)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	if asJSON {
		return renderInboxListJSON(entries, stdout, stderr)
	}
	renderInboxListText(entries, stdout)
	return 0
}

// runCsuiteInboxRead implements
// `drem csuite inbox read <index|path> [--with-frontmatter|--json]`.
// The positional <index|path> can appear before OR after the flags —
// stdlib `flag` stops at the first non-flag token, so we pre-split
// args into (flags, positionals) with splitFlagsAndPositionals.
func runCsuiteInboxRead(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("inbox read", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var (
		withFrontmatter bool
		asJSON          bool
	)
	fs.BoolVar(&withFrontmatter, "with-frontmatter", false, "print reply with full YAML frontmatter")
	fs.BoolVar(&asJSON, "json", false, "emit reply as a JSON envelope (supersedes --with-frontmatter)")

	flagArgs, positionals := splitFlagsAndPositionals(args, nil)
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	rest := append(positionals, fs.Args()...)
	if len(rest) == 0 {
		fmt.Fprintln(stderr, "error: read requires an <index|path> argument")
		return 2
	}
	if len(rest) > 1 {
		fmt.Fprintf(stderr, "error: unexpected extra arguments %v\n", rest[1:])
		return 2
	}

	mode, err := selectReplyMode(withFrontmatter, asJSON)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 2
	}

	path, code := resolveInboxTarget(rest[0], stderr)
	if code != 0 {
		return code
	}

	raw, err := os.ReadFile(path) //nolint:gosec // path resolved against the configured inbox tree
	if err != nil {
		fmt.Fprintf(stderr, "error: read %q: %v\n", path, err)
		return 1
	}

	out, err := formatReply(mode, raw, path)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	fmt.Fprint(stdout, out)
	if !strings.HasSuffix(out, "\n") {
		fmt.Fprintln(stdout)
	}
	return 0
}

// runCsuiteInboxArchive implements `drem csuite inbox archive <index|path>`.
func runCsuiteInboxArchive(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("inbox archive", flag.ContinueOnError)
	fs.SetOutput(stderr)

	flagArgs, positionals := splitFlagsAndPositionals(args, nil)
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	rest := append(positionals, fs.Args()...)
	if len(rest) == 0 {
		fmt.Fprintln(stderr, "error: archive requires an <index|path> argument")
		return 2
	}
	if len(rest) > 1 {
		fmt.Fprintf(stderr, "error: unexpected extra arguments %v\n", rest[1:])
		return 2
	}

	srcPath, code := resolveInboxTarget(rest[0], stderr)
	if code != 0 {
		return code
	}

	liveDir, err := resolveInboxDir(false)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	archiveDir := filepath.Join(liveDir, ".archive")

	if err := os.MkdirAll(archiveDir, 0o755); err != nil {
		fmt.Fprintf(stderr, "error: create archive dir %q: %v\n", archiveDir, err)
		return 1
	}

	dstPath := filepath.Join(archiveDir, filepath.Base(srcPath))
	// Refuse to clobber an existing archived file of the same name —
	// hash-collision protection against the operator shuffling files
	// by hand into .archive/ then trying to re-archive the live copy.
	if _, statErr := os.Stat(dstPath); statErr == nil {
		fmt.Fprintf(stderr, "error: destination already exists: %s\n", dstPath)
		fmt.Fprintln(stderr, "refusing to overwrite; move or delete the archived copy first")
		return 1
	} else if !errors.Is(statErr, os.ErrNotExist) {
		fmt.Fprintf(stderr, "error: stat %q: %v\n", dstPath, statErr)
		return 1
	}

	if err := os.Rename(srcPath, dstPath); err != nil {
		fmt.Fprintf(stderr, "error: archive %q → %q: %v\n", srcPath, dstPath, err)
		return 1
	}
	fmt.Fprintln(stdout, dstPath)
	return 0
}

// resolveInboxDir returns the absolute path to the inbox dir selected
// by archived. archived==true means `<root>/operator/inbox/.archive/`,
// else `<root>/operator/inbox/`. Does NOT create the dir — `list` on
// a missing dir is a legitimate empty-inbox case handled downstream.
func resolveInboxDir(archived bool) (string, error) {
	root, err := resolveCsuiteHomeRoot()
	if err != nil {
		return "", err
	}
	base := filepath.Join(root, "operator", "inbox")
	if archived {
		return filepath.Join(base, ".archive"), nil
	}
	return base, nil
}

// resolveInboxTarget maps a <index|path> argument to a concrete file
// path. Returns the resolved path and a zero exit code on success, or
// an empty path and a non-zero code (with a diagnostic already written
// to stderr) on failure.
//
// Precedence:
//   - if arg contains `/` OR os.Stat finds a file at arg: treat arg as
//     a direct path.
//   - else parse arg as a base-10 int and look up that index in the
//     live inbox listing.
func resolveInboxTarget(arg string, stderr io.Writer) (string, int) {
	// Direct-path shortcut: a slash unambiguously means "this is a
	// path, don't try to integer-parse it". Also covers the bare-stat
	// case for a relative filename in cwd.
	if strings.ContainsRune(arg, os.PathSeparator) || strings.Contains(arg, "/") {
		info, err := os.Stat(arg)
		if err != nil {
			fmt.Fprintf(stderr, "error: stat %q: %v\n", arg, err)
			return "", 2
		}
		if info.IsDir() {
			fmt.Fprintf(stderr, "error: %q is a directory\n", arg)
			return "", 2
		}
		return arg, 0
	}

	// Positive integer? → resolve via list ordering.
	n, err := strconv.Atoi(arg)
	if err == nil && n > 0 {
		dir, dirErr := resolveInboxDir(false)
		if dirErr != nil {
			fmt.Fprintf(stderr, "error: %v\n", dirErr)
			return "", 1
		}
		entries, listErr := listInboxEntries(dir)
		if listErr != nil {
			fmt.Fprintf(stderr, "error: %v\n", listErr)
			return "", 1
		}
		if n > len(entries) {
			fmt.Fprintf(stderr, "error: index %d out of range (inbox has %d entries)\n", n, len(entries))
			return "", 2
		}
		return entries[n-1].Path, 0
	}

	// Not a slash-path and not a positive int. Try one last fallback:
	// an existing file in cwd with that exact name. Lets the operator
	// copy-paste a filename out of `list` output without prefixing it.
	if info, statErr := os.Stat(arg); statErr == nil && !info.IsDir() {
		return arg, 0
	}

	fmt.Fprintf(stderr, "error: %q is neither a positive index nor an existing file\n", arg)
	return "", 2
}

// listInboxEntries reads all *.md files in dir, parses each file's
// frontmatter + body, sorts by sent_at ascending (mtime fallback for
// files missing frontmatter or a parseable sent_at), and assigns
// 1-based indexes. A missing dir is treated as an empty inbox — the
// operator has not received any replies yet — so the caller gets a
// zero-length slice and a nil error.
func listInboxEntries(dir string) ([]inboxEntry, error) {
	des, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read inbox dir %q: %w", dir, err)
	}

	out := make([]inboxEntry, 0, len(des))
	for _, de := range des {
		if de.IsDir() {
			continue
		}
		name := de.Name()
		if !strings.HasSuffix(name, ".md") {
			continue
		}
		path := filepath.Join(dir, name)

		info, statErr := de.Info()
		if statErr != nil {
			// os.ReadDir already told us the entry exists; a Lstat-race
			// that makes the file disappear between ReadDir and Info is
			// rare but benign — skip.
			continue
		}

		data, readErr := os.ReadFile(path) //nolint:gosec // operator inbox, paths under configured tree
		if readErr != nil {
			// Same race window as above. Still surface the entry so
			// `archive` can clean it up, but with no frontmatter/body.
			out = append(out, inboxEntry{
				Path:    path,
				ModTime: info.ModTime(),
			})
			continue
		}

		fm, _ := parseFrontmatterMap(data) // may return nil on malformed
		body, _ := splitBody(data)         // ok=false → body = ""

		out = append(out, inboxEntry{
			Path:        path,
			Frontmatter: fm,
			Body:        body,
			ModTime:     info.ModTime(),
		})
	}

	sort.SliceStable(out, func(i, j int) bool {
		return entrySortKey(out[i]).Before(entrySortKey(out[j]))
	})
	for i := range out {
		out[i].Index = i + 1
	}
	return out, nil
}

// entrySortKey returns the time used to order an inboxEntry: the
// parsed sent_at frontmatter if present + parseable, else the file
// mtime. Ascending-order sort (oldest first) so the operator's "index
// 1" is the oldest unread reply.
//
// yaml.v3 auto-parses RFC3339 timestamp scalars into time.Time when
// the target is `any`, so the switch covers both shapes: a raw string
// (when a persona wrote a non-RFC3339 stamp) or a pre-parsed time
// (the common path).
func entrySortKey(e inboxEntry) time.Time {
	if e.Frontmatter != nil {
		switch v := e.Frontmatter["sent_at"].(type) {
		case time.Time:
			if !v.IsZero() {
				return v
			}
		case string:
			if v != "" {
				if t, err := time.Parse(time.RFC3339, v); err == nil {
					return t
				}
			}
		}
	}
	return e.ModTime
}

// renderInboxListText emits the default (non-JSON) `list` output:
//
//	<index>  <sent_at|mtime>  <from>  <topic-or-body-first-line>  <filename>
//
// One row per entry, tab-separated for easy downstream awk/cut.
// Empty inbox → zero lines (no header).
func renderInboxListText(entries []inboxEntry, stdout io.Writer) {
	for _, e := range entries {
		fmt.Fprintf(stdout, "%d\t%s\t%s\t%s\t%s\n",
			e.Index,
			entrySortKey(e).UTC().Format(time.RFC3339),
			entryFrom(e),
			truncateRunes(entryDisplaySubject(e), inboxTopicMaxChars),
			filepath.Base(e.Path),
		)
	}
}

// renderInboxListJSON emits the --json output: a JSON array of objects
// with pinned keys (see listInboxJSONRow). Shape is stable and meant
// to be consumed by operator automation.
func renderInboxListJSON(entries []inboxEntry, stdout, stderr io.Writer) int {
	rows := make([]listInboxJSONRow, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, listInboxJSONRow{
			Index:       e.Index,
			Path:        e.Path,
			SentAt:      entrySortKey(e).UTC().Format(time.RFC3339),
			From:        entryFrom(e),
			Topic:       entryTopic(e),
			BodyExcerpt: truncateRunes(firstBodyLine(e.Body), bodyExcerptMaxChars),
		})
	}
	buf, err := json.Marshal(rows)
	if err != nil {
		fmt.Fprintf(stderr, "error: marshal json: %v\n", err)
		return 1
	}
	_, _ = stdout.Write(buf)
	_, _ = stdout.Write([]byte{'\n'})
	return 0
}

// listInboxJSONRow is the --json envelope for a single entry. Pinned
// keys match plan §Phase 4. sent_at is always formatted RFC3339 so
// consumers can parse without branching on source (frontmatter vs
// mtime).
type listInboxJSONRow struct {
	Index       int    `json:"index"`
	Path        string `json:"path"`
	SentAt      string `json:"sent_at"`
	From        string `json:"from"`
	Topic       string `json:"topic"`
	BodyExcerpt string `json:"body_excerpt"`
}

// entryFrom returns the frontmatter `from:` field or "?" when missing.
// The question mark is deliberate — it reads as "unknown sender" in a
// table column and invites the operator to inspect the file.
func entryFrom(e inboxEntry) string {
	if e.Frontmatter == nil {
		return "?"
	}
	if s, ok := e.Frontmatter["from"].(string); ok && s != "" {
		return s
	}
	return "?"
}

// entryTopic returns the frontmatter `topic:` field or "" if absent.
// Separate from entryDisplaySubject because --json callers want the
// raw topic (empty-string when missing), while the text renderer
// falls back to the body's first line for column density.
func entryTopic(e inboxEntry) string {
	if e.Frontmatter == nil {
		return ""
	}
	if s, ok := e.Frontmatter["topic"].(string); ok {
		return s
	}
	return ""
}

// entryDisplaySubject picks the best one-line descriptor for the text
// renderer: frontmatter topic if present and non-empty, else the
// body's first non-empty line. Falls back to "(no body)" only when
// both are unavailable — keeps the column aligned.
func entryDisplaySubject(e inboxEntry) string {
	if topic := entryTopic(e); topic != "" {
		return topic
	}
	if line := firstBodyLine(e.Body); line != "" {
		return line
	}
	return "(no body)"
}

// firstBodyLine returns body's first non-empty line, trimmed. Used by
// both the body_excerpt JSON field and the text-renderer fallback
// when no topic is set.
func firstBodyLine(body string) string {
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// truncateRunes returns s truncated to max runes, appending "…" when
// truncation happens. Rune-safe so multi-byte UTF-8 (emoji, accented
// chars) doesn't get cut mid-codepoint.
func truncateRunes(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}

// splitFlagsAndPositionals reorders args so stdlib `flag` (which
// stops at the first non-flag token) still sees all the flags when
// the operator types `drem csuite inbox read 2 --json`. Anything
// beginning with "-" (excluding the bare dash) is treated as a flag;
// `--` terminates flag mode.
//
// flagsTakingValue names flags whose NEXT token is the value (e.g.
// `-m VALUE`). Pass nil when the subcommand has only boolean flags.
// Unknown flags are kept in flagArgs so the subsequent fs.Parse
// produces a real diagnostic, not a silent drop into positionals.
func splitFlagsAndPositionals(args []string, flagsTakingValue map[string]bool) (flagArgs, positionals []string) {
	flagArgs = make([]string, 0, len(args))
	positionals = make([]string, 0, len(args))
	afterDoubleDash := false
	i := 0
	for i < len(args) {
		a := args[i]
		if !afterDoubleDash && a == "--" {
			afterDoubleDash = true
			flagArgs = append(flagArgs, a)
			i++
			continue
		}
		if !afterDoubleDash && strings.HasPrefix(a, "-") && a != "-" {
			flagArgs = append(flagArgs, a)
			if !strings.Contains(a, "=") && flagsTakingValue[a] {
				if i+1 < len(args) {
					flagArgs = append(flagArgs, args[i+1])
					i += 2
					continue
				}
			}
			i++
			continue
		}
		positionals = append(positionals, a)
		i++
	}
	return flagArgs, positionals
}

// printInboxUsage writes the `drem csuite inbox` help block. Matches
// the shape of printSendUsage for consistency.
func printInboxUsage(w io.Writer) {
	fmt.Fprintln(w, "drem csuite inbox — operator-inbox management")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage: drem csuite inbox {list|read|archive} [args]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	fmt.Fprintln(w, "  list [--json] [--archived]        List operator-inbox messages.")
	fmt.Fprintln(w, "  read <index|path> [--with-frontmatter|--json]")
	fmt.Fprintln(w, "                                    Print a message body.")
	fmt.Fprintln(w, "  archive <index|path>              Move a message to .archive/.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Files: ~/.drem-csuite/operator/inbox/")
	fmt.Fprintln(w, "Populated by: drem csuite send <persona> ... (persona replies land")
	fmt.Fprintln(w, "here via watcher ClassOperator routing, see")
	fmt.Fprintln(w, "plans/drem-csuite-send-cli.md).")
}
