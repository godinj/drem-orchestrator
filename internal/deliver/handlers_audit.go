package deliver

// Audit endpoints: GET /v1/deliveries and GET /v1/queue.
//
// The audit surface is designed in plans/csuite-audit-cli.md. It is
// the read-only HTTP backend for `drem csuite audit list` and `drem
// csuite audit queue`. Both endpoints require bearer auth against a
// separate token file (~/.drem/csuite-watcher.token) so that a leak
// of the operator's audit token cannot be used to post deliveries,
// and vice versa — the /deliver path's X-Csuite-Token secret has a
// different audience (in-network personas).
//
// Split out of deliver.go so the deliver package's primary pipeline
// (/deliver, /rescan, /healthz) stays focused. Per the constitution
// check in plans/csuite-audit-cli.md §Constitution fit: deliver.go
// is 368 lines at dispatch time, well under the 800-line cap, so a
// split is not forced by size — it is forced by separation of
// concerns. The handlers here only read from the ledger and
// filesystem; they never write.
//
// This file owns the shared middleware + the /v1/deliveries handler.
// The /v1/queue handler lives in handlers_audit_queue.go.

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// auditAuthHeader + auditBearerPfx are the HTTP header name and
// token prefix the `drem csuite audit` CLI sets. Distinct from
// tokenHeader (X-Csuite-Token) used by the persona-side /deliver
// path so a leak of one secret does not compromise the other.
const (
	auditAuthHeader = "Authorization"
	auditBearerPfx  = "Bearer "
)

// auditListCap is the hard upper bound on /v1/deliveries limit.
// Requests asking for more get clamped silently — the CLI's own
// --limit flag enforces the same cap client-side, so a well-behaved
// client never hits this. Matches plan §V1 subcommand surface.
const auditListCap = 500

// auditListDefault is the default limit when no ?limit= is supplied.
const auditListDefault = 50

// deliveryRow is the wire shape returned by GET /v1/deliveries. The
// field names pin the contract documented in the plan's §V1 endpoint
// surface: `drem csuite audit list` decodes these same keys.
type deliveryRow struct {
	ID          string `json:"id"`
	From        string `json:"from"`
	To          string `json:"to"`
	Type        string `json:"type"`
	Priority    string `json:"priority"`
	Subject     string `json:"subject"`
	TLDR        string `json:"tldr"`
	DeliveredAt string `json:"delivered_at"`
	Status      string `json:"status"`
	Filename    string `json:"filename"`
}

// registerAuditRoutes wires the /v1/* endpoints onto the handler's
// mux. Kept on the existing *handler so the audit endpoints share
// the same ledger + classifier state the /deliver pipeline uses.
// /v1/queue is registered in handlers_audit_queue.go via
// registerQueueRoute — separate so the two endpoints ship in
// separate feat commits.
func (h *handler) registerAuditRoutes(mux *http.ServeMux) {
	mux.Handle("/v1/deliveries", auditBearerAuth(h.cfg.AuditToken, http.HandlerFunc(h.listDeliveries)))
	h.registerQueueRoute(mux)
}

