package deliver

// /v1/queue — filesystem-backed queue state aggregator.
//
// Walks /csuite/<agent>/inbox, /csuite/<agent>/outbox, and
// /csuite/quarantine/<agent> to count .md files and bound mtimes.
// Filesystem paths never leak to the client — the wire shape carries
// only {agent, scope, count, oldest, newest} tuples, matching
// plans/csuite-audit-cli.md §V1 endpoint surface.

import (
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

// queueRow is the wire shape returned by GET /v1/queue. Only the
// aggregate counts + timestamps are surfaced — filesystem paths stay
// inside the watcher.
type queueRow struct {
	Agent  string `json:"agent"`
	Scope  string `json:"scope"`
	Count  int    `json:"count"`
	Oldest string `json:"oldest"`
	Newest string `json:"newest"`
}

// queueAgents is the closed set of recipients the watcher aggregates
// for /v1/queue. Kept explicit so stray dirs never leak into the
// response. The quarantine scope is reported per source persona.
var queueAgents = []string{"alex", "mike", "seth", "kyle", "operator"}

// queueScopes is the closed set of scopes. "inbox" and "outbox" map
// to /csuite/<agent>/<scope>; "quarantine" maps to
// /csuite/quarantine/<agent>.
var queueScopes = []string{"inbox", "outbox", "quarantine"}

// registerQueueRoute wires /v1/queue onto the handler's mux.
func (h *handler) registerQueueRoute(mux *http.ServeMux) {
	mux.Handle("/v1/queue", auditBearerAuth(h.cfg.AuditToken, http.HandlerFunc(h.listQueue)))
}

// listQueue serves GET /v1/queue. See plan §V1 endpoint surface.
//
// Reads filesystem dirs the watcher already owns. No paths leak to
// the client — only {agent, scope, count, oldest, newest} tuples.
func (h *handler) listQueue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	q := r.URL.Query()
	agentFilter := q.Get("agent")
	scopeFilter := q.Get("scope")
	if scopeFilter == "" {
		scopeFilter = "all"
	}
	var staleCutoff time.Time
	staleSet := false
	if s := q.Get("stale"); s != "" {
		if d, err := time.ParseDuration(s); err == nil {
			staleCutoff = time.Now().Add(-d)
			staleSet = true
		}
	}

	rows := h.gatherQueue(agentFilter, scopeFilter, staleSet, staleCutoff)
	writeJSONArray(w, rows)
}

// gatherQueue walks the csuite tree and returns one row per
// (agent, scope) tuple that has at least one message, filtered by
// the supplied params. Returns an empty slice when nothing matches.
func (h *handler) gatherQueue(agent, scope string, staleSet bool, staleCutoff time.Time) []queueRow {
	out := make([]queueRow, 0)
	for _, a := range queueAgents {
		if agent != "" && a != agent {
			continue
		}
		for _, s := range queueScopes {
			if scope != "" && scope != "all" && s != scope {
				continue
			}
			dir := scopeDir(a, s)
			row, ok := readQueueDir(a, s, dir, staleSet, staleCutoff)
			if !ok {
				continue
			}
			out = append(out, row)
		}
	}
	return out
}

// scopeDir returns the on-disk path for an (agent, scope) tuple.
// Uses resolveCsuitePath so tests can redirect.
func scopeDir(agent, scope string) string {
	switch scope {
	case "quarantine":
		return resolveCsuitePath("/csuite/quarantine/" + agent)
	default:
		return resolveCsuitePath("/csuite/" + agent + "/" + scope)
	}
}

// readQueueDir walks a single (agent, scope) directory and returns a
// queueRow if it contains at least one message file (after stale
// filtering). The second return is false if the row should be
// omitted (dir missing, no matching files).
//
// Only regular .md files at the top level of dir are counted —
// delivered/ and other subdirs are skipped so outbox counts reflect
// pending (un-delivered) messages only.
func readQueueDir(agent, scope, dir string, staleSet bool, staleCutoff time.Time) (queueRow, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return queueRow{}, false
	}
	var mtimes []time.Time
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		name := ent.Name()
		if !strings.HasSuffix(name, ".md") {
			continue
		}
		info, err := ent.Info()
		if err != nil {
			continue
		}
		mt := info.ModTime()
		if staleSet && mt.After(staleCutoff) {
			// --stale 30m means "only entries older than 30m";
			// newer mtimes are excluded.
			continue
		}
		mtimes = append(mtimes, mt)
	}
	if len(mtimes) == 0 {
		return queueRow{}, false
	}
	sort.Slice(mtimes, func(i, j int) bool { return mtimes[i].Before(mtimes[j]) })
	return queueRow{
		Agent:  agent,
		Scope:  scope,
		Count:  len(mtimes),
		Oldest: mtimes[0].UTC().Format(time.RFC3339),
		Newest: mtimes[len(mtimes)-1].UTC().Format(time.RFC3339),
	}, true
}
