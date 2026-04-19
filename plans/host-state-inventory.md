# Host-State Inventory — Everything Phase 1–6 Must Move Into Containers

**Scope:** `docs/prd-containerization.md` Phase 1 shared infra through Phase 6 merger/watchdog. This doc enumerates every host-side dependency the current drem-orchestrator relies on, with file:line citations so Alex's phase-slicing and the Dockerfile seeds can be validated against reality.

**Conventions:**

- All paths shown as they appear in default config. `~` = the user account running drem. Tildes expand in-code via `os.UserHomeDir()` lookups.
- Port numbers shown are defaults; all are overridable via `drem.toml` or env.
- Citations use `file:line` relative to repo root.

---

## 1. Daemons and long-running services

### 1.1 SGLang (LLM server)

| Field | Value | Citation |
|---|---|---|
| Listen | `http://localhost:8081` | `drem.toml:6` (`llama_server_endpoint`) |
| Launcher | **Out of repo** — started externally by operator; drem assumes it is already running | — |
| Consumers | GQ upstream (`GQ_UPSTREAM` default), classifier/prep direct, `direct_tool_agent.endpoint` | `internal/gq/config.go:128`, `drem.toml:45` |
| Health check | `GET /v1/models` (used by gq health probe since commit `7afdce6`) | — |
| Model dir | Host-side; path is SGLang-internal and NOT owned by drem. Must be bind-mounted into the SGLang container verbatim so nothing re-downloads. | — |
| GPU | Required (`--gpus all` or compose `deploy.resources.reservations.devices`) | PRD §Compose topology |

### 1.2 GQ (Gemma Queue proxy)

| Field | Value | Citation |
|---|---|---|
| Proxy listen | `127.0.0.1:8090` | `internal/gq/config.go:126` |
| Metrics listen | `127.0.0.1:8091` (`/metrics`) | `internal/gq/config.go:127` |
| Config file | `~/.drem-csuite/gq.toml` (optional; built-in defaults if missing) | `cmd/gq/main.go:34`, `internal/gq/config.go:172-179` |
| Upstream | `http://localhost:8081` (SGLang) — overridable via `GQ_UPSTREAM` | `internal/gq/config.go:124-129` |
| Env overrides | `GQ_BIND_ADDR`, `GQ_METRICS_ADDR`, `GQ_UPSTREAM`, `GQ_MAX_SLOTS`, `GQ_QUEUE_MAX_DEPTH` | `internal/gq/config.go:185-205` |
| Default slots | `max_slots=4`, `queue_max_depth=128`, `upstream_timeout=600s` | `internal/gq/config.go:124-166` |
| Lane timeouts | high=60s, normal=300s, low=900s; aging normal→high 30s, low→normal 90s | `internal/gq/config.go` |
| Binary | `cmd/gq/main.go` → `./gq` (statically linked Go) | — |

### 1.3 csuite-watcher (C-Suite agent spawner)

| Field | Value | Citation |
|---|---|---|
| HTTP listen | `:8080` (default; `[serve].listen_addr`) | `cmd/csuite-watcher/serve.go:78,101`, `drem.toml:64` |
| Bearer token | `drem-local-dev` (default for local dev; override in prod) | `drem.toml:65` |
| C-Suite DB path | `~/.drem-csuite/csuite.db` | `drem.toml:66`, `cmd/csuite-watcher/serve.go:78` |
| Watcher DB path | `~/.drem-csuite/watcher.db` (separate state DB) | `drem.toml:55` |
| Inbox root | `~/.drem-csuite/` (per-agent subdirs: `mike/inbox`, `alex/inbox`, `ross/inbox`, `seth/inbox`, `kyle/inbox`) | `drem.toml:56`, `[watcher].inbox_base_dir` |
| Prompt dir | `docs/csuite-agents/prompts` (relative to bare repo) | `drem.toml`, `[watcher].prompt_dir` |
| Binary | `cmd/csuite-watcher/` | — |

### 1.4 drem-bridge (transcript ingestor)

