// Command drem-spawner is the only process in the Drem Orchestrator stack
// that holds the Docker socket. Every other service (orchestrator,
// agentmon) talks to it over a JSON-RPC 2.0 Unix socket surface defined in
// internal/spawner. Per docs/prd-containerization.md §"Spawner service
// owns the Docker socket" this is an intentionally narrow privilege
// boundary.
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/godinj/drem-orchestrator/internal/container"
	"github.com/godinj/drem-orchestrator/internal/spawner"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "drem-spawner: %v\n", err)
		os.Exit(1)
	}
}

// run parses flags, builds the runtime, opens the listener, and blocks on
// spawner.Serve. It is separated from main so a future integration test
// can exercise the binary without calling os.Exit.
func run(args []string) error {
	fs := flag.NewFlagSet("drem-spawner", flag.ContinueOnError)
	var (
		socket      = fs.String("socket", "/var/run/drem/spawner.sock", "Unix socket path to listen on")
		runtimeName = fs.String("runtime", "docker", "runtime backend: \"docker\" or \"fake\"")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}

	rt, err := buildRuntime(*runtimeName)
	if err != nil {
		return err
	}
	if closer, ok := rt.(interface{ Close() error }); ok {
		defer closer.Close()
	}

	if err := os.MkdirAll(filepath.Dir(*socket), 0o755); err != nil {
		return fmt.Errorf("prepare socket dir: %w", err)
	}
	// Remove a stale socket from a previous crash so Listen does not fail
	// with "address already in use". If the file exists but is not a
	// socket, Listen will surface a clearer error on its own.
	_ = os.Remove(*socket)

	ln, err := net.Listen("unix", *socket)
	if err != nil {
		return fmt.Errorf("listen %s: %w", *socket, err)
	}
	// The socket is protected by filesystem perms only — 0600 keeps the
	// owning user the sole caller, which is the entire auth model on the
	// first cut (see PRD §"Networking and security").
	_ = os.Chmod(*socket, 0o600)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	fmt.Fprintf(os.Stderr, "drem-spawner: listening on %s runtime=%s\n", *socket, *runtimeName)
	return spawner.Serve(ctx, ln, rt)
}

// buildRuntime picks the container runtime implementation. "docker" is the
// production path; "fake" is used by the agent-team harness to exercise
// the spawner without a running Docker daemon.
func buildRuntime(name string) (container.Runtime, error) {
	switch name {
	case "docker":
		return container.NewDockerRuntime()
	case "fake":
		return container.NewFakeRuntime(), nil
	default:
		return nil, fmt.Errorf("unknown runtime %q (expected \"docker\" or \"fake\")", name)
	}
}
