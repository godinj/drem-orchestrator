# Agent: cmd/drem + TUI Decouple from tmux/worktree

You are working on the `master` branch of Drem Orchestrator, a Go-based multi-agent task orchestrator. Your task is the third and last pre-deletion follow-up prompt: get `cmd/drem/main.go` + `cmd/drem/cli_cmd.go` off direct imports of `internal/tmux/` and `internal/worktree/` (via a tiny bridge package), and swap the TUI's tmux-pane/worktree-path rendering to container IDs from the spawner's HTTP API.

After this prompt lands, the repo-wide grep for `internal/tmux|internal/worktree` (excluding self-imports inside those packages and the new bridge package) must be empty — which makes prompt 21 possible.

## Context

Read these specs before starting:

- `docs/containerization/remaining-work.md` (sections: Step 5, Step 7)
- `cmd/drem/main.go` and `cmd/drem/cli_cmd.go` (current bootstrap — wires `worktree.NewManager()` and `tmux.NewManager()`)
- `internal/tui/app.go` (current rendering — shows tmux pane IDs + worktree paths)
- `internal/tui/datasource.go` (landed in prompt 15 — HTTP-backed data source)
- `internal/tui/dto_adapter.go` (landed in prompt 15)
- `internal/spawner/` (the RPC service — prompt 07; `ListWorkers` is the source of container info)
- `pkg/orchclient/` (HTTP client for orchestrator — prompt 08)
- `pkg/orchdto/` (DTOs — prompt 08)
- `internal/orchestrator/worktree_manager.go` (the interface introduced in prompt 18)

## Dependencies