| Field | Value | Citation |
|---|---|---|
| DB target | `~/.drem-csuite/csuite.db` (from `CSUITE_DB` env or `-db` flag) | `cmd/drem-bridge/main.go:40` |
| Input | Reads claude transcripts; posts structured events to csuite DB | — |
| Binary | `cmd/drem-bridge/` | — |

### 1.5 drem orchestrator (TUI + scheduler)

| Field | Value | Citation |
|---|---|---|
| tmux session | `󱇯 dash <project>` (dots/colons mangled to `-`) | `cmd/drem/main.go:114`, `internal/tmux/tmux.go:49` |
| tmux socket | `drem` (configurable via `tmux_socket` in drem.toml) | `cmd/drem/config.go:140` |
| tmux config | `<bare>/master/.tmux.conf` | `cmd/drem/config.go:141` |
| DB path | `./drem.db` (default) — per-project SQLite with WAL | `cmd/drem/config.go:123`, `internal/db/db.go:25-26` |
| Log path | `./drem.log` (default) | `cmd/drem/config.go:136` |
| HTTP API | *Not yet implemented* — arrives in PRD Phase 3 | — |
| Binary | `cmd/drem/` | — |

### 1.6 csuite-inbox-watch (helper)

| Field | Value | Citation |
|---|---|---|
| Function | Watches inbox dir, sends `send-keys` into tmux session to notify an agent of new mail | `cmd/csuite-inbox-watch/main.go:143` |
| Shells out to | `tmux` | same |

---

## 2. Unix / AF_UNIX sockets

**Currently: none.** No `net.Listen("unix", ...)` call sites exist in the codebase today.

PRD introduces one: the **spawner RPC Unix socket** for `SpawnWorker`/`DestroyWorker`/`ListWorkers`/`InspectWorker` (PRD §145). Phase 2 work.

---

## 3. Filesystem paths

### 3.1 Paths under `<bare>` (bare repo root, e.g. `/home/godinj/git/drem-orchestrator.git`)

| Path | Purpose | Citation |
|---|---|---|
| `<bare>/.git/` (implicit; bare repo metadata) | Git authoritative state | — |
| `<bare>/feature/<name>/integration/` | Feature-level worktree | `internal/worktree/manager.go:76,82` |
| `<bare>/feature/<name>/agent-<uuid>/` | Per-agent nested worktree | `internal/worktree/manager.go:66-67` |
| `<bare>/master/` | The main worktree (also the working dir for drem itself) | `cmd/drem/config.go:141` |
| `<bare>/master/.tmux.conf` | tmux config used when drem EnsureSession spawns the dashboard | `cmd/drem/config.go:141` |
| `<bare>/.drem/bug-reports/*.json` | Bug-report drops picked up by classifier | `cmd/drem/main.go:211` |
| `<bare>/.drem/constraints.toml` | Constraint definitions used by constraint-gate | `internal/prompt/prompt_helpers.go` |
| `<bare>/.drem/critical-rules.md` | Critical-rules context injected into prompts | `internal/prompt/prompt_helpers.go` |
| `<worktree>/.drem-agent-output.log` | Per-agent stdout/stderr tail | `internal/agent/runner.go` |

### 3.2 Paths under `~/.drem-csuite/`

| Path | Purpose | Citation |
|---|---|---|
| `~/.drem-csuite/csuite.db` | C-Suite event-bus SQLite | `drem.toml:66` |
| `~/.drem-csuite/watcher.db` | csuite-watcher state SQLite | `drem.toml:55` |
| `~/.drem-csuite/gq.toml` | GQ config (optional) | `cmd/gq/main.go:34` |
| `~/.drem-csuite/<agent>/inbox/` | Per-agent inbox drops (`mike`, `alex`, `ross`, `seth`, `kyle`) | `[watcher].inbox_base_dir` |
| `~/.drem-csuite/<agent>/inbox/archive/` | Processed inbox archive | convention |

### 3.3 Paths under `~/.drem/` (introduced in PRD — does not exist yet)

| Path | Purpose | Citation |
|---|---|---|
| `~/.drem/projects.toml` | Host-wide project registry | PRD §30-31,100 |
| `~/.drem/projects/<name>/compose.yml` | Per-project generated compose | PRD §30,95 |

### 3.4 CWD assumptions

