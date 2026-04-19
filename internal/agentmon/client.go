// This file defines the HTTP ingestion surface used by the Docker-stdout
// path in agentmon. The orchestrator's POST /internal/logs endpoint
// accepts a batch of discriminated-union records; IngestRecord is the
// in-memory form, HTTPIngestor is the production uploader, and the
// Ingestor interface (declared in docker_source.go) is the seam tests use
// with an in-memory fake.

package agentmon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/godinj/drem-orchestrator/pkg/orchdto"
)

// agentmonTokenHeader mirrors the constant in internal/orchhttp. Copied
// verbatim rather than imported to keep agentmon's compile-time deps free
// of internal/orchhttp (which pulls in GORM).
const agentmonTokenHeader = "X-Drem-Agentmon-Token"

// ingestTimeout bounds a single POST to the orchestrator. Batches are
// small (≤ 32 records per docker_tail.go) so a short deadline is fine;
// agentmon logs and drops on timeout per the "no retry queue" scope note.
const ingestTimeout = 10 * time.Second

// IngestRecord is the in-memory shape of a single row in a POST
// /internal/logs batch. Type is one of the constants accepted by the
// orchestrator ingest handler (commit, push, test_result, build_error,
// heartbeat, crash, tool_call). Payload carries the type-specific fields
// that the handler fans out into TaskEvent.Details; the top-level
// envelope fields (type, container_id, worker_id, timestamp) are merged
// with Payload on marshal so the server sees a single flat object per
// record — matching the discriminated-union contract in prompt 08.
type IngestRecord struct {
	Type        string
	ContainerID string
	WorkerID    string
	Timestamp   time.Time
	Payload     map[string]any
}

// MarshalJSON flattens the envelope fields and Payload into a single JSON
// object. Payload keys win collisions with envelope keys only when the
// envelope field is the zero value, preserving the invariant that the
// server can rely on top-level type/container_id/worker_id/timestamp.
func (r IngestRecord) MarshalJSON() ([]byte, error) {
	out := make(map[string]any, len(r.Payload)+4)
	for k, v := range r.Payload {
		out[k] = v
	}
	out["type"] = r.Type
	if r.ContainerID != "" {
		out["container_id"] = r.ContainerID
	}
	if r.WorkerID != "" {
		out["worker_id"] = r.WorkerID
	}
	if !r.Timestamp.IsZero() {
		out["timestamp"] = r.Timestamp.UTC().Format(time.RFC3339Nano)
	}
	return json.Marshal(out)
}

// HTTPIngestor posts IngestRecord batches to the orchestrator's
// /internal/logs endpoint. It is the production Ingestor used when
// agentmon runs alongside an orchestrator in a project compose stack.
type HTTPIngestor struct {
	// OrchURL is the scheme+host+port of the orchestrator, for example
	// "http://orch:8080". The /internal/logs path is appended by this
	// type so callers do not have to remember it.
	OrchURL string
	// Token is the shared per-project secret sent in the
	// X-Drem-Agentmon-Token header. An empty token will be rejected by
	// the server with 401; callers are expected to validate config
	// before constructing the ingestor.
	Token string
	// HTTPClient is optional; a zero value produces a *http.Client with
	// the package-level ingestTimeout applied.
	HTTPClient *http.Client
}

// Ingest posts records as a single batch. On HTTP errors the call
// returns an error so the caller (docker_tail) can log; there is no
// retry queue by design (scope note).
func (h *HTTPIngestor) Ingest(ctx context.Context, records []IngestRecord) error {
	if len(records) == 0 {
		return nil
	}
	if h.OrchURL == "" {
		return fmt.Errorf("agentmon ingest: OrchURL is empty")
	}

	raws := make([]json.RawMessage, 0, len(records))
	for i := range records {
		b, err := json.Marshal(records[i])
		if err != nil {
			return fmt.Errorf("agentmon ingest: marshal record %d: %w", i, err)
		}
		raws = append(raws, b)
	}
	body, err := json.Marshal(orchdto.IngestRequest{Records: raws})
	if err != nil {
		return fmt.Errorf("agentmon ingest: marshal batch: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, ingestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost,
		h.OrchURL+"/internal/logs", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("agentmon ingest: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(agentmonTokenHeader, h.Token)

	client := h.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: ingestTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("agentmon ingest: do: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		preview, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("agentmon ingest: status %d: %s", resp.StatusCode, bytes.TrimSpace(preview))
	}
	return nil
}
