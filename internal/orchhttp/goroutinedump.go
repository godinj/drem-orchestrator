package orchhttp

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
)

// Bug E W3.2: distroless orch has no way to capture a goroutine dump
// when something hangs — no pgrep, no `kill -QUIT`, no Go debug tools
// on PATH inside the container. This file installs a SIGUSR1 handler
// that writes runtime.Stack(buf, true) output to
// /tmp/drem-goroutines-<unix_ts>.log so the operator can trigger a
// capture from the host via:
//
//	docker kill --signal=USR1 drem-orchestrator-orch-1
//
// ...and read the dump via a /tmp bind-mount declared in the compose
// template. Stdlib only — os/signal + runtime.
//
// SIGUSR1 is the convention: it is guaranteed-not-to-be-used by
// anything the Go runtime itself cares about (which is critical —
// SIGQUIT would kill the process), and the handler pattern is the
// same one cmd/drem/main.go already uses for SIGTERM/SIGHUP.

// goroutineDumpBufferSize is the starting buffer for runtime.Stack.
// Orch has observed up to a few hundred goroutines under load; 1 MiB
// comfortably holds a dump well past that. If the buffer turns out
// too small, runtime.Stack truncates silently rather than panicking —
// the caller grows and retries.
const goroutineDumpBufferSize = 1 << 20 // 1 MiB

// InstallGoroutineDumpHandler wires a SIGUSR1 handler that writes a
// full-process goroutine stack dump to dir/drem-goroutines-<ts>.log on
// every signal. dir is created if missing; a zero-value dir defaults
// to /tmp (the bind-mount target used by the compose template).
//
// The handler goroutine returns when ctx is cancelled, matching the
// lifecycle of the caller's root context. InstallGoroutineDumpHandler
// is safe to call from cmd/drem/main.go before the orchestrator runs;
// it returns immediately after registering the signal receiver.
func InstallGoroutineDumpHandler(ctx context.Context, dir string) error {
	if dir == "" {
		dir = "/tmp"
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("goroutine dump dir: %w", err)
	}

	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGUSR1)

	go func() {
		defer signal.Stop(ch)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ch:
				path, err := writeGoroutineDump(dir, time.Now())
				if err != nil {
					slog.Error("goroutine dump failed", "err", err)
					continue
				}
				slog.Info("goroutine dump written", "path", path)
			}
		}
	}()
	return nil
}

// writeGoroutineDump captures runtime.Stack(buf, true) and writes it
// atomically to dir/drem-goroutines-<unix_ts>.log. The file is
// 0644-readable so the host operator (who may have a different UID
// than the container's root) can read it from the bind-mount.
func writeGoroutineDump(dir string, now time.Time) (string, error) {
	buf := make([]byte, goroutineDumpBufferSize)
	n := runtime.Stack(buf, true)
	// runtime.Stack truncates rather than grows; if we used the full
	// buffer, grow once and retry for a clean dump.
	if n == len(buf) {
		buf = make([]byte, 8*goroutineDumpBufferSize)
		n = runtime.Stack(buf, true)
	}
	path := filepath.Join(dir, fmt.Sprintf("drem-goroutines-%d.log", now.Unix()))
	if err := os.WriteFile(path, buf[:n], 0o644); err != nil {
		return "", fmt.Errorf("write dump: %w", err)
	}
	return path, nil
}