Drem is invoked from the bare repo's main worktree directory (`<bare>/master/`) by convention. `database_path=./drem.db` and `log_path=./drem.log` both resolve relative to CWD; both effectively live at `<bare>/master/drem.db` and `<bare>/master/drem.log` today.

---

## 4. tmux sessions drem expects

| Session name | Windows | Purpose | Citation |
|---|---|---|---|
| `󱇯 dash <project>` | `dashboard` (+ dynamic agent windows) | Hosts the TUI plus one window per agent / supervisor / shell invocation | `cmd/drem/main.go:114`, `internal/tmux/tmux.go:49` |
| Per-agent windows within dashboard session | `{agent-type}-{short-uuid}` (e.g. `planner-abc123`, `coder-def456`, `supervisor-ghi789`) | One window per spawned agent. Supervisors run as sibling windows. | `internal/agent/runner.go`, `internal/tmux/tmux.go:294-504` |
| Ad-hoc shell sessions | `<dash>/shell <4-char-uuid>` | TUI-spawned "open shell in worktree" action | `internal/tui/actions.go:371` |
| csuite-* sessions | One per C-Suite agent (`csuite-mike`, `csuite-alex`, `csuite-ross`, `csuite-seth`, `csuite-kyle`) — spawned by csuite-watcher on inbox events | csuite-watcher hosts long-running agent sessions for one-shot prompts | csuite-watcher runbook (not in-code) |

Every session uses the `drem` socket (`tmux -L drem`) by default (`cmd/drem/config.go:140`).

---

## 5. Environment variables / secrets the code reads

| Name | Consumer | Default | Citation |
|---|---|---|---|
| `HOME` | Resolves `~/.drem-csuite/*` paths | (system) | `cmd/drem/main.go:291`, `cmd/gq/main.go:33` |
| `CSUITE_DB` | drem-bridge DB target override | `~/.drem-csuite/csuite.db` | `cmd/drem-bridge/main.go:40` |
| `DREM_SESSION` | Internal marker distinguishing outer shell vs. inner tmux invocation | unset | `cmd/drem/main.go:128` |
| `GQ_BIND_ADDR` | GQ proxy listen override | `127.0.0.1:8090` | `internal/gq/config.go:185` |
| `GQ_METRICS_ADDR` | GQ metrics listen override | `127.0.0.1:8091` | `internal/gq/config.go:189` |
| `GQ_UPSTREAM` | GQ upstream SGLang URL override | `http://localhost:8081` | `internal/gq/config.go:193` |
| `GQ_MAX_SLOTS` | GQ concurrency override | `4` | `internal/gq/config.go:197` |
| `GQ_QUEUE_MAX_DEPTH` | GQ queue depth override | `128` | `internal/gq/config.go:201` |
| `EDITOR` | TUI bug-report editor spawn | (system) | `internal/tui/bugreports.go:542` |