- Prompt 18 (WorktreeManager interface — the shape `cmd/drem/` will wire)
- Prompt 19 (agent runner doesn't import tmux — removes the transitive reason `cmd/drem/` constructed a `tmux.Manager`)
- Baseline grep — record before starting:

```bash
grep -rn "internal/tmux\|internal/worktree" cmd/ internal/ pkg/ --include="*.go" \
  | grep -v "internal/tmux/\|internal/worktree/" | wc -l
```

## Deliverables

Two independent tracks. Can be done serially in one agent session.

---

### Track A — cmd/drem/ decouple

#### 1. New package: `internal/wtbridge/`

The ship-and-hide shim. Isolates the `internal/worktree/` import from everything outside the bridge, so `cmd/drem/` can stay import-clean.

```go
// Package wtbridge constructs a concrete WorktreeManager for the CLI.
// It exists only to localize the last remaining import of internal/worktree/
// so the rest of the repo does not need to know about the package's existence.
// Remove this package in prompt 21 alongside internal/worktree/.
package wtbridge

import (
    "github.com/godinj/drem-orchestrator/internal/orchestrator"
    "github.com/godinj/drem-orchestrator/internal/worktree"
)

func NewManager(opts Options) (orchestrator.WorktreeManager, error) {
    return worktree.NewManager(...), nil
}

type Options struct {
    BareRepoPath  string
    DefaultBranch string
    // any existing NewManager options
}
```

Only `internal/wtbridge/` imports `internal/worktree/` outside `internal/worktree/` itself. Mark the package with a file-header comment indicating it is scheduled for deletion in prompt 21.

#### 2. Rewire `cmd/drem/main.go`

- Replace `worktree.NewManager(...)` construction with `wtbridge.NewManager(...)`.
- Remove the `internal/worktree` import from `cmd/drem/main.go`.
- Delete any `tmux.NewManager()` construction. The agent package owns tmux-equivalent lifecycle via `agent.Manager` (prompt 19). If some CLI flag or subcommand was passing tmux options through, trace what it did and either drop the flag or route it to `agent.Manager` configuration.
- Remove the `internal/tmux` import.

#### 3. Rewire `cmd/drem/cli_cmd.go`

Same treatment. Any CLI subcommand that attached to a tmux session (`drem attach` or equivalent) should route via the orchestrator HTTP API → `docker exec` / `docker attach` against the container ID. For the first cut, a thin `drem attach <agentID>` that shells out to `docker attach` (after resolving the container ID via `pkg/orchclient`) is acceptable.

If a CLI command resists migration (e.g. something deeply tmux-session-aware), flag it in the prompt output rather than leaving a dangling import — prompt 21 cannot succeed with one in place.

#### 4. Scripts

Search `scripts/` for shell entries that the CLI invoked for tmux bootstrap:

- `scripts/csuite-launch.sh`, `scripts/csuite-bootstrap.sh`, `scripts/csuite-spawn-worker.sh`, `scripts/tmux-*.sh`, etc.

For each script referenced by Go code (via `exec.Command` or similar), either:
- Update to use `docker exec` / `docker compose exec` against the relevant service, or
- Delete if it is no longer referenced by any active code path

Deletions here are acceptable — prompt 21 does a final sweep, but catching what we can now reduces the deletion diff.

---

### Track B — TUI rendering swap

#### 5. `internal/tui/app.go`

The TUI currently renders rows showing tmux pane IDs and worktree filesystem paths for each active agent. Swap to container-centric rendering:

- **Container ID column** — display the short form (first 12 chars) of the container ID
- **Image column** — display the image tag (e.g. `localhost:5000/drem-worker-go:latest`)
- Drop the tmux-pane-ID column entirely
- Drop the worktree-path column if it is not load-bearing for operator decisions; otherwise show the feature name (derived from metadata) instead of the filesystem path

Source: `pkg/orchclient.ListWorkers(ctx)` (or the equivalent spawner endpoint — use whatever prompt 15 already plumbed through the datasource).

#### 6. `internal/tui/datasource.go`

If `datasource.go` already exposes workers via `ListWorkers`, extend it with any fields the new rendering requires. Otherwise bind the new method now.

Keep the datasource interface shape stable — `internal/tui/app.go` should not know whether it is talking to the real HTTP client or a test fake.

#### 7. Remove tmux/worktree imports from `internal/tui/`

After steps 5 and 6, no `internal/tui/*.go` file should import `internal/tmux` or `internal/worktree`:

```bash
grep -rn "internal/tmux\|internal/worktree" internal/tui/ --include="*.go"
# → must be empty
```

#### 8. Tests

- `internal/tui/app_test.go` — golden render for one row of container-based output
- `internal/tui/datasource_test.go` — already landed in prompt 15; extend if you added methods

---

## Verification

Final import audit — this is the gate for prompt 21:

```bash
grep -rn "internal/tmux\|internal/worktree" cmd/ internal/ pkg/ --include="*.go" \
  | grep -v "internal/tmux/\|internal/worktree/\|internal/wtbridge/"
# → must be empty
```

The only remaining consumer of `internal/worktree/` is `internal/wtbridge/`. No remaining consumer of `internal/tmux/` anywhere.

Build + test:

```bash
go build ./...
go test ./...
```

Both green (baseline failures in `direct_tool_agent_compaction_test.go` and `fasttrack_atomicity_test.go` remain out of scope).

Constitution:

```bash
bash scripts/check_constitution.sh
```

Must pass.

Smoke:

```bash
drem --help
drem project register --help
drem tui --orch-url http://127.0.0.1:8080  # against a running stack
```

The TUI should render at least one container-backed row when workers exist.

## Scope Limitation

- **Do not delete `internal/tmux/` or `internal/worktree/` in this prompt.** That is prompt 21. `internal/wtbridge/` keeps `internal/worktree/` alive as the concrete implementation until then.
- **Do not change the orchestrator, agent, spawner, or merger packages.** Earlier prompts own those.
- **Do not change the `WorktreeManager` interface.** It is frozen by prompt 18; if an omission shows up here, flag it back to prompt 18's scope rather than extending mid-flight.
- **No new CLI commands.** If `drem attach` needs to exist and does not, add a minimal version (shell out to `docker attach` after resolving the container ID). Do not design a richer attach UX here.
- **`internal/wtbridge/` is explicitly temporary.** Mark it for deletion in prompt 21. Do not grow its surface beyond the single constructor.

## Commit Hygiene

Suggested sequence:

1. `add internal/wtbridge package as worktree construction shim`
2. `cmd/drem: wire orchestrator via wtbridge, remove tmux/worktree imports`
3. `cmd/drem: migrate attach-style subcommands to container semantics`
4. `scripts: remove or migrate tmux bootstrap scripts`
5. `tui: swap rendering from tmux pane IDs to container IDs`
6. `tui: remove tmux/worktree imports`

Each commit should leave `go build ./...` green.

## Conventions

- Module path: `github.com/godinj/drem-orchestrator`
- New package: `wtbridge` (intentionally tiny, intentionally temporary)
- Testing: `testify/require`; TUI golden-file or string-equals rendering checks
- File-length + function-count ceilings per `ARCHITECTURE.md`
- Build verification: `go build ./... && go test ./...`
- Constitution check: `bash scripts/check_constitution.sh`
