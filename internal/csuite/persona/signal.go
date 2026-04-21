package persona

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"time"
)

// SignalOutcome enumerates the final disposition of a post-write
// watcher signal attempt. It is written into state.md as
// last_signal_status so an operator can tell at a glance whether the
// persona -> watcher hand-off is healthy without grepping logs.
type SignalOutcome string

const (
	// SignalDisabled means CSUITE_SIGNAL_ENDPOINT is empty or unset;
	// no signal code path ran at all.
	SignalDisabled SignalOutcome = "disabled"
	// SignalOK means the watcher returned 202 (enqueued) or 409
	// (already delivered — idempotent success).
	SignalOK SignalOutcome = "ok"
	// SignalFailed means every retry attempt returned an error or a
	// non-successful status. The outbox file remains in place; the
	// watcher's rescan path will recover it.
	SignalFailed SignalOutcome = "failed"
)

// Default translation prefixes. The persona container sees its own
// subtree at /home/drem/.drem-csuite/<persona>/; the watcher sees the
// whole tree at /csuite/. The signal payload must carry the watcher's
// view, so the persona translates before POSTing.
const (
	DefaultPersonaPathPrefix = "/home/drem/.drem-csuite"
	DefaultWatcherPathPrefix = "/csuite"
)

// permanentSignalError wraps a 400/401 status — a category of failure
// that must not be retried. Callers check via errors.Is against the
// sentinel ErrPermanentSignal.
type permanentSignalError struct {
	status int
	body   string
}

func (e *permanentSignalError) Error() string {
	return fmt.Sprintf("watcher rejected signal: status=%d body=%q", e.status, e.body)
}

// ErrPermanentSignal is the sentinel callers check for to distinguish
// a 400/401 (do-not-retry) from a 5xx/network error (retryable).
var ErrPermanentSignal = errors.New("persona: permanent signal rejection")

func (e *permanentSignalError) Is(target error) bool {
	return target == ErrPermanentSignal
}

// SignalConfig holds the knobs the post-write signal path cares about.
// Zero value of the config struct is a no-op signaler (Endpoint
// empty).
type SignalConfig struct {
	// Endpoint is the full URL to POST to, e.g.
	// "http://csuite-watcher:8090/deliver". Empty disables signaling.
	Endpoint string

	// Token is the X-Csuite-Token header value. Required whenever
	// Endpoint is set — the watcher rejects token-less requests with
	// 401 even when the configured token is empty.
	Token string

	// PersonaPrefix is the local filesystem prefix the persona uses
	// to address its own outbox, e.g. /home/drem/.drem-csuite.
	PersonaPrefix string

	// WatcherPrefix is the path prefix the watcher sees for the same
	// files, e.g. /csuite. The persona rewrites
	// <PersonaPrefix>/<persona>/outbox/<file> to
	// <WatcherPrefix>/<persona>/outbox/<file> before signaling.
	WatcherPrefix string

	// HTTPClient does the POST. Default: a 5-second-timeout client
	// with Keep-Alive disabled.
	HTTPClient *http.Client

	// Backoff returns the sleep duration before attempt n (1-indexed).
	// Default: ~1s, ~3s, ~9s with ±20% jitter, clamped 500ms..12s.
	Backoff func(attempt int) time.Duration

	// MaxAttempts caps retry attempts. Default 3.
	MaxAttempts int

	// Sleep is the sleep primitive, overridable in tests to avoid
	// real time.Sleep.
	Sleep func(context.Context, time.Duration) error
}

// ApplyDefaults fills zero-value fields.
func (c *SignalConfig) ApplyDefaults() {
	if c.PersonaPrefix == "" {
		c.PersonaPrefix = DefaultPersonaPathPrefix
	}
	if c.WatcherPrefix == "" {
		c.WatcherPrefix = DefaultWatcherPathPrefix
	}
	if c.HTTPClient == nil {
		// No keep-alive: the endpoint is hit at most a few times per
		// poll tick. Pool churn is negligible; explicit fresh
		// connections keep failure modes simpler.
		t := &http.Transport{
			DisableKeepAlives: true,
			Proxy:             http.ProxyFromEnvironment,
		}
		c.HTTPClient = &http.Client{
			Timeout:   5 * time.Second,
			Transport: t,
		}
	}
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = 3
	}
	if c.Backoff == nil {
		c.Backoff = DefaultBackoff
	}
	if c.Sleep == nil {
		c.Sleep = sleepCtx
	}
}