**Secrets:** the only "secret" today is `[serve].bearer_token = "drem-local-dev"` in drem.toml — a plaintext shared token for the csuite-watcher HTTP API. No Anthropic/OpenAI API keys are read by drem itself; the `claude` / `opencode` CLIs read their own auth from `~/.config/claude/` and `~/.config/opencode/` (outside drem's process).

---

## 6. Host binaries invoked via `exec.Command` / `exec.CommandContext`

Deduped set (verified by grep over all Go sources):

| Binary | Callers (sample) | Why |
|---|---|---|
| `git` | `internal/worktree/git.go:45`, `internal/merge/conflict.go:127`, `internal/orchestrator/dedup.go:145`, `internal/testutil/testutil_git.go:20,37` | All git operations |
| `tmux` | `internal/tmux/tmux.go:542`, `cmd/csuite-inbox-watch/main.go:143` | Pane/session/window management |
| `sh` | `internal/orchestrator/orchestrator.go:622`, `internal/orchestrator/test_execution.go:410` | Test / compile command shell-out |
| `bash` | `internal/constraints/evaluate.go:127`, `internal/agent/direct_tool_agent.go:581`, `internal/worktree/repomap.go:41`, `cmd/drembench/main.go:274` | Constraint evaluation, direct-tool `Bash` tool, repo-map script, benchmark harness |
| `claude` | `internal/agent/process.go:53`, `internal/supervisor/supervisor.go:45`, `internal/watcher/lifecycle.go:90`, `internal/watcher/subprocess.go:29` | Agent CLI spawn (planner, reviewer, supervisor, csuite) |
| `opencode` | `internal/agent/process.go:145` | Alternative agent CLI |
| `rg` (ripgrep) | `internal/agent/direct_tool_agent.go:647` | G4 direct-tool `Grep` tool |
| `cp` | `cmd/drembench/main.go:245` | Benchmark fixture copy (non-critical) |
| `$EDITOR` | `internal/tui/bugreports.go:542` | TUI bug-report edit |

Each of these must be present in the container image that runs the corresponding code path. For worker images (Phase 2): `git`, `bash`, `claude`, `opencode`, `rg`, plus the language toolchain.

---

## 7. Default config values (for Dockerfile / compose authors)

### 7.1 From `drem.toml` (checked-in example)

| Key | Value |
|---|---|
| `bare_repo_path` | `/home/godinj/git/drem-orchestrator.git` |
| `default_branch` | `master` |
| `max_concurrent_agents` | `4` |
| `opencode_bin` | `opencode` |
| `opencode_context_window` | `98304` |
| `llama_server_endpoint` | `http://localhost:8081` |
| `test_command` | `go test ./...` |
| `compile_command` | `go vet ./...` |
| `skip_constraint_gate` | `false` |
| `[agents.classifier].provider` | `sglang-direct` |
| `[agents.classifier].model` | `gemma4-26b` |
| `[direct_tool_agent].endpoint` | `http://localhost:8090/v1/chat/completions` |
| `[direct_tool_agent].model` | `gemma4-26b` |
| `[watcher].db_path` | `~/.drem-csuite/watcher.db` |
| `[watcher].inbox_base_dir` | `~/.drem-csuite` |
| `[serve].listen_addr` | `:8080` |
| `[serve].bearer_token` | `drem-local-dev` |
| `[serve].db_path` | `~/.drem-csuite/csuite.db` |

### 7.2 From `DefaultConfig()` in `cmd/drem/config.go:119-156`

| Key | Default |
|---|---|
| `database_path` | `./drem.db` |
| `default_branch` | `master` |
| `claude_bin` | `claude` |
| `opencode_bin` | `opencode` |
| `max_concurrent_agents` | `5` |
| `tick_interval` | `5s` |
| `heartbeat_interval` | `30s` |
| `stale_timeout` | `5m` |
| `supervisor_enabled` | `true` |
| `supervisor_timeout` | `2m` |
| `context_warn_percent` | `75` |
| `context_stop_percent` | `90` |
| `context_fixer_percent` | `85` |
| `log_path` | `./drem.log` |
| `test_timeout` | `5m` |
| `tmux_socket` | `drem` |
| `tmux_config_file` | `master/.tmux.conf` |

### 7.3 From `gq.DefaultConfig()` in `internal/gq/config.go:124-166`

See Table §1.2 above.

---

## 8. Phase-1 critical summary (the Dockerfile seed targets)

What Phase 1 containerizes: **SGLang + GQ + local registry.** No agent-facing behaviour changes. Minimum inventory Alex needs to seed compose:

- **SGLang container:** GPU access mandatory; bind-mount host model dir read-only; publish `:8081` inside the compose network only (do not expose to host after migration, or keep `:8081` on host during transition so un-migrated callers still resolve).
- **GQ container:** depends on `sglang`; resolve `GQ_UPSTREAM=http://sglang:8081` by service name; publish `:8090` + `:8091`; mount `~/.drem-csuite/gq.toml` read-only if present.
- **Registry container:** `registry:2` on `:5000`; persistent volume for image data so repeated spawns don't re-pull.

Everything else (orchestrator, csuite-watcher, spawner, agentmon, Kyle, C-Suite, workers, merger) is Phase 2+.

---

*Generated by Seth (CTO) 2026-04-18 under Kyle pivot directive. Paired: `plans/kill-list.md`, `infra/docker/*.Dockerfile`, root `docker-compose.yml`.*
