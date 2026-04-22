package cli

// kyle_inbox.go implements the `drem cli kyle inbox` subcommand — a
// filesystem-rooted reader for messages operators have sent (or routed)
// to Kyle's personal inbox at <CsuiteHomeRoot>/kyle/inbox/. Scoreboard
// item 4 / attack plan §3 Group A.
//
// # Why a CLI subcommand, not a daemon?
//
// Pass-2 framing (Seth): "Kyle is a binary, not a claude-looped
// persona." The C-Suite personas run in containers with a polling
// loop that invokes `claude -p` per message; Kyle is the operator
// (human), not an automated agent. Forcing Kyle's inbox into a
// polling loop would invert the symmetry — the operator doesn't
// need a bot re-reading their inbox, they need a command they can
// run from the shell to see what's new.
//
// # Subcommand surface
//
//   drem cli kyle inbox            — equivalent to `--list`
//   drem cli kyle inbox --list     — list unread messages (one row each)
//   drem cli kyle inbox --read <n> — print the body of message index <n>
//   drem cli kyle inbox --count    — emit a single integer (unread count)
//   drem cli kyle inbox --archive <n> — move message <n> to .archive/
//
// All subcommands respect --json for machine-readable output.
//
// # Filesystem contract
//
// Kyle's inbox lives at $HOME/.drem-csuite/kyle/inbox/ by default;
// overridable via DREM_CSUITE_HOME or --inbox-dir. Files are ordered
// by filename (which is conventionally ISO-8601 timestamp-prefixed)
// so mtime is secondary.

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"gopkg.in/yaml.v3"
)

// KyleInboxMessage is the shape returned by --list (one per .md file
// in the kyle inbox). Kept minimal on purpose — deep parsing happens
// only when the operator runs --read.
type KyleInboxMessage struct {
	// Index is 1-based (operator-facing) so `--read 1` reads the
	// oldest; mirrors the UX of `mail` / `mutt`.
	Index int `json:"index"`
	// Filename is the raw basename on disk.
	Filename string `json:"filename"`
	// From is the frontmatter `from:` field, empty if the file has no
	// frontmatter or the field is absent.
	From string `json:"from"`
	// Subject is the frontmatter `subject:` field, empty if absent.
	Subject string `json:"subject"`
	// Timestamp is the frontmatter `timestamp:` field (raw string — no
	// parsing attempt; operators see what the sender sent).
	Timestamp string `json:"timestamp"`
	// Priority is the frontmatter `priority:` field.
	Priority string `json:"priority"`
	// Type is the frontmatter `type:` field.
	Type string `json:"type"`
	// TLDR is the frontmatter `tldr:` field, truncated to 120 chars
	// for the table view.
	TLDR string `json:"tldr,omitempty"`
}

// inboxFrontmatter is the subset of a csuite message's YAML
// frontmatter that the inbox reader cares about. Unknown fields in
// the source file are ignored.
type inboxFrontmatter struct {
	From      string `yaml:"from"`
	To        string `yaml:"to"`
	Subject   string `yaml:"subject"`
	Timestamp string `yaml:"timestamp"`
	Priority  string `yaml:"priority"`
	Type      string `yaml:"type"`
	TLDR      string `yaml:"tldr"`
}

