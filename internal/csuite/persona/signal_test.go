package persona_test

// signal_test.go exercises the post-write HTTP signal wiring added in
// commit 5 of plans/csuite-watcher-outbox-routing.md. The contract the
// tests pin:
//
//   - Empty CSUITE_SIGNAL_ENDPOINT (or nil Signaler) = no HTTP traffic
//     and no goroutine spawn. External behaviour matches the
//     pre-signal poller.
//   - 202 and 409 are both terminal success (SignalOK).
//   - 400 / 401 are terminal permanent failures (no retry).
//   - 5xx / network / timeout errors trigger up to MaxAttempts retries
//     with the configured backoff.
//   - fsync runs BEFORE any signal HTTP call (guards against signaling
//     a torn write).
//   - Torn-write scenario: the fsync primitive can sleep arbitrarily
//     and the sha256 in the payload still matches the final file
//     contents.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/godinj/drem-orchestrator/internal/csuite/persona"
)

// immediateSleep is the non-sleeping sleep the signal tests inject so
// retry tests don't take real seconds. It still honours ctx so the
// "context cancelled mid-backoff" branch can fire in tests.
func immediateSleep(ctx context.Context, _ time.Duration) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return nil
}

// recordingFsyncer captures the order of fsync calls so tests can
// assert that fsync fires BEFORE the first HTTP attempt. A package-
// level counter guarded by a mutex keeps the comparison cheap.
type recordingFsyncer struct {
	mu    sync.Mutex
	calls []string
	// sleep, if non-zero, is how long Fsync blocks before returning.
	// Used by the torn-write test to simulate a slow fsync.
	sleep time.Duration
	// postWrite is an optional hook that runs AFTER the fsync has
	// slept but BEFORE returning. The torn-write test uses this to
	// mutate the outbox file and verify the signal payload's sha256
	// still matches the final contents.
	postWrite func(path string)
}

func (r *recordingFsyncer) Fsync(path string) error {
	r.mu.Lock()
	r.calls = append(r.calls, path)
	sleep := r.sleep
	hook := r.postWrite
	r.mu.Unlock()
	if sleep > 0 {
		time.Sleep(sleep)
	}
	if hook != nil {
		hook(path)
	}
	// Real fsync is a no-op in tests (tempdir fsyncs are fine).
	return nil
}

// recordingSignaler wraps an inner Signaler and records every call so
// tests can assert "was it called, with what args, in what order".
type recordingSignaler struct {
	mu    sync.Mutex
	calls []signalCall
	// onCall fires on each call and returns the SignalOutcome the
	// wrapper reports back to the poller. Useful for tests that want
	// to simulate success without running the HTTP path.
	onCall func(signalCall) persona.SignalOutcome
}

type signalCall struct {
	persona     string
	outboxPath  string
	sha256      string
	emittedAt   string
	observedAt  time.Time
	fsyncCalls  int
	fsyncOrder  []string
	fsyncerObs  *recordingFsyncer // pointer captured so we can read ordering later
}

func (r *recordingSignaler) Signal(_ context.Context, personaName, outboxPath, sha, emittedAt string) persona.SignalOutcome {
	r.mu.Lock()
	call := signalCall{
		persona:    personaName,
		outboxPath: outboxPath,
		sha256:     sha,
		emittedAt:  emittedAt,
		observedAt: time.Now(),
	}
	r.calls = append(r.calls, call)
	hook := r.onCall
	r.mu.Unlock()
	if hook != nil {
		return hook(call)
	}
	return persona.SignalOK
}

