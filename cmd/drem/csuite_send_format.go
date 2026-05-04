package main

// drem csuite send — reply output formatter.
//
// Phase 3 of plans/drem-csuite-send-cli.md. Given the raw bytes of a
// reply file (frontmatter + body) and a mode selector, render the
// form the operator asked for: body-only (default), the full raw
// content ("with_frontmatter"), or a JSON envelope.
//
// Kept in its own file so tests can exercise each mode without
// standing up the waiter / writer pipeline — the formatter is a pure
// function of its inputs.

import (
	"encoding/json"
	"fmt"

	"gopkg.in/yaml.v3"
)

// Reply output modes. Kept as typed consts rather than stringly-
// matched magic values so a typo in csuite_send.go fails at compile
// time, not at runtime.
const (
	replyModeBody            = "body"
	replyModeWithFrontmatter = "with_frontmatter"
	replyModeJSON            = "json"
)

// jsonReplyEnvelope is the --json output schema pinned in plan §Phase
// 3. `frontmatter` is a free-form map so arbitrary persona-added keys
// survive the round-trip; `body` preserves verbatim newlines (JSON
// escapes them automatically, and the operator's jq/python reader
// un-escapes on the other side). `path` is included when non-empty
// so `--no-wait` callers can correlate to the source file.
type jsonReplyEnvelope struct {
	Frontmatter map[string]any `json:"frontmatter"`
	Body        string         `json:"body"`
	Path        string         `json:"path,omitempty"`
}

// formatReply renders raw per the chosen mode. path is included only
// when mode == "json" AND the caller supplied a non-empty value (the
// body/with_frontmatter modes ignore it).
//
// Error conditions:
//   - unknown mode → "unknown reply mode %q".
//   - raw is malformed (no frontmatter delimiters) AND mode wants the
//     frontmatter dict → "reply has no frontmatter".
//   - JSON marshalling fails (should never happen for map[string]any
//   - string, but guarded for completeness).
//
// Modes:
//   - "body": returns the body text with a guaranteed trailing "\n"
//     so `drem csuite send ... | cat` doesn't leave a dangling line.
//   - "with_frontmatter": returns raw as a string, trailing "\n"
//     ensured for the same reason.
//   - "json": returns a single-line JSON object, no trailing newline
//     (the command layer adds one via fmt.Fprintln).
func formatReply(mode string, raw []byte, path string) (string, error) {
	switch mode {
	case replyModeBody, "":
		body, ok := splitBody(raw)
		if !ok {
			// No frontmatter at all — treat the whole payload as body.
			// Covers degraded-persona replies that skip the YAML block.
			body = string(raw)
		}
		return ensureTrailingNewline(body), nil

	case replyModeWithFrontmatter:
		s := string(raw)
		return ensureTrailingNewline(s), nil

	case replyModeJSON:
		fm, err := parseFrontmatterMap(raw)
		if err != nil {
			return "", err
		}
		body, ok := splitBody(raw)
		if !ok {
			// Without a frontmatter close, the body is unknowable.
			// Surface as empty rather than silently attaching the
			// whole file — JSON consumers expect the envelope shape
			// to hold even if one field is ""
			body = ""
		}
		env := jsonReplyEnvelope{
			Frontmatter: fm,
			Body:        body,
			Path:        path,
		}
		out, err := json.Marshal(env)
		if err != nil {
			return "", fmt.Errorf("format: marshal json envelope: %w", err)
		}
		return string(out), nil

	default:
		return "", fmt.Errorf("unknown reply mode %q", mode)
	}
}

// parseFrontmatterMap extracts the YAML frontmatter bytes from raw
// and decodes them into a free-form map. Unlike the waiter's typed
// replyFrontmatter struct (which pulls only to/in_reply_to), this
// preserves every key the persona included — topic, sent_at, from,
// and any experimental fields future prompts may add.
//
// Returns an error if the frontmatter delimiters are missing, since
// the JSON envelope contract promises a frontmatter object. Callers
// that tolerate malformed replies should use the body-only mode.
func parseFrontmatterMap(raw []byte) (map[string]any, error) {
	fmBytes, ok := extractFrontmatterBytes(raw)
	if !ok {
		return nil, fmt.Errorf("reply has no frontmatter")
	}
	out := map[string]any{}
	if err := yaml.Unmarshal(fmBytes, &out); err != nil {
		return nil, fmt.Errorf("format: parse frontmatter yaml: %w", err)
	}
	return out, nil
}

// selectReplyMode centralises the body/with-frontmatter/json flag
// resolution so csuite_send.go's run function stays readable. Both
// booleans can be false (body-only default); both-true is a conflict
// surfaced as an error so the operator's intent is explicit.
func selectReplyMode(withFrontmatter, asJSON bool) (string, error) {
	if withFrontmatter && asJSON {
		return "", fmt.Errorf("--with-frontmatter and --json are mutually exclusive")
	}
	switch {
	case asJSON:
		return replyModeJSON, nil
	case withFrontmatter:
		return replyModeWithFrontmatter, nil
	default:
		return replyModeBody, nil
	}
}