// RunKyleInbox is the top-level dispatcher for `drem cli kyle inbox
// ...`. args holds every argument AFTER "inbox". stdout is where
// output goes; stderr gets only flag-parse-level errors via the
// returned error.
func RunKyleInbox(inboxDir string, args []string, w io.Writer, jsonMode bool) error {
	// Extract flags. We accept --list, --read <n>, --count, --archive <n>.
	// Unknown flags short-circuit to help output.
	var (
		list    bool
		count   bool
		readN   int
		readSet bool
		archN   int
		archSet bool
	)

	i := 0
	for i < len(args) {
		a := args[i]
		switch a {
		case "--list", "-l":
			list = true
			i++
		case "--count", "-c":
			count = true
			i++
		case "--read", "-r":
			if i+1 >= len(args) {
				return fmt.Errorf("--read requires a message index")
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil {
				return fmt.Errorf("--read: invalid index %q", args[i+1])
			}
			readN = n
			readSet = true
			i += 2
		case "--archive":
			if i+1 >= len(args) {
				return fmt.Errorf("--archive requires a message index")
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil {
				return fmt.Errorf("--archive: invalid index %q", args[i+1])
			}
			archN = n
			archSet = true
			i += 2
		default:
			return fmt.Errorf("unknown flag %q; see `drem cli kyle inbox --help`", a)
		}
	}

	// --count and --read are mutually exclusive with --list (they are
	// different modes). Default mode (no flags) is --list.
	if !list && !count && !readSet && !archSet {
		list = true
	}

	msgs, err := scanKyleInbox(inboxDir)
	if err != nil {
		return err
	}

	switch {
	case count:
		return writeKyleCount(w, len(msgs), jsonMode)
	case readSet:
		return readKyleMessage(inboxDir, msgs, readN, w)
	case archSet:
		return archiveKyleMessage(inboxDir, msgs, archN, w, jsonMode)
	case list:
		return listKyleInbox(msgs, w, jsonMode)
	default:
		return fmt.Errorf("unreachable: flag routing bug")
	}
}

// scanKyleInbox lists unread .md files in inboxDir and parses each
// file's frontmatter enough to populate a KyleInboxMessage. Files
// under .archive/ are NOT included — that's the "read pile".
// Ordering is filename-ascending so timestamp-prefixed filenames sort
// oldest-first, and the 1-based index in the returned slice is stable
// across invocations as long as no new files arrive between them.
func scanKyleInbox(inboxDir string) ([]KyleInboxMessage, error) {
	entries, err := os.ReadDir(inboxDir)
	if err != nil {
		return nil, fmt.Errorf("read kyle inbox %q: %w", inboxDir, err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	msgs := make([]KyleInboxMessage, 0, len(names))
	for idx, name := range names {
		msg := KyleInboxMessage{
			Index:    idx + 1,
			Filename: name,
		}
		fm, ok := parseInboxFrontmatter(filepath.Join(inboxDir, name))
		if ok {
			msg.From = fm.From
			msg.Subject = fm.Subject
			msg.Timestamp = fm.Timestamp
			msg.Priority = fm.Priority
			msg.Type = fm.Type
			msg.TLDR = fm.TLDR
			if len(msg.TLDR) > 120 {
				msg.TLDR = msg.TLDR[:117] + "..."
			}
		}
		msgs = append(msgs, msg)
	}
	return msgs, nil
}

// parseInboxFrontmatter reads the first ~8 KiB of path and decodes
// any YAML frontmatter block. A file without frontmatter returns
// (zero-value, false). Decode errors are swallowed — the operator
// CLI doesn't choke on a malformed file, it just shows it with
// empty fields.
func parseInboxFrontmatter(path string) (inboxFrontmatter, bool) {
	f, err := os.Open(path) //nolint:gosec // operator-selected path
	if err != nil {
		return inboxFrontmatter{}, false
	}
	defer f.Close()
	buf := make([]byte, 8*1024)
	n, _ := f.Read(buf)
	data := buf[:n]

	const open = "---\n"
	if !strings.HasPrefix(string(data), open) {
		return inboxFrontmatter{}, false
	}
	rest := data[len(open):]
	end := strings.Index(string(rest), "\n---")
	if end < 0 {
		return inboxFrontmatter{}, false
	}
	fm := rest[:end]
	var parsed inboxFrontmatter
	if err := yaml.Unmarshal(fm, &parsed); err != nil {
		return inboxFrontmatter{}, false
	}
	return parsed, true
}

// listKyleInbox renders either a JSON array (jsonMode) or a
// tab-aligned table of unread messages.
func listKyleInbox(msgs []KyleInboxMessage, w io.Writer, jsonMode bool) error {
	if jsonMode {
		return json.NewEncoder(w).Encode(msgs)
	}
	if len(msgs) == 0 {
		fmt.Fprintln(w, "kyle inbox is empty.")
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "#\tFROM\tTIMESTAMP\tPRIORITY\tTYPE\tSUBJECT")
	for _, m := range msgs {
		from := m.From
		if from == "" {
			from = "(no frontmatter)"
		}
		subject := m.Subject
		if subject == "" {
			subject = m.Filename
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\n",
			m.Index, from, m.Timestamp, m.Priority, m.Type, subject)
	}
	return tw.Flush()
}

// readKyleMessage emits the raw body of message #n to w. `n` is
// 1-based (operator-facing).
func readKyleMessage(inboxDir string, msgs []KyleInboxMessage, n int, w io.Writer) error {
	if n < 1 || n > len(msgs) {
		return fmt.Errorf("message index %d out of range (have %d messages)", n, len(msgs))
	}
	path := filepath.Join(inboxDir, msgs[n-1].Filename)
	body, err := os.ReadFile(path) //nolint:gosec // operator-selected path
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	_, err = w.Write(body)
	return err
}

// writeKyleCount emits a single integer (JSON or plaintext) with the
// current unread count. Useful for prompt-line integrations.
func writeKyleCount(w io.Writer, n int, jsonMode bool) error {
	if jsonMode {
		return json.NewEncoder(w).Encode(map[string]int{"count": n})
	}
	_, err := fmt.Fprintln(w, n)
	return err
}

// archiveKyleMessage moves a read message into inboxDir/.archive/ so
// it no longer appears in --list. If .archive/ doesn't exist yet,
// create it (0700). Returns an error on index out of range or
// rename failure.
func archiveKyleMessage(inboxDir string, msgs []KyleInboxMessage, n int, w io.Writer, jsonMode bool) error {
	if n < 1 || n > len(msgs) {
		return fmt.Errorf("message index %d out of range (have %d messages)", n, len(msgs))
	}
	archive := filepath.Join(inboxDir, ".archive")
	if err := os.MkdirAll(archive, 0o700); err != nil {
		return fmt.Errorf("mkdir archive: %w", err)
	}
	src := filepath.Join(inboxDir, msgs[n-1].Filename)
	dst := filepath.Join(archive, msgs[n-1].Filename)
	if err := os.Rename(src, dst); err != nil {
		return fmt.Errorf("archive: %w", err)
	}
	if jsonMode {
		return json.NewEncoder(w).Encode(map[string]string{
			"status":   "archived",
			"filename": msgs[n-1].Filename,
		})
	}
	fmt.Fprintf(w, "archived: %s\n", msgs[n-1].Filename)
	return nil
}

// DefaultKyleInboxDir returns the conventional Kyle inbox path for
// the current operator. Honors DREM_CSUITE_HOME when set, otherwise
// falls back to $HOME/.drem-csuite/kyle/inbox.
func DefaultKyleInboxDir() string {
	if root := os.Getenv("DREM_CSUITE_HOME"); root != "" {
		return filepath.Join(root, "kyle", "inbox")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		// Fall through to a bare relative path — will fail at read
		// time with a clear error.
		return filepath.Join(".drem-csuite", "kyle", "inbox")
	}
	return filepath.Join(home, ".drem-csuite", "kyle", "inbox")
}
