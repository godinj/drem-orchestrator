# Agent: Kyle Binary and Container

You are working on the `master` branch of Drem Orchestrator, a Go-based multi-agent task orchestrator. Your task is Phase 3 integration work for the containerization initiative: build Kyle — a single globally-running container that aggregates state across every registered project by calling each project's orchestrator HTTP API. Kyle is the C-Suite's reporting surface; he answers "what is happening" and "what went wrong" without log archaeology.

## Context

Read these specs before starting:

- `docs/prd-containerization.md` (sections: "Kyle is a global singleton"; "RPC and HTTP contracts"; user stories 6, 12, 13, 14, 15, 51)
- `internal/projects/registry.go` (prompt 05 — Kyle reads `~/.drem/projects.toml` to discover orchestrators)
- `pkg/orchclient/` (prompt 08 — the client Kyle uses to call each orchestrator)
- `internal/csuite/` (existing C-Suite model types — Kyle may reuse the reporting shapes already defined)

## Dependencies

- Prompt 05 (`internal/projects/`) — project discovery
- Prompt 08 (`pkg/orchclient/`, `pkg/orchdto/`) — HTTP client

## Deliverables

### New files

#### 1. `cmd/drem-kyle/main.go`

Binary entry point.

Flags:

- `--registry string` (default `/etc/drem/projects.toml`) — path to the project registry mounted read-only into the container
- `--listen string` (default `:8090`) — Kyle's own HTTP surface, used by the C-Suite and the `drem kyle` CLI subcommand
- `--poll-interval duration` (default `30s`) — how often Kyle refreshes its cache of cross-project state
- `--docker-query-url string` (default `http://docker-query-proxy:9000`) — URL of the read-only Docker query proxy (see Dockerfile 4 below)

On start: load registry, construct `kyle.Service`, serve HTTP, run the background poll loop.

#### 2. `internal/kyle/service.go`

- `type Service struct { Registry *projects.Registry; Clients map[string]*orchclient.Client; DockerQueryURL string; Cache *Cache; PollInterval time.Duration }`
- `func New(reg *projects.Registry, dockerQueryURL string, interval time.Duration) *Service`
- `func (s *Service) Run(ctx context.Context) error` — polls every project's orchestrator, updates the cache, exits on ctx cancel
- `func (s *Service) Routes() http.Handler` — Kyle's HTTP surface

Kyle's endpoints:

- `GET /world` — the unified "what is happening now" view: all projects, all active workers, all in-flight tasks. JSON.
- `GET /world/summary` — human-readable text digest suitable for pasting into a chat message.
- `GET /projects/:name` — one project's detailed state.
- `GET /events?since=&project=` — merged event stream across projects.
- `GET /docker/query?filter=...` — proxies to the docker-query-proxy for deep-dive container inspection (returns container status, exit code, live log tail).

#### 3. `internal/kyle/cache.go`

- `type Cache struct { mu sync.RWMutex; byProject map[string]*ProjectSnapshot; lastRefresh map[string]time.Time }`
- `type ProjectSnapshot struct { Project string; Tasks []orchdto.TaskDTO; Workers []orchdto.WorkerDTO; RecentEvents []orchdto.EventDTO; FetchedAt time.Time }`
- `func (c *Cache) Set(project string, snap *ProjectSnapshot)`
- `func (c *Cache) Get(project string) (*ProjectSnapshot, bool)`
- `func (c *Cache) All() []*ProjectSnapshot`
- Staleness detection: if a project's `FetchedAt` is older than `2 * PollInterval`, mark it in responses as `stale: true`

#### 4. `internal/kyle/poll.go`

- `func (s *Service) pollOnce(ctx context.Context, project projects.Project) error` — calls `ListWorkers`, `ListTasks`, `Events`; builds a `ProjectSnapshot`; writes to cache. On error, log and leave the old snapshot in place (PRD "fail closed": shared services pause projects rather than serve stale data, but Kyle is a reporting tool; stale with a staleness flag is acceptable here)

#### 5. `internal/kyle/docker_query.go`

- `func (s *Service) DockerQueryHandler(w http.ResponseWriter, r *http.Request)` — proxies to `DockerQueryURL` after validating the `filter` query param against an allowlist (labels, container ID, image name; no raw Docker API verbs)

#### 6. `internal/kyle/summary.go`

- `func RenderSummary(snapshots []*ProjectSnapshot) string` — produces a compact text digest. Format:

```
== Drem World State @ 2026-04-19T15:22:00Z ==

drem-orchestrator:
  workers:    3 running (2 coder-go, 1 merger), 0 failed
  tasks:      12 in-flight (4 planning, 5 in_progress, 3 merging)
  recent:     2 commits, 1 merge-success, 0 crashes
  health:     OK

drem-canvas:
  workers:    1 running (1 coder-cpp)
  tasks:      3 in-flight (1 in_progress, 2 testing_ready)
  recent:     1 build-error, 1 test-result (passed)
  health:     OK
```

Kyle's `/world/summary` endpoint returns this text.

### Tests

#### 7. `internal/kyle/service_test.go`

- Stand up a fake orchestrator (using `httptest.NewServer` per project) returning canned responses
- Write a registry file with two projects pointing at the fake orchestrators
- Run `pollOnce` for each; assert the cache populates correctly
- `GET /world` returns a JSON body with both projects; `GET /projects/<name>` returns one
- When a fake orchestrator returns 500, the cache retains the old snapshot and the response carries `stale: true`

#### 8. `internal/kyle/summary_test.go`

Given two canned `ProjectSnapshot`s, assert the rendered summary contains the expected lines. Golden-file style is fine; commit the golden under `internal/kyle/testdata/summary_basic.txt`.

### Docker artifacts

#### 9. `deploy/docker/kyle.Dockerfile`

Multi-stage Go build. Runtime: distroless. Bind-mount `~/.drem/projects.toml` at `/etc/drem/projects.toml` read-only. Tag: `localhost:5000/drem-kyle:latest`.

#### 10. `deploy/docker/docker-query-proxy.Dockerfile`

A small read-only proxy so Kyle never touches the raw Docker socket.

Choose one of two implementations; document whichever you pick inline:

- **socat + Docker TCP:** `socat TCP-LISTEN:9000,reuseaddr,fork UNIX-CONNECT:/var/run/docker.sock` with an `iptables`/nftables allowlist limiting HTTP methods to `GET` and paths to `/containers/json`, `/containers/*/json`, `/containers/*/logs`, `/events`. Deny everything else.
- **Thin Go proxy:** `cmd/drem-docker-query-proxy/main.go` — HTTP server that accepts a narrow API (`GET /containers?labels=...`, `GET /containers/:id`, `GET /containers/:id/logs?since=`) and translates to Docker SDK calls. Rejects any other path with 403.

Prefer the Go proxy for clearer allowlisting. Tag: `localhost:5000/drem-docker-query-proxy:latest`.

#### 11. Additions to prompt 06's global compose

```yaml
kyle:
  image: localhost:5000/drem-kyle:latest
  volumes:
    - ${HOME}/.drem/projects.toml:/etc/drem/projects.toml:ro
  networks: [drem-net]
  restart: unless-stopped
  labels:
    drem.scope: global
    drem.service: kyle
  ports:
    - "127.0.0.1:8090:8090"

docker-query-proxy:
  image: localhost:5000/drem-docker-query-proxy:latest
  volumes:
    - /var/run/docker.sock:/var/run/docker.sock:ro
  networks: [drem-net]
  restart: unless-stopped
  labels:
    drem.scope: global
    drem.service: docker-query-proxy
```

### MCP Integration

#### 12. `internal/kyle/mcp.go` (optional; only if the existing C-Suite tooling uses MCP)

If the project's existing C-Suite agents use an MCP server for tool access, expose `docker_query` as an MCP tool that wraps `GET /docker/query`. Register it in the C-Suite's MCP config. If MCP is not in use, skip this and expose `docker_query` as a plain HTTP call that the C-Suite agents learn via prompt documentation.

Search the repo for existing MCP wiring (`grep -r "mcp" cmd/ internal/` or similar) to decide.

## Scope Limitation

- Kyle is read-only. It never writes to any orchestrator or any SQLite database.
- Kyle has no authority over any project. It does not spawn, kill, or reprioritize tasks. It only reports.
- Kyle runs from the baked image, never bind-mounted source (per PRD user story 38). Do not add a dev-mode bind-mount path for Kyle.
- No alerting in the first cut. Kyle's job is to answer questions. Proactive notification is a follow-up.
- No retention policy on Kyle's cache. The cache holds only the latest snapshot per project; event history is queried from the orchestrator on demand via `/events`.

## Conventions

- Module path: `github.com/godinj/drem-orchestrator`
- Package name: `kyle`
- Binary name: `drem-kyle`, output `bin/drem-kyle`
- Second binary (if chosen): `drem-docker-query-proxy`
- File-length and function-count ceilings per `ARCHITECTURE.md`
- Tests: `testify/require`, `httptest.NewServer` for each fake orchestrator
- Build verification: `go build ./cmd/drem-kyle/... ./internal/kyle/... && go test ./internal/kyle/...`
- Constitution check: `bash scripts/check_constitution.sh`
