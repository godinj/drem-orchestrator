# Containerization Initiative — Remaining Work

Status snapshot written 2026-04-19 after the Tier 1–3 swarm landed on `master`.

## Summary

16 of the 17 planned prompts (see `docs/containerization/prompts/`) have landed. The 17th — **delete `internal/tmux/` and `internal/worktree/`** — is blocked behind unfinished consumer migration that Tier 3's orchestrator/agent integration chose to defer.

All new infrastructure compiles and tests cleanly (`go build ./...` is green; Tier 1–3 package tests pass). Pre-existing test-file failures in `internal/agent/direct_tool_agent_compaction_test.go` and `internal/orchestrator/fasttrack_atomicity_test.go` are unrelated to the containerization work and were untouched.

## Landed work (Tiers 1–3)

| # | Prompt | Package(s) / artifact | Commit |
|---|---|---|---|
| 01 | container-runtime | `internal/container/` (Runtime, FakeRuntime, DockerRuntime) | `012e56e` |
| 02 | extract-package | `internal/extract/` (ParseLine + typed events) | `0e8e926` |
| 03 | gitref-package | `internal/gitref/` (BranchRef + Registry) | `be29840` |
| 04 | watchdog-binary | `cmd/drem-watchdog`, `internal/watchdog/` | `d737a2a` |
| 05 | project-registry-cli | `internal/projects/`, `drem project` CLI | `c253711` |
| 06 | global-infra-compose | `deploy/compose/global.yml`, `deploy/docker/{sglang,gq}.Dockerfile` | (folded into `be29840`) |
| 07 | spawner-service | `internal/spawner/`, `cmd/drem-spawner`, `deploy/docker/spawner.Dockerfile` | `dcdcc03` |
| 08 | orch-http-api | `internal/orchhttp/`, `pkg/orchclient/`, `pkg/orchdto/` | `ce506bf` |
| 09 | merger-package | `internal/merger/`, `cmd/drem-merger`, `deploy/docker/merger.Dockerfile` | `f90bd13` |
| 10 | worker-images | `deploy/docker/worker-{base,go,cpp}.Dockerfile`, build+smoke scripts | `39d4b4a` |
| 11 | agentmon-docker-input | `internal/agentmon/docker_*.go`, `cmd/drem-agentmon`, `deploy/docker/agentmon.Dockerfile` | `e12d685` |
| 12 | orchestrator-integration | `internal/orchestrator/{worker_spawn,docker_events,merge_dispatch,reconcile_containers}.go` (additive) | `d78e51a` |
| 13 | agent-spawn-routing | `internal/agent/{image_resolver,spawn,teardown}.go`, `drem.toml` schema | `604e073` |
| 14 | kyle-binary | `internal/kyle/`, `cmd/drem-kyle`, `cmd/drem-docker-query-proxy`, global compose additions | `4fb5e00` |
| 15 | tui-http-migration | `internal/tui/{datasource,dto_adapter}.go`, `drem tui --orch-url` | `db4bcd9` |
| 16 | csuite-images-and-compose | `deploy/docker/{csuite-*,orch,orch-dev}.Dockerfile`, per-project compose template | `765c5c0` |
| 17 | delete-tmux-worktree | **NOT LANDED** — blocked (see below) | — |

Doc-comment cleanup in `internal/gitref/model.go` and `internal/watchdog/git.go` landed as `3e66a2f` during the blocker investigation.

## The Tier 4 blocker

Prompt 17 requires this invariant before deletion:

```bash
grep -rn "internal/tmux\|internal/worktree" cmd/ internal/ pkg/ --include="*.go" \
  | grep -v "internal/tmux/\|internal/worktree/"
# → must be EMPTY
```

Current state: **54 files, ~61 import lines, ~300 call sites** across the repo still reach into `internal/tmux/` or `internal/worktree/`.

