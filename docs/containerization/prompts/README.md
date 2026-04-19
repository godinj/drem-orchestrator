# Containerization Prompts

Agent prompts generated from `docs/prd-containerization.md`. Each prompt is a self-contained work unit that one Claude Code agent can execute independently. Prompts are grouped into tiers by dependency order.

## Prompt Index

| # | Name | Tier | Depends on | Files created | Files migrated |
|---|---|---|---|---|---|
| 01 | container-runtime | 1 | — | `internal/container/{runtime,docker,fake}.go` + tests | — |
| 02 | extract-package | 1 | — | `internal/extract/{event,parse}.go` + tests | `internal/agentmon/parse.go` |
| 03 | gitref-package | 1 | — | `internal/gitref/{model,registry,git}.go` + tests | — |
| 04 | watchdog-binary | 1 | — | `cmd/drem-watchdog/main.go`, `internal/watchdog/{loop,git}.go` + tests | — |
| 05 | project-registry-cli | 1 | — | `internal/projects/{registry,template}.go` + template skeleton, `cmd/drem/project.go` + tests | — |
| 06 | global-infra-compose | 1 | — | `deploy/compose/global.yml`, `deploy/docker/{sglang,gq}.Dockerfile`, `deploy/compose/README.md` | `docker-compose.yml` |
| 07 | spawner-service | 2 | 01 | `cmd/drem-spawner/main.go`, `internal/spawner/{service,types,methods,client,images}.go` + tests, `deploy/docker/spawner.Dockerfile` | `deploy/compose/global.yml` |
| 08 | orch-http-api | 2 | — | `internal/orchhttp/*.go`, `pkg/orchclient/client.go`, `pkg/orchdto/*.go` + tests | orchestrator startup wiring |
| 09 | merger-package | 2 | 03 | `internal/merger/{merger,git,interfaces}.go` + tests, `cmd/drem-merger/main.go`, `deploy/docker/merger.Dockerfile` | — |
| 10 | worker-images | 2 | 04 | `deploy/docker/{worker-base,worker-go,worker-cpp}.Dockerfile`, `deploy/docker/{build-workers,test-worker-image}.sh`, `deploy/docker/context/worker-entrypoint.sh` | — |
| 11 | agentmon-docker-input | 3 | 01, 02, 08 | `internal/agentmon/{docker_source,docker_tail,client,convert}.go` + tests, `deploy/docker/agentmon.Dockerfile`, optional `cmd/drem-agentmon/main.go` | `internal/agentmon/agentmon.go` |
| 12 | orchestrator-integration | 3 | 01, 03, 07 | `internal/orchestrator/{worker_spawn,docker_events,merge_dispatch,reconcile_containers}.go` + tests | `internal/orchestrator/{orchestrator,task_processing,session_spawning,reconcile,merge_execution}.go` |
| 13 | agent-spawn-routing | 3 | 07 | `internal/agent/{image_resolver,spawn,teardown}.go` + tests | agent lifecycle call sites, `internal/model/` migration, `drem.toml` schema |
| 14 | kyle-binary | 3 | 05, 08 | `cmd/drem-kyle/main.go`, `internal/kyle/{service,cache,poll,docker_query,summary,mcp}.go` + tests, `deploy/docker/{kyle,docker-query-proxy}.Dockerfile` | `deploy/compose/global.yml` |
| 15 | tui-http-migration | 3 | 08 | `internal/tui/datasource_test.go` | `internal/tui/*.go` data source swap, model tests |
| 16 | csuite-images-and-compose | 3 | 05, 06, 07, 09, 10 | `deploy/docker/{csuite-base,csuite-{mike,alex,ross,seth},csuite-watcher,orch,orch-dev}.Dockerfile`, `deploy/docker/build-csuite.sh` | `internal/projects/templates/project-compose.yml.tmpl`, `internal/projects/template.go` |
| 17 | delete-tmux-worktree | 4 | 12, 13, 15 | — | delete `internal/tmux/`, delete `internal/worktree/`, drop tmux from `drem.toml`, update `ARCHITECTURE.md` |

## Execution Order

### Tier 1 — Foundations (all parallel)

```bash
claude --agent docs/containerization/prompts/01-container-runtime.md
claude --agent docs/containerization/prompts/02-extract-package.md
claude --agent docs/containerization/prompts/03-gitref-package.md
claude --agent docs/containerization/prompts/04-watchdog-binary.md
claude --agent docs/containerization/prompts/05-project-registry-cli.md
claude --agent docs/containerization/prompts/06-global-infra-compose.md
```

