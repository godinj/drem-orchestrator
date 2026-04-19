// Package tui: data-source abstraction used by the dashboard to fetch
// tasks, workers (agents), events, worker history, and log streams.
//
// Before containerization (docs/prd-containerization.md) the TUI hit GORM
// directly. This file introduces a DataSource seam so the TUI can talk to
// the orchestrator's read-only HTTP API via pkg/orchclient. The GORM-backed
// dashboard loaders are gone; the remaining direct-DB paths
// (bug reports, experiments, detail-pane enrichment such as task
// dependencies and comments) are out of scope of the public HTTP API and
// are left on *gorm.DB in this prompt. Remote-orchestrator support for
// those surfaces is follow-up work — see internal/tui/README.md.
package tui

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/godinj/drem-orchestrator/pkg/orchclient"
	"github.com/godinj/drem-orchestrator/pkg/orchdto"
)

// DataSource is the read-only façade the dashboard uses to populate tasks,
// workers, events, and logs. The interface mirrors the orchestrator's
// public HTTP API so the TUI can eventually run against a remote
// orchestrator without importing internal packages.
//
// Implementations must be safe to call from multiple tea.Cmd goroutines
// concurrently. Contexts are honoured for cancellation; callers typically
// pass a context tied to the Bubble Tea program lifetime.
type DataSource interface {
	// ListTasks returns the task projection for the orchestrator's single
	// project. filter fields map 1:1 onto orchclient.TaskFilter.
	ListTasks(ctx context.Context, filter orchclient.TaskFilter) ([]orchdto.TaskDTO, error)

	// ListWorkers returns every known worker (agent) for the project.
	ListWorkers(ctx context.Context) ([]orchdto.WorkerDTO, error)

	// Events returns structured events emitted since the given time. A
	// zero since requests all recorded events up to the server-side cap.
	Events(ctx context.Context, since time.Time) ([]orchdto.EventDTO, error)

	// WorkerHistory returns the transition log for a single worker.
	WorkerHistory(ctx context.Context, id string) (orchdto.WorkerHistoryDTO, error)

	// StreamLogs opens a follow-mode log stream for the named container.
	// Callers must Close the returned reader to release the connection;
	// cancelling ctx also terminates the stream.
	StreamLogs(ctx context.Context, containerID string) (io.ReadCloser, error)
}

// HTTPDataSource is the DataSource implementation backed by the
// orchestrator's read-only HTTP API. It is the only production
// implementation; tests use a fake DataSource defined in the test files.
type HTTPDataSource struct {
	// Client is the orchclient.Client used for every call. Required.
	Client *orchclient.Client
	// Project is the project name sent in URL paths. Required.
	Project string
}

// NewHTTPDataSource constructs an HTTPDataSource for the given orchestrator
// base URL and project name. It is a thin convenience around orchclient.New
// so callers do not need to import orchclient in addition to tui.
func NewHTTPDataSource(baseURL, project string) *HTTPDataSource {
	return &HTTPDataSource{Client: orchclient.New(baseURL), Project: project}
}

// ListTasks implements DataSource by forwarding to orchclient.
func (h *HTTPDataSource) ListTasks(ctx context.Context, filter orchclient.TaskFilter) ([]orchdto.TaskDTO, error) {
	if h == nil || h.Client == nil {
		return nil, fmt.Errorf("tui: HTTPDataSource: nil client")
	}
	return h.Client.ListTasks(ctx, h.Project, filter)
}

// ListWorkers implements DataSource by forwarding to orchclient.
func (h *HTTPDataSource) ListWorkers(ctx context.Context) ([]orchdto.WorkerDTO, error) {
	if h == nil || h.Client == nil {
		return nil, fmt.Errorf("tui: HTTPDataSource: nil client")
	}
	return h.Client.ListWorkers(ctx, h.Project)
}

// Events implements DataSource by forwarding to orchclient. The server-side
// cap is honoured; this TUI passes 0 (no explicit limit) because it polls
// frequently and only cares about events since the last fetch.
func (h *HTTPDataSource) Events(ctx context.Context, since time.Time) ([]orchdto.EventDTO, error) {
	if h == nil || h.Client == nil {
		return nil, fmt.Errorf("tui: HTTPDataSource: nil client")
	}
	return h.Client.Events(ctx, since, 0)
}

// WorkerHistory implements DataSource by forwarding to orchclient.
func (h *HTTPDataSource) WorkerHistory(ctx context.Context, id string) (orchdto.WorkerHistoryDTO, error) {
	if h == nil || h.Client == nil {
		return orchdto.WorkerHistoryDTO{}, fmt.Errorf("tui: HTTPDataSource: nil client")
	}
	return h.Client.WorkerHistory(ctx, id)
}

// StreamLogs implements DataSource by forwarding to orchclient with
// follow=true. Closing the returned reader (or cancelling ctx) cancels the
// in-flight request; the log pane does this on navigate-away.
func (h *HTTPDataSource) StreamLogs(ctx context.Context, containerID string) (io.ReadCloser, error) {
	if h == nil || h.Client == nil {
		return nil, fmt.Errorf("tui: HTTPDataSource: nil client")
	}
	return h.Client.StreamLogs(ctx, containerID, time.Time{}, true)
}

// ResolveOrchURL normalises an orchestrator URL. If raw is empty the
// fallback "http://127.0.0.1:<port>" is used, where port comes from the
// provided default (typically drem.toml's orch_http_port). An empty
// fallbackPort means the returned URL is http://127.0.0.1:8080.
//
// The returned URL always has a scheme. Callers that need to treat a bare
// host:port as http:// get this for free.
func ResolveOrchURL(raw, fallbackPort string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		port := strings.TrimSpace(fallbackPort)
		if port == "" {
			port = "8080"
		}
		return "http://127.0.0.1:" + port
	}
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	// Strip any trailing slash so callers that concatenate paths don't
	// get "//projects".
	u, err := url.Parse(s)
	if err != nil {
		return s
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return u.String()
}
