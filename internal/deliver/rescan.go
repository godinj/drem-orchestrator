package deliver

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// rescanPersonas is the closed allow-list walked by /rescan and by
// the startup rescan. Held explicit (not auto-discovered from the
// filesystem) so a stray directory can never accidentally become a
// source persona. Kept identical to validPersonas but as a slice so
// walk order is deterministic for tests. Kyle is present so the
// host-side CEO persona can emit outbox messages that the watcher
// will route into containerized personas' inboxes on the same rescan
// cadence as mike/alex/seth.
var rescanPersonas = []string{"mike", "alex", "seth", "kyle"}

// DefaultMaxRescanFilesPerPersona is the safe bounded-drain default for rescan.
const DefaultMaxRescanFilesPerPersona = 25

// RescanResult is the JSON body returned by POST /rescan. The field
// names are stable — tooling may parse them.
type RescanResult struct {
	Scanned     int                            `json:"scanned"`
	Delivered   int                            `json:"delivered"`
	Skipped     int                            `json:"skipped"`
	Suppressed  int                            `json:"suppressed"`
	Quarantined int                            `json:"quarantined"`
	Errors      []string                       `json:"errors"`
	Personas    map[string]RescanPersonaResult `json:"personas,omitempty"`
}

// RescanPersonaResult is additive per-source detail for rescan observability.
type RescanPersonaResult struct {
	Scanned                 int   `json:"scanned"`
	Delivered               int   `json:"delivered"`
	Skipped                 int   `json:"skipped"`
	Suppressed              int   `json:"suppressed"`
	Quarantined             int   `json:"quarantined"`
	Errors                  int   `json:"errors"`
	OldestPendingAgeSeconds int64 `json:"oldest_pending_age_seconds"`
}

// Rescan walks each /csuite/<persona>/outbox/ directory and, for
// every .md file not already in the ledger, routes it through the
// same delivery pipeline that the /deliver HTTP path uses. Files
// under delivered/ are skipped unconditionally.
//
// Per-destination FIFO is preserved by calling the same
// deliverToInbox method /deliver does; that method holds the
// per-destination mutex. Ledger lookups happen BEFORE the mutex so a
// partially-delivered file (ledger row exists, source not yet moved
// to delivered/) is skipped rather than re-delivered with a
// potentially different body sha.
//
// Files are processed in source-mtime order within each persona so
// the destination inbox sees writes in emission order. Cross-persona
// order is not stabilised — §6 of the plan does not require it.
//
// Errors from individual files are collected into result.Errors and
// do not abort the walk.
func (h *handler) Rescan() RescanResult {
	res := RescanResult{Personas: make(map[string]RescanPersonaResult, len(rescanPersonas))}
	if h.cfg.Ledger == nil {
		res.Errors = append(res.Errors, "rescan aborted: no ledger configured")
		return res
	}
	for _, src := range rescanPersonas {
		h.rescanPersona(src, &res)
	}
	return res
}

