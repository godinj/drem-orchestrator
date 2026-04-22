package orchhttp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"strings"
	"time"
)

// Bug E W3.1: distroless orch has zero live-debugging surface — no
// shell inside the container, no way to capture a goroutine profile
// when something hangs. This file provides the missing surface as a
// localhost-only net/http/pprof listener, gated behind DREM_PPROF=1
// so it cannot be accidentally left enabled in production. Stdlib
// only — no third-party profiling libraries.
//
// Bind is always loopback; env var DREM_PPROF_ADDR overrides the
// default 127.0.0.1:6060 for tests (":0" picks an ephemeral port) but
// must resolve to a loopback address or StartPprofListener errors out.
// That last check makes it impossible to fat-finger a public bind.

const (
	envPprofEnable = "DREM_PPROF"
	envPprofAddr   = "DREM_PPROF_ADDR"

	defaultPprofAddr = "127.0.0.1:6060"
)

// StartPprofListener launches the pprof HTTP listener when DREM_PPROF
// is set to a truthy value (any of "1", "true", "yes"). A no-op
// returns ("", noopStop, nil) when the env gate is off — callers
// always receive a non-nil stop so deferring it is safe.
//
// The listener binds to the address in DREM_PPROF_ADDR (defaulting to
// 127.0.0.1:6060). A non-loopback bind returns an error so the pprof
// surface cannot be exposed beyond the host.
//
// Returned addr is the listener's actual address (important when the
// env passes ":0" for an ephemeral port under test); stop is the
// graceful-shutdown hook.
func StartPprofListener(ctx context.Context) (addr string, stop func(context.Context) error, err error) {
	if !pprofEnabled() {
		return "", noopStop, nil
	}
	bind := os.Getenv(envPprofAddr)
	if bind == "" {
		bind = defaultPprofAddr
	}
	if err := requireLoopback(bind); err != nil {
		return "", noopStop, err
	}

	mux := http.NewServeMux()
	// Register the canonical pprof routes. net/http/pprof.Handler is
	// the per-profile factory; Index covers the top-level listing.
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	ln, err := net.Listen("tcp", bind)
	if err != nil {
		return "", noopStop, fmt.Errorf("pprof listen %s: %w", bind, err)
	}

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		slog.Info("pprof listener started", "addr", ln.Addr().String())
		if serveErr := srv.Serve(ln); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			slog.Error("pprof listener failed", "err", serveErr)
		}
	}()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	return ln.Addr().String(), srv.Shutdown, nil
}

// pprofEnabled checks DREM_PPROF for a truthy value. Intentionally
// lenient on the accepted strings so operators can use whatever spelling
// they are used to; an unrecognised value is treated as off.
func pprofEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(envPprofEnable)))
	switch v {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// requireLoopback errs if addr resolves to anything but a loopback
// IP. The check accepts both "127.0.0.1:6060" and "localhost:6060"
// shapes; a bare ":0" or "0.0.0.0:..." is rejected because those
// resolve to all interfaces, which would leak the profile surface to
// anything that can reach the container port.
func requireLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("pprof addr %q: %w", addr, err)
	}
	if host == "" {
		return fmt.Errorf("pprof addr %q: host must be loopback (empty = bind-all, not permitted)", addr)
	}
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("pprof addr %q: host %q is not a loopback address", addr, host)
	}
	return nil
}

// noopStop is the stop function returned when pprof is disabled or
// the gate rejects the bind. It lets callers defer the result without
// a nil check.
func noopStop(context.Context) error { return nil }
