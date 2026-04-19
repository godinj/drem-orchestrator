# Agent: Agent Runner Off Tmux + Merge → Merger Migration

You are working on the `master` branch of Drem Orchestrator, a Go-based multi-agent task orchestrator. Your task is the second of three follow-up prompts: rewrite `internal/agent/runner.go` to use the container-based `agent.Manager.Spawn`/`Teardown` path that landed in prompt 13 (dropping the `internal/tmux/` import), and flip `internal/orchestrator/merge_execution.go` to the containerized `dispatchMerge` path unconditionally so `internal/merge/` can be deleted.

This prompt bundles remaining-work.md Steps 3 and 4. The two tracks are independent — runner.go does not touch `internal/merge/`, and `merge_execution.go` does not touch the agent runner. They can be done serially inside one agent session, or parallelized via subagents if preferred.

## Context

Read these specs before starting:

- `docs/containerization/remaining-work.md` (sections: Step 3, Step 4; "What's actually coupled" for the tmux surface inventory)
- `internal/agent/runner.go` (current tmux-coupled agent launcher)
- `internal/agent/spawn.go` and `internal/agent/teardown.go` (landed in prompt 13 — the target surface)
- `internal/tmux/` (the surface to be unimported: `Manager`, `NewManager`, `EnsureSession`, `Attach`, `WithSocket`, `WithConfigFile`, `ErrDashboardRespawned`)
- `internal/merge/` (the in-process merge→test→push pipeline to be deleted)
- `internal/merger/` (the containerized replacement, landed in prompt 09)
- `internal/orchestrator/merge_execution.go` (current in-process entry point)
- `internal/orchestrator/merge_dispatch.go` (landed in prompt 12 — spawns a merger container via `internal/merger/`)
- `docs/prd-containerization.md`

## Dependencies

- Prompt 18 (WorktreeManager interface): `merge_execution.go` rewrite should consume `WorktreeManager`, not `*worktree.Manager`
- Prompts 09, 12, 13 already landed
- Baseline grep — record before starting:

```bash
grep -rn "internal/tmux\|internal/worktree" cmd/ internal/ pkg/ --include="*.go" \
  | grep -v "internal/tmux/\|internal/worktree/" | wc -l
```

## Deliverables

This prompt has two independent tracks. Do them in any order.

---

### Track A — Agent runner off tmux

#### 1. Audit tmux surface consumed by `internal/agent/runner.go`

Grep `runner.go` and any sibling files in `internal/agent/` for all `tmux.*` references. Expected surface:

- `tmux.Manager`, `tmux.NewManager`
- `tmux.EnsureSession(sessionName, window, cmd, args...)`
- `tmux.Attach(session, window)`
- `tmux.WithSocket(path)`, `tmux.WithConfigFile(path)` options
- `tmux.ErrDashboardRespawned` sentinel

Map each one to a container-equivalent in `agent.Manager`:

| tmux surface | container equivalent | notes |
|---|---|---|
| `EnsureSession` + exec | `agent.Manager.Spawn` | already landed (prompt 13) |
| `Attach` | orchestrator HTTP endpoint + `docker exec` from CLI | TUI attach, user-facing |
| `ErrDashboardRespawned` | new sentinel, e.g. `agent.ErrDashboardRespawned` | respawn-on-dashboard-exit semantics |
| heartbeat observation | DB column updated by agentmon | unchanged, confirm |

#### 2. Extend `agent.Manager` as needed

New methods if not already present:

```go
func (m *Manager) Attach(ctx context.Context, agentID string) error
func (m *Manager) RespawnIfDashboardExited(ctx context.Context, h *Handle) (*Handle, error)
```

Add a sentinel:

```go
var ErrDashboardRespawned = errors.New("dashboard respawned; re-attach to new agent session")
```

Put new surface in its own file (e.g. `internal/agent/attach.go`, `internal/agent/respawn.go`) to keep `spawn.go` and `teardown.go` focused.

#### 3. Rewrite `internal/agent/runner.go`

Replace the tmux-driven lifecycle with `agent.Manager.Spawn` / `.Attach` / `.Teardown`. Keep the public API of `runner` as close to the current shape as possible so orchestrator call sites don't ripple.

After this step, grep must show zero `internal/tmux` imports in `internal/agent/`:

```bash
grep -rn "internal/tmux" internal/agent/ --include="*.go"
# → must be empty
```

#### 4. Tests

- `internal/agent/runner_test.go` — migrate to the fake `agent.Manager.Spawner` that prompt 13 established. Preserve existing scenarios.
- Add new cases for: attach-success, attach-not-found, respawn-on-dashboard-exit returns `ErrDashboardRespawned`, heartbeat staleness triggers `Spawner.DestroyWorker` + a fresh `Manager.Spawn`.

---

### Track B — Merge → Merger

#### 5. Rewrite `internal/orchestrator/merge_execution.go`

Current shape (from remaining-work.md's audit): `merge_execution.go` calls `internal/merge/` in-process, gated by `o.Spawner != nil` so the container path coexists additively.

Target shape: call `o.dispatchMerge(ctx, task)` unconditionally. Remove the `o.Spawner != nil` gate and the in-process fallback entirely.

Before deleting the fallback, verify `o.dispatchMerge` covers the full shape `merge_execution.go` expects (blocking merge completion, result propagation, error wrapping). If any gap, extend `merge_dispatch.go` rather than keeping the fallback.

#### 6. Delete `internal/merge/`

Once no caller remains:

```bash
grep -rn "\"github.com/godinj/drem-orchestrator/internal/merge\"" cmd/ internal/ pkg/ --include="*.go"
# → must be empty (note: exclude internal/merger/)
```

Then:

```bash
rm -rf internal/merge/
```

Remove any scripts or Makefile targets that built or tested `internal/merge/` in isolation.

#### 7. Tests

- Update `internal/orchestrator/merge_execution_test.go` to use a fake `MergeDispatcher` (whatever shape `merge_dispatch.go` uses) instead of the old in-process merge fake.
- End-to-end sanity: orchestrator receives a merge task → dispatches via `internal/merger/` client → fake merger container records the spawn. Prompt 12 likely already has a version of this test; extend if needed.

## Verification

After both tracks land:

```bash
go build ./...
go test ./...
```

Both must be green (baseline failures in `direct_tool_agent_compaction_test.go` and `fasttrack_atomicity_test.go` remain out of scope).

Import audits:

```bash
grep -rn "internal/tmux" internal/agent/ --include="*.go" | wc -l
# → 0

grep -rn "\"github.com/godinj/drem-orchestrator/internal/merge\"" cmd/ internal/ pkg/ --include="*.go" | wc -l
# → 0

grep -rn "internal/tmux\|internal/worktree" cmd/ internal/ pkg/ --include="*.go" \
  | grep -v "internal/tmux/\|internal/worktree/" | wc -l
# → should decrease further from prompt 18's output
```

Constitution:

```bash
bash scripts/check_constitution.sh
```

Must pass.

## Scope Limitation

- **Do not touch `cmd/drem/` or `internal/tui/`.** Those are prompt 20.
- **Do not delete `internal/tmux/` or `internal/worktree/`.** Those are prompt 21. This prompt only removes imports.
- **No new agent lifecycle states.** Reuse the existing status enum.
- **No new orchestrator fields on the worktree path.** Prompt 18 owns the `Orchestrator.worktree` shape.
- **`internal/merger/` is frozen.** Do not redesign the merger service here. If `dispatchMerge` has a gap, extend the dispatcher (`merge_dispatch.go`), not the merger.
- **Prompt-delivery mechanism unchanged.** Prompt 13 documented the host-path-bind-mount approach. Inherit it; do not redesign.

## Commit Hygiene

Suggested sequence — each commit should leave the build green on its own:

1. `agent: extend Manager with Attach and respawn semantics`
2. `agent: rewrite runner off tmux`
3. `agent: remove tmux import from internal/agent`
4. `orchestrator: call dispatchMerge unconditionally in merge_execution`
5. `remove internal/merge package`

Parallelizing Track A and Track B in separate branches before merge is fine, but within a single merge the sequence above preserves bisectability.

## Conventions

- Module path: `github.com/godinj/drem-orchestrator`
- Package names: `agent`, `orchestrator` (extending existing)
- Testing: `testify/require`, fake spawner (from prompt 13), fake merge dispatcher
- File-length + function-count ceilings per `ARCHITECTURE.md`
- Build verification: `go build ./... && go test ./...`
- Constitution check: `bash scripts/check_constitution.sh`
