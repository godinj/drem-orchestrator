// Package orchclient is a thin HTTP client for the orchestrator's
// public API. It exists so Kyle and the TUI can read state without
// importing internal/orchhttp, keeping the "Kyle talks to the
// orchestrator only through HTTP" invariant from the containerization
// PRD enforceable at compile time.
//
// The client decodes responses into pkg/orchdto types and surfaces
// network or status errors verbatim. It does not retry, cache, or rate
// limit — those concerns belong to the caller (or to a higher-level
// helper built on top of Client).
package orchclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/godinj/drem-orchestrator/pkg/orchdto"
)

// DefaultTimeout is the per-request timeout applied to non-streaming
// endpoints when the caller does not supply their own deadline via the
// context. Streaming endpoints (StreamLogs) do not use this value; they
// rely on the caller's context for cancellation so large transfers can
// continue indefinitely.
const DefaultTimeout = 30 * time.Second

// Client is the Kyle/TUI-facing handle to an orchestrator's HTTP API.
// Zero values are not usable; construct with New.
type Client struct {
	baseURL string
	http    *http.Client
}

// New builds a Client pointed at baseURL (for example
// "http://drem-orch-canvas:8080"). The returned client uses the
// package-level DefaultTimeout for non-streaming calls; callers that
// need a different timeout can replace Client.HTTPClient() or use a
// context with a deadline.
func New(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: DefaultTimeout},
	}
}

// TaskFilter is the set of filters honoured by ListTasks. The zero
// value requests every task for the project; fields are combined with
// AND semantics at the server.
type TaskFilter struct {
	Status          string
	Limit           int
	Offset          int
	IncludeArchived bool
}

// ListProjects fetches the project records known to this orchestrator.
// In the single-project-per-orchestrator model used by the first-cut
// containerization, the slice has exactly one element; Kyle still uses
// the list shape for future-proofing.
func (c *Client) ListProjects(ctx context.Context) ([]orchdto.ProjectDTO, error) {
	var out []orchdto.ProjectDTO
	if err := c.get(ctx, "/projects", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ListTasks fetches tasks for the named project, applying the given
// filter. Paging is optional: zero values request the server's defaults.
func (c *Client) ListTasks(ctx context.Context, project string, filter TaskFilter) ([]orchdto.TaskDTO, error) {
	q := url.Values{}
	if filter.Status != "" {
		q.Set("status", filter.Status)
	}
	if filter.Limit > 0 {
		q.Set("limit", strconv.Itoa(filter.Limit))
	}
	if filter.Offset > 0 {
		q.Set("offset", strconv.Itoa(filter.Offset))
	}
	if filter.IncludeArchived {
		q.Set("include_archived", "true")
	}
	var out []orchdto.TaskDTO
	if err := c.get(ctx, "/projects/"+url.PathEscape(project)+"/tasks", q, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ListWorkers fetches the worker list for the named project.
func (c *Client) ListWorkers(ctx context.Context, project string) ([]orchdto.WorkerDTO, error) {
	var out []orchdto.WorkerDTO
	if err := c.get(ctx, "/projects/"+url.PathEscape(project)+"/workers", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetWorker fetches a single worker by UUID. A 404 from the server is
// surfaced as an error whose string contains "not found".
func (c *Client) GetWorker(ctx context.Context, id string) (orchdto.WorkerDTO, error) {
	var out orchdto.WorkerDTO
	if err := c.get(ctx, "/workers/"+url.PathEscape(id), nil, &out); err != nil {
		return orchdto.WorkerDTO{}, err
	}
	return out, nil
}

// TaskAttempts fetches the worker/container attempts attributed to a task.
func (c *Client) TaskAttempts(ctx context.Context, taskID string) ([]orchdto.WorkerAttemptDTO, error) {
	var out []orchdto.WorkerAttemptDTO
	if err := c.get(ctx, "/tasks/"+url.PathEscape(taskID)+"/attempts", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// WorkerHistory fetches a worker's transition log.
func (c *Client) WorkerHistory(ctx context.Context, id string) (orchdto.WorkerHistoryDTO, error) {
	var out orchdto.WorkerHistoryDTO
	if err := c.get(ctx, "/workers/"+url.PathEscape(id)+"/history", nil, &out); err != nil {
		return orchdto.WorkerHistoryDTO{}, err
	}
	return out, nil
}

// Events fetches structured events written by agentmon, optionally
// filtered to those at or after the since timestamp and capped at
// limit rows. A zero since is encoded as empty (no filter).
func (c *Client) Events(ctx context.Context, since time.Time, limit int) ([]orchdto.EventDTO, error) {
	q := url.Values{}
	if !since.IsZero() {
		q.Set("since", since.UTC().Format(time.RFC3339Nano))
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out []orchdto.EventDTO
	if err := c.get(ctx, "/events", q, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// StreamLogs opens an HTTP GET to /logs and returns an io.ReadCloser
// over the raw response body. The caller must Close the returned
// reader; doing so cancels the request and releases the underlying
// connection. follow=true keeps the server-side reader open so the
// caller can tail indefinitely; the caller controls shutdown by
// cancelling ctx.
func (c *Client) StreamLogs(ctx context.Context, container string, since time.Time, follow bool) (io.ReadCloser, error) {
	if container == "" {
		return nil, fmt.Errorf("container is required")
	}
	q := url.Values{}
	q.Set("container", container)
	if !since.IsZero() {
		q.Set("since", since.UTC().Format(time.RFC3339Nano))
	}
	if follow {
		q.Set("follow", "true")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url("/logs", q), nil)
	if err != nil {
		return nil, err
	}
	// Streaming: do not apply the default per-request timeout.
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("stream logs: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return resp.Body, nil
}

// get issues an HTTP GET to path with the given query values and
// decodes the JSON body into out. Non-2xx responses are surfaced as
// errors whose message includes the status code and a truncated body
// so callers can distinguish "not found" from "server error" without
// parsing.
func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url(path, q), nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("GET %s: status %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// url composes a fully-qualified request URL from the client's baseURL,
// the supplied path, and optional query values. The path is assumed to
// be already-escaped by the caller (see the url.PathEscape calls above).
func (c *Client) url(path string, q url.Values) string {
	u := c.baseURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u
}