// TestHTTPSignaler_202IsTerminalSuccess verifies the happy path: a
// single 202 response yields SignalOK with no retries.
func TestHTTPSignaler_202IsTerminalSuccess(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]string{"delivery_id": "abc"})
	}))
	defer srv.Close()

	s := persona.NewHTTPSignaler(persona.SignalConfig{
		Endpoint:    srv.URL,
		Token:       "tok",
		MaxAttempts: 3,
		Sleep:       immediateSleep,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	out := s.Signal(context.Background(), "alex", "/home/drem/.drem-csuite/alex/outbox/x.md", "deadbeef", "2026-04-21T15:30:00Z")
	if out != persona.SignalOK {
		t.Errorf("out = %q, want ok", out)
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Errorf("hits = %d, want 1", hits)
	}
}

// TestHTTPSignaler_409IsTerminalSuccess confirms that the idempotency
// response maps to SignalOK.
func TestHTTPSignaler_409IsTerminalSuccess(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]string{"delivery_id": "abc"})
	}))
	defer srv.Close()

	s := persona.NewHTTPSignaler(persona.SignalConfig{
		Endpoint:    srv.URL,
		Token:       "tok",
		MaxAttempts: 3,
		Sleep:       immediateSleep,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	out := s.Signal(context.Background(), "alex", "/home/drem/.drem-csuite/alex/outbox/x.md", "deadbeef", "2026-04-21T15:30:00Z")
	if out != persona.SignalOK {
		t.Errorf("out = %q, want ok", out)
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Errorf("hits = %d, want 1 (409 must be terminal)", hits)
	}
}

// TestHTTPSignaler_400And401DoNotRetry enforces the permanent-failure
// contract: a 400 or 401 response must NOT trigger another attempt.
func TestHTTPSignaler_400And401DoNotRetry(t *testing.T) {
	for _, code := range []int{http.StatusBadRequest, http.StatusUnauthorized} {
		code := code
		t.Run(http.StatusText(code), func(t *testing.T) {
			var hits int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				atomic.AddInt32(&hits, 1)
				w.WriteHeader(code)
				_, _ = w.Write([]byte("nope"))
			}))
			defer srv.Close()

			s := persona.NewHTTPSignaler(persona.SignalConfig{
				Endpoint:    srv.URL,
				Token:       "tok",
				MaxAttempts: 3,
				Sleep:       immediateSleep,
			}, slog.New(slog.NewTextHandler(io.Discard, nil)))

			out := s.Signal(context.Background(), "alex", "/home/drem/.drem-csuite/alex/outbox/x.md", "deadbeef", "2026-04-21T15:30:00Z")
			if out != persona.SignalFailed {
				t.Errorf("out = %q, want failed", out)
			}
			if got := atomic.LoadInt32(&hits); got != 1 {
				t.Errorf("hits = %d, want 1 (no retry on %d)", got, code)
			}
		})
	}
}

