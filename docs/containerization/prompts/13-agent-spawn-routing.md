# Agent: Agent Package Spawn Routing

You are working on the `master` branch of Drem Orchestrator, a Go-based multi-agent task orchestrator. Your task is Phase 3 integration work for the containerization initiative: route `internal/agent/`'s agent-lifecycle operations through the spawner RPC client instead of direct subprocess exec, and add an agent-type-to-image mapping driven by the project's declared language and any per-agent-type container image overrides in `drem.toml`.

## Context

Read these specs before starting:

- `docs/prd-containerization.md` (sections: "Modified modules" → agent package; "Images" → agent-type-to-image mapping; user stories 20, 32, 33, 49)
- `internal/agent/` (existing agent lifecycle code — spawn, heartbeat, status, teardown)
- `internal/spawner/client.go` (prompt 07 — the RPC client this package calls)
- `internal/spawner/images.go` (prompt 07 — the default image map; this package may override per-project)
- `drem.toml` (existing parsing — where to add `language` and `container_image` override fields)
- `ARCHITECTURE.md`

## Dependencies

- Prompt 07 (`internal/spawner/`) — the RPC client must exist

## Deliverables

### New files

#### 1. `internal/agent/image_resolver.go`

- `type ImageResolver struct { Language string; Overrides map[string]string }` — `Language` is the project's language from `drem.toml` (`"go"` or `"cpp"`); `Overrides` maps agent type → image tag from `drem.toml`'s per-agent-type `container_image` field
- `func (r *ImageResolver) Resolve(agentType string) (string, error)` — first checks `Overrides[agentType]`, then falls back to language-specialized default (e.g. `"coder"` + `"go"` → `drem-worker-go:latest`); returns a meaningful error if no mapping applies
- Default map (consistent with prompt 07):

```go
var defaults = map[string]string{
    "coder-go":       "localhost:5000/drem-worker-go:latest",
    "coder-cpp":      "localhost:5000/drem-worker-cpp:latest",
    "g4":             "localhost:5000/drem-worker-go:latest",
    "merger":         "localhost:5000/drem-merger:latest",
    "reviewer":       "localhost:5000/drem-worker-go:latest",  // reviewer shares worker toolchain
    "fixer":          "localhost:5000/drem-worker-go:latest",
    "supervisor":     "localhost:5000/drem-worker-go:latest",
    "classifier":     "localhost:5000/drem-worker-go:latest",
}
```

Expose `defaults` as a package-level var so prompt 07 and prompt 13 stay consistent. If both land independently, document that the spawner's own default map is the authoritative source and the agent package delegates via `ImageResolver` → `spawner.DefaultImage(agentType)` helper (add that helper in prompt 07 if it's not there yet).

#### 2. `internal/agent/spawn.go` (new file or major rewrite of existing spawn path)

- `type Spawner interface { SpawnWorker(ctx, spawner.SpawnWorkerParams) (*spawner.SpawnWorkerResult, error); DestroyWorker(ctx, string) error }`
- `func (m *Manager) Spawn(ctx context.Context, req SpawnRequest) (*Handle, error)` — builds `SpawnWorkerParams` from `req`, resolves the image via `ImageResolver`, calls `Spawner.SpawnWorker`, records the returned container ID in the agent DB row, returns a `Handle` with container ID, endpoint, and started-at
- `type SpawnRequest struct { Project string; AgentType string; AgentID string; TaskID string; Branch string; Env map[string]string; PromptPath string }` — `PromptPath` is a file inside the container (written by the orchestrator before spawn via a volume mount) that the worker's entrypoint reads
- `type Handle struct { ContainerID string; AgentID string; StartedAt time.Time; Image string }`

#### 3. `internal/agent/teardown.go` (extract or add)

- `func (m *Manager) Teardown(ctx context.Context, handle *Handle) error` — calls `Spawner.DestroyWorker(handle.ContainerID)`, updates the agent DB row with final status

