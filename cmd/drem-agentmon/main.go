// Command drem-agentmon runs the agentmon Docker-stdout subscription
// loop. It connects to a container runtime, subscribes to lifecycle
// events for drem-labeled containers in a single project, tails each
// running container's stdout, parses lines into structured events, and
// POSTs them to the orchestrator's /internal/logs endpoint.
//
// Claude-transcript tailing is a separate input channel served by
// package agentmon's Monitor type and is not driven by this binary;
// agentmon-transcript lives alongside the agent process in the coder
// worker, whereas this binary runs as a project-level sidecar.
//
// Flags are backward compatible with the library-only form: --docker
// must be supplied (either true, which is the default when running in
// a container where DREM_IN_CONTAINER=1 is set, or false to disable
// the Docker input channel entirely). When --docker is true the
// --orch-url, --agentmon-token, and --project flags are required.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/godinj/drem-orchestrator/internal/agentmon"
	"github.com/godinj/drem-orchestrator/internal/container"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "drem-agentmon: %v\n", err)
		os.Exit(1)
	}
}

// run parses flags, builds a DockerSource plus HTTPIngestor pair, and
// drives the subscription until ctx is cancelled. Separated from main
// so a future integration test can exercise the binary entry point
// without calling os.Exit.
func run(args []string) error {
	fs := flag.NewFlagSet("drem-agentmon", flag.ContinueOnError)
	var (
		dockerOn      = fs.Bool("docker", defaultDockerOn(), "enable Docker stdout subscription input channel")
		orchURL       = fs.String("orch-url", os.Getenv("DREM_ORCH_URL"), "orchestrator base URL, e.g. http://orch:8080 (required when --docker=true)")
		agentmonToken = fs.String("agentmon-token", os.Getenv("DREM_AGENTMON_TOKEN"), "shared token for X-Drem-Agentmon-Token header (required when --docker=true)")
		project       = fs.String("project", os.Getenv("DREM_PROJECT"), "project name, matched against the drem.project label on worker containers (required when --docker=true)")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}

	if !*dockerOn {
		fmt.Fprintln(os.Stderr, "drem-agentmon: --docker=false, nothing to do")
		return nil
	}
	if *orchURL == "" || *agentmonToken == "" || *project == "" {
		fs.Usage()
		return fmt.Errorf("--orch-url, --agentmon-token, and --project are required when --docker=true")
	}

	rt, err := container.NewDockerRuntime()
	if err != nil {
		return fmt.Errorf("docker runtime: %w", err)
	}
	defer rt.Close()

	src := &agentmon.DockerSource{
		Runtime:  rt,
		Ingestor: &agentmon.HTTPIngestor{OrchURL: *orchURL, Token: *agentmonToken},
		Project:  *project,
		ContainerFilter: container.EventFilter{
			Labels: map[string]string{"drem.project": *project},
		},
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	return src.Run(ctx)
}

// defaultDockerOn decides whether the Docker input channel is on by
// default. Running in a container (signaled by DREM_IN_CONTAINER=1 per
// deploy/docker/agentmon.Dockerfile) implies the operator intends the
// subscription path; running on a developer host defaults to false so
// a casual invocation does not start poking at the local docker
// socket.
func defaultDockerOn() bool {
	return os.Getenv("DREM_IN_CONTAINER") == "1"
}