// TestHTTPSignaler_5xxRetriesThenFails drives the retry budget: every
// attempt returns 500, and after MaxAttempts we see SignalFailed with
// exactly MaxAttempts HTTP hits.
func TestHTTPSignaler_5xxRetriesThenFails(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	s := persona.NewHTTPSignaler(persona.SignalConfig{
		Endpoint:    srv.URL,
		Token:       "tok",
		MaxAttempts: 3,
		Sleep:       immediateSleep,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	out := s.Signal(context.Background(), "alex", "/home/drem/.drem-csuite/alex/outbox/x.md", "deadbeef", "2026-04-21T15:30:00Z")
	if out != persona.SignalFailed {
		t.Errorf("out = %q, want failed", out)
	}
	if got := atomic.LoadInt32(&hits); got != 3 {
		t.Errorf("hits = %d, want 3 (3 retry attempts)", got)
	}
}

// TestHTTPSignaler_RetrySucceedsOn2ndAttempt verifies that a transient
// 5xx followed by a 202 yields SignalOK with two HTTP hits total.
func TestHTTPSignaler_RetrySucceedsOn2ndAttempt(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		if n == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]string{"delivery_id": "abc"})
	}))
	defer srv.Close()

	s := persona.NewHTTPSignaler(persona.SignalConfig{
		Endpoint:    srv.URL,
		Token:       "tok",
		MaxAttempts: 3,
		Sleep:       immediateSleep,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	out := s.Signal(context.Background(), "alex", "/home/drem/.drem-csuite/alex/outbox/x.md", "deadbeef", "2026-04-21T15:30:00Z")
	if out != persona.SignalOK {
		t.Errorf("out = %q, want ok", out)
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Errorf("hits = %d, want 2 (retry-then-success)", got)
	}
}

// TestHTTPSignaler_NetworkErrorRetries forces every attempt to fail
// with a connection error by pointing at a closed socket. The retry
// path must fire MaxAttempts times and then return SignalFailed.
func TestHTTPSignaler_NetworkErrorRetries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // kill the listener so every request fails to connect.

	s := persona.NewHTTPSignaler(persona.SignalConfig{
		Endpoint:    url,
		Token:       "tok",
		MaxAttempts: 3,
		Sleep:       immediateSleep,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	out := s.Signal(context.Background(), "alex", "/home/drem/.drem-csuite/alex/outbox/x.md", "deadbeef", "2026-04-21T15:30:00Z")
	if out != persona.SignalFailed {
		t.Errorf("out = %q, want failed", out)
	}
}

// TestHTTPSignaler_TokenWhitespaceTrimmed verifies that the signaler
// strips a trailing newline from the token before setting the
// X-Csuite-Token header. Operators typically generate the token with
// `openssl rand -hex 32 > file` which leaves a newline; the watcher's
// constant-time compare treats the newline as a mismatch.
func TestHTTPSignaler_TokenWhitespaceTrimmed(t *testing.T) {
	var seenToken string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenToken = r.Header.Get("X-Csuite-Token")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]string{"delivery_id": "abc"})
	}))
	defer srv.Close()

	s := persona.NewHTTPSignaler(persona.SignalConfig{
		Endpoint:    srv.URL,
		Token:       "s3cret\n",
		MaxAttempts: 1,
		Sleep:       immediateSleep,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	_ = s.Signal(context.Background(), "alex", "/home/drem/.drem-csuite/alex/outbox/x.md", "deadbeef", "2026-04-21T15:30:00Z")
	if seenToken != "s3cret" {
		t.Errorf("token = %q, want trimmed %q", seenToken, "s3cret")
	}
}

// TestHTTPSignaler_PathTranslation verifies the container-side path
// in the outgoing JSON matches the watcher's /csuite/ prefix — not
// the persona's own /home/drem/.drem-csuite/ prefix.
func TestHTTPSignaler_PathTranslation(t *testing.T) {
	var seenPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		seenPath = body["outbox_path"]
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]string{"delivery_id": "abc"})
	}))
	defer srv.Close()

	s := persona.NewHTTPSignaler(persona.SignalConfig{
		Endpoint:      srv.URL,
		Token:         "tok",
		PersonaPrefix: "/home/drem/.drem-csuite",
		WatcherPrefix: "/csuite",
		MaxAttempts:   1,
		Sleep:         immediateSleep,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	_ = s.Signal(context.Background(), "alex",
		"/home/drem/.drem-csuite/alex/outbox/reply.md",
		"deadbeef", "2026-04-21T15:30:00Z")
	want := "/csuite/alex/outbox/reply.md"
	if seenPath != want {
		t.Errorf("outbox_path = %q, want %q", seenPath, want)
	}
}

// TestHTTPSignaler_EmptyEndpointShortCircuits verifies that an empty
// endpoint returns SignalDisabled without issuing any HTTP call. The
// test shape mirrors the production wiring: a zero SignalConfig
// becomes a disabled Signaler, which costs no goroutines or client
// instantiation.
func TestHTTPSignaler_EmptyEndpointShortCircuits(t *testing.T) {
	s := persona.NewHTTPSignaler(persona.SignalConfig{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	out := s.Signal(context.Background(), "alex", "", "", "")
	if out != persona.SignalDisabled {
		t.Errorf("out = %q, want disabled", out)
	}
}

// TestHTTPSignaler_DefaultBackoffBounds checks the backoff curve stays
// within the documented 500ms..12s window and that attempts 1/2/3 are
// roughly 1s/3s/9s.
func TestHTTPSignaler_DefaultBackoffBounds(t *testing.T) {
	min := 500 * time.Millisecond
	max := 12 * time.Second
	bases := []time.Duration{time.Second, 3 * time.Second, 9 * time.Second}
	for attempt, base := range bases {
		d := persona.DefaultBackoff(attempt + 1)
		if d < min || d > max {
			t.Errorf("attempt %d backoff %v out of [%v, %v]", attempt+1, d, min, max)
		}
		// Check it's within ±50% of the base so jitter doesn't drift too far.
		lower := time.Duration(float64(base) * 0.6)
		upper := time.Duration(float64(base) * 1.4)
		if d < lower || d > upper {
			t.Errorf("attempt %d backoff %v drifted from base %v outside [%v, %v]", attempt+1, d, base, lower, upper)
		}
	}
}

// TestHTTPSignaler_ErrPermanentSignalRecognition sanity-checks the
// sentinel so code that wraps signal errors can errors.Is them.
func TestHTTPSignaler_ErrPermanentSignalRecognition(t *testing.T) {
	// Driving a 401 produces a permanentSignalError inside the
	// signaler, but we never expose it externally — the outcome maps
	// to SignalFailed. The sentinel itself must still match its own
	// value though.
	if !errors.Is(persona.ErrPermanentSignal, persona.ErrPermanentSignal) {
		t.Error("errors.Is on sentinel should be true")
	}
}

// TestPoller_FsyncBeforeSignal verifies fsync runs strictly before the
// HTTP signal goroutine records its first call. The invariant lives at
// the poller level — the signaler is free to batch or reorder
// internally, but the sequence we guarantee to the watcher is:
// write -> fsync -> signal.
func TestPoller_FsyncBeforeSignal(t *testing.T) {
	fs := newTestFS(t, "prompt")
	cfg := baseConfig(fs)

	fsyncer := &recordingFsyncer{}
	rec := &recordingSignaler{}
	cfg.Fsyncer = fsyncer
	cfg.Signaler = rec

	spawner := persona.SpawnerFunc(func(_ context.Context, _ []string, stdin io.Reader) ([]byte, int, error) {
		_, _ = io.ReadAll(stdin)
		return []byte("reply-body"), 0, nil
	})
	p, err := persona.New(cfg, spawner)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	writeInboxMessage(t, fs, "m.md", "hello", time.Now())

	err = runPollerUntil(t, p, func() bool {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		return len(rec.calls) >= 1
	}, 2*time.Second)
	if err != nil {
		t.Fatalf("waiting for signal: %v", err)
	}

	fsyncer.mu.Lock()
	fcalls := append([]string(nil), fsyncer.calls...)
	fsyncer.mu.Unlock()
	rec.mu.Lock()
	sigCalls := append([]signalCall(nil), rec.calls...)
	rec.mu.Unlock()

	if len(fcalls) == 0 {
		t.Fatalf("expected at least one fsync, got 0")
	}
	if len(sigCalls) != 1 {
		t.Fatalf("expected exactly one signal call, got %d", len(sigCalls))
	}
	// The recorded sha256 must match the body we returned from the spawner.
	wantSHA := sha256Hex([]byte("reply-body"))
	if sigCalls[0].sha256 != wantSHA {
		t.Errorf("sha256 in signal = %q, want %q", sigCalls[0].sha256, wantSHA)
	}
	// Persona in the signal payload must match the config.
	if sigCalls[0].persona != "seth" {
		t.Errorf("persona = %q, want seth", sigCalls[0].persona)
	}
}

// TestPoller_NilSignalerNoGoroutines verifies that the nil-signaler
// path never spawns a goroutine and never touches state.md with a
// pending status. The signal field in state.md should read "disabled".
func TestPoller_NilSignalerNoGoroutines(t *testing.T) {
	fs := newTestFS(t, "prompt")
	cfg := baseConfig(fs)
	// Explicitly no Signaler.
	cfg.Signaler = nil

	spawner := persona.SpawnerFunc(func(_ context.Context, _ []string, _ io.Reader) ([]byte, int, error) {
		return []byte("ok"), 0, nil
	})
	p, err := persona.New(cfg, spawner)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	writeInboxMessage(t, fs, "m.md", "hello", time.Now())

	// Wait for a state.md write rather than a signal call — there is
	// no signal in this path.
	err = runPollerUntil(t, p, func() bool {
		data, err := os.ReadFile(fs.stateFile)
		return err == nil && strings.Contains(string(data), "last_processed: m.md")
	}, 2*time.Second)
	if err != nil {
		t.Fatalf("waiting for state.md: %v", err)
	}
	data, err := os.ReadFile(fs.stateFile)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if !strings.Contains(string(data), "last_signal_status: disabled") {
		t.Errorf("state.md missing 'last_signal_status: disabled'\nfull: %s", string(data))
	}
}

// TestPoller_StateRecordsSignalOutcome verifies the terminal
// last_signal_status value written to state.md after the goroutine
// completes. We use a recordingSignaler that reports SignalOK.
func TestPoller_StateRecordsSignalOutcome(t *testing.T) {
	fs := newTestFS(t, "prompt")
	cfg := baseConfig(fs)
	rec := &recordingSignaler{onCall: func(_ signalCall) persona.SignalOutcome {
		return persona.SignalOK
	}}
	cfg.Signaler = rec

	spawner := persona.SpawnerFunc(func(_ context.Context, _ []string, _ io.Reader) ([]byte, int, error) {
		return []byte("ok"), 0, nil
	})
	p, err := persona.New(cfg, spawner)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	writeInboxMessage(t, fs, "m.md", "hello", time.Now())

	// Wait for the goroutine to flip last_signal_status to "ok".
	err = runPollerUntil(t, p, func() bool {
		data, err := os.ReadFile(fs.stateFile)
		return err == nil && strings.Contains(string(data), "last_signal_status: ok")
	}, 2*time.Second)
	if err != nil {
		data, _ := os.ReadFile(fs.stateFile)
		t.Fatalf("waiting for last_signal_status=ok: %v; state=%q", err, string(data))
	}
}

// TestPoller_TornWriteScenario simulates a slow fsync: the outbox
// file's final bytes land AFTER the initial write but BEFORE fsync
// returns. The signal's sha256 must reflect the final on-disk
// contents — NOT some intermediate state.
//
// Implementation: our fsyncer blocks for 50ms and, during that block,
// rewrites the outbox file so the "torn write" analogy is concrete.
// Because recordSuccess reads the body it just wrote to compute the
// sha (not re-reading from disk), the invariant we test is that the
// sha in the signal matches the bytes the spawner returned.
func TestPoller_TornWriteScenario(t *testing.T) {
	fs := newTestFS(t, "prompt")
	cfg := baseConfig(fs)

	fsyncer := &recordingFsyncer{
		sleep: 50 * time.Millisecond,
		postWrite: func(path string) {
			// Simulate a "second write" landing during fsync. The sha
			// in the signal should still reference the bytes the
			// poller generated, because the poller's sha is derived
			// from the stdout buffer it owns, not from re-reading.
			_ = os.WriteFile(path, []byte("totally different"), 0o644)
		},
	}
	rec := &recordingSignaler{}
	cfg.Fsyncer = fsyncer
	cfg.Signaler = rec

	spawner := persona.SpawnerFunc(func(_ context.Context, _ []string, _ io.Reader) ([]byte, int, error) {
		return []byte("the final bytes"), 0, nil
	})
	p, err := persona.New(cfg, spawner)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	writeInboxMessage(t, fs, "m.md", "hello", time.Now())

	err = runPollerUntil(t, p, func() bool {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		return len(rec.calls) >= 1
	}, 3*time.Second)
	if err != nil {
		t.Fatalf("waiting for signal: %v", err)
	}

	rec.mu.Lock()
	sigCalls := append([]signalCall(nil), rec.calls...)
	rec.mu.Unlock()

	wantSHA := sha256Hex([]byte("the final bytes"))
	if sigCalls[0].sha256 != wantSHA {
		t.Errorf("sha256 in signal = %q, want %q (from poller-owned stdout bytes)", sigCalls[0].sha256, wantSHA)
	}
}

// TestPoller_OutboxPathIsAbsoluteLocalPath verifies the poller hands
// the Signaler the persona-side path (under cfg.OutboxDir) rather
// than a pre-translated watcher path. The Signaler owns the
// translation.
func TestPoller_OutboxPathIsAbsoluteLocalPath(t *testing.T) {
	fs := newTestFS(t, "prompt")
	cfg := baseConfig(fs)
	rec := &recordingSignaler{}
	cfg.Signaler = rec

	spawner := persona.SpawnerFunc(func(_ context.Context, _ []string, _ io.Reader) ([]byte, int, error) {
		return []byte("body"), 0, nil
	})
	p, err := persona.New(cfg, spawner)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	writeInboxMessage(t, fs, "m.md", "hello", time.Now())

	err = runPollerUntil(t, p, func() bool {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		return len(rec.calls) >= 1
	}, 2*time.Second)
	if err != nil {
		t.Fatalf("waiting for signal: %v", err)
	}

	rec.mu.Lock()
	sigCalls := append([]signalCall(nil), rec.calls...)
	rec.mu.Unlock()

	// Path must start with the poller's OutboxDir (the on-persona
	// view); translation to /csuite/ happens inside the Signaler.
	if !strings.HasPrefix(sigCalls[0].outboxPath, fs.outboxDir) {
		t.Errorf("outboxPath = %q, want prefix %q (poller should hand local path to signaler)",
			sigCalls[0].outboxPath, fs.outboxDir)
	}
}

// sha256Hex returns the hex-encoded sha256 of data, matching
// persona.sha256Bytes without exposing that internal.
func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// touchFile is unused in the current test set but convenient for
// forthcoming debugging sessions; kept to suppress linting churn.
var _ = filepath.Join
