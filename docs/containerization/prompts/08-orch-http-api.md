# Agent: Orchestrator HTTP API and Client Library

You are working on the `master` branch of Drem Orchestrator, a Go-based multi-agent task orchestrator. Your task is Phase 2 work for the containerization initiative: build the HTTP API package that exposes read-only state to Kyle and the TUI, plus the internal ingestion endpoint that agentmon posts to, plus a client library Kyle imports. The orchestrator remains the sole SQLite writer — this API is the only surface through which agents and reporting tools read state.

## Context

Read these specs before starting:

- `docs/prd-containerization.md` (sections: "Orchestrator is the single state surface per project"; "RPC and HTTP contracts" → Orchestrator HTTP; user stories 13, 14, 28, 40, 47, 48)
- `internal/model/` (existing GORM models — `Task`, `Agent`/`Worker`, events; match field names)
- `internal/orchestrator/` (existing task and agent state accessors you will query)
- `internal/db/` (how GORM is constructed; reuse the same `*gorm.DB` wiring)
- `internal/testutil/testutil.go` (`NewTestDB`, `NewTestDBWithModels`)
- `ARCHITECTURE.md` (file-length and function-count ceilings)

## Deliverables

### New files

#### 1. `internal/orchhttp/server.go`

- `type Server struct { DB *gorm.DB; SharedToken string; DockerLogs LogStreamer }` — `LogStreamer` is an interface (see below) so tests inject a fake instead of hitting the real Docker client
- `type LogStreamer interface { StreamLogs(ctx context.Context, containerID string) (io.ReadCloser, error) }`
- `func New(db *gorm.DB, token string, logs LogStreamer) *Server`
- `func (s *Server) Routes() http.Handler` — returns an `http.ServeMux` with all endpoints wired
- Shared middleware: request logging, panic recovery, `X-Drem-Agentmon-Token` check for `/internal/*` routes only

#### 2. `internal/orchhttp/handlers_public.go`

Read-only endpoints. All return JSON. All accept only `GET`.

