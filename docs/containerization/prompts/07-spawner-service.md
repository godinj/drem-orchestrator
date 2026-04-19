# Agent: Spawner Service

You are working on the `master` branch of Drem Orchestrator, a Go-based multi-agent task orchestrator. Your task is Phase 2 work for the containerization initiative: build the spawner service — the only process in the stack that holds the Docker socket. Every other component (orchestrator, agentmon) talks to the spawner over a narrow JSON-RPC 2.0 surface on a Unix socket.

## Context

Read these specs before starting:

- `docs/prd-containerization.md` (sections: "Spawner service owns the Docker socket"; "RPC and HTTP contracts" → Spawner RPC; "Networking and security"; user stories 20, 21)
- `internal/container/runtime.go` (produced by prompt 01 — the Runtime interface this service wraps)
- `internal/container/fake.go` (produced by prompt 01 — use in tests here)
- `ARCHITECTURE.md` (file-length, function-count, import ceilings)

## Dependencies

This agent depends on prompt 01 (`internal/container/`). If that package does not yet exist, create a stub header at `internal/container/runtime.go` containing only the `Runtime` interface, `Spec`, `Handle`, `State`, `Event`, `EventFilter` type declarations from prompt 01, and implement against that stub. Do not stub `FakeRuntime`; it must come from prompt 01.

## Deliverables

### New files

#### 1. `cmd/drem-spawner/main.go`

Binary entry point.

Flags:

- `--socket string` (default `/var/run/drem/spawner.sock`) — Unix socket path
- `--runtime string` (default `"docker"`) — `"docker"` or `"fake"` for local testing without Docker

On start: construct the runtime (`container.NewDockerRuntime()` or `container.NewFakeRuntime()`), remove any stale socket file, `net.Listen("unix", ...)`, run `spawner.Serve(ctx, listener, runtime)`, install SIGTERM/SIGINT handlers for clean shutdown.

#### 2. `internal/spawner/service.go`

The JSON-RPC 2.0 server.

- `type Service struct { Runtime container.Runtime }`
- `func Serve(ctx context.Context, ln net.Listener, rt container.Runtime) error` — accepts connections, each connection runs one `conn.Handle` goroutine
- Per-connection handler reads `Content-Length: N\r\n\r\n<json>` framed JSON-RPC messages (standard LSP-style framing), dispatches to the method, writes the framed response
- Method table:
  - `SpawnWorker(params SpawnWorkerParams) (SpawnWorkerResult, error)`
  - `DestroyWorker(params DestroyWorkerParams) (EmptyResult, error)`
  - `ListWorkers(params ListWorkersParams) (ListWorkersResult, error)`
  - `InspectWorker(params InspectWorkerParams) (InspectWorkerResult, error)`
- On unknown method, return JSON-RPC error `-32601` (Method not found)
- On invalid params, return `-32602`
- On runtime error, return `-32000` with the error message in `data`

#### 3. `internal/spawner/types.go`

Request and response types. Every request has JSON field tags matching the RPC contract.

- `type SpawnWorkerParams struct { Project string; AgentType string; WorkerID string; Branch string; Labels map[string]string; Image string; Env map[string]string; BareRepoMount string }` — `BareRepoMount` is the host path of the project's bare repo; the spawner mounts it read-only at `/bare` inside the container. `Image` overrides the default agent-type-to-image mapping; empty means use the default for `AgentType`.
- `type SpawnWorkerResult struct { ContainerID string; Endpoint string }`
- `type DestroyWorkerParams struct { ContainerID string }`
- `type EmptyResult struct{}`
- `type ListWorkersParams struct { Project string }` — empty project means list all drem-labeled containers
- `type WorkerInfo struct { ContainerID string; Project string; AgentType string; WorkerID string; Branch string; Status string; StartedAt time.Time }`
- `type ListWorkersResult struct { Workers []WorkerInfo }`
- `type InspectWorkerParams struct { ContainerID string }`
- `type InspectWorkerResult struct { Status string; ExitCode int; StartedAt time.Time; FinishedAt time.Time; OOMKilled bool }`

#### 4. `internal/spawner/methods.go`

Implementation of each method against `container.Runtime`.

- `SpawnWorker`: build a `container.Spec` — set `Labels` to include `drem.project`, `drem.agent_type`, `drem.worker_id`, `drem.branch` plus the caller-provided labels; add the bare-repo mount (read-only at `/bare`); wire `Env`; set `Network: "drem-net"`; resolve `Image` via the agent-type-to-image mapping (see below) if empty; call `Runtime.Spawn`
- `DestroyWorker`: call `Runtime.Destroy`
- `ListWorkers`: the Runtime interface from prompt 01 does not expose List. Add a capability detection: assert the runtime implements `interface { List(ctx, filter) ([]Handle, error) }` for the docker runtime, fall back to an in-memory map for the fake. If this requires extending the Runtime interface, coordinate by adding the `List` method in prompt 01's deliverable set — prefer to keep the interface narrow and use Docker's label filter via an additional method documented in a comment.
- `InspectWorker`: call `Runtime.Inspect`, map to `InspectWorkerResult`

