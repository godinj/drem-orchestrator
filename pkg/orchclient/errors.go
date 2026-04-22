package orchclient

// Typed errors returned by the gate mutation methods (Approve, Reject,
// Pass, Fail, Answer) and by ResolveTaskID. See
// plans/orch-api-gate-mutations.md for the authoritative status-code
// contract; this file is the client-side counterpart.
//
// Callers distinguish classes of failure with errors.As, for example:
//
//	var wrong *orchclient.ErrWrongStatus
//	if errors.As(err, &wrong) {
//	    // task was in a state that couldn't accept the transition
//	}
//
// Phase 2 (CLI) uses these to pick exit codes, and Phase 3 (TUI) uses
// them to render sensible banners in place of raw HTTP noise.

import (
	"fmt"
	"strings"
)

// ErrBadRequest is returned for 400 responses — malformed UUID,
// malformed JSON body, or a missing required field (for example an
// Answer call with an empty body that reached the server anyway).
type ErrBadRequest struct{ Message string }

func (e *ErrBadRequest) Error() string { return "orchclient: bad request: " + e.Message }

// ErrNotFound is returned for 404 responses — no task with that ID in
// the given project, or the project name itself is unknown to the
// orchestrator.
type ErrNotFound struct{ Message string }

func (e *ErrNotFound) Error() string { return "orchclient: not found: " + e.Message }

// ErrWrongStatus is returned for 409 responses — the task exists but
// is in a status that cannot accept the requested transition, for
// example calling Approve on a task that is still in backlog.
type ErrWrongStatus struct{ Message string }

func (e *ErrWrongStatus) Error() string { return "orchclient: wrong status: " + e.Message }

// ErrServer is returned for 500 responses — the orchestrator's
// handler or underlying service returned an internal error.
type ErrServer struct{ Message string }

func (e *ErrServer) Error() string { return "orchclient: server error: " + e.Message }

// ErrNoMatch is returned by ResolveTaskID when no task in the
// supplied project has an ID starting with the supplied prefix. It
// is distinct from ErrNotFound (which maps to an HTTP 404) because
// the underlying ListTasks call succeeded — the prefix simply didn't
// match any row. Callers can use errors.As to distinguish "typo in
// the prefix" from transport or server failures.
type ErrNoMatch struct {
	Project string
	Prefix  string
}

func (e *ErrNoMatch) Error() string {
	return fmt.Sprintf("orchclient: no task in project %q matches prefix %q", e.Project, e.Prefix)
}

// ErrAmbiguousPrefix is returned by ResolveTaskID when two or more
// tasks share the supplied prefix. Matches holds every full UUID
// that matched so caller-side UX can render a disambiguation list.
// Error() renders at most the first five to keep output bounded; the
// full slice is still available on the struct.
type ErrAmbiguousPrefix struct {
	Project string
	Prefix  string
	Matches []string
}

func (e *ErrAmbiguousPrefix) Error() string {
	const maxShow = 5
	shown := e.Matches
	extra := 0
	if len(shown) > maxShow {
		extra = len(shown) - maxShow
		shown = shown[:maxShow]
	}
	msg := fmt.Sprintf("orchclient: prefix %q in project %q matches multiple tasks: %s",
		e.Prefix, e.Project, strings.Join(shown, ", "))
	if extra > 0 {
		msg += fmt.Sprintf(" (+%d more)", extra)
	}
	return msg
}
