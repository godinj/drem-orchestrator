package orchclient_test

// Tests for Client.ResolveTaskID (Phase 1c of the orch API gate-mutation
// pivot). The helper expands a short task-ID prefix to a full UUID by
// calling ListTasks(project) and filtering. Consolidating this logic
// into one helper prevents the three-copy risk Seth flagged in his
// 2026-04-22 design review (§Q2): CLI (Phase 2), TUI (Phase 3), and
// future csuite automation would otherwise each reinvent list-then-match.
//
// See plans/orch-api-gate-mutations.md for the server contract ("full
// UUID only") that motivates this helper.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/pkg/orchclient"
)

// tasksListHandler serves a canned JSON array on GET
// /projects/{name}/tasks and records whether it was called. It is the
// minimal stub needed to drive ResolveTaskID.
type tasksListHandler struct {
	calls  int32
	status int
	body   string
}

func (h *tasksListHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	atomic.AddInt32(&h.calls, 1)
	w.Header().Set("Content-Type", "application/json")
	if h.status == 0 {
		h.status = http.StatusOK
	}
	w.WriteHeader(h.status)
	_, _ = io.WriteString(w, h.body)
}

func newResolveClient(t *testing.T, h *tasksListHandler) (*orchclient.Client, *httptest.Server) {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return orchclient.New(ts.URL), ts
}

// listBody renders a []TaskDTO JSON array inline using only the ID
// field (the only field ResolveTaskID inspects). Keeping this local
// and minimal avoids importing orchdto into every test.
func listBody(ids ...string) string {
	var b strings.Builder
	b.WriteString("[")
	for i, id := range ids {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"id":"`)
		b.WriteString(id)
		b.WriteString(`","title":"t","status":"backlog","created_at":"2026-04-20T10:00:00Z","updated_at":"2026-04-20T11:00:00Z","assigned_worker":""}`)
	}
	b.WriteString("]")
	return b.String()
}

// TestResolveTaskIDEmptyPrefixErrorsWithoutRoundTrip asserts that an
// empty prefix fails client-side before any HTTP call — we never want
// to DoS ListTasks for obviously-bad input.
func TestResolveTaskIDEmptyPrefixErrorsWithoutRoundTrip(t *testing.T) {
	h := &tasksListHandler{status: http.StatusOK, body: "[]"}
	c, _ := newResolveClient(t, h)

	_, err := c.ResolveTaskID(context.Background(), "canvas", "")
	require.Error(t, err)
	require.Contains(t, strings.ToLower(err.Error()), "empty prefix")
	require.Equal(t, int32(0), atomic.LoadInt32(&h.calls),
		"expected no network call for empty prefix")
}

// TestResolveTaskIDTooShortPrefixErrorsWithoutRoundTrip asserts the
// <4-char guard. Matches drem CLI convention; prevents a naked "a" from
// matching every task that starts with the letter a.
func TestResolveTaskIDTooShortPrefixErrorsWithoutRoundTrip(t *testing.T) {
	h := &tasksListHandler{status: http.StatusOK, body: "[]"}
	c, _ := newResolveClient(t, h)

	_, err := c.ResolveTaskID(context.Background(), "canvas", "abc")
	require.Error(t, err)
	require.Contains(t, strings.ToLower(err.Error()), "minimum")
	require.Equal(t, int32(0), atomic.LoadInt32(&h.calls),
		"expected no network call for too-short prefix")
}

// TestResolveTaskIDFullUUIDReturnedVerbatim asserts that a 36-char
// canonical UUID short-circuits the server call. Validating the UUID
// against the server is explicitly not this helper's job — if it
// doesn't exist, the caller's next POST will 404.
func TestResolveTaskIDFullUUIDReturnedVerbatim(t *testing.T) {
	h := &tasksListHandler{status: http.StatusOK, body: "[]"}
	c, _ := newResolveClient(t, h)

	full := "11111111-2222-3333-4444-555555555555"
	got, err := c.ResolveTaskID(context.Background(), "canvas", full)
	require.NoError(t, err)
	require.Equal(t, full, got)
	require.Equal(t, int32(0), atomic.LoadInt32(&h.calls),
		"expected no network call for full UUID input")
}

// TestResolveTaskIDSingleMatchReturnsFullUUID drives the happy path:
// one task whose ID starts with the supplied prefix, no others.
func TestResolveTaskIDSingleMatchReturnsFullUUID(t *testing.T) {
	target := "abcdef01-2222-3333-4444-555555555555"
	other := "9999aaaa-2222-3333-4444-555555555555"
	h := &tasksListHandler{status: http.StatusOK, body: listBody(target, other)}
	c, _ := newResolveClient(t, h)

	got, err := c.ResolveTaskID(context.Background(), "canvas", "abcdef01")
	require.NoError(t, err)
	require.Equal(t, target, got)
}