// auditBearerAuth wraps next with Authorization: Bearer <token>
// validation. Missing or mismatched tokens return a minimal JSON 401
// body. An empty configured token rejects every request (same
// fail-closed posture as TokenAuth for /deliver).
//
// Comparison is constant-time via crypto/subtle.
func auditBearerAuth(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token == "" {
			writeJSONError(w, http.StatusUnauthorized, "audit token not configured")
			return
		}
		hdr := r.Header.Get(auditAuthHeader)
		if !strings.HasPrefix(hdr, auditBearerPfx) {
			writeJSONError(w, http.StatusUnauthorized, "missing Authorization: Bearer header")
			return
		}
		got := strings.TrimPrefix(hdr, auditBearerPfx)
		if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			writeJSONError(w, http.StatusUnauthorized, "invalid bearer token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// listDeliveries serves GET /v1/deliveries. Query params map 1:1 to
// `drem csuite audit list` flags. See plan §V1 endpoint surface.
func (h *handler) listDeliveries(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.cfg.Ledger == nil {
		writeJSONError(w, http.StatusInternalServerError, "ledger not configured")
		return
	}

	q := r.URL.Query()
	filter := deliveryFilter{
		from:   q.Get("from"),
		to:     q.Get("to"),
		status: q.Get("status"),
		typ:    q.Get("type"),
	}
	if s := q.Get("since"); s != "" {
		if t, err := parseSince(s, time.Now().UTC()); err == nil {
			filter.since = t
			filter.sinceSet = true
		}
	}
	filter.limit = auditListDefault
	if s := q.Get("limit"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			filter.limit = n
		}
	}
	if filter.limit > auditListCap {
		filter.limit = auditListCap
	}
	if s := q.Get("offset"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			filter.offset = n
		}
	}

	rows, err := h.queryDeliveries(filter)
	if err != nil {
		h.logger().Printf("audit: list deliveries: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "ledger query failed")
		return
	}

	writeJSONArray(w, rows)
}

// deliveryFilter collects parsed query-param filters for
// /v1/deliveries. Field names match the plan.
type deliveryFilter struct {
	from     string
	to       string
	status   string
	typ      string
	since    time.Time
	sinceSet bool
	limit    int
	offset   int
}

// queryDeliveries fetches Delivery rows from the ledger, applies the
// filter in-memory, and returns deliveryRow wire structs ordered
// newest-first. The in-memory filter is intentional: at v1 volumes
// (thousands of rows) the overhead is nil, and keeping the SQL query
// simple avoids gorm query-builder drift.
func (h *handler) queryDeliveries(f deliveryFilter) ([]deliveryRow, error) {
	var all []Delivery
	if err := h.cfg.Ledger.db.
		Order("delivered_at DESC").
		Find(&all).Error; err != nil {
		return nil, err
	}
	out := make([]deliveryRow, 0)
	skipped := 0
	for _, d := range all {
		row := toDeliveryRow(d)
		if !matchesDeliveryFilter(row, d, f) {
			continue
		}
		if skipped < f.offset {
			skipped++
			continue
		}
		out = append(out, row)
		if f.limit > 0 && len(out) >= f.limit {
			break
		}
	}
	return out, nil
}

// matchesDeliveryFilter reports whether a row should be included
// given the supplied filter. Empty filter fields mean "no filter".
func matchesDeliveryFilter(row deliveryRow, raw Delivery, f deliveryFilter) bool {
	if f.from != "" && row.From != f.from {
		return false
	}
	if f.to != "" && row.To != f.to {
		return false
	}
	if f.status != "" && f.status != "all" && row.Status != f.status {
		return false
	}
	if f.typ != "" && row.Type != f.typ {
		return false
	}
	if f.sinceSet && raw.DeliveredAt.Before(f.since) {
		return false
	}
	return true
}

// toDeliveryRow converts a ledger Delivery into the wire-shape
// struct, best-effort parsing frontmatter from the delivered file to
// populate type/priority/subject/tldr. Missing or unreadable files
// leave those fields empty — the CLI renders them as "-".
func toDeliveryRow(d Delivery) deliveryRow {
	status := "delivered"
	filename := filepath.Base(d.DestPath)
	readPath := d.DestPath
	if d.Dest == ClassQuarantine {
		status = "quarantined"
	}
	row := deliveryRow{
		ID:          d.SHA256,
		From:        d.SourcePersona,
		To:          d.Dest,
		DeliveredAt: d.DeliveredAt.UTC().Format(time.RFC3339),
		Status:      status,
		Filename:    filename,
	}
	// Best-effort frontmatter extraction. Any failure (file gone,
	// binary, malformed YAML) leaves type/priority/subject/tldr at
	// their zero values.
	if fm, err := readFrontmatter(readPath); err == nil {
		row.Type = fm.Type
		row.Priority = fm.Priority
		row.Subject = fm.Subject
		row.TLDR = fm.TLDR
	}
	return row
}

// frontmatterFields is the YAML shape the audit endpoints care about.
// Kept permissive — any missing field is fine.
type frontmatterFields struct {
	Type     string `yaml:"type"`
	Priority string `yaml:"priority"`
	Subject  string `yaml:"subject"`
	TLDR     string `yaml:"tldr"`
}

// readFrontmatter opens path (after resolveCsuitePath remapping),
// reads up to frontmatterCap bytes, and decodes any YAML frontmatter
// block into frontmatterFields.
func readFrontmatter(path string) (frontmatterFields, error) {
	realPath := resolveCsuitePath(path)
	f, err := os.Open(realPath) //nolint:gosec // path validated upstream by ValidateRequest + ClassifyFile
	if err != nil {
		return frontmatterFields{}, err
	}
	defer f.Close()

	buf := make([]byte, frontmatterCap)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return frontmatterFields{}, err
	}
	body, _, ok := extractFrontmatter(buf[:n])
	if !ok {
		return frontmatterFields{}, errNoFrontmatter
	}
	var fm frontmatterFields
	if err := yaml.Unmarshal(body, &fm); err != nil {
		return frontmatterFields{}, err
	}
	return fm, nil
}

var errNoFrontmatter = errors.New("no frontmatter")

// parseSince accepts either a duration ("1h", "24h") or an RFC3339
// timestamp ("2026-04-21T00:00:00Z") or a short date ("2026-04-21").
// Returns the earliest point in time rows must be at or after.
func parseSince(s string, now time.Time) (time.Time, error) {
	if d, err := time.ParseDuration(s); err == nil {
		return now.Add(-d), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, errors.New("unrecognised since value")
}

// writeJSONArray encodes rows as a JSON array with Content-Type set
// and a trailing newline stripped. The empty-slice case still emits
// `[]` (not `null`) — callers rely on this for "empty list" shape
// parity with populated responses.
func writeJSONArray(w http.ResponseWriter, rows any) {
	w.Header().Set("Content-Type", "application/json")
	buf, err := json.Marshal(rows)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "encode failed")
		return
	}
	if bytes.Equal(buf, []byte("null")) {
		buf = []byte("[]")
	}
	_, _ = w.Write(buf)
}