// sleepCtx sleeps for d or returns early when ctx is cancelled.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// DefaultBackoff implements the retry curve documented in
// plans/csuite-watcher-outbox-routing.md §Failure modes: roughly
// 1s/3s/9s with ±20% jitter, clamped to [500ms, 12s].
func DefaultBackoff(attempt int) time.Duration {
	base := []time.Duration{time.Second, 3 * time.Second, 9 * time.Second}
	var d time.Duration
	if attempt <= 0 || attempt > len(base) {
		d = 9 * time.Second
	} else {
		d = base[attempt-1]
	}
	// ±20% jitter. rand.Float64 is fine here — we don't need crypto.
	jitter := (rand.Float64()*0.4 - 0.2) * float64(d)
	d += time.Duration(jitter)
	if d < 500*time.Millisecond {
		d = 500 * time.Millisecond
	}
	if d > 12*time.Second {
		d = 12 * time.Second
	}
	return d
}

// signalRequest is the JSON body for POST /deliver. Mirrors
// internal/deliver.DeliverRequest; duplicated here rather than
// imported to keep the persona package free of server-side
// dependencies.
type signalRequest struct {
	SourcePersona string `json:"source_persona"`
	OutboxPath    string `json:"outbox_path"`
	SHA256        string `json:"sha256"`
	EmittedAt     string `json:"emitted_at"`
}

// signalResponse is the happy-path response shape — only delivery_id
// is load-bearing for logging.
type signalResponse struct {
	DeliveryID string `json:"delivery_id"`
}

// Signaler is the surface the poller calls after a successful outbox
// write. Implementations are responsible for retry bookkeeping; the
// poller treats Signal as fire-and-forget and only reads the returned
// outcome to record in state.md.
type Signaler interface {
	// Signal POSTs the documented payload to the configured endpoint
	// and returns the final SignalOutcome. A nil Signaler — or a
	// Signaler whose config is disabled — returns SignalDisabled.
	// The call must not block the poller's main loop; callers run
	// Signal in a short-lived goroutine.
	Signal(ctx context.Context, persona string, absOutboxPath string, sha256, emittedAt string) SignalOutcome
}

// httpSignaler is the production Signaler. Zero value is unusable;
// construct with NewHTTPSignaler.
type httpSignaler struct {
	cfg    SignalConfig
	logger *slog.Logger
}

// NewHTTPSignaler returns an HTTP-backed Signaler. An empty
// cfg.Endpoint is allowed; the returned Signaler becomes a no-op that
// reports SignalDisabled without instantiating an HTTP client. The
// logger is required.
func NewHTTPSignaler(cfg SignalConfig, logger *slog.Logger) Signaler {
	if logger == nil {
		// Persona callers already validate Logger; keep this defensive
		// so tests don't panic on a zero logger.
		logger = slog.Default()
	}
	if cfg.Endpoint == "" {
		return disabledSignaler{}
	}
	cfg.ApplyDefaults()
	return &httpSignaler{cfg: cfg, logger: logger}
}

// disabledSignaler is the no-op used when CSUITE_SIGNAL_ENDPOINT is
// empty. Its Signal method neither instantiates an HTTP client nor
// spawns retries — it just returns SignalDisabled.
type disabledSignaler struct{}

func (disabledSignaler) Signal(context.Context, string, string, string, string) SignalOutcome {
	return SignalDisabled
}

// Signal implements Signaler. The method never returns SignalDisabled
// because NewHTTPSignaler short-circuits to disabledSignaler when the
// endpoint is empty.
func (s *httpSignaler) Signal(ctx context.Context, persona string, absOutboxPath string, sha256, emittedAt string) SignalOutcome {
	watcherPath := translatePath(absOutboxPath, s.cfg.PersonaPrefix, s.cfg.WatcherPrefix)
	payload := signalRequest{
		SourcePersona: persona,
		OutboxPath:    watcherPath,
		SHA256:        sha256,
		EmittedAt:     emittedAt,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		// A marshal error here implies a programmer bug — the payload
		// shape is fully static. Log once and exit SignalFailed.
		s.logger.Error("signal: marshal payload",
			slog.String("persona", persona),
			slog.Any("err", err))
		return SignalFailed
	}

	for attempt := 1; attempt <= s.cfg.MaxAttempts; attempt++ {
		outcome, retry, err := s.attempt(ctx, body)
		if err == nil {
			return outcome
		}
		if errors.Is(err, ErrPermanentSignal) {
			s.logger.Error("signal: permanent failure",
				slog.String("persona", persona),
				slog.String("sha256", sha256),
				slog.Any("err", err))
			return SignalFailed
		}
		if !retry || attempt == s.cfg.MaxAttempts {
			s.logger.Error("signal: giving up after retries",
				slog.String("persona", persona),
				slog.String("sha256", sha256),
				slog.Int("attempts", attempt),
				slog.Any("err", err))
			return SignalFailed
		}
		wait := s.cfg.Backoff(attempt)
		s.logger.Warn("signal: retry scheduled",
			slog.String("persona", persona),
			slog.String("sha256", sha256),
			slog.Int("attempt", attempt),
			slog.Duration("wait", wait),
			slog.Any("err", err))
		if serr := s.cfg.Sleep(ctx, wait); serr != nil {
			// Context cancelled during backoff — abort quietly. The
			// file stays in outbox; rescan will recover it.
			return SignalFailed
		}
	}
	return SignalFailed
}