### Tier 2 — Services (parallel, after Tier 1 merges)

```bash
claude --agent docs/containerization/prompts/07-spawner-service.md          # needs 01
claude --agent docs/containerization/prompts/08-orch-http-api.md            # no deps
claude --agent docs/containerization/prompts/09-merger-package.md           # needs 03
claude --agent docs/containerization/prompts/10-worker-images.md            # needs 04
```

### Tier 3 — Integrations (parallel, after Tier 2 merges)

```bash
claude --agent docs/containerization/prompts/11-agentmon-docker-input.md    # needs 01, 02, 08
claude --agent docs/containerization/prompts/12-orchestrator-integration.md # needs 01, 03, 07
claude --agent docs/containerization/prompts/13-agent-spawn-routing.md      # needs 07
claude --agent docs/containerization/prompts/14-kyle-binary.md              # needs 05, 08
claude --agent docs/containerization/prompts/15-tui-http-migration.md       # needs 08
claude --agent docs/containerization/prompts/16-csuite-images-and-compose.md # needs 05, 06, 07, 09, 10
```

### Tier 4 — Cleanup (after 12, 13, 15 merge)

```bash
claude --agent docs/containerization/prompts/17-delete-tmux-worktree.md
```

## Dependency Graph

```
Tier 1:  01   02   03   04   05   06
          \    \    \    \    \    \
Tier 2:   07←01  08   09←03  10←04
             \    \    \       \
Tier 3:  11←01,02,08   12←01,03,07   13←07   14←05,08   15←08   16←05,06,07,09,10
                         \               \              \
Tier 4:                   \_______________17←12,13,15___/
```

## Cross-Cutting Notes

- **Module path:** `github.com/godinj/drem-orchestrator` (Go 1.24.4)
- **Image registry:** `localhost:5000` — bring up via prompt 06 before building any other image
- **Network:** `drem-net` external Docker network — created once via `deploy/compose/network-setup.sh`
- **Secrets:** per-project shared token generated on `drem project register`, stored in `~/.drem/projects.toml`, passed through env vars to orchestrator and agentmon
- **Test conventions:** `internal/testutil/` is authoritative for DB factories (`NewTestDB`, `NewTestDBWithModels`) and bare-repo helpers (`SetupBareRepo`, `CommitFile`). Do not duplicate.
- **Constitution:** `bash scripts/check_constitution.sh` must pass after every prompt. File-length and function-count ceilings from `ARCHITECTURE.md` apply to every new file.
- **State machine:** preserved intact. Only spawn/observe/replace mechanics change.

## Phasing Alignment with PRD

PRD section "Phased rollout" maps to prompt tiers as follows:

| PRD phase | Description | Prompts |
|---|---|---|
| 1 | Global compose + SGLang/GQ containerization | 06 |
| 2 | Spawner + runtime + Go worker image, route coder tasks | 01, 04, 07, 10, 12, 13 |
| 3 | Agentmon docker input + orch HTTP + TUI migration | 02, 08, 11, 15 |
| 4 | Kyle + per-project compose + project registration | 05, 14, 16 |
| 5 | Second project (`drem-canvas`) — operational milestone, no new code | — |
| 6 | Warm merger pool + watchdog-enabled crash recovery | 09 (+ 04/12 already delivered) |
| 7 | Retire tmux and worktree | 17 |

## Validation Checklist

Before declaring the initiative complete:

- [ ] `docker compose -f deploy/compose/global.yml ps` shows registry, sglang, gq, spawner, kyle, docker-query-proxy all `Up`
- [ ] `drem project register --name drem-orchestrator --bare /path/to/drem-orchestrator.git --language go --orch-url http://127.0.0.1:8080` produces a working compose file and `docker compose -f ~/.drem/projects/drem-orchestrator/compose.yml up -d` brings the stack up
- [ ] `drem project register --name drem-canvas --bare /path/to/drem-canvas.git --language cpp --orch-url http://127.0.0.1:8081` repeats for the second project
- [ ] `curl http://127.0.0.1:8080/projects` returns the orchestrator project DTO
- [ ] `curl http://127.0.0.1:8090/world/summary` returns a cross-project digest
- [ ] `grep -rn "internal/tmux\|internal/worktree" cmd/ internal/ pkg/ --include="*.go"` is empty
- [ ] `go build ./... && go test ./...` passes
- [ ] `bash scripts/check_constitution.sh` passes