### Migration

#### 4. Existing agent spawn sites

Find every place in `internal/agent/` that currently exec's a Claude session (look for `os/exec`, `exec.Command`, and tmux-based launches). Replace with `Manager.Spawn`. The DB row shape may need a new field `ContainerID string` — add it as an additive migration in `internal/model/`.

#### 5. Heartbeat tracking

Current heartbeat tracking likely watches a file or a subprocess. In the new world, heartbeats arrive through agentmon's `/internal/logs` ingestion as `Heartbeat` records (see prompts 02, 11). The agent package reads its heartbeat state from the DB column that the orchestrator updates on ingestion — no changes to the ingestion path here, just a confirmation that the agent package's heartbeat-staleness detection queries the same column.

If heartbeat staleness currently triggers a kill via a signal or tmux command, replace with `Spawner.DestroyWorker` + a `Manager.Spawn` for the replacement.

#### 6. `drem.toml` schema extensions

Add to the TOML schema (update the parser in `internal/config/` or wherever `drem.toml` is loaded):

```toml
[project]
language = "go"  # or "cpp"

[agents.coder]
container_image = "localhost:5000/drem-worker-go:v1.2"  # optional override

[agents.merger]
container_image = "localhost:5000/drem-merger:v1.2"     # optional override
```

Remove any existing `[tmux]` section reference from the schema. If a user's existing `drem.toml` has a `[tmux]` block, accept and ignore it for one release cycle; prompt 17 removes the tolerance entirely.

### Tests

#### 7. `internal/agent/image_resolver_test.go`

- `Resolve("coder")` for `Language="go"` returns `drem-worker-go:latest`
- `Resolve("coder")` for `Language="cpp"` returns `drem-worker-cpp:latest`
- `Resolve("merger")` always returns the merger image regardless of language
- An explicit override in `Overrides["coder"]` takes precedence over the language-derived default
- Unknown agent type with no override returns a descriptive error

#### 8. `internal/agent/spawn_test.go`

Use a fake `Spawner` (records calls). Assert:

- `Manager.Spawn` resolves the image via `ImageResolver`
- The `SpawnWorkerParams` passed to the spawner contain the correct project, agent type, agent ID, branch, and labels
- The returned `Handle` carries the container ID from the fake's response
- The agent DB row is written with the container ID and image
- `Teardown` calls `Spawner.DestroyWorker` and updates the DB

#### 9. `internal/agent/spawn_integration_test.go` (build tag `integration`)

Against a real spawner service on a temp Unix socket (wire the fake container runtime behind the spawner). End-to-end: `Manager.Spawn` → spawner RPC → fake runtime records a spawn call.

## Scope Limitation

- **No orchestrator changes.** Prompt 12 handles orchestrator-side rewiring. This prompt only touches `internal/agent/` and the config loader.
- **No tmux removal.** If `internal/agent/` currently imports `internal/tmux/`, remove those imports as you rewire spawn sites. Do not delete `internal/tmux/` itself — prompt 17 handles that. Leave the file in place; just stop importing it.
- **No new lifecycle states.** Use the existing agent status enum.
- **Prompt delivery.** How the orchestrator gets a prompt file into the container is an operational detail (bind a tmpfs, `docker cp`, or env var for small prompts) — the PRD does not dictate. For the first cut, write the prompt to a host path under `~/.drem/projects/<name>/prompts/<agent_id>.md` and bind-mount that directory into the container. Document in `internal/agent/README.md` as an operational note.

## Conventions

- Module path: `github.com/godinj/drem-orchestrator`
- Package name: `agent` (extending existing)
- File-length and function-count ceilings per `ARCHITECTURE.md`
- Tests: `testify/require`, fake spawner, fake runtime
- Build verification: `go build ./internal/agent/... && go test ./internal/agent/...`
- Full suite (sanity): `go test ./...`
- Constitution check: `bash scripts/check_constitution.sh`