// attempt performs one POST. It returns:
//
//   - (SignalOK, false, nil) on 202 or 409 — terminal success.
//   - (SignalFailed, false, permanentSignalError) on 4xx — terminal failure.
//   - (SignalFailed, true, err) on 5xx / network / timeout — retryable failure.
func (s *httpSignaler) attempt(ctx context.Context, body []byte) (SignalOutcome, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.Endpoint, bytes.NewReader(body))
	if err != nil {
		// NewRequestWithContext only fails on malformed URL — terminal.
		return SignalFailed, false, &permanentSignalError{status: 0, body: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Csuite-Token", strings.TrimSpace(s.cfg.Token))

	resp, err := s.cfg.HTTPClient.Do(req)
	if err != nil {
		// Network error / timeout: retryable.
		return SignalFailed, true, fmt.Errorf("http do: %w", err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusAccepted:
		var dec signalResponse
		_ = json.NewDecoder(resp.Body).Decode(&dec)
		s.logger.Info("signaled watcher",
			slog.String("delivery_id", dec.DeliveryID),
			slog.Int("status", resp.StatusCode))
		return SignalOK, false, nil
	case resp.StatusCode == http.StatusConflict:
		s.logger.Info("signaled watcher (already delivered)",
			slog.Int("status", resp.StatusCode))
		return SignalOK, false, nil
	case resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnauthorized:
		b, _ := io.ReadAll(resp.Body)
		return SignalFailed, false, &permanentSignalError{status: resp.StatusCode, body: string(b)}
	case resp.StatusCode >= 500:
		b, _ := io.ReadAll(resp.Body)
		return SignalFailed, true, fmt.Errorf("watcher 5xx: status=%d body=%q", resp.StatusCode, string(b))
	default:
		// Any other 4xx (404 on missing outbox_path, 507 on disk
		// pressure, etc.) — treat as retryable so a transient missing
		// file (rename race) gets another shot. 404 on a file the
		// persona just fsync'd is improbable but not impossible.
		b, _ := io.ReadAll(resp.Body)
		return SignalFailed, true, fmt.Errorf("watcher non-terminal status=%d body=%q", resp.StatusCode, string(b))
	}
}

// translatePath rewrites paths rooted at the persona's mount prefix
// to the watcher's mount prefix. Non-matching paths are returned
// unchanged so absolute system paths outside the tree (e.g. tempdir
// roots in tests) still survive.
func translatePath(p, personaPrefix, watcherPrefix string) string {
	if personaPrefix == "" || watcherPrefix == "" {
		return p
	}
	if p == personaPrefix {
		return watcherPrefix
	}
	if strings.HasPrefix(p, personaPrefix+"/") {
		return watcherPrefix + strings.TrimPrefix(p, personaPrefix)
	}
	return p
}

// Fsyncer abstracts the file-fsync primitive so tests can inject a
// fake that records call order. Production uses osFsyncer which
// wraps os.File.Sync.
type Fsyncer interface {
	Fsync(path string) error
}

// osFsyncer opens the file, calls Sync, and closes it. Returns any
// error from the three steps.
type osFsyncer struct{}

func (osFsyncer) Fsync(path string) error {
	f, err := os.Open(path) //nolint:gosec // path controlled by poller
	if err != nil {
		return fmt.Errorf("fsync open: %w", err)
	}
	defer f.Close()
	if err := f.Sync(); err != nil {
		return fmt.Errorf("fsync sync: %w", err)
	}
	return nil
}
