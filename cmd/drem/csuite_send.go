package main

// drem csuite send — one-shot operator→persona messaging.
//
// Phase 2 of plans/drem-csuite-send-cli.md landed the core CLI at
// commit 2bf75f3. Phase 3 (this commit) layers:
//
//   -f, --file FILE          read body from file (≤ 64 KiB, must exist)
//   -e, --editor             open $EDITOR with an instructional header
//   --with-frontmatter       print full reply (frontmatter + body)
//   --json                   emit reply as a JSON envelope
//
// Body-source precedence (plan §Phase 3):
//
//   --message > --file > --editor > positional '-' (stdin) >
//   auto-stdin (non-TTY) > error.
//
// Supplying two or more of -m / -f / -e / '-' is a usage error.
// --with-frontmatter and --json are mutually exclusive with each other.
//
// Deferred to Phase 4: drem csuite inbox list/read/archive subcommands.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/godinj/drem-orchestrator/internal/csuite/persona"
)

// defaultSendTimeout matches the plan §6 default. A persona's `claude -p`
// turn typically finishes under a minute; 3 min leaves headroom for a
// slow OAuth-refresh or a heavy reasoning task without auto-failing a
// healthy reply.
const defaultSendTimeout = 3 * time.Minute

// topicMaxChars caps the auto-derived topic at ~60 rune width so it
// fits on a single terminal line in `drem csuite inbox list` later.
const topicMaxChars = 60

