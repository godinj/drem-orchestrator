// Package deliver implements the csuite-watcher outbox routing HTTP
// endpoint (POST /deliver) plus the unauthenticated liveness probe
// (GET /healthz).
//
// This endpoint is the second responsibility of the csuite-watcher
// service, complementing the bridge HTTP API in internal/serve. It
// consumes signals emitted by the csuite-persona binary after a
// successful outbox write, reads the referenced outbox file from the
// shared /csuite/ tree, classifies the destination by its YAML
// frontmatter, and atomically writes the body into the appropriate
// inbox (or quarantine) — without ever calling Claude.
//
// The design is frozen in plans/csuite-watcher-outbox-routing.md. This
// commit implements the first tracer-bullet slice: token auth and
// schema validation only. Real delivery work is added in later commits.
package deliver

import (
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// tokenHeader is the HTTP header the csuite-persona binary sets to the
// shared CSUITE_WATCHER_TOKEN secret. It is deliberately distinct from
// the bridge API's Authorization: Bearer header — the bridge and the
// deliver endpoint have different audiences (external PWA client vs
// in-network persona containers) and should not share credentials.
const tokenHeader = "X-Csuite-Token"

// validPersonas is the closed set of source personas the operator has
// approved. Ross was retired 2026-04-22; the remaining three
// containerized personas are mike, alex, seth. Kyle (host-side CEO
// persona running as a Claude Code instance, not a container) is
// also a valid source so Kyle can route outbox messages through the
// watcher the same way the containerized personas do. Without kyle
// here, host-Kyle → persona delivery has no routed path and the
// operator is forced into direct-inbox filesystem drops.
var validPersonas = map[string]struct{}{
	"mike": {},
	"alex": {},
	"seth": {},
	"kyle": {},
}

// DeliverRequest is the JSON wire format for POST /deliver. The body
// is intentionally small — the watcher reads the source file itself
// rather than having the persona duplicate bytes over the wire.
type DeliverRequest struct {
	SourcePersona string `json:"source_persona"`
	OutboxPath    string `json:"outbox_path"`
	SHA256        string `json:"sha256"`
	EmittedAt     string `json:"emitted_at"`
}

// Config holds the dependencies needed to serve /deliver and /healthz.
// All fields are required except where noted; zero-value Config is
// unusable.
type Config struct {
	// Token is the shared secret expected in the X-Csuite-Token header.
	// An empty token causes every request to be rejected with 401 —
	// this is a deliberate safety rail so a misconfigured watcher
	// cannot accidentally accept anonymous traffic.
	Token string

	// Ledger is the SQLite-backed delivery ledger used for sha256
	// idempotency checks. When nil (commit-1 behaviour) /deliver still
	// validates auth and schema but returns 501 without consulting any
	// durable state. Commit 2 and later wire a non-nil ledger.
	Ledger *Ledger

	// Logger is the destination for /deliver's informational log lines
	// ("would deliver", classify decisions, etc.). When nil the
	// package falls back to the default log package.
	Logger *log.Logger

	// AuditToken is the bearer secret checked by the /v1/deliveries
	// and /v1/queue read-only audit endpoints. Intentionally a
	// separate field from Token: /deliver's X-Csuite-Token
	// authenticates in-network persona containers, while AuditToken
	// authenticates the operator's `drem csuite audit` CLI. An empty
	// AuditToken causes the audit endpoints to reject every request
	// (fail-closed, same posture as Token).
	//
	// See plans/csuite-audit-cli.md §Auth flow.
	AuditToken string
}

// Handler returns an http.Handler that dispatches /deliver, /rescan,
// /healthz, and the read-only audit endpoints (/v1/deliveries,
// /v1/queue). Callers register it under a parent mux; the handler
// switches internally by path so a single registration covers all
// endpoints. /healthz is intentionally unauthenticated so external
// liveness probes work without the shared secret; /deliver and
// /rescan share the X-Csuite-Token auth; the /v1/* audit endpoints
// use bearer auth against Config.AuditToken.
func Handler(cfg Config) http.Handler {
	h := &handler{cfg: cfg, clock: time.Now}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", h.healthz)
	mux.Handle("/deliver", TokenAuth(cfg.Token, http.HandlerFunc(h.deliver)))
	mux.Handle("/rescan", TokenAuth(cfg.Token, http.HandlerFunc(h.rescan)))
	h.registerAuditRoutes(mux)
	return mux
}

// handler carries the configured state for the deliver endpoint.
// Kept private; callers compose via Handler.
type handler struct {
	cfg Config

	// destMutexes serialises write operations for a single
	// destination so within-destination FIFO ordering (plan §6) holds.
	// Cross-destination writes run concurrently. Value type is
	// *sync.Mutex so zero-value semantics of sync.Map give us a fresh
	// mutex per-destination on first LoadOrStore.
	destMutexes sync.Map

	// clock supplies the RFC3339 timestamp embedded in destination
	// filenames. Overridable by tests to pin deterministic filenames
	// where FIFO ordering matters.
	clock func() time.Time
}

// logger returns the configured logger, defaulting to the stdlib
// default if none was supplied. Keeps the call sites terse.
func (h *handler) logger() *log.Logger {
	if h.cfg.Logger != nil {
		return h.cfg.Logger
	}
	return log.Default()
}

// healthz serves GET /healthz with a fixed body. No auth, no state —
// suitable for docker HEALTHCHECK and external probes.
func (h *handler) healthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

// deliver serves POST /deliver. The current tracer slice layers
// auth, schema, and ledger idempotency. On a duplicate sha256 the
// handler returns 409; on a new sha256 it logs "would deliver" and
// replies 501 until later commits wire real delivery work.
func (h *handler) deliver(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req DeliverRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON: %v", err))
		return
	}

	if err := ValidateRequest(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Without a ledger we can't enforce idempotency — fall back to
	// commit-1 behaviour so test scaffolding that omits a ledger
	// keeps working.
	if h.cfg.Ledger == nil {
		writeJSONError(w, http.StatusNotImplemented, "delivery not yet implemented")
		return
	}

	existing, found, err := h.cfg.Ledger.Lookup(req.SHA256)
	if err != nil {
		h.logger().Printf("deliver: ledger lookup failed for sha=%s: %v", req.SHA256, err)
		writeJSONError(w, http.StatusInternalServerError, "ledger lookup failed")
		return
	}
	if found {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]string{"delivery_id": existing.SHA256})
		return
	}

	class, err := ClassifyFile(req.OutboxPath)
	if errors.Is(err, ErrMultiRecipient) {
		writeJSONError(w, http.StatusBadRequest, "multi-recipient not supported")
		return
	}
	if err != nil {
		h.logger().Printf("deliver: classify failed for sha=%s path=%s: %v", req.SHA256, req.OutboxPath, err)
		writeJSONError(w, http.StatusInternalServerError, "classify failed")
		return
	}

	switch class.Class {
	case ClassQuarantine:
		h.logger().Printf("deliver: quarantine: source=%s reason=%q sha=%s", req.SourcePersona, class.Reason, req.SHA256)
		dest := quarantinePath(req.SourcePersona, req.OutboxPath)
		if err := atomicCopyFile(req.OutboxPath, dest); err != nil {
			h.logger().Printf("deliver: quarantine write failed: %v", err)
			writeJSONError(w, http.StatusInternalServerError, "quarantine write failed")
			return
		}
		if err := h.cfg.Ledger.Insert(Delivery{
			SHA256:        req.SHA256,
			SourcePersona: req.SourcePersona,
			Dest:          ClassQuarantine,
			SourcePath:    req.OutboxPath,
			DestPath:      dest,
			DeliveredAt:   time.Now().UTC(),
		}); err != nil && !errors.Is(err, ErrDuplicateDelivery) {
			h.logger().Printf("deliver: quarantine ledger insert failed: %v", err)
			writeJSONError(w, http.StatusInternalServerError, "ledger insert failed")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]string{"delivery_id": req.SHA256})
		return
	case ClassPersona, ClassKyle:
		destPath, err := h.deliverToInbox(req, class)
		if err != nil {
			h.logger().Printf("deliver: %s -> %s failed sha=%s: %v",
				req.SourcePersona, class.Dest, req.SHA256, err)
			writeJSONError(w, http.StatusInternalServerError, "delivery failed")
			return
		}
		h.logger().Printf("deliver: delivered source=%s dest=%s sha=%s dest_path=%s",
			req.SourcePersona, class.Dest, req.SHA256, destPath)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]string{"delivery_id": req.SHA256})
		return
	default:
		// Defensive — classifyBytes always returns one of the above.
		h.logger().Printf("deliver: unknown class %q for sha=%s", class.Class, req.SHA256)
		writeJSONError(w, http.StatusInternalServerError, "unknown classification")
		return
	}
}

