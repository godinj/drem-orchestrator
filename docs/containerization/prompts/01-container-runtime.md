# Agent: Container Runtime Abstraction

You are working on the `master` branch of Drem Orchestrator, a Go-based multi-agent task orchestrator that runs Claude Code agents against project worktrees. Your task is Phase 1 foundation work for the containerization initiative: build the container runtime abstraction that every other containerized component (spawner, agentmon, orchestrator event subscription) will depend on.

## Context

Read these specs before starting:

- `docs/prd-containerization.md` (sections: "Modules to be built or modified" → container runtime abstraction; "RPC and HTTP contracts" → Spawner RPC; "Lifecycle and recovery"; "Networking and security")
- `ARCHITECTURE.md` (file-length and function-count ceilings, package import rules)
- `internal/testutil/testutil.go` (test database factory patterns you will follow)

## Prerequisites

The host already has Docker installed and the user's UID is in the `docker` group. Verify:

```bash
docker version
```

The integration test described below requires a live Docker daemon. Gate it behind a build tag so unit tests remain hermetic.

Go module: `github.com/godinj/drem-orchestrator`. Add the Docker SDK dependency:

```bash
go get github.com/docker/docker/client@v25
go get github.com/docker/docker/api/types
```

## Deliverables

### New files (`internal/container/`)

#### 1. `runtime.go`

The runtime interface plus the value types it operates on.

- `type Runtime interface { Spawn(ctx context.Context, spec Spec) (Handle, error); Inspect(ctx context.Context, id string) (State, error); StreamLogs(ctx context.Context, id string) (io.ReadCloser, error); SubscribeEvents(ctx context.Context, filter EventFilter) (<-chan Event, error); Destroy(ctx context.Context, id string) error }`
- `type Spec struct { Image string; Cmd []string; Env map[string]string; Labels map[string]string; Mounts []Mount; Network string; Workdir string; AutoRemove bool }`
- `type Mount struct { Source string; Target string; ReadOnly bool }` (source is a host path for bind mounts; for the worker-clone pattern in the PRD, the bare repo is the only read-only mount a worker needs)
- `type Handle struct { ID string; Endpoint string }` (Endpoint is optional and used when the container exposes an HTTP or RPC port that the caller will connect to next)
- `type State struct { Status Status; ExitCode int; StartedAt time.Time; FinishedAt time.Time; OOMKilled bool }`
- `type Status string` with constants `StatusCreated`, `StatusRunning`, `StatusExited`, `StatusDead`, `StatusRemoved`
- `type EventFilter struct { Labels map[string]string }` (match any container whose labels are a superset of the filter)
- `type Event struct { Type EventType; ContainerID string; Labels map[string]string; Timestamp time.Time; ExitCode int; OOMKilled bool }`
- `type EventType string` with constants `EventStart`, `EventDie`, `EventOOM`, `EventDestroy`

No method on these types should perform I/O. The interface is the only I/O surface.

#### 2. `docker.go`

`DockerRuntime` — the production implementation of `Runtime` backed by the Docker Engine API over the default Unix socket.

- `func NewDockerRuntime() (*DockerRuntime, error)` — constructs a client via `client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())`
- `func (r *DockerRuntime) Spawn(...)` — creates the container with `ContainerCreate`, starts it with `ContainerStart`, returns `Handle{ID: ...}`. Populate `Endpoint` only when `spec.Network` is set and the caller specifies a single port (keep the current cut minimal — leave `Endpoint` empty if no port info is available)
- `func (r *DockerRuntime) Inspect(...)` — calls `ContainerInspect`, maps `state.Status` to our `Status` constants, copies `ExitCode`, `StartedAt` (parse RFC3339), `FinishedAt`, `OOMKilled`
- `func (r *DockerRuntime) StreamLogs(...)` — calls `ContainerLogs` with `{ShowStdout: true, ShowStderr: true, Follow: true}`, returns the io.ReadCloser (the caller demuxes the Docker stream header if it needs split stdout/stderr — for now we multiplex and let agentmon handle it)
- `func (r *DockerRuntime) SubscribeEvents(...)` — calls `Events` with filter args built from `EventFilter.Labels`, fans messages onto the returned channel, closes the channel when ctx is cancelled
- `func (r *DockerRuntime) Destroy(...)` — calls `ContainerRemove` with `Force: true`

Keep `docker.go` under the file-length ceiling in `ARCHITECTURE.md`. If you exceed, split into `docker_spawn.go`, `docker_events.go`, `docker_logs.go`.

#### 3. `fake.go`

`FakeRuntime` — an in-memory implementation used by spawner and orchestrator unit tests. Every mutating call is recorded for assertions.

- `type FakeRuntime struct { mu sync.Mutex; containers map[string]*fakeContainer; calls []Call; events chan Event }`
- `type Call struct { Op string; Spec *Spec; ID string }` (Op is `"Spawn"`, `"Inspect"`, `"Destroy"`, etc.)
- `func NewFakeRuntime() *FakeRuntime`
- `func (f *FakeRuntime) Calls() []Call` — returns a copy
- `func (f *FakeRuntime) EmitEvent(ev Event)` — test helper to push a synthetic event to all active subscribers
- `func (f *FakeRuntime) SetInspectResult(id string, st State)` — test helper

`Spawn` allocates a synthetic ID (`"fake-" + uuid`), records the call, stores the spec, returns a `Handle` immediately. `Inspect` returns the last value passed to `SetInspectResult` (default: `StatusRunning`). `StreamLogs` returns a reader over a per-container byte buffer that tests can write to via a helper `WriteLog(id string, data []byte)`. `SubscribeEvents` returns a channel that receives events emitted via `EmitEvent` whose labels match the filter.

### Tests

#### 4. `runtime_test.go`

Unit tests against `FakeRuntime`. Cover:

- `Spawn` returns a non-empty ID and the call is recorded.
- `Destroy` on an unknown ID returns an error.
- `SubscribeEvents` receives only events whose labels are a superset of the filter; non-matching events are dropped.
- Cancelling the subscribe context closes the channel.

#### 5. `docker_integration_test.go`

Behind build tag `//go:build integration`. Skipped by default.

- Uses `NewDockerRuntime()`, spawns `alpine:3` with `Cmd: []string{"echo", "hello"}` and label `drem.test=1`, waits for `EventDie`, asserts `State.ExitCode == 0`, then `Destroy`s the container.

## Scope Limitation

- Do not build the spawner service here. This package exposes only the runtime interface. The spawner service (prompt 07) will wrap `Runtime` and expose RPC methods.
- Do not implement MCP or any HTTP surface. Pure library package.
- Do not add agent-type-to-image mapping logic here. That lives in the agent package (prompt 13).
- The Docker event channel is best-effort. If Docker's event stream drops or disconnects, log and return an error — do not silently reconnect. Reconnection policy belongs to callers.

## Conventions

- Module path: `github.com/godinj/drem-orchestrator`
- Package name: `container`
- Follow the file-length and function-count ceilings in `ARCHITECTURE.md`
- No direct `os/exec` calls; use the Docker SDK only
- Tests: use `github.com/stretchr/testify/require` for assertions, consistent with `internal/testutil/` patterns
- Build verification: `go build ./internal/container/... && go test ./internal/container/...`
- Integration test verification (optional, requires Docker): `go test -tags=integration ./internal/container/...`
