// Command drem-docker-query-proxy is the tiny sidecar that fronts the
// host Docker socket with a narrow, allowlisted HTTP API. Kyle talks to
// this proxy (never to the Docker socket directly) so the privilege
// boundary is obvious from a topology diagram: one container has the
// socket, one container (Kyle) has the reporting HTTP surface, and the
// rest of the stack has neither.
//
// Allowlisted endpoints:
//
//	GET /containers          — list (optional ?labels=k=v[,k=v])
//	GET /containers/:id      — inspect one container
//	GET /containers/:id/logs — tail logs (?since=RFC3339, ?tail=N)
//
// Everything else returns 403. The proxy never invokes Docker verbs that
// mutate state (create/start/stop/remove); those would violate Kyle's
// "read-only, no authority" invariant.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	dockertypes "github.com/docker/docker/api/types"
	dockercontainer "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	fs := flag.NewFlagSet("drem-docker-query-proxy", flag.ContinueOnError)
	listen := fs.String("listen", ":9000", "HTTP listen address")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintf(os.Stderr, "drem-docker-query-proxy: %v\n", err)
		return 2
	}

	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		fmt.Fprintf(os.Stderr, "drem-docker-query-proxy: docker client: %v\n", err)
		return 1
	}
	defer cli.Close()

	srv := &http.Server{
		Addr:              *listen,
		Handler:           newHandler(cli),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	serveErr := make(chan error, 1)
	go func() {
		slog.Info("docker-query-proxy listening", "addr", *listen)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	select {
	case <-ctx.Done():
	case err := <-serveErr:
		if err != nil {
			fmt.Fprintf(os.Stderr, "drem-docker-query-proxy: %v\n", err)
			return 1
		}
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	_ = srv.Shutdown(shutdownCtx)
	return 0
}

// dockerAPI is the minimal subset of the Docker client this proxy uses.
// Defining it as an interface keeps the handler testable without needing
// a live daemon.
type dockerAPI interface {
	ContainerList(ctx context.Context, opts dockercontainer.ListOptions) ([]dockertypes.Container, error)
	ContainerInspect(ctx context.Context, id string) (dockertypes.ContainerJSON, error)
	ContainerLogs(ctx context.Context, id string, opts dockercontainer.LogsOptions) (io.ReadCloser, error)
}

// newHandler wires the allowlisted routes. The 403 default is intentional:
// anything not listed here must be explicitly rejected rather than 404ing,
// so an operator auditing the access log can tell the difference between
// "endpoint does not exist" and "caller tried to hit something blocked".
func newHandler(cli dockerAPI) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /containers", listContainersHandler(cli))
	mux.HandleFunc("GET /containers/{id}", inspectContainerHandler(cli))
	mux.HandleFunc("GET /containers/{id}/logs", logsContainerHandler(cli))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	// Catch-all: everything else is forbidden.
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	})
	return mux
}

// listContainersHandler implements GET /containers. The ?labels= parameter
// is a comma-separated list of key=value pairs that Docker translates into
// an AND-joined filter. ?all=true includes stopped containers.
func listContainersHandler(cli dockerAPI) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		opts := dockercontainer.ListOptions{All: q.Get("all") == "true"}
		if labels := q.Get("labels"); labels != "" {
			args := filters.NewArgs()
			for _, pair := range strings.Split(labels, ",") {
				pair = strings.TrimSpace(pair)
				if pair == "" {
					continue
				}
				args.Add("label", pair)
			}
			opts.Filters = args
		}
		containers, err := cli.ContainerList(r.Context(), opts)
		if err != nil {
			http.Error(w, "docker: "+err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, http.StatusOK, containers)
	}
}

// inspectContainerHandler implements GET /containers/:id and surfaces the
// Docker InspectResponse verbatim. Callers consume the full struct (it is
// already a read-only shape).
func inspectContainerHandler(cli dockerAPI) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if id == "" {
			http.Error(w, "id is required", http.StatusBadRequest)
			return
		}
		info, err := cli.ContainerInspect(r.Context(), id)
		if err != nil {
			if client.IsErrNotFound(err) {
				http.Error(w, "container not found", http.StatusNotFound)
				return
			}
			http.Error(w, "docker: "+err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, http.StatusOK, info)
	}
}

// logsContainerHandler implements GET /containers/:id/logs. Streams the
// multiplexed stdout+stderr body through to the caller verbatim. ?follow=
// is intentionally not honoured on this proxy — Kyle is polling, not
// streaming, so unbounded log follow from a reporting surface is not a
// feature we need.
func logsContainerHandler(cli dockerAPI) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if id == "" {
			http.Error(w, "id is required", http.StatusBadRequest)
			return
		}
		q := r.URL.Query()
		opts := dockercontainer.LogsOptions{ShowStdout: true, ShowStderr: true}
		if s := q.Get("since"); s != "" {
			opts.Since = s
		}
		if t := q.Get("tail"); t != "" {
			// Docker accepts either "all" or a decimal number; validate
			// a number so we can reject garbage early.
			if t != "all" {
				if _, err := strconv.Atoi(t); err != nil {
					http.Error(w, "tail must be a number or \"all\"", http.StatusBadRequest)
					return
				}
			}
			opts.Tail = t
		}
		rc, err := cli.ContainerLogs(r.Context(), id, opts)
		if err != nil {
			if client.IsErrNotFound(err) {
				http.Error(w, "container not found", http.StatusNotFound)
				return
			}
			http.Error(w, "docker: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer rc.Close()
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(w, rc)
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Error("docker-query-proxy writeJSON failed", "err", err)
	}
}