// ValidateRequest enforces the schema documented in
// plans/csuite-watcher-outbox-routing.md §2. Exported so tests and
// later commits (which layer additional logic on top) can reuse it.
func ValidateRequest(req *DeliverRequest) error {
	if _, ok := validPersonas[req.SourcePersona]; !ok {
		return fmt.Errorf("source_persona must be one of mike|alex|seth|kyle, got %q", req.SourcePersona)
	}
	if req.OutboxPath == "" {
		return fmt.Errorf("outbox_path must not be empty")
	}
	expectedPrefix := "/csuite/" + req.SourcePersona + "/outbox/"
	if !strings.HasPrefix(req.OutboxPath, expectedPrefix) {
		return fmt.Errorf("outbox_path must start with %q", expectedPrefix)
	}
	if len(req.SHA256) != 64 {
		return fmt.Errorf("sha256 must be 64 hex characters, got length %d", len(req.SHA256))
	}
	if _, err := hex.DecodeString(req.SHA256); err != nil {
		return fmt.Errorf("sha256 must be valid hex: %v", err)
	}
	if req.EmittedAt == "" {
		return fmt.Errorf("emitted_at must not be empty")
	}
	if _, err := time.Parse(time.RFC3339, req.EmittedAt); err != nil {
		return fmt.Errorf("emitted_at must be RFC3339, got %q: %v", req.EmittedAt, err)
	}
	return nil
}

