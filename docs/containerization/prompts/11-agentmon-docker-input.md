# Agent: Agentmon Docker Stdout Subscription

You are working on the `master` branch of Drem Orchestrator, a Go-based multi-agent task orchestrator. Your task is Phase 3 integration work for the containerization initiative: extend `internal/agentmon/` to subscribe to every drem-labeled container's stdout via the container runtime, parse log lines through the extraction package, and POST structured event records to the orchestrator's internal HTTP ingestion endpoint. The existing Claude-transcript tailing path remains in place; this is an additional input channel, not a replacement.

## Context

Read these specs before starting:

- `docs/prd-containerization.md` (sections: "Agentmon absorbs log shipping"; user stories 16, 26, 27, 48)
- `internal/agentmon/agentmon.go` (current agentmon loop — understand the shape of the pipeline the new input feeds into)
- `internal/agentmon/parse.go` (post-prompt-02, this delegates to `internal/extract`)
- `internal/container/runtime.go` (prompt 01 — `SubscribeEvents`, `StreamLogs`)
- `internal/extract/` (prompt 02 — `ParseLine` and event types)
- `internal/orchhttp/handlers_internal.go` and `pkg/orchclient/` (prompt 08 — the ingestion endpoint and client)

## Dependencies

- Prompt 01 (`internal/container/`)
- Prompt 02 (`internal/extract/`)
- Prompt 08 (`pkg/orchclient/` or an equivalent POSTer for `/internal/logs`)

If any are unavailable, stub with narrow interfaces in a local `interfaces.go` and wire fakes in tests. The real wiring happens in integration.

## Deliverables

### New files (`internal/agentmon/`)

#### 1. `docker_source.go`

- `type DockerSource struct { Runtime container.Runtime; Ingestor Ingestor; Project string; ContainerFilter container.EventFilter }`
- `type Ingestor interface { Ingest(ctx context.Context, records []IngestRecord) error }` — implementation lives in `client.go` below
- `func (s *DockerSource) Run(ctx context.Context) error` — subscribes to container start events matching the filter; for each started container, spawns a per-container tail goroutine; on container die event, cancels the corresponding tail goroutine; blocks until ctx is cancelled
- Handles: concurrent tailing of N containers via a `map[containerID]context.CancelFunc` protected by a mutex
- Graceful shutdown: on ctx cancel, cancel all per-container contexts, wait for tail goroutines to drain, return

#### 2. `docker_tail.go`

- `func tailContainer(ctx context.Context, rt container.Runtime, ing Ingestor, containerID string, labels map[string]string) error` — opens `StreamLogs`, scans line-by-line with `bufio.Scanner`, calls `extract.ParseLine("docker:"+containerID, line, time.Now())`, converts the event to an `IngestRecord`, batches records (max 32 or max 500ms), calls `ing.Ingest(ctx, batch)`
- Demultiplex Docker's stream header (8-byte prefix on each line when the container is not TTY): detect by looking at whether stdout/stderr are multiplexed; if so, strip the header. If prompt 01's `StreamLogs` documents whether it emits raw multiplexed or demuxed data, follow that contract.
- Per-container goroutine returns when ctx is cancelled or the log stream closes (container exited and Docker closed the stream)

#### 3. `client.go`

- `type HTTPIngestor struct { Client *orchclient.Client; Token string }` — if `pkg/orchclient` exposes an ingestion method, use it directly; otherwise, this wraps a raw `http.Client` and POSTs to `<orch-url>/internal/logs` with the `X-Drem-Agentmon-Token` header
- `func (h *HTTPIngestor) Ingest(ctx context.Context, records []IngestRecord) error`
- `type IngestRecord struct { Type string; ContainerID string; WorkerID string; Timestamp time.Time; Payload map[string]any }` — marshals to the discriminated-union JSON shape prompt 08 defined

#### 4. `convert.go`

- `func toIngestRecord(containerID, workerID string, ev extract.Event) IngestRecord` — maps each `extract.Event` variant to the right `IngestRecord.Type` and builds the `Payload` fields. Pure function.

### Tests

#### 5. `docker_source_test.go`

Use `container.NewFakeRuntime()`:

- `FakeRuntime.EmitEvent(Event{Type: EventStart, ContainerID: "c1", Labels: {...}})` → `DockerSource.Run` starts a tail goroutine
- `FakeRuntime.WriteLog("c1", []byte("[master abc123] fix thing\n"))` → an `IngestRecord` with `Type: "commit"` is delivered to the fake Ingestor
- `FakeRuntime.EmitEvent(Event{Type: EventDie, ContainerID: "c1"})` → tail goroutine for `c1` exits; subsequent log writes produce no ingestions
- Ctx cancel shuts down cleanly within 1 second

