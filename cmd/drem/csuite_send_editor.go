package main

// drem csuite send — $EDITOR-backed body source.
//
// Phase 3 of plans/drem-csuite-send-cli.md. Seeds a tempfile with an
// instructional header, spawns the operator's $EDITOR (default vi),
// then strips the header on read. Kept in its own file so the spawner
// is an injection seam for testing without touching a real editor
// binary.
//
// Contract:
//   * Spawner is called with (editor, tempfile-path). A nil error
//     means the editor exited cleanly; the tempfile then holds the
//     operator's edited content.
//   * openEditorForBody strips leading '#'-prefixed lines AND leading
//     blank lines up to the first non-comment, non-blank line. Any '#'
//     lines that appear later in the body stay intact — the stripping
//     stops at the first piece of real content.
//   * An empty body (post-strip) is an error, not a silent no-op.
//   * The tempfile is removed best-effort on success AND on error so a
//     crashed editor doesn't leave prompt scraps scattered around
//     /tmp.

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// editorSpawner runs an editor against a pre-seeded tempfile. Returns
// nil on clean exit; otherwise an error whose message is surfaced to
// the operator verbatim. Defaults to execCommandSpawner (below); tests
// inject a pure-Go fake that simulates keystrokes by writing to the
// tempfile directly.
type editorSpawner func(editor, path string) error

// editorConfig collects the inputs for one editor invocation. All
// fields are pre-resolved by the command layer: the editor path has
// already been defaulted to "vi" when $EDITOR was empty, the topic
// has already been left as "" for auto-derivation, etc. Kept as a
// struct so adding future pre-fill fields (e.g. a signature block)
// doesn't rewrite the call site.
type editorConfig struct {
	// Persona is the destination (mike | alex | seth | kyle). Shown
	// in the instructional header so the operator never has to think
	// "wait, who am I writing to again?"
	Persona string

	// Topic is either the operator-supplied --topic value or "" when
	// the topic will be auto-derived from the body first line. Shown
	// in the header verbatim; "" renders as "<auto — first line of
	// body>" so the header documents what will happen.
	Topic string

	// CorrelationID is the 8-hex ID the waiter matches on. Surfacing
	// it in the header lets the operator grep it out of the terminal
	// scrollback if the wait times out and they want to tail the
	// inbox manually.
	CorrelationID string

	// Editor is the resolved editor command. The caller owns the
	// $EDITOR → "vi" fallback; openEditorForBody does not re-resolve.
	// Accepts any string exec.Command accepts (absolute path, bare
	// binary name on $PATH, or a single-token command; multi-token
	// commands with flags are not supported — keep $EDITOR simple).
	Editor string

	// Spawner is the injection seam. Production code leaves this nil
	// to pick up the exec-based default; tests supply a fake.
	Spawner editorSpawner
}

// openEditorForBody creates a tempfile seeded with the instructional
// header described in plan §Phase 3, spawns cfg.Editor against it,
// reads the result back, strips the header, and returns the body.
//
// Failure modes, all surfaced as typed errors:
//   - tempfile creation / seeding fails (rare; filesystem-level).
//   - editor exits non-zero (user aborted; or binary is broken).
//   - resulting body is empty or whitespace-only after stripping.
//
// The tempfile is always removed before return (best effort; an
// os.Remove error is swallowed rather than shadowed over the editor's
// exit code — we care about the user's intent, not /tmp hygiene).
func openEditorForBody(cfg editorConfig) (string, error) {
	if cfg.Persona == "" {
		return "", fmt.Errorf("editor: persona is required")
	}
	if cfg.CorrelationID == "" {
		return "", fmt.Errorf("editor: correlation id is required")
	}
	if cfg.Editor == "" {
		return "", fmt.Errorf("editor: editor command is required")
	}

	spawner := cfg.Spawner
	if spawner == nil {
		spawner = execCommandSpawner
	}

	tmp, err := os.CreateTemp("", "drem-csuite-send-*.md")
	if err != nil {
		return "", fmt.Errorf("editor: create tempfile: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	header := buildEditorHeader(cfg)
	if _, err := tmp.WriteString(header); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("editor: seed tempfile: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("editor: close tempfile: %w", err)
	}

	if err := spawner(cfg.Editor, tmpPath); err != nil {
		return "", fmt.Errorf("editor exited %v; aborting send", err)
	}

	raw, err := os.ReadFile(tmpPath) //nolint:gosec // path under os.TempDir
	if err != nil {
		return "", fmt.Errorf("editor: read back tempfile: %w", err)
	}

	body := stripEditorHeader(string(raw))
	if strings.TrimSpace(body) == "" {
		return "", fmt.Errorf("editor produced empty body; aborting send")
	}
	return body, nil
}

// buildEditorHeader composes the commented instructional preamble
// that seeds the tempfile. The exact text is spelled out in the Phase
// 3 plan; changing it is a user-visible change and needs a plan
// update first. Topic renders as "<auto — first line of body>" when
// cfg.Topic is empty so the operator sees what will happen.
func buildEditorHeader(cfg editorConfig) string {
	topic := cfg.Topic
	if topic == "" {
		topic = "<auto — first line of body>"
	}
	return "# Write your message to " + cfg.Persona + " below this line.\n" +
		"# Lines starting with '#' at the top of the file will be stripped\n" +
		"# before sending. Save and exit to send; leave the file empty (or\n" +
		"# body-only empty after stripping) to abort.\n" +
		"#\n" +
		"# Topic (optional): " + topic + "\n" +
		"# Correlation ID: " + cfg.CorrelationID + "\n"
}

// stripEditorHeader removes leading '#'-prefixed lines AND leading
// blank lines up to the first line that is neither a comment nor
// blank. Once real content begins, subsequent '#' lines (e.g. a
// markdown heading) are preserved verbatim.
//
// Line endings are normalised to '\n' internally; the returned body
// re-uses '\n' even if the input had '\r\n'. Downstream atomic write
// adds a trailing newline if missing.
func stripEditorHeader(raw string) string {
	// Normalise CRLF so split works uniformly on editors that save
	// Windows-style line endings.
	normalized := strings.ReplaceAll(raw, "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	start := 0
	for start < len(lines) {
		line := lines[start]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			start++
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			start++
			continue
		}
		break
	}
	if start >= len(lines) {
		return ""
	}
	return strings.Join(lines[start:], "\n")
}

// execCommandSpawner is the production spawner. It runs editor with
// the tempfile path as its sole argument, wiring std{in,out,err}
// through so a TTY-based editor like vi sees a real terminal.
func execCommandSpawner(editor, path string) error {
	cmd := exec.Command(editor, path) //nolint:gosec // editor is operator-provided $EDITOR
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// resolveEditor picks the editor binary from $EDITOR, falling back to
// "vi" when unset or empty. Trimmed so an accidental trailing space
// in the environment variable doesn't exec "vi " (which fails with a
// confusing "file not found" error).
func resolveEditor() string {
	if e := strings.TrimSpace(os.Getenv("EDITOR")); e != "" {
		return e
	}
	return "vi"
}