// runCsuiteSend is the top-level entry point dispatched from
// dispatchCsuite's `"send"` arm. args excludes "csuite" and "send".
// Returns an exit code: 0 success, 1 generic error, 2 bad usage, 3
// timeout.
func runCsuiteSend(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("csuite send", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printSendUsage(stderr) }

	var (
		message         string
		filePath        string
		useEditor       bool
		topic           string
		wait            bool
		noWait          bool
		timeout         time.Duration
		correlationID   string
		withFrontmatter bool
		asJSON          bool
	)
	fs.StringVar(&message, "m", "", "inline message body")
	fs.StringVar(&message, "message", "", "inline message body")
	fs.StringVar(&filePath, "f", "", "read body from file")
	fs.StringVar(&filePath, "file", "", "read body from file")
	fs.BoolVar(&useEditor, "e", false, "open $EDITOR to compose the body")
	fs.BoolVar(&useEditor, "editor", false, "open $EDITOR to compose the body")
	fs.StringVar(&topic, "t", "", "frontmatter topic (default: first body line)")
	fs.StringVar(&topic, "topic", "", "frontmatter topic (default: first body line)")
	fs.BoolVar(&wait, "wait", true, "block until reply arrives (default)")
	fs.BoolVar(&noWait, "no-wait", false, "exit after inbox drop; print filename")
	fs.DurationVar(&timeout, "timeout", defaultSendTimeout, "wait budget (Go duration syntax)")
	fs.StringVar(&correlationID, "correlation-id", "", "override auto-generated 8-hex correlation id")
	fs.BoolVar(&withFrontmatter, "with-frontmatter", false, "print reply with full YAML frontmatter")
	fs.BoolVar(&asJSON, "json", false, "emit reply as a JSON envelope (supersedes --with-frontmatter)")

	// The plan's command shape puts `<persona>` as the first positional
	// argument, but stdlib `flag` stops at the first non-flag token.
	// Pull the persona out first so flags can appear in any order
	// relative to it — matching the ergonomics of `drem csuite send
	// mike --no-wait -m "..."` vs `drem csuite send --no-wait mike ...`.
	remaining, personaName, err := extractPersonaArg(args)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		printSendUsage(stderr)
		return 2
	}
	if err := fs.Parse(remaining); err != nil {
		// flag already printed its diagnostic; echo usage for context.
		return 2
	}
	rest := fs.Args()
	if personaName == "" {
		fmt.Fprintln(stderr, "error: persona is required")
		printSendUsage(stderr)
		return 2
	}

	if !isAllowedPersona(personaName) {
		fmt.Fprintf(stderr, "error: unknown persona %q (want one of %s)\n",
			personaName, strings.Join(persona.AllowedPersonas, ", "))
		return 2
	}

	// --wait and --no-wait are mutually exclusive. --no-wait wins when
	// both are set (Go flags default `wait` to true, so we can't tell
	// an explicit `--wait` from the default; --no-wait is the explicit
	// opt-out signal).
	waitForIt := !noWait

	// Reply output mode resolution (Phase 3). Resolved up-front so a
	// --with-frontmatter + --json clash fails before we write an inbox
	// file — the operator can retry without a stale message already
	// dropped for the persona to process.
	replyMode, err := selectReplyMode(withFrontmatter, asJSON)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 2
	}

	// Pre-resolve the correlation ID so it can be surfaced in the
	// editor's instructional header (operator can grep it out of the
	// scrollback if the wait times out). Generation order is unchanged
	// otherwise.
	if correlationID == "" {
		cid, err := generateCorrelationID()
		if err != nil {
			fmt.Fprintf(stderr, "error: generate correlation id: %v\n", err)
			return 1
		}
		correlationID = cid
	}

	body, err := resolveBody(bodyResolveConfig{
		Message:       message,
		FilePath:      filePath,
		UseEditor:     useEditor,
		Positional:    rest,
		Stdin:         stdin,
		Persona:       personaName,
		Topic:         topic,
		CorrelationID: correlationID,
	})
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 2
	}
	if strings.TrimSpace(body) == "" {
		fmt.Fprintln(stderr, "error: message body is empty")
		return 2
	}

	if topic == "" {
		topic = deriveTopic(body)
	}

	csuiteHome, err := resolveCsuiteHomeRoot()
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	now := time.Now().UTC()
	path, err := writeInboxFile(writerConfig{
		CsuiteHomeRoot: csuiteHome,
		Persona:        personaName,
		Topic:          topic,
		Body:           body,
		Now:            func() time.Time { return now },
		CorrelationID:  correlationID,
	})
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	if !waitForIt {
		fmt.Fprintln(stdout, path)
		return 0
	}

	ctx := context.Background()
	_, replyPath, err := waitForReply(ctx, waiterConfig{
		OperatorInboxDir: filepath.Join(csuiteHome, "operator", "inbox"),
		CorrelationID:    correlationID,
		SentAt:           now,
		Timeout:          timeout,
	})
	if err != nil {
		if errors.Is(err, errWaiterTimeout) {
			fmt.Fprintf(stderr, "error: timed out after %s waiting for reply (correlation_id=%s)\n",
				timeout, correlationID)
			fmt.Fprintf(stderr, "  message landed at: %s\n", path)
			return 3
		}
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	// Re-read the reply file raw — the waiter only returns the body,
	// but --with-frontmatter and --json both need the YAML block too.
	// Cheap re-read: the file is ≤ 64 KiB by the watcher's own cap.
	raw, err := os.ReadFile(replyPath) //nolint:gosec // path produced by the waiter against the configured inbox
	if err != nil {
		fmt.Fprintf(stderr, "error: read reply %q: %v\n", replyPath, err)
		return 1
	}
	out, err := formatReply(replyMode, raw, replyPath)
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

// sendFlagsTakingValue is the set of flags whose NEXT argv token is
// the flag's value (`-m "foo"` form). Used by extractPersonaArg to
// skip past flag values when hunting for the persona positional.
// --help / -h are bool flags and not listed.
var sendFlagsTakingValue = map[string]bool{
	"-m":               true,
	"--message":        true,
	"-f":               true,
	"--file":           true,
	"-t":               true,
	"--topic":          true,
	"--timeout":        true,
	"--correlation-id": true,
}

// extractPersonaArg walks args and splits off the first non-flag,
// non-bare-dash token as the persona name. Returns the remaining args
// (preserving order, with the persona token removed) plus the persona
// string. Unknown flags are left in place — the subsequent fs.Parse
// call produces the definitive diagnostic if any are actually invalid.
//
// The bare `-` positional (stdin sentinel) is NOT treated as a
// persona. "--" terminates flag processing, so anything after it is a
// candidate; we still take the first such token as persona.
func extractPersonaArg(args []string) ([]string, string, error) {
	out := make([]string, 0, len(args))
	persona := ""
	afterDoubleDash := false
	i := 0
	for i < len(args) {
		a := args[i]
		if persona == "" && !afterDoubleDash && a == "--" {
			afterDoubleDash = true
			out = append(out, a)
			i++
			continue
		}
		// In flag territory, skip over flag-with-value pairs so we don't
		// mistake the value for the persona.
		if persona == "" && !afterDoubleDash && strings.HasPrefix(a, "-") && a != "-" {
			out = append(out, a)
			// `--foo=bar` carries its own value; `--foo bar` eats the
			// next token.
			if !strings.Contains(a, "=") && sendFlagsTakingValue[a] {
				if i+1 >= len(args) {
					return nil, "", fmt.Errorf("flag %q needs a value", a)
				}
				out = append(out, args[i+1])
				i += 2
				continue
			}
			i++
			continue
		}
		// First non-flag token (or any token after `--`) is the persona,
		// unless it's the bare-dash stdin sentinel.
		if persona == "" && a != "-" {
			persona = a
			i++
			continue
		}
		out = append(out, a)
		i++
	}
	return out, persona, nil
}

// isAllowedPersona checks name against persona.AllowedPersonas. Kept
// as a helper so csuite_send_test.go can exercise it without a full
// fs.Parse round-trip.
func isAllowedPersona(name string) bool {
	for _, p := range persona.AllowedPersonas {
		if p == name {
			return true
		}
	}
	return false
}

// bodyResolveConfig bundles the inputs to resolveBody. Struct form
// keeps the signature stable as new body-source flags land (one field
// per flag, rather than an ever-growing positional arg list).
type bodyResolveConfig struct {
	Message       string    // --message
	FilePath      string    // --file
	UseEditor     bool      // --editor
	Positional    []string  // remaining positional args post flag-parse
	Stdin         io.Reader // source for '-' / auto-stdin
	Persona       string    // for editor header
	Topic         string    // for editor header ("" → auto)
	CorrelationID string    // for editor header
}

// resolveBody implements the Phase 3 precedence from plan §Phase 3:
//
//	--message > --file > --editor > positional '-' (stdin) >
//	auto-stdin (non-TTY) > error.
//
// Supplying two or more of -m / -f / -e / '-' is a usage error
// ("multiple body sources specified; pick one"). A positional string
// other than "-" remains a usage error (would collide with future
// subcommand extensions).
func resolveBody(cfg bodyResolveConfig) (string, error) {
	// Dash-positional detection. Bare "-" is the stdin sentinel; any
	// other positional is a misuse.
	dashStdin := false
	switch len(cfg.Positional) {
	case 0:
		// nothing to do.
	case 1:
		if cfg.Positional[0] == "-" {
			dashStdin = true
		} else {
			return "", fmt.Errorf("unexpected positional argument %q (use -m STR or pipe via '-')", cfg.Positional[0])
		}
	default:
		return "", fmt.Errorf("unexpected positional arguments %v", cfg.Positional)
	}

	// Conflict detection: the operator must pick one explicit body
	// source. Two or more of -m / -f / -e / '-' is never ambiguous
	// enough to silently pick a winner.
	explicit := 0
	if cfg.Message != "" {
		explicit++
	}
	if cfg.FilePath != "" {
		explicit++
	}
	if cfg.UseEditor {
		explicit++
	}
	if dashStdin {
		explicit++
	}
	if explicit > 1 {
		return "", fmt.Errorf("multiple body sources specified; pick one of -m / -f / -e / '-'")
	}

	switch {
	case cfg.Message != "":
		return cfg.Message, nil

	case cfg.FilePath != "":
		return readBodyFromFile(cfg.FilePath)

	case cfg.UseEditor:
		return openEditorForBody(editorConfig{
			Persona:       cfg.Persona,
			Topic:         cfg.Topic,
			CorrelationID: cfg.CorrelationID,
			Editor:        resolveEditor(),
		})

	case dashStdin:
		data, err := io.ReadAll(cfg.Stdin)
		if err != nil {
			return "", fmt.Errorf("read stdin: %w", err)
		}
		return string(data), nil
	}

	// No explicit body source. Auto-read stdin when not a TTY.
	if f, ok := cfg.Stdin.(*os.File); ok {
		info, statErr := f.Stat()
		if statErr == nil && info.Mode()&os.ModeCharDevice == 0 && info.Size() >= 0 {
			data, err := io.ReadAll(f)
			if err != nil {
				return "", fmt.Errorf("read stdin: %w", err)
			}
			return string(data), nil
		}
	}
	return "", fmt.Errorf("no body source; pass -m, -f, -e, or pipe via stdin")
}

// readBodyFromFile reads path, enforcing the 64 KiB cap and rejecting
// a missing file with a clean diagnostic. Directories and non-regular
// files fall out naturally through os.Stat / os.ReadFile — the first
// error wins and surfaces to the operator. Plan §Phase 3 names an
// exists + size check; anything else (symlink-to-outside-HOME, etc.)
// stays out of scope for v1.
func readBodyFromFile(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("read body file %q: %w", path, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("read body file %q: is a directory", path)
	}
	if info.Size() > maxBodyBytes {
		return "", fmt.Errorf("read body file %q: too large (%d bytes, max %d)", path, info.Size(), maxBodyBytes)
	}
	data, err := os.ReadFile(path) //nolint:gosec // operator-supplied path is intentional
	if err != nil {
		return "", fmt.Errorf("read body file %q: %w", path, err)
	}
	return string(data), nil
}

// deriveTopic produces a default topic from the body's first non-empty
// line, truncated to topicMaxChars runes, with trailing punctuation
// and whitespace trimmed for readability.
func deriveTopic(body string) string {
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		runes := []rune(trimmed)
		if len(runes) > topicMaxChars {
			runes = runes[:topicMaxChars]
		}
		out := strings.TrimRightFunc(string(runes), func(r rune) bool {
			return unicode.IsSpace(r) || unicode.IsPunct(r)
		})
		if out == "" {
			// Punctuation-only line (e.g. "---"): skip and try next.
			continue
		}
		return out
	}
	return "message"
}