- `GET /projects` — returns the current project record from `drem.toml` plus live worker counts (this orchestrator instance knows about one project; Kyle calls one orchestrator per project and stitches the results)
- `GET /projects/:name/tasks?status=&limit=&offset=` — paginated task list
- `GET /projects/:name/workers` — current worker list (queried from the orchestrator's in-memory + DB view)
- `GET /workers/:id` — single worker, including current status, current task, container ID, agent type, branch, started-at, last heartbeat
- `GET /workers/:id/history` — recent state transitions and exit events for the worker
- `GET /events?since=<rfc3339>&limit=` — the structured event stream agentmon ingested
- `GET /logs?container=<id>&since=<rfc3339>&follow=<bool>` — proxies `docker logs` via `LogStreamer`; responds with `text/plain; charset=utf-8` and `Transfer-Encoding: chunked`; `follow=true` keeps the connection open

#### 3. `internal/orchhttp/handlers_internal.go`

Authenticated via per-project shared token in `X-Drem-Agentmon-Token` header.

- `POST /internal/logs` — accepts a JSON body `{ "records": [...] }` where each record is one of the extract package's event types plus `container_id` and `worker_id` fields. Writes all records to the event table in a single transaction. Returns `202 Accepted` with `{"accepted": N}`.

The record shape is a discriminated union, tagged by a `type` field:

```json
{"type":"commit","container_id":"...","worker_id":"...","timestamp":"...","sha":"...","branch":"...","message":"..."}
{"type":"push","container_id":"...","worker_id":"...","timestamp":"...","branch":"...","remote":"..."}
{"type":"test_result","container_id":"...","worker_id":"...","timestamp":"...","passed":true,"summary":"..."}
{"type":"build_error","container_id":"...","worker_id":"...","timestamp":"...","tool":"...","message":"...","file":"...","line":0}
{"type":"heartbeat","container_id":"...","worker_id":"...","timestamp":"...","agent_id":"..."}
{"type":"crash","container_id":"...","worker_id":"...","timestamp":"...","reason":"...","exit_code":0}
{"type":"tool_call","container_id":"...","worker_id":"...","timestamp":"...","tool":"...","target":"..."}
```

These align with the event types in `internal/extract/` (prompt 02).

#### 4. `internal/orchhttp/types.go`

DTO types for each response shape. Use exported struct types with JSON tags. Do not leak GORM models directly — always marshal through a DTO so schema changes to models do not break the API.

- `type ProjectDTO struct { Name string; Language string; OrchURL string; WorkerCount int }`
- `type TaskDTO struct { ID string; Title string; Status string; CreatedAt time.Time; UpdatedAt time.Time; AssignedWorker string }`
- `type WorkerDTO struct { ID string; ContainerID string; Project string; AgentType string; Branch string; Status string; StartedAt time.Time; LastHeartbeat time.Time; CurrentTask string }`
- `type WorkerHistoryDTO struct { WorkerID string; Events []WorkerHistoryEntry }`
- `type WorkerHistoryEntry struct { Timestamp time.Time; Kind string; Detail string; ExitCode int }`
- `type EventDTO struct { Timestamp time.Time; Type string; Payload json.RawMessage }`
- `type IngestRequest struct { Records []json.RawMessage }` — handlers_internal decodes the discriminated union based on `type`

#### 5. `internal/orchhttp/server_test.go`

Tests use `httptest.NewServer(srv.Routes())`. Seed a `testutil.NewTestDBWithModels` with task/worker/event fixtures.

- `GET /projects` returns the expected DTO
- `GET /projects/<name>/tasks?status=backlog` filters correctly
- `GET /workers/<id>` returns 404 on unknown, 200 with DTO on known
- `GET /logs?container=abc` calls `LogStreamer.StreamLogs("abc")` exactly once and pipes its output through unchanged
- `POST /internal/logs` without `X-Drem-Agentmon-Token` returns 401
- `POST /internal/logs` with the token ingests all records and the count matches
- `POST /internal/logs` with an unknown `type` returns 400 and ingests none (all-or-nothing per request)

Use a tiny fake `LogStreamer` (returns a `strings.NewReader` wrapped in `io.NopCloser`).

### Client library

#### 6. `pkg/orchclient/client.go`

Kyle and the TUI import this; it must not depend on any `internal/` package except DTO types if they live in `internal/orchhttp`. Move DTOs to `pkg/orchdto` if you need Kyle to import them without pulling `internal/orchhttp` — otherwise keep them in `internal/orchhttp` and declare separate client-side mirror types here. Prefer the shared `pkg/orchdto` approach.

- `type Client struct { baseURL string; http *http.Client }`
- `func New(baseURL string) *Client`
- One method per public endpoint: `ListProjects(ctx)`, `ListTasks(ctx, project, filter)`, `ListWorkers(ctx, project)`, `GetWorker(ctx, id)`, `WorkerHistory(ctx, id)`, `Events(ctx, since, limit)`, `StreamLogs(ctx, container, since, follow) (io.ReadCloser, error)`
- Each method wraps a `GET`, decodes JSON into the DTO, returns

#### 7. `pkg/orchclient/client_test.go`

`httptest.NewServer` + a stub handler returning canned JSON. Assert each client method hits the expected path with the expected query params and decodes the response correctly.

## Migration

#### 8. TUI direct DB reads

**Do not change the TUI here.** Prompt 15 swaps TUI reads from GORM to `pkg/orchclient`. This prompt only guarantees the API and client exist so prompt 15 has something to call.

#### 9. Orchestrator wiring

Add a single call site in `cmd/drem/` or the orchestrator binary that, at startup, constructs the `Server` and starts it on the HTTP port declared in `drem.toml`. The orchestrator already has a main loop; add the HTTP listener as a goroutine that the main context cancels cleanly.

If `drem.toml` does not yet have an `orch_http_port` field, add it with a default of `8080` and a comment noting Kyle and TUI both read from it.

## Scope Limitation

- Read-only public API only. No mutations from Kyle, the TUI, or any external caller. The only write endpoint is `/internal/logs` used by agentmon.
- No WebSocket or SSE surface in the first cut. `/logs?follow=true` uses chunked HTTP; no upgrade.
- No authentication on public endpoints. Network isolation (the `drem-net` docker network) plus the fact that only local Kyle and the local TUI call these endpoints is sufficient for the first cut.
- No pagination beyond `limit`/`offset`. Cursor pagination is a follow-up if event volume grows.

## Conventions

- Module path: `github.com/godinj/drem-orchestrator`
- Package name: `orchhttp` (internal) and `orchclient` (public)
- Keep public types in `pkg/orchdto` to avoid forcing Kyle to import `internal/`
- File-length and function-count ceilings per `ARCHITECTURE.md`
- Tests: `testify/require`, `testutil.NewTestDBWithModels`, `httptest` for both server and client
- Build verification: `go build ./internal/orchhttp/... ./pkg/orchclient/... ./pkg/orchdto/... && go test ./internal/orchhttp/... ./pkg/orchclient/...`
- Constitution check: `bash scripts/check_constitution.sh`