Why: the Tier 3 orchestrator and agent integration was deliberately **additive**. Agent-12 added new `spawnCoder`/`spawnReviewer`/`spawnFixer`/`spawnSupervisor`, `dispatchMerge`, and `reconcileOnStartup` methods but gated them on `o.Spawner != nil` and left every existing worktree/tmux call site intact so the build stayed green. Agent-13 did the same in `internal/agent/` — added `Manager.Spawn`/`Teardown` beside the old `runner.go` rather than replacing it.

The result: two execution paths coexist, the host path is still load-bearing, and `internal/tmux/` + `internal/worktree/` can't be deleted without migrating the host path.

### What's actually coupled

Orchestrator struct field `Orchestrator.worktree *worktree.Manager` is read by roughly every phase:

- `o.worktree.FeatureWorktreePath(fn)` — 17+ sites, resolves feature working dirs for in-process merge/test operations
- `o.worktree.BareRepoPath`, `o.worktree.DefaultBranch` — 30+ sites, path composition and git-operation targets
- `worktree.RunGit(args, dir)` — 10+ sites, direct git exec bound to a specific working copy
- `o.worktree.RemoveAgentWorktree(...)` / `RemoveFeature(...)` — cleanup paths
- `o.worktree.ListAgentWorktrees(...)` — reconciliation scans
- `CommitUnstagedChanges`, `IsClean`, `BranchHasNewCommits`, `CommitInfo`, `MergeResult`, `RebaseBranch`, `RebaseResult`, `SyncResult`, `UntrackEphemeralFiles`, `GenerateRepoMapForMain`, `CreateAgentWorktree`, `GetChangedFiles`, `CreateFeature` — scattered across constraint evaluation, depth diagnosis, changed-file diffs, repo-map generation, empty-commit detection, ancestor checks

`internal/agent/runner.go` is tightly coupled to tmux session allocation (`tmux.Manager`, `tmux.NewManager`, `tmux.EnsureSession`, `tmux.Attach`, `tmux.WithSocket`, `tmux.WithConfigFile`, `tmux.ErrDashboardRespawned`). The new `agent.Manager.Spawn`/`Teardown` does not yet cover the full surface runner.go uses.

`internal/merge/` runs an entire in-process merge→test→push pipeline against host worktrees; its ~75 worktree call sites would need full replacement by the new `internal/merger/` service (which runs in a container instead of in-process).

`cmd/drem/main.go` and `cmd/drem/cli_cmd.go` wire `worktree.NewManager()` and `tmux.NewManager()` as the foundation the whole CLI is built on.

### Shortcuts that won't work

- **Build-tag-out the tests.** Roughly 30 test files tag out cleanly, but the 15 production files in `internal/orchestrator/`, `internal/agent/runner.go`, `internal/merge/*.go`, `cmd/drem/main.go`, `cmd/drem/cli_cmd.go`, and `internal/tui/app.go` must still compile.
- **Copy-rename shim (`internal/worktreehost/`).** Satisfies the grep literally but silently defeats the containerization goal — the same ~4000 lines of host-mode logic would still live in the repo under a different name.
- **Narrow `WorktreeManager` interface in orchestrator.** Tractable for orchestrator production/test files, but `cmd/drem/main.go` still needs a concrete constructor, and `internal/merge/*.go` calls package-level `worktree.RunGit` that can't be interfaced away.

## Follow-up sequencing

Each of these is a reasonably-scoped agent session. They must land in order because each step's cleanup assumes the previous step's interface.

### Step 1 — extract leaf git helpers into `internal/gitexec/`

Lift the handful of package-level `worktree.*` functions used like utilities into a standalone leaf package:

- `worktree.RunGit(args, dir)`
- `worktree.GetChangedFiles(...)`
- `worktree.IsClean(dir)`
- `worktree.CommitInfo(dir, sha)`
- `worktree.BranchHasNewCommits(...)`
- `worktree.CommitUnstagedChanges(...)` (if stateless enough)

Update the 3–5 orchestrator files that call these as utilities to import `internal/gitexec` instead. No breaking changes — just relocation.