// rescanPersona walks a single persona's outbox and routes any
// unledgered files. Errors are recorded in res.Errors; the walk
// continues.
func (h *handler) rescanPersona(src string, res *RescanResult) {
	persona := RescanPersonaResult{}
	defer func() {
		res.Personas[src] = persona
	}()
	recordErr := func(format string, args ...any) {
		res.Errors = append(res.Errors, fmt.Sprintf(format, args...))
		persona.Errors++
	}

	outboxDir := resolveCsuitePath("/csuite/" + src + "/outbox")
	entries, err := os.ReadDir(outboxDir)
	if err != nil {
		if os.IsNotExist(err) {
			// Missing outbox dir is fine: the persona has not
			// written yet or the tree is not mounted for this
			// persona.
			return
		}
		recordErr("%s: read outbox: %v", src, err)
		return
	}

	type candidate struct {
		name  string
		mtime time.Time
	}
	var cands []candidate
	for _, ent := range entries {
		if ent.IsDir() {
			// Skip delivered/ (and any other subdir the operator
			// has introduced). Dirs are not outbox files.
			continue
		}
		name := ent.Name()
		if !strings.HasSuffix(name, ".md") {
			continue
		}
		info, err := ent.Info()
		if err != nil {
			recordErr("%s/%s: stat: %v", src, name, err)
			continue
		}
		cands = append(cands, candidate{name: name, mtime: info.ModTime()})
	}
	sort.Slice(cands, func(i, j int) bool {
		if !cands[i].mtime.Equal(cands[j].mtime) {
			return cands[i].mtime.Before(cands[j].mtime)
		}
		return cands[i].name < cands[j].name
	})
	if len(cands) > 0 {
		now := time.Now()
		if h.clock != nil {
			now = h.clock()
		}
		age := now.Sub(cands[0].mtime)
		if age < 0 {
			age = 0
		}
		persona.OldestPendingAgeSeconds = int64(age.Seconds())
	}
	if limit := h.maxRescanFilesPerPersona(); limit >= 0 && len(cands) > limit {
		cands = cands[:limit]
	}

	for _, c := range cands {
		res.Scanned++
		persona.Scanned++
		protocolPath := "/csuite/" + src + "/outbox/" + c.name
		realPath := resolveCsuitePath(protocolPath)

		data, err := os.ReadFile(realPath) //nolint:gosec // walk-scoped path
		if err != nil {
			recordErr("%s: read %s: %v", src, c.name, err)
			continue
		}
		sum := sha256.Sum256(data)
		sha := hex.EncodeToString(sum[:])

		// Skip-BEFORE-deliver: any file already in the ledger
		// belongs to a prior delivery. The source move into
		// delivered/ may have failed, but the ledger is
		// authoritative.
		if _, found, err := h.cfg.Ledger.Lookup(sha); err != nil {
			recordErr("%s: ledger lookup %s: %v", src, c.name, err)
			continue
		} else if found {
			res.Skipped++
			persona.Skipped++
			h.logger().Printf("rescan: skip (ledger hit) %s/%s sha=%s", src, c.name, sha)
			continue
		}

		emittedAt := c.mtime.UTC().Format(time.RFC3339)
		req := DeliverRequest{
			SourcePersona: src,
			OutboxPath:    protocolPath,
			SHA256:        sha,
			EmittedAt:     emittedAt,
		}
		if err := ValidateRequest(&req); err != nil {
			recordErr("%s: validate %s: %v", src, c.name, err)
			continue
		}
		class, err := ClassifyFile(protocolPath)
		if errors.Is(err, ErrMultiRecipient) {
			res.Quarantined++
			persona.Quarantined++
			h.logger().Printf("rescan: multi-recipient rejected %s/%s", src, c.name)
			continue
		}
		if err != nil {
			recordErr("%s: classify %s: %v", src, c.name, err)
			continue
		}

		switch class.Class {
		case ClassSuppress:
			if err := h.cfg.Ledger.Insert(Delivery{
				SHA256:        req.SHA256,
				SourcePersona: req.SourcePersona,
				Dest:          ClassSuppress,
				SourcePath:    req.OutboxPath,
				DestPath:      "",
				DeliveredAt:   time.Now().UTC(),
			}); err != nil && !errors.Is(err, ErrDuplicateDelivery) {
				recordErr("%s: suppress ledger %s: %v", src, c.name, err)
				continue
			}
			res.Suppressed++
			persona.Suppressed++
			h.logger().Printf("rescan: suppressed %s/%s reason=%q", src, c.name, class.Reason)
		case ClassQuarantine:
			dest := quarantinePath(req.SourcePersona, req.OutboxPath)
			if err := atomicCopyFile(req.OutboxPath, dest); err != nil {
				recordErr("%s: quarantine write %s: %v", src, c.name, err)
				continue
			}
			if err := h.cfg.Ledger.Insert(Delivery{
				SHA256:        req.SHA256,
				SourcePersona: req.SourcePersona,
				Dest:          ClassQuarantine,
				SourcePath:    req.OutboxPath,
				DestPath:      dest,
				DeliveredAt:   time.Now().UTC(),
			}); err != nil && !errors.Is(err, ErrDuplicateDelivery) {
				recordErr("%s: quarantine ledger %s: %v", src, c.name, err)
				continue
			}
			res.Quarantined++
			persona.Quarantined++
			h.logger().Printf("rescan: quarantine %s/%s reason=%q", src, c.name, class.Reason)
		case ClassPersona, ClassOperator:
			// ClassOperator is destination-only: a persona wrote a
			// "to: operator" reply that the rescan must deliver into
			// /csuite/operator/inbox/. deliverToInbox handles both
			// classes identically because class.Dest carries the
			// destination name (persona or "operator"). Operator is
			// NOT added to rescanPersonas — it has no outbox to scan
			// from. See plans/drem-csuite-send-cli.md §Phase 1.
			destPath, err := h.deliverToInbox(req, class)
			if err != nil {
				recordErr("%s: deliver %s: %v", src, c.name, err)
				continue
			}
			res.Delivered++
			persona.Delivered++
			h.logger().Printf("rescan: delivered source=%s dest=%s sha=%s dest_path=%s",
				src, class.Dest, sha, destPath)
		default:
			recordErr("%s: unknown class %q for %s", src, class.Class, c.name)
		}
	}
}

// rescan serves POST /rescan. Returns 200 with the RescanResult JSON.
// Auth is handled by TokenAuth middleware at the mux level (same
// shared secret as /deliver).
func (h *handler) rescan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Drain the body so HTTP/1.1 keep-alive can recycle the
	// connection. We don't actually read anything from it — /rescan
	// takes no parameters in this iteration.
	_, _ = io.Copy(io.Discard, r.Body)

	res := h.Rescan()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(res)
}

// RescanOnce is the package-level entry point used by the csuite-
// watcher serve command to run a single rescan pass at startup. It
// accepts the same Config the HTTP handler uses and logs the outcome.
// Returns the RescanResult so tests can assert on it.
func RescanOnce(cfg Config) RescanResult {
	h := &handler{cfg: cfg, clock: time.Now}
	res := h.Rescan()
	logger := cfg.Logger
	if logger == nil {
		logger = log.Default()
	}
	logger.Printf("rescan: scanned=%d delivered=%d skipped=%d quarantined=%d errors=%d",
		res.Scanned, res.Delivered, res.Skipped, res.Quarantined, len(res.Errors))
	for _, src := range rescanPersonas {
		p := res.Personas[src]
		logger.Printf("rescan: persona=%s scanned=%d delivered=%d skipped=%d quarantined=%d errors=%d oldest_pending_age_seconds=%d",
			src, p.Scanned, p.Delivered, p.Skipped, p.Quarantined, p.Errors, p.OldestPendingAgeSeconds)
	}
	return res
}

func (h *handler) maxRescanFilesPerPersona() int {
	if h.cfg.MaxRescanFilesPerPersona < 0 {
		return -1
	}
	if h.cfg.MaxRescanFilesPerPersona == 0 {
		return DefaultMaxRescanFilesPerPersona
	}
	return h.cfg.MaxRescanFilesPerPersona
}

// rescanBasename returns the final path component of a protocol path
// like /csuite/alex/outbox/foo.md. Used by log lines only; kept
// private to avoid leaking an implementation detail.
func rescanBasename(p string) string {
	return filepath.Base(p)
}

// Guard against unused-import complaints when the package pulls in
// filepath only through this helper.
var _ = rescanBasename
