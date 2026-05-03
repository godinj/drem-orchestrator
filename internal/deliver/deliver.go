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

	// MaxRescanFilesPerPersona bounds how many outbox files a single
	// /rescan pass will process for one source persona. Zero uses the
	// package default; negative disables the bound for tests only.
	MaxRescanFilesPerPersona int
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
	svc := NewDeliveryService(cfg)
	h := &handler{cfg: cfg, svc: svc}
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
	cfg   Config
	svc   *DeliveryService
	svcMu sync.Mutex

	// clock is retained for package tests that construct handler
	// directly. Production handlers set the service explicitly.
	clock func() time.Time
}

// DeliveryService owns durable delivery behavior. HTTP handlers are
// responsible for auth, JSON, methods, and status mapping only.
type DeliveryService struct {
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

// DeliveryResult is the transport-neutral result of a delivery attempt.
type DeliveryResult struct {
	DeliveryID string
	Duplicate  bool
}

var (
	errNoLedger        = errors.New("delivery not yet implemented")
	errLedgerLookup    = errors.New("ledger lookup failed")
	errClassify        = errors.New("classify failed")
	errLedgerInsert    = errors.New("ledger insert failed")
	errQuarantineWrite = errors.New("quarantine write failed")
	errDeliveryFailed  = errors.New("delivery failed")
	errUnknownClass    = errors.New("unknown classification")
)

// NewDeliveryService builds the package delivery boundary for callers
// that need to invoke delivery or rescan without HTTP transport logic.
func NewDeliveryService(cfg Config) *DeliveryService {
	return &DeliveryService{cfg: cfg, clock: time.Now}
}

func (h *handler) service() *DeliveryService {
	h.svcMu.Lock()
	defer h.svcMu.Unlock()
	if h.svc != nil {
		return h.svc
	}
	clock := h.clock
	if clock == nil {
		clock = time.Now
	}
	h.svc = &DeliveryService{cfg: h.cfg, clock: clock}
	return h.svc
}

// logger returns the configured logger, defaulting to the stdlib
// default if none was supplied. Keeps the call sites terse.
func (s *DeliveryService) logger() *log.Logger {
	if s.cfg.Logger != nil {
		return s.cfg.Logger
	}
	return log.Default()
}

func (h *handler) logger() *log.Logger {
	return h.service().logger()
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

// deliver serves POST /deliver. Auth is handled by TokenAuth at the mux
// level; this method owns HTTP method, JSON/schema, and status mapping.
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

	result, err := h.service().Deliver(req)
	if err == nil {
		w.Header().Set("Content-Type", "application/json")
		if result.Duplicate {
			w.WriteHeader(http.StatusConflict)
		} else {
			w.WriteHeader(http.StatusAccepted)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"delivery_id": result.DeliveryID})
		return
	}

	switch {
	case errors.Is(err, errNoLedger):
		writeJSONError(w, http.StatusNotImplemented, "delivery not yet implemented")
	case errors.Is(err, errLedgerLookup):
		writeJSONError(w, http.StatusInternalServerError, "ledger lookup failed")
	case errors.Is(err, ErrMultiRecipient):
		writeJSONError(w, http.StatusBadRequest, "multi-recipient not supported")
	case errors.Is(err, errClassify):
		writeJSONError(w, http.StatusInternalServerError, "classify failed")
	case errors.Is(err, errQuarantineWrite):
		writeJSONError(w, http.StatusInternalServerError, "quarantine write failed")
	case errors.Is(err, errLedgerInsert):
		writeJSONError(w, http.StatusInternalServerError, "ledger insert failed")
	case errors.Is(err, errDeliveryFailed):
		writeJSONError(w, http.StatusInternalServerError, "delivery failed")
	case errors.Is(err, errUnknownClass):
		writeJSONError(w, http.StatusInternalServerError, "unknown classification")
	default:
		writeJSONError(w, http.StatusInternalServerError, "delivery failed")
	}
}