// TokenAuth wraps next with X-Csuite-Token header authentication.
// Requests with a missing or mismatched header are rejected with 401.
// An empty configured token rejects ALL requests — the deliver
// endpoint must never accept anonymous traffic, even in a misconfigured
// deployment. Comparison is constant-time via crypto/subtle.
func TokenAuth(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token == "" {
			writeJSONError(w, http.StatusUnauthorized, "watcher token not configured")
			return
		}
		got := r.Header.Get(tokenHeader)
		if got == "" {
			writeJSONError(w, http.StatusUnauthorized, "missing "+tokenHeader+" header")
			return
		}
		if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			writeJSONError(w, http.StatusUnauthorized, "invalid "+tokenHeader+" header")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// writeJSONError emits a JSON body with a single "error" key at the
// given status code. All error paths in this package go through this
// helper so responses have a consistent shape.
func writeJSONError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// deliverToInbox is the real-delivery code path for persona and kyle
// classes. It serialises per-destination via destMutexes, copies the
// source file into the destination inbox with an atomic rename,
// inserts the ledger row, and then moves the source into the
// source's outbox/delivered/ tree. The ordering matters: ledger
// commit happens AFTER the inbox write but BEFORE the source move,
// so a crash between inbox-write and ledger-insert causes a rescan
// replay (good — idempotent) and a crash between ledger-insert and
// source-move leaves the source file in outbox/ (acceptable — next
// rescan will see the ledger row and skip).
//
// Returns the protocol dest path on success.
func (h *handler) deliverToInbox(req DeliverRequest, class Classification) (string, error) {
	mu := h.destMutex(class.Dest)
	mu.Lock()
	defer mu.Unlock()

	now := h.clock().UTC()
	sha8 := req.SHA256
	if len(sha8) > 8 {
		sha8 = sha8[:8]
	}
	filename := fmt.Sprintf("%s-%s-%s.md", now.Format(time.RFC3339), req.SourcePersona, sha8)
	destPath := fmt.Sprintf("/csuite/%s/inbox/%s", class.Dest, filename)

	if err := atomicCopyFile(req.OutboxPath, destPath); err != nil {
		return "", fmt.Errorf("copy to inbox: %w", err)
	}

	if err := h.cfg.Ledger.Insert(Delivery{
		SHA256:        req.SHA256,
		SourcePersona: req.SourcePersona,
		Dest:          class.Dest,
		SourcePath:    req.OutboxPath,
		DestPath:      destPath,
		DeliveredAt:   now,
	}); err != nil {
		// Ledger failed after the inbox write landed. Best-effort
		// cleanup of the inbox file so a rescan doesn't see a
		// ghost that has no ledger row and tries to redeliver (the
		// recipient may already have processed it). The source file
		// is NOT moved — the next rescan will re-route and hit the
		// idempotency check once the ledger is healthy.
		realDst := resolveCsuitePath(destPath)
		_ = removeIfExists(realDst)
		return "", fmt.Errorf("ledger insert: %w", err)
	}

	// Move the source into outbox/delivered/. Failure here is
	// logged by the caller but does not undo the delivery — the
	// ledger is authoritative, and a duplicate signal for a source
	// still in outbox/ will hit 409 on the next attempt.
	deliveredPath := fmt.Sprintf("/csuite/%s/outbox/delivered/%s",
		req.SourcePersona, filepath.Base(req.OutboxPath))
	realSrc := resolveCsuitePath(req.OutboxPath)
	realDelivered := resolveCsuitePath(deliveredPath)
	if err := moveSource(realSrc, realDelivered); err != nil {
		h.logger().Printf("deliver: source move failed (ledger is committed): %v", err)
	}
	return destPath, nil
}

// destMutex returns the mutex guarding writes to the given
// destination, creating one on first use. The mutex map never
// shrinks — the set of destinations is small and bounded (four
// personas + kyle + quarantine), so this is fine.
func (h *handler) destMutex(dest string) *sync.Mutex {
	v, _ := h.destMutexes.LoadOrStore(dest, &sync.Mutex{})
	return v.(*sync.Mutex)
}
