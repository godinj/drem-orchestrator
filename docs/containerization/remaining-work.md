# Containerization Initiative — Completion Log

Status: **DONE.** All 20 prompts landed on `master`. `internal/tmux/` and `internal/worktree/` (plus the transitional `wtbridge`/`tmuxbridge` shims) are removed. The host path is gone.

## Tier 1–3 — initial infrastructure (16 prompts)

| # | Prompt | Package(s) / artifact | Commit |
|---|---|---|---|
| 01 | container-runtime | `internal/container/` | `012e56e` |
| 02 | extract-package | `internal/extract/` | `0e8e926` |
| 03 | gitref-package | `internal/gitref/` | `be29840` |
| 04 | watchdog-binary | `cmd/drem-watchdog`, `internal/watchdog/` | `d737a2a` |
| 05 | project-registry-cli | `internal/projects/`, `drem project` CLI | `c253711` |
| 06 | global-infra-compose | `deploy/compose/global.yml`, sglang/gq Dockerfiles | (folded into `be29840`) |
| 07 | spawner-service | `internal/spawner/`, `cmd/drem-spawner` | `dcdcc03` |
| 08 | orch-http-api | `internal/orchhttp/`, `pkg/orchclient/`, `pkg/orchdto/` | `ce506bf` |
| 09 | merger-package | `internal/merger/`, `cmd/drem-merger` | `f90bd13` |
| 10 | worker-images | worker-{base,go,cpp} Dockerfiles | `39d4b4a` |
| 11 | agentmon-docker-input | `internal/agentmon/docker_*.go` | `e12d685` |
| 12 | orchestrator-integration | spawner/event/merge dispatch in `internal/orchestrator/` | `d78e51a` |
| 13 | agent-spawn-routing | `internal/agent/{image_resolver,spawn,teardown}.go` | `604e073` |
| 14 | kyle-binary | `internal/kyle/`, `cmd/drem-kyle`, docker-query-proxy | `4fb5e00` |
| 15 | tui-http-migration | `internal/tui/{datasource,dto_adapter}.go` | `db4bcd9` |
| 16 | csuite-images-and-compose | C-Suite Dockerfiles, per-project compose template | `765c5c0` |

## Tier 4–5 — host-path migration + deletion (4 prompts)

Original prompt 17 was split (`8f5b8ce`) into staged migration prompts because Tier 3 was deliberately additive.

| # | Prompt | Summary | Commits |
|---|---|---|---|
| 18 | gitexec-and-worktree-interface | Leaf `internal/gitexec/`; `WorktreeManager` interface + fake; orchestrator test batches 1–4 | `7fed1f6`, `5e4a398`, `435f36f`, `98a6ed1`, `d3947e9`, `dbb8d3d`, `c009f28`, `c6a3fe4`, `1ab5c48` |
| 19 | runner-and-merger-migration | `runner.go` off tmux via `TmuxSessionManager`; `dispatchMerge` unconditional; `internal/merge/` removed | `daaa872`, `2389766`, `454dc68`, `c237bb7`, `9c4ef75` |
| 20 | cmd-and-tui-decouple | `cmd/drem` via `wtbridge`/`tmuxbridge` shims; TUI renders container+image instead of tmux | `f8160f7`, `2ca3299`, `d84c9d5`, `21abaef` |
| 21 | delete-tmux-worktree | `internal/worktreehost` host adapter; consumers migrated; `tmux`/`worktree`/`wtbridge`/`tmuxbridge` deleted | `588d980`, `6dd529f`, `bb9176d`, `5575578` |

## Post-cutover polish

- `efe6d95` — shave `plan_validation`/`orchestrator.go` to meet file-length ceilings
- `f418522` — refresh stale orchestrator test fixtures exposed by compile-fix
- `9314307` — containerization plan + inventories + task disposition drafts
- `b62345f` — compose schema + Go toolchain + useradd path fixes
- `f8b5ad9` — session install log as install-script source
- `f500f26` — parameterize sglang compose command via `.env`
- `15c50b3` — install runbook (`docs/containerization/install.md`) + script

## Known deviations carried forward

Architectural compromises from the initial cut that follow-up work should be aware of:

- **Kyle host port.** `gq` already owns `127.0.0.1:8090`, so Kyle's internal `:8090` is published as `:8095` on the host. Inside `drem-net` callers still resolve by service name (`kyle:8090`). See `deploy/compose/global.yml:200-211`.
- **Agentmon Docker socket.** Mounted read-only directly into agentmon (`/var/run/docker.sock:...:ro`) rather than going through `docker-query-proxy`. The PRD's "socket only in spawner" principle is deviated from in the first cut; the Dockerfile header flags it. Routing agentmon through the proxy remains a hardening follow-up.
- **Agent/spawner adapter types.** `internal/agent/` uses package-local `WorkerSpawnParams`/`WorkerSpawnResult` mirrors instead of importing `internal/spawner/` directly, to stay under the orchestrator internal-import ceiling. Tiny per-method adapters live in the caller.
- **C-Suite compose defaults.** `cmd/drem/project.go`'s `templateDataFor` uses `uuid.NewString()` for the shared token and relies on `applyDefaults` for `OrchHostPort`/`DevMode`/`OrchImage`. Follow-up should wire `projects.NewSharedToken()` + `Registry.AllocateOrchHostPort()` + a `--dev` flag into `project register`.
- **Pre-existing failing tests.** `internal/agent/direct_tool_agent_compaction_test.go` (undefined `NewDirectToolAgent`) and `internal/orchestrator/fasttrack_atomicity_test.go` (undefined `testutil.NewOrchestrator`, `model.ProjectID`, `model.Event`) are baseline failures unrelated to containerization. Fix in their own prompts.
- **Kyle registry bind-mount foot-gun.** `deploy/compose/global.yml:208-209` bind-mounts `${HOME}/.drem/projects.toml` with no require-file semantics, so a missing source (or `sudo` resetting `$HOME` to `/root`) silently autocreates the path as a root-owned directory and Kyle crash-loops on "is a directory". Documented as Step 1 / 1b in `install.md`. Durable fix: either Compose `configs:` with a required file, or a `DREM_HOME` explicit env var validated by a small Kyle entrypoint guard that fails fast with a clear message.

## Verification

```bash
grep -rn "internal/tmux\|internal/worktree[^h]\|internal/merge[^r]" cmd/ internal/ pkg/ --include="*.go"
# → zero production imports (only docstring/adapter references remain)

go build ./...    # green
go test ./...     # green modulo the two pre-existing failures above
```
