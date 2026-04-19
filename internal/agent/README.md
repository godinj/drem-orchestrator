# internal/agent

Two coexisting spawn paths live in this package during the
containerization migration tracked in
`docs/containerization/prompts/13-agent-spawn-routing.md`:

1. **Legacy subprocess path** — `Runner.SpawnAgent` in `runner.go`, used
   today for Claude Code and OpenCode agents and for supervisor/shell
   tmux sessions. Prompt 17 removes this path.
2. **Spawner-routed path** — `Manager.Spawn` in `spawn.go`, which builds
   `spawner.SpawnWorkerParams` and calls the JSON-RPC spawner service
   over its Unix socket. This is the target state; new call sites
   should use it.

## Image resolution

`image_resolver.go` maps an agent type to a container image. Inputs:

- `ImageResolver.Language` — from `drem.toml`'s `[project].language`.
  Supported values match `internal/projects`: `"go"`, `"cpp"`.
- `ImageResolver.Overrides` — built from `drem.toml`'s per-agent
  `container_image` fields via `AgentsConfig.ContainerImageOverrides()`.

Resolution order:

1. Override map lookup by agent type.
2. For `coder`, append `-<language>` and look up `DefaultImages`.
3. For other types, look up `DefaultImages` directly.
4. Otherwise, return a descriptive error.

## Wiring the spawner client

`agent.Spawner` is declared on package-local mirror types
(`WorkerSpawnParams`, `WorkerSpawnResult`) so this package does not
import `internal/spawner`. Callers that want to drive a real
`spawner.Client` declare a tiny adapter where they already know both
types. The adapter is mechanical — three methods, one translation
each:

```go
type adapter struct{ c *spawner.Client }

func (a *adapter) SpawnWorker(ctx context.Context, p agent.WorkerSpawnParams) (agent.WorkerSpawnResult, error) {
    res, err := a.c.SpawnWorker(ctx, spawner.SpawnWorkerParams{
        Project: p.Project, AgentType: p.AgentType, WorkerID: p.WorkerID,
        Branch: p.Branch, Labels: p.Labels, Image: p.Image,
        Env: p.Env, BareRepoMount: p.BareRepoMount,
    })
    if err != nil {
        return agent.WorkerSpawnResult{}, err
    }
    return agent.WorkerSpawnResult{ContainerID: res.ContainerID, Endpoint: res.Endpoint}, nil
}

func (a *adapter) DestroyWorker(ctx context.Context, id string) error {
    return a.c.DestroyWorker(ctx, spawner.DestroyWorkerParams{ContainerID: id})
}
```

The integration test at `spawn_integration_test.go` shows the exact
shape.

## Prompt delivery

The first cut does not yet put prompts inside the container image. The
caller writes the prompt file to the host at

    ~/.drem/projects/<project>/prompts/<agent_id>.md

and the spawner bind-mounts that path into the worker. `Manager.PromptDir`
returns the directory so callers do not have to reconstruct the layout.
The planned future path is to deliver prompts over the spawner RPC
itself; until then the host-mount approach keeps the spawner schema
stable.

## drem.toml schema

```toml
[project]
language = "go"                                      # or "cpp"

[agents.coder]
container_image = "localhost:5000/drem-worker-go:v1.2"  # optional override

[agents.merger]
container_image = "localhost:5000/drem-merger:v1.2"     # optional override
```

A legacy `[tmux]` table is accepted but ignored for one release so
existing drem.toml files keep loading. Prompt 17 removes this
tolerance.

## Heartbeat tracking (transition)

Today `heartbeat.go` writes `Agent.HeartbeatAt` from a goroutine
owned by `Runner`. Post-containerization the authoritative heartbeat
signal arrives from the worker through `agentmon`'s `/internal/logs`
endpoint, and the orchestrator updates `Agent.HeartbeatAt` from there.
Stale-heartbeat kills become `Manager.Teardown` + `Manager.Spawn`
instead of a subprocess `Kill()` + re-exec. That wiring is tracked in
prompt 14 and is deliberately out of scope here.