func TestResolveTaskIDPaginatesUntilMatch(t *testing.T) {
	target := "abcdef01-2222-3333-4444-555555555555"
	var queries []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("offset") == "500" {
			_, _ = io.WriteString(w, listBody(target))
			return
		}
		ids := make([]string, 0, 500)
		for i := 0; i < 500; i++ {
			ids = append(ids, fmt.Sprintf("%08x-2222-3333-4444-555555555555", i))
		}
		_, _ = io.WriteString(w, listBody(ids...))
	}))
	defer ts.Close()
	c := orchclient.New(ts.URL)

	got, err := c.ResolveTaskID(context.Background(), "canvas", "abcdef01")
	require.NoError(t, err)
	require.Equal(t, target, got)
	require.Len(t, queries, 2)
	require.Contains(t, queries[0], "limit=500")
	require.NotContains(t, queries[0], "offset=")
	require.Contains(t, queries[1], "limit=500")
	require.Contains(t, queries[1], "offset=500")
}

// TestResolveTaskIDMultipleMatchesReturnsErrAmbiguousPrefix covers the
// disambiguation UX: two or more tasks match, caller gets a typed error
// with a bounded Matches slice so higher-level code can render richer
// output later.
func TestResolveTaskIDMultipleMatchesReturnsErrAmbiguousPrefix(t *testing.T) {
	a := "abcd1111-2222-3333-4444-555555555555"
	b := "abcd2222-2222-3333-4444-555555555555"
	c2 := "abcd3333-2222-3333-4444-555555555555"
	h := &tasksListHandler{status: http.StatusOK, body: listBody(a, b, c2)}
	c, _ := newResolveClient(t, h)

	_, err := c.ResolveTaskID(context.Background(), "canvas", "abcd")
	require.Error(t, err)
	var amb *orchclient.ErrAmbiguousPrefix
	require.True(t, errors.As(err, &amb),
		"want *ErrAmbiguousPrefix, got %T: %v", err, err)
	require.ElementsMatch(t, []string{a, b, c2}, amb.Matches)
}

// TestResolveTaskIDNoMatchReturnsErrNoMatch asserts the "zero results"
// path maps to a dedicated typed error so callers can distinguish
// "prefix typo" from "server error" without parsing strings.
func TestResolveTaskIDNoMatchReturnsErrNoMatch(t *testing.T) {
	other := "9999aaaa-2222-3333-4444-555555555555"
	h := &tasksListHandler{status: http.StatusOK, body: listBody(other)}
	c, _ := newResolveClient(t, h)

	_, err := c.ResolveTaskID(context.Background(), "canvas", "deadbeef")
	require.Error(t, err)
	var nm *orchclient.ErrNoMatch
	require.True(t, errors.As(err, &nm),
		"want *ErrNoMatch, got %T: %v", err, err)
}

// TestResolveTaskIDServerErrorWrapped asserts that a transport or
// status error from ListTasks is surfaced — never swallowed — so the
// caller doesn't confuse "orchestrator is down" with "no match".
func TestResolveTaskIDServerErrorWrapped(t *testing.T) {
	h := &tasksListHandler{status: http.StatusInternalServerError, body: `{"error":"db exploded"}`}
	c, _ := newResolveClient(t, h)

	_, err := c.ResolveTaskID(context.Background(), "canvas", "abcd1234")
	require.Error(t, err)
	// ListTasks uses the plain GET helper which wraps non-2xx into a
	// generic error containing the status code — that shape is enough
	// here; we just assert the error propagated.
	require.Contains(t, err.Error(), "500")
}

// TestResolveTaskIDCaseInsensitivePrefix accepts upper-case prefix
// input as a convenience; UUIDs on the wire are lowercase-canonical.
// Saves callers from having to normalize every clipboard paste.
func TestResolveTaskIDCaseInsensitivePrefix(t *testing.T) {
	target := "abcdef01-2222-3333-4444-555555555555"
	h := &tasksListHandler{status: http.StatusOK, body: listBody(target)}
	c, _ := newResolveClient(t, h)

	got, err := c.ResolveTaskID(context.Background(), "canvas", "ABCDEF01")
	require.NoError(t, err)
	require.Equal(t, target, got)
}
