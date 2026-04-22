package orchclient

// ResolveTaskID: client-side short-prefix → full-UUID resolution.
//
// The server takes full UUIDs only (see plans/orch-api-gate-mutations.md
// §2). Without this helper, CLI (Phase 2), TUI (Phase 3), and future
// csuite automation would each reinvent "list tasks, filter by prefix,
// error on ambiguity" — the three-copy risk Seth flagged in his
// 2026-04-22 design review response (§Q2). One helper, all callers
// import it, the server contract stays clean.

import (
	"context"
	"fmt"
	"strings"
)

// minPrefixLen is the shortest short-prefix we accept. Matches the
// existing drem CLI convention and prevents pathological inputs like
// "a" from matching every task whose UUID happens to start with that
// character. Callers pasting a full 36-char UUID short-circuit this
// guard; see the uuidCanonicalLen branch below.
const minPrefixLen = 4

// uuidCanonicalLen is the length of a canonical 8-4-4-4-12 UUID.
// ResolveTaskID treats any string of this length as already-resolved
// and returns it verbatim without hitting the server — the caller's
// subsequent POST will 404 if the UUID doesn't exist. Validating the
// UUID's structure is out of scope: this helper does only prefix
// expansion, not UUID-well-formedness checks.
const uuidCanonicalLen = 36

// ResolveTaskID expands a short task-ID prefix to the full UUID by
// calling ListTasks(project) and filtering. A full UUID passed
// verbatim (36-char canonical form) is returned without a server
// call. Prefix comparison is case-insensitive — UUIDs on the wire
// are lowercase-canonical, but callers often paste from sources that
// upper-cased them.
//
// Returns *ErrAmbiguousPrefix when two or more tasks match. Returns
// *ErrNoMatch when no task matches. Any transport or server error
// from ListTasks is wrapped and returned verbatim.
//
// See plans/orch-api-gate-mutations.md §Q2 and Seth's design review
// reply 2026-04-22T02:18Z for the rationale on consolidating this
// logic into one helper.
func (c *Client) ResolveTaskID(ctx context.Context, project, prefix string) (string, error) {
	if prefix == "" {
		return "", fmt.Errorf("orchclient: empty prefix")
	}
	if len(prefix) == uuidCanonicalLen {
		// Full canonical UUID — skip the server call. Callers who pass
		// garbage of length 36 will see a 404 on their next POST; that
		// is the correct layer to surface "no such task".
		return prefix, nil
	}
	if len(prefix) < minPrefixLen {
		return "", fmt.Errorf("orchclient: prefix %q below minimum length %d", prefix, minPrefixLen)
	}

	tasks, err := c.ListTasks(ctx, project, TaskFilter{})
	if err != nil {
		return "", fmt.Errorf("orchclient: resolve prefix %q: %w", prefix, err)
	}

	needle := strings.ToLower(prefix)
	var matches []string
	for _, t := range tasks {
		if strings.HasPrefix(strings.ToLower(t.ID), needle) {
			matches = append(matches, t.ID)
		}
	}

	switch len(matches) {
	case 0:
		return "", &ErrNoMatch{Project: project, Prefix: prefix}
	case 1:
		return matches[0], nil
	default:
		return "", &ErrAmbiguousPrefix{
			Project: project,
			Prefix:  prefix,
			Matches: matches,
		}
	}
}