// generateCorrelationID returns an 8-char lowercase-hex string from
// crypto/rand. Same entropy shape as the plan §7 specifies.
func generateCorrelationID() (string, error) {
	var buf [4]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

// resolveCsuiteHomeRoot mirrors internal/cli.DefaultKyleInboxDir's
// env/home precedence so CLI subcommands agree on where the csuite
// tree lives. DREM_CSUITE_HOME wins; fallback is $HOME/.drem-csuite.
func resolveCsuiteHomeRoot() (string, error) {
	if root := os.Getenv("DREM_CSUITE_HOME"); root != "" {
		return root, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".drem-csuite"), nil
}

// printSendUsage writes the help block to w. Matches the shape of
// cmd/drem/csuite_audit.go's per-subcommand usage and sticks to the
// Phase 2 surface — deferred flags are not mentioned here.
func printSendUsage(w io.Writer) {
	fmt.Fprintln(w, "drem csuite send — one-shot persona messaging with reply wait")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  drem csuite send <persona> [body source] [options]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Persona (required, one of):")
	fmt.Fprintln(w, "  kyle | mike | alex | seth")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Body source (pick one):")
	fmt.Fprintln(w, "  -m, --message STR     inline message body")
	fmt.Fprintln(w, "  -f, --file FILE       read body from file (≤ 64 KiB)")
	fmt.Fprintln(w, "  -e, --editor          open $EDITOR (default vi) with an instructional header")
	fmt.Fprintln(w, "  -                     read body from stdin (positional)")
	fmt.Fprintln(w, "  (none)                read stdin when piped, else error")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Options:")
	fmt.Fprintln(w, "  -t, --topic TOPIC     frontmatter topic (default: first body line)")
	fmt.Fprintln(w, "      --wait            block until reply arrives (default)")
	fmt.Fprintln(w, "      --no-wait         exit after inbox drop; print written filename")
	fmt.Fprintln(w, "      --timeout DUR     wait budget (Go duration; default 3m)")
	fmt.Fprintln(w, "      --correlation-id ID  override auto-generated 8-hex corrid")
	fmt.Fprintln(w, "      --with-frontmatter   print reply with full YAML frontmatter")
	fmt.Fprintln(w, "      --json               emit reply as a JSON envelope")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Exit codes:")
	fmt.Fprintln(w, "  0  success")
	fmt.Fprintln(w, "  1  generic error")
	fmt.Fprintln(w, "  2  bad usage / invalid arguments")
	fmt.Fprintln(w, "  3  wait timed out (persona did not reply within --timeout)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Examples:")
	fmt.Fprintln(w, "  drem csuite send mike -m \"status of Pod 1?\"")
	fmt.Fprintln(w, "  my-report | drem csuite send alex -")
	fmt.Fprintln(w, "  drem csuite send seth --no-wait -m \"review when free\"")
}