**Prompt scope:** `internal/gitexec/` package + grep-based migration of utility call sites. Should land in under 400 lines of diff.

### Step 2 — `WorktreeManager` interface in `internal/orchestrator/`

Define `type WorktreeManager interface { ... }` covering the ~9 methods the Orchestrator struct calls on `*worktree.Manager`:

```go
type WorktreeManager interface {
    BareRepoPath() string
    DefaultBranch() string
    FeatureWorktreePath(feature string) string
    MainWorktreePath() string
    CreateFeature(ctx context.Context, feature string) error
    RemoveFeature(ctx context.Context, feature string) error
    CreateAgentWorktree(ctx context.Context, agentID string) (string, error)
    RemoveAgentWorktree(ctx context.Context, agentID string) error
    ListAgentWorktrees(ctx context.Context) ([]string, error)
}
```

Change `Orchestrator.worktree` field type from `*worktree.Manager` to `WorktreeManager`. Wire the concrete `*worktree.Manager` in `cmd/drem/main.go` (it still imports the package; that's fine for now).

Result: every `internal/orchestrator/*.go` production file stops needing to import `internal/worktree`. Test files can use fakes.

**Prompt scope:** interface definition, `Orchestrator` field retype, fake implementation in `internal/orchestrator/` for tests, migrate ~15 production orchestrator files' imports.

### Step 3 — migrate `internal/agent/runner.go` off tmux

Rewrite `runner.go` to use `agent.Manager.Spawn`/`Teardown` (already landed in `internal/agent/spawn.go` and `teardown.go`). Cover the full tmux surface runner.go currently uses — this may require extending `agent.Manager` with heartbeat monitoring, attach semantics, and respawn-on-dashboard-exit (see `tmux.ErrDashboardRespawned`).

**Prompt scope:** `internal/agent/runner.go` rewrite + any API extensions to `agent.Manager`. Remove the `internal/tmux` import.

### Step 4 — replace `internal/merge/` with `internal/merger/`

`internal/orchestrator/merge_execution.go` currently calls `internal/merge/` in-process. The replacement — `agent-12`'s `merge_dispatch.go` — spawns a merger container via `internal/merger/`. Flip `merge_execution.go` to call `o.dispatchMerge(ctx, task)` unconditionally. Once that's the only caller, delete `internal/merge/` entirely (this explicitly goes beyond prompt 17's scope, but prompt 09's migration section already anticipated it: *"after prompt 12 lands, `internal/merge/` will have no callers left and can be removed"*).

**Prompt scope:** `merge_execution.go` rewrite + delete `internal/merge/` + remove its tests. Biggest chunk; probably warrants its own prompt.

### Step 5 — `cmd/drem/` stops importing worktree/tmux

Update `cmd/drem/main.go` and `cmd/drem/cli_cmd.go` to build the orchestrator without directly constructing `worktree.NewManager()` or `tmux.NewManager()`. Two reasonable approaches:

1. **Ship-and-hide:** move the `NewManager()` construction into a new `internal/wtbridge/` that re-exports just the constructor, then import that. Cuts the import at the cmd/ level while leaving orchestrator clean — a small, ugly-but-contained shim.
2. **Pure containerized CLI:** skip `worktree.Manager` construction entirely; instead, build an `Orchestrator` that only runs the containerized path (requires Step 6 first to avoid breaking existing tests).

Recommended: approach 1 for Step 5, then remove the bridge in Step 7.

**Prompt scope:** `cmd/drem/main.go` + `cmd/drem/cli_cmd.go` rewrite, possible `internal/wtbridge/` package.

### Step 6 — migrate or tag-out ~30 orchestrator test files

After Step 2's interface lands, the orchestrator test files can stop importing `internal/worktree` by using the `WorktreeManager` interface + a fake. Likely last because the interface shape depends on Step 2's decisions.

**Prompt scope:** bulk rewrite of `internal/orchestrator/*_test.go`, same interface everywhere. Could be parallelized across 3 agents operating on disjoint test-file slices.

### Step 7 — rewrite `internal/tui/app.go` session rendering

TUI currently renders tmux pane IDs and worktree paths. Swap to container IDs from `spawner.ListWorkers` (via `pkg/orchclient`'s HTTP API). Or drop the column entirely if it's not load-bearing.

**Prompt scope:** `internal/tui/app.go` rendering-only change. Small.

### Step 8 — run the original prompt 17

Once the audit grep is empty, run the original `17-delete-tmux-worktree.md` prompt verbatim. It will delete the two packages, scrub `drem.toml`'s `[tmux]` section, clean up `scripts/csuite-*.sh`, and update `ARCHITECTURE.md`'s package map.

## Known caveats carried forward from Tier 3

A few design decisions that follow-up work should know about:

- **agent-14 (Kyle)** binds host port `127.0.0.1:8095:8090` for Kyle instead of `:8090:8090` because `gq` already occupies `:8090`. Inside `drem-net`, callers still resolve by service name (`kyle:8090`). Document this discrepancy in `deploy/compose/README.md` if not already noted.
- **agent-11 (agentmon)** mounts the Docker socket read-only directly (`/var/run/docker.sock:...:ro`) rather than going through the docker-query-proxy. The PRD's "socket only in spawner" principle is deviated from in this first cut; the Dockerfile header comment flags the deviation. Route agentmon through the docker-query-proxy as a Phase-1 hardening follow-up.
- **agent-13 (agent package)** uses package-local `WorkerSpawnParams`/`WorkerSpawnResult` mirror types instead of importing `internal/spawner` directly, to stay under the internal-import ceiling. A tiny adapter (one per method) lives in the caller. Document the adapter pattern in `internal/agent/README.md` if it isn't already.
- **agent-16 (C-Suite)** left `cmd/drem/project.go`'s `templateDataFor` using `uuid.NewString()` for the shared token and not setting `OrchHostPort`/`DevMode`/`OrchImage`. The template renders sensibly because `applyDefaults` fills them in, but a follow-up should wire `projects.NewSharedToken()` + `Registry.AllocateOrchHostPort()` + a `--dev` flag into `project register`.
- **Pre-existing test-file failures** in `internal/agent/direct_tool_agent_compaction_test.go` (undefined `NewDirectToolAgent`) and `internal/orchestrator/fasttrack_atomicity_test.go` (undefined `testutil.NewOrchestrator`, `model.ProjectID`, `model.Event`) are baseline failures unrelated to the containerization swarm. They should be fixed in their own prompts — don't conflate them with migration work.

## Suggested ordering

The migration steps fan out into roughly 3–5 agent sessions. A reasonable sequence:

1. **One session for Steps 1+2** (gitexec extraction + WorktreeManager interface). These are mechanically related and small.
2. **One session for Step 3** (runner.go off tmux).
3. **One session for Step 4** (merge → merger + delete internal/merge). Largest single step.
4. **One session for Steps 5+7** (cmd/drem/ + TUI rendering). Small, independent.
5. **One session for Step 6** (orchestrator test-file migration). Could parallelize across 3 agents on disjoint slices if time-sensitive.
6. **One session for Step 8** (run prompt 17 verbatim, deletion).

Alternatively, Steps 3 + 4 can run in parallel if the agents agree on the boundary (runner.go doesn't touch internal/merge, merge_execution doesn't touch agent runner).

## Verification gates for each follow-up step

Every follow-up agent should, before declaring done:

1. `go build ./...` — must be clean
2. `go test ./...` — must be at-least as green as baseline (no new regressions beyond the pre-existing failures noted above)
3. `grep -rn "internal/tmux\|internal/worktree" cmd/ internal/ pkg/ --include="*.go" | grep -v "internal/tmux/\|internal/worktree/" | wc -l` — monotonically decreasing across the migration
4. `bash scripts/check_constitution.sh` — no new violations relative to baseline

Only Step 8 needs the grep to hit zero. Earlier steps should report the current count and show progress.