**Agent-type-to-image mapping.** Keep the mapping in a single var inside `internal/spawner/images.go`:

```go
var defaultImages = map[string]string{
    "coder-go":       "localhost:5000/drem-worker-go:latest",
    "coder-cpp":      "localhost:5000/drem-worker-cpp:latest",
    "g4":             "localhost:5000/drem-worker-go:latest",
    "merger":         "localhost:5000/drem-merger:latest",
    "csuite-mike":    "localhost:5000/drem-csuite-mike:latest",
    "csuite-alex":    "localhost:5000/drem-csuite-alex:latest",
    "csuite-ross":    "localhost:5000/drem-csuite-ross:latest",
    "csuite-seth":    "localhost:5000/drem-csuite-seth:latest",
}
```

Language-sensitive lookup: if `AgentType == "coder"`, append `-<language>` derived from the caller's `Labels["drem.language"]` before lookup.

#### 5. `internal/spawner/client.go`

Client library so other packages (orchestrator in prompt 12, agent package in prompt 13) don't hand-roll JSON-RPC framing.

- `type Client struct { socket string }`
- `func NewClient(socket string) *Client`
- One method per RPC: `SpawnWorker(ctx, params) (*SpawnWorkerResult, error)`, etc.
- Each call dials the socket, writes a framed request, reads the framed response, returns. No connection pooling for the first cut.

#### 6. `deploy/docker/spawner.Dockerfile`

Multi-stage Go build. Runtime stage is `gcr.io/distroless/static-debian12`. The Docker socket is bind-mounted at runtime via compose (`/var/run/docker.sock:/var/run/docker.sock`); the spawner socket dir `/var/run/drem/` is a tmpfs or compose-managed volume so other containers can mount it.

Tag: `localhost:5000/drem-spawner:latest`.

### Tests

#### 7. `internal/spawner/service_test.go`

Use `container.NewFakeRuntime()`. Stand up the service on a temp Unix socket (`t.TempDir()`), use the client to call each method, assert:

- `SpawnWorker` produces exactly one `Spawn` call on the fake runtime with the expected labels and mounts
- The returned `ContainerID` matches the fake's allocated ID
- `DestroyWorker` produces a `Destroy` call with the correct ID
- `InspectWorker` returns the state the test injected via `fake.SetInspectResult`
- `ListWorkers` filters by project label
- Invalid method name returns JSON-RPC error `-32601`
- Malformed params return `-32602`

#### 8. `internal/spawner/client_test.go`

Round-trip tests against `httptest`-equivalent Unix-socket harness: write a mock server that returns canned framed responses, assert the client produces the expected method name and params in the request.

### Additions to `deploy/compose/global.yml` (prompt 06's file)

Append a `spawner` service:

```yaml
spawner:
  image: localhost:5000/drem-spawner:latest
  volumes:
    - /var/run/docker.sock:/var/run/docker.sock
    - drem-spawner-sock:/var/run/drem
  networks: [drem-net]
  restart: unless-stopped
  labels:
    drem.scope: global
    drem.service: spawner
```

And add the named volume `drem-spawner-sock:` to the `volumes:` stanza.

## Scope Limitation

- The spawner does not touch any SQLite database. It does not know about tasks, projects (beyond the label it passes through), or state transitions.
- The spawner does not stream logs. Log streaming is agentmon's job via `container.Runtime.StreamLogs` in the agentmon process (prompt 11).
- The spawner does not subscribe to Docker events. Event subscription is the orchestrator's job in prompt 12.
- No authentication on the Unix socket beyond filesystem permissions. Only other drem containers mount the socket dir.
- No rate limiting on spawn requests. The orchestrator is the single caller and self-limits via its tick loop.

## Conventions

- Module path: `github.com/godinj/drem-orchestrator`
- Package name: `spawner`
- Binary name: `drem-spawner`, output `bin/drem-spawner`
- File-length, function-count, and import ceilings per `ARCHITECTURE.md`
- JSON-RPC 2.0 framing: `Content-Length: N\r\n\r\n<json>` (LSP-style). Do not invent a custom framing.
- Tests: `testify/require`, fake runtime from `internal/container`
- Build verification: `go build ./cmd/drem-spawner/... ./internal/spawner/... && go test ./internal/spawner/...`
- Integration verification (requires prompt 01 Docker impl + a live Docker daemon): `go test -tags=integration ./internal/spawner/...`
- Constitution check: `bash scripts/check_constitution.sh`