// Deliver routes a validated delivery request through the durable
// delivery pipeline: ledger idempotency, classification,
// suppress/quarantine/persona/operator routing, inbox writes, source
// movement, destination locking, logging, and clock use.
func (s *DeliveryService) Deliver(req DeliverRequest) (DeliveryResult, error) {
	if s.cfg.Ledger == nil {
		return DeliveryResult{}, errNoLedger
	}

	existing, found, err := s.cfg.Ledger.Lookup(req.SHA256)
	if err != nil {
		s.logger().Printf("deliver: ledger lookup failed for sha=%s: %v", req.SHA256, err)
		return DeliveryResult{}, fmt.Errorf("%w: %v", errLedgerLookup, err)
	}
	if found {
		return DeliveryResult{DeliveryID: existing.SHA256, Duplicate: true}, nil
	}

	class, err := ClassifyFile(req.OutboxPath)
	if errors.Is(err, ErrMultiRecipient) {
		return DeliveryResult{}, ErrMultiRecipient
	}
	if err != nil {
		s.logger().Printf("deliver: classify failed for sha=%s path=%s: %v", req.SHA256, req.OutboxPath, err)
		return DeliveryResult{}, fmt.Errorf("%w: %v", errClassify, err)
	}

	switch class.Class {
	case ClassSuppress:
		s.logger().Printf("deliver: suppressed source=%s reason=%q sha=%s", req.SourcePersona, class.Reason, req.SHA256)
		if err := s.cfg.Ledger.Insert(Delivery{
			SHA256:        req.SHA256,
			SourcePersona: req.SourcePersona,
			Dest:          ClassSuppress,
			SourcePath:    req.OutboxPath,
			DestPath:      "",
			DeliveredAt:   s.now().UTC(),
		}); err != nil && !errors.Is(err, ErrDuplicateDelivery) {
			s.logger().Printf("deliver: suppress ledger insert failed: %v", err)
			return DeliveryResult{}, fmt.Errorf("%w: %v", errLedgerInsert, err)
		}
		return DeliveryResult{DeliveryID: req.SHA256}, nil
	case ClassQuarantine:
		s.logger().Printf("deliver: quarantine: source=%s reason=%q sha=%s", req.SourcePersona, class.Reason, req.SHA256)
		dest := quarantinePath(req.SourcePersona, req.OutboxPath)
		if err := atomicCopyFile(req.OutboxPath, dest); err != nil {
			s.logger().Printf("deliver: quarantine write failed: %v", err)
			return DeliveryResult{}, fmt.Errorf("%w: %v", errQuarantineWrite, err)
		}
		if err := s.cfg.Ledger.Insert(Delivery{
			SHA256:        req.SHA256,
			SourcePersona: req.SourcePersona,
			Dest:          ClassQuarantine,
			SourcePath:    req.OutboxPath,
			DestPath:      dest,
			DeliveredAt:   s.now().UTC(),
		}); err != nil && !errors.Is(err, ErrDuplicateDelivery) {
			s.logger().Printf("deliver: quarantine ledger insert failed: %v", err)
			return DeliveryResult{}, fmt.Errorf("%w: %v", errLedgerInsert, err)
		}
		return DeliveryResult{DeliveryID: req.SHA256}, nil
	case ClassPersona, ClassOperator:
		// ClassOperator shares the same delivery mechanics as
		// ClassPersona: class.Dest is "operator", so deliverToInbox
		// lands the file at /csuite/operator/inbox/ via the same
		// path-builder. See plans/drem-csuite-send-cli.md §Phase 1.
		destPath, err := s.deliverToInbox(req, class)
		if err != nil {
			s.logger().Printf("deliver: %s -> %s failed sha=%s: %v",
				req.SourcePersona, class.Dest, req.SHA256, err)
			return DeliveryResult{}, fmt.Errorf("%w: %v", errDeliveryFailed, err)
		}
		s.logger().Printf("deliver: delivered source=%s dest=%s sha=%s dest_path=%s",
			req.SourcePersona, class.Dest, req.SHA256, destPath)
		return DeliveryResult{DeliveryID: req.SHA256}, nil
	default:
		// Defensive — classifyBytes always returns one of the above.
		s.logger().Printf("deliver: unknown class %q for sha=%s", class.Class, req.SHA256)
		return DeliveryResult{}, errUnknownClass
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
func (s *DeliveryService) deliverToInbox(req DeliverRequest, class Classification) (string, error) {
	mu := s.destMutex(class.Dest)
	mu.Lock()
	defer mu.Unlock()

	now := s.now().UTC()
	sha8 := req.SHA256
	if len(sha8) > 8 {
		sha8 = sha8[:8]
	}
	filename := fmt.Sprintf("%s-%s-%s.md", now.Format(time.RFC3339), req.SourcePersona, sha8)
	destPath := fmt.Sprintf("/csuite/%s/inbox/%s", class.Dest, filename)

	if err := atomicCopyFile(req.OutboxPath, destPath); err != nil {
		return "", fmt.Errorf("copy to inbox: %w", err)
	}

	if err := s.cfg.Ledger.Insert(Delivery{
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
		s.logger().Printf("deliver: source move failed (ledger is committed): %v", err)
	}
	return destPath, nil
}

func (h *handler) deliverToInbox(req DeliverRequest, class Classification) (string, error) {
	return h.service().deliverToInbox(req, class)
}

// destMutex returns the mutex guarding writes to the given
// destination, creating one on first use. The mutex map never
// shrinks — the set of destinations is small and bounded (four
// personas + kyle + quarantine), so this is fine.
func (s *DeliveryService) destMutex(dest string) *sync.Mutex {
	v, _ := s.destMutexes.LoadOrStore(dest, &sync.Mutex{})
	return v.(*sync.Mutex)
}

func (h *handler) destMutex(dest string) *sync.Mutex {
	return h.service().destMutex(dest)
}

func (s *DeliveryService) now() time.Time {
	if s.clock != nil {
		return s.clock()
	}
	return time.Now()
}