Use `require.Eventually` for the async assertions; cap each at 2 seconds.

#### 6. `docker_tail_test.go`

- Per-container tail unit tests against an in-memory `io.Reader` (simulating `StreamLogs`): feed canned lines, assert the expected batch of `IngestRecord`s
- Batching: 32 lines emitted in a burst produce one `Ingest` call with 32 records; a slow trickle of 3 lines over 600ms produces at least two `Ingest` calls (batch flush at 500ms)

#### 7. `convert_test.go`

Table-driven: for each `extract.Event` variant, assert the resulting `IngestRecord.Type` and `Payload` fields.

## Migration

#### 8. `internal/agentmon/agentmon.go`

Thread-safely add a second input source. The existing transcript tailing continues as-is; `DockerSource` runs alongside it. Both sources funnel into the same `Ingestor`, so the orchestrator receives one canonical event stream.

Keep the CLI entry point backward compatible — the existing flags (transcript path, poll interval) remain; add:

- `--docker` (bool, default true in container-run mode, false for host-side backward compat)
- `--orch-url` (required when `--docker`)
- `--agentmon-token` (required when `--docker`)
- `--project` (required when `--docker`)

#### 9. `cmd/drem-agentmon/main.go` (if not already present)

If the existing agentmon is launched as part of a larger binary, extract a dedicated `drem-agentmon` main so it can run as its own container. If there is already one, just add the new flags.

#### 10. `deploy/docker/agentmon.Dockerfile`

Multi-stage Go build; runtime distroless. Mount the Docker socket (via spawner or directly? — agentmon needs Docker access to subscribe to events; per the PRD, "The Docker socket is mounted only into the spawner service," so agentmon must go through the spawner. BUT the PRD also says agentmon subscribes to Docker stdout directly. Resolve as follows: the agentmon container mounts a read-only Docker socket via a socat/docker-query-proxy variant, OR — simpler for the first cut — agentmon mounts the Docker socket read-only, under an explicit security note.)

Pick the simpler path for the first cut: mount the Docker socket read-only into agentmon (`/var/run/docker.sock:/var/run/docker.sock:ro`). Document the deviation from the "socket only in spawner" principle in a comment at the top of the Dockerfile and open a follow-up to route through a read-only proxy (same proxy Kyle uses in prompt 14).

Tag: `localhost:5000/drem-agentmon:latest`.

### Additions to prompt 06's global compose

Agentmon is per-project (each project's orchestrator has its own agentmon so tokens and DB writes stay scoped). Therefore agentmon goes in the PER-PROJECT compose template (prompt 16), not the global compose. Do not add it to `deploy/compose/global.yml`.

Leave a note in prompt 16's deliverable (the per-project template) that agentmon is required and provide the service block as part of prompt 11's output inside `internal/agentmon/README.md`:

```yaml
agentmon:
  image: localhost:5000/drem-agentmon:latest
  environment:
    DREM_PROJECT: "${PROJECT_NAME}"
    DREM_ORCH_URL: "http://orch:8080"
    DREM_AGENTMON_TOKEN: "${AGENTMON_TOKEN}"
  volumes:
    - /var/run/docker.sock:/var/run/docker.sock:ro
  networks: [drem-net]
  labels:
    drem.project: "${PROJECT_NAME}"
    drem.service: agentmon
```

## Scope Limitation

- No duplicate log storage. Raw logs remain in Docker's log driver. Agentmon extracts structured events only.
- No retry queue for failed `Ingest` calls in the first cut. If the orchestrator's `/internal/logs` is unreachable, log the error and drop the batch. A future iteration can add a bounded on-disk buffer.
- Do not change the extraction package or its event types. If agentmon needs a new event type, add it in prompt 02 first.
- Do not modify the orchestrator's `/internal/logs` handler shape. Prompt 08 defined the schema; match it exactly.

## Conventions

- Module path: `github.com/godinj/drem-orchestrator`
- Package name: `agentmon`
- File-length and function-count ceilings per `ARCHITECTURE.md` — if `docker_source.go` grows past the ceiling, split subscription logic into a separate file
- Tests use the fake runtime; no real Docker in unit tests
- Build verification: `go build ./internal/agentmon/... ./cmd/drem-agentmon/... && go test ./internal/agentmon/...`
- Constitution check: `bash scripts/check_constitution.sh`
