# Plan: Containerization of Drem Orchestrator Services and Agents

> Source PRD: `docs/prd-containerization.md`
>
> Phasing follows PRD §Further Notes verbatim. This plan fleshes out each phase
> with concrete slices, tags every task `[G4]` (tight scope, clear success
> criteria, limited file touch) or `[Opus]` (needs architectural judgment,
> cross-package refactor, or novel design), and estimates scope by file-touch /
> review-risk.
>
> Execution is PARALLEL, not via drem. Each slice is dispatched to a temp agent
> outside the drem pipeline. Kyle moves last.

## Architectural decisions

Durable across phases — every slice honors these:

- **Container filesystem is canonical for agents.** No host-side worktrees.
  Bare repository is the single source of truth; clone-on-spawn into container fs.
- **Spawner owns the Docker socket.** All container lifecycle ops route through
  spawner RPC over Unix socket. Orchestrator never touches the socket.
- **Orchestrator is the single state surface per project.** TUI, Kyle, and any
  reporter read exclusively from orchestrator HTTP API.
- **Kyle is a global singleton.** One container per host, polls each project's
  orchestrator over HTTP; reads `~/.drem/projects.toml`.
- **Agentmon absorbs log shipping.** Extended to subscribe to Docker stdout
  for every labeled container; POSTs structured state records to the correct
  project's orchestrator. Raw logs remain in Docker's log driver.
- **Per-project isolation + shared heavy infra.** One SQLite per project; one
  SGLang; one GQ; one registry; per-project orchestrator + C-Suite + merger
  pool.
- **Recovery = commit + push + respawn.** Worker image bakes a watchdog that
  commits and pushes every minute and after every passing test. Orchestrator
  reacts to Docker exit events, spawns a replacement, and resumes from
  most-recent-pushed commit. No volume-based restore.
- **State machine preserved.** `backlog → planning → plan_review → test_writing
  → test_review → in_progress → testing_ready → merging → done` is unchanged.
  Only execution substrate changes.
- **Image strategy.** Per-language worker images (`drem-worker-go`,
  `drem-worker-cpp`). Git-sha-pinned tags in compose; `latest` for dev.
- **Compose topology.**
  - **Global compose** in `drem-orchestrator` repo: registry, SGLang, GQ,
    spawner, agentmon, Kyle.
  - **Per-project compose** generated into `~/.drem/projects/<name>/compose.yml`:
    orchestrator, csuite-watcher, four C-Suite containers, merger warm pool.
  - **Ephemeral** (workers, merger invocations, one-shot C-Suite prompts) are
    spawned on demand; never listed in compose.
- **HTTP contracts** (frozen early):
  - Public read-only: `GET /projects`, `GET /projects/:name/tasks`,
    `GET /projects/:name/workers`, `GET /workers/:id`,
    `GET /workers/:id/history`, `GET /events?since=`,
    `GET /logs?container=&since=`.
  - Internal ingestion: `POST /internal/logs` (per-project shared token auth).
- **Spawner RPC** (frozen early): `SpawnWorker`, `DestroyWorker`,
  `ListWorkers`, `InspectWorker` over Unix socket, JSON-RPC 2.0 framed.
- **Module shape.** New: `internal/container`, `internal/spawner`,
  `internal/gitref`, `internal/merger`, `internal/api` (server+client),
  `internal/extraction`, `cmd/watchdog`, `cmd/kyle`, `cmd/spawner`.
  Deleted: `internal/tmux/`, `internal/worktree/`.
- **Tests**: new modules ship with tests from day one using existing
  `internal/testutil/` helpers. Real-Docker integration test only for the
  container-runtime abstraction (smoke). Everything else uses a fake runtime.
- **Networking**: workers have full egress for the first cut. Docker socket
  mounted only into spawner. Kyle reads Docker state via a read-only proxy
  container.

---

## Phase 1: Shared infra — registry, SGLang, GQ in containers

**User stories**: 1 (long-running services in containers), 5 (global SGLang),
4 (global GQ rate-limit), 34 (local registry), 29 (compose lives in repo),
35 (remote registry not architecturally blocked).

### What to build

Stand up the global compose at the repo root with three services: local Docker
registry, SGLang, GQ. Both SGLang and GQ currently run on the host — move them
into containers with zero agent-facing behavior change. Host TUI and
orchestrator continue to hit SGLang at `:8081` and GQ at `:8090/:8091` exactly
as today; those ports are now published by the containers. Validates the
shared-infra story end-to-end and proves the compose + registry loop works.

### Slices

| # | Slice | Tag | Scope |
|---|---|---|---|
| 1.1 | `Dockerfile.registry` + compose service publishing `127.0.0.1:5000`; `docker push localhost:5000/hello:test` from host round-trips successfully. | [G4] | 1 Dockerfile + compose stanza, 1 smoke test script |
| 1.2 | `Dockerfile.sglang` — reuse current host startup script (`bin/sglang-serve.sh` or equivalent). Publish `:8081`. GPU passthrough via `--gpus all`. Model volume mount read-only from host path. Health check = `GET /v1/models` returns 200. | [Opus] | 1 Dockerfile, needs judgment on GPU/mount layout; ~1 day |
| 1.3 | `Dockerfile.gq` — containerize existing gq binary. Publish `:8090/:8091`. Upstream SGLang target is the compose service name, not `localhost`. Systemd user unit on host is stopped, documented as replaced. | [G4] | 1 Dockerfile, env var change for upstream target |
| 1.4 | Root `docker-compose.yml` with registry + sglang + gq services on a shared user-defined network `drem_shared`. Compose-up ordering: registry → sglang → gq. Document `docker compose up -d` as the supported startup. | [G4] | 1 compose file, 1 README section |
| 1.5 | Smoke test: host TUI and orchestrator hit `:8081` and `:8090` with no config changes beyond the systemd-unit disable. Run drembench against containerized stack; compare tok/s and latency to host baseline; tolerate ≤5% degradation. | [Opus] | New bench script; interpret results |

### Acceptance criteria

- [ ] `docker compose up -d` from repo root starts all three services; `docker compose ps` shows all healthy.
- [ ] Host orchestrator + TUI continue to function with host SGLang/GQ stopped and containers running.
- [ ] drembench results within 5% of host baseline.
- [ ] Local registry round-trips a test push+pull.
- [ ] README "Getting Started" section documents the compose workflow; old systemd GQ unit marked deprecated.

---

## Phase 2: Spawner service + container runtime + worker-go image

**User stories**: 20 (spawner owns Docker socket), 21 (spawner narrow RPC),
32 (per-language worker images), 33 (image driven by config language field),
8 (workers as ephemeral containers), 10 (worker clones bare repo into container),
44 (full egress for now).

### What to build

Build the container-runtime abstraction, the spawner binary exposing four RPCs
over a Unix socket, and the first worker image (`drem-worker-go`). Route Opus
coder tasks for the drem-orchestrator project through the new path. Host-side
workers remain available as a fallback while the new path bakes. End-to-end
demo: an Opus coder task picks up, spawner creates the worker container, the
worker clones its branch from the bare repo into the container fs, executes
the agent entry point (existing `claude` CLI), and on exit the orchestrator
records the exit code.

### Slices

| # | Slice | Tag | Scope |
|---|---|---|---|
| 2.1 | `internal/container` package — runtime interface (`Spawn`, `Inspect`, `StreamLogs`, `SubscribeEvents`, `Destroy`) + real Docker-backed impl + in-memory fake. Fake used by unit tests across the codebase. Real impl exercised by one integration test against a local daemon with a hello-world image. | [Opus] | New package, interface design critical |
| 2.2 | `cmd/spawner` binary — JSON-RPC 2.0 server over Unix socket `/run/drem-spawner.sock`. Implements `SpawnWorker`, `DestroyWorker`, `ListWorkers`, `InspectWorker` against the runtime interface. Structured logs. Graceful shutdown. | [Opus] | New binary, protocol framing, auth model (uid check on socket) |
| 2.3 | `Dockerfile.spawner` + compose service mounting `/var/run/docker.sock` and exposing the Unix socket on a host-side volume so per-project orchestrators can reach it. | [G4] | 1 Dockerfile, compose edits |
| 2.4 | `Dockerfile.worker-go` — Go toolchain pinned to `go.mod` version, `git`, `make`, `claude` CLI, watchdog binary (placeholder in this phase), minimal CA roots for egress. Entrypoint clones `$BRANCH` from `$BARE_REPO` (read-only bind mount) into `/workspace` and execs the agent with `$AGENT_ARGS`. | [Opus] | Entrypoint shell is tricky; testing needs real git |
| 2.5 | Spawner client library in `internal/spawner/client` — typed Go wrapper over the RPC. Used by orchestrator. Has a fake for tests. | [G4] | Client code + fakes |
| 2.6 | Orchestrator integration — add a `container` agent-spawn path behind a feature flag (`drem.toml: workers.engine = "spawner" | "host"`). When set to `spawner`, Opus-coder dispatch calls `SpawnWorker`. State machine unchanged. Host path remains default. | [Opus] | Cross-package edit in orchestrator dispatch; review risk high |
| 2.7 | Image selection — `drem.toml` gains `language = "go"`. Spawner maps `language → image` via compose env or a constants table (`go → drem-worker-go:<sha>`). Reject spawn if language is unknown. | [G4] | Config plumbing + one map |
| 2.8 | End-to-end test on drem-orchestrator project: one Opus coder task dispatched through spawner, observed to spawn a container, clone its branch, run the agent to completion, and return exit status. Failure modes (image pull fail, clone fail, OOM) produce typed errors in orchestrator logs. | [Opus] | Black-box integration; may surface runtime-API bugs |

### Acceptance criteria

- [ ] `drem.toml: workers.engine = "spawner"` routes Opus coder tasks through a container.
- [ ] Worker container clones bare repo into `/workspace`, exits on agent completion, is destroyed by the spawner.
- [ ] Container runtime abstraction's fake is used in ≥5 existing test files without real Docker.
- [ ] Spawner RPC has unit tests for all four methods + error paths.
- [ ] Exit code, OOM flag, started_at, finished_at recorded in orchestrator DB for each worker run.

---

## Phase 3: Orchestrator HTTP API + agentmon Docker stdin + TUI migration

**User stories**: 13 (live state per project), 14 (historical state), 15 (docker_query escape hatch), 26 (agentmon filters stdout), 27 (no log duplication to DB), 28 (`GET /logs` proxy), 40 (TUI reads from API), 47 (orchestrator sole DB writer), 48 (agentmon POSTs to orchestrator), 16 (structured events in SQLite).

### What to build

Introduce the orchestrator HTTP API server + client library, extend agentmon
to subscribe to Docker container stdout for every labeled container, and
migrate the TUI from direct-SQLite reads to the API. After this phase, the
database is only written by the orchestrator process, only read by the
orchestrator process, and everyone else (TUI, agentmon, Kyle-to-be) goes
through HTTP.

### Slices

| # | Slice | Tag | Scope |
|---|---|---|---|
| 3.1 | `internal/api/server` — HTTP handlers for `GET /projects`, `GET /projects/:name/tasks`, `GET /projects/:name/workers`, `GET /workers/:id`, `GET /workers/:id/history`, `GET /events?since=`. Read-only; reads directly from orchestrator's DB handle. JSON contract documented in `docs/api.md`. | [Opus] | New HTTP surface; contract design is durable |
| 3.2 | `internal/api/client` — typed Go client for the public API. Used by TUI and later Kyle. Has a fake-server test helper using `httptest.Server`. | [G4] | Wrapper code |
| 3.3 | `GET /logs?container=&since=` — proxies `docker logs` via the container-runtime abstraction's `StreamLogs`. Streams, does not buffer. | [G4] | One handler + streaming |
| 3.4 | `POST /internal/logs` — accepts batched structured state records (commits, pushes, test results, crashes, heartbeat, tool-call counts, build errors). Per-project shared token auth. Writes to DB. This becomes the orchestrator's only ingress for agentmon data. | [Opus] | Auth model + DB write path + backpressure |
| 3.5 | `internal/extraction` package — pure functions that parse log lines into typed state events. Shared between agentmon's Docker stdout tail and the existing Claude-transcript tail. Tests: line → event (or nil). No fixtures beyond string inputs. | [Opus] | Event taxonomy is durable — invest carefully |
| 3.6 | Agentmon Docker-stdout subscription — for every container labeled with a known project, tail stdout via runtime `StreamLogs`, feed to `extraction`, batch structured events to `POST /internal/logs`. Coexist with existing Claude-transcript tail. | [Opus] | Cross-package edit; lifecycle subtleties |
| 3.7 | TUI migration — replace every direct-DB read with an API client call. Rendering behavior unchanged. Smoke test: open TUI, navigate, verify no SQLite cursor is opened by the TUI process. | [Opus] | Broad TUI refactor; review risk |
| 3.8 | Remove write paths from non-orchestrator binaries — audit any remaining callsite that opens the DB for write and either delete it or route through the API. TUI, agentmon, Kyle stubs all fail closed if they try. | [G4] | Mechanical audit |

### Acceptance criteria

- [ ] Every public-API endpoint has a documented contract, handler, client, and test.
- [ ] Agentmon ingests Docker stdout for labeled containers and produces events in the orchestrator DB via `POST /internal/logs`.
- [ ] `extraction` tests cover at least: git-commit line, git-push line, test-pass line, test-fail line, build-error line, crash line, heartbeat line, tool-call-count line.
- [ ] TUI runs with zero direct DB cursors (verified by `lsof`).
- [ ] Only the orchestrator process holds a writable SQLite handle.

---

## Phase 4: Kyle binary + per-project registry + drem-orchestrator project registered

**User stories**: 6 (Kyle global container), 12 (container labels for attribution), 30 (per-project compose generated), 31 (`~/.drem/projects.toml`), 50 (two projects registered via same flow), 38 (Kyle from baked image).

### What to build

Build the Kyle binary and container, the `drem project` CLI surface that
creates and tears down project registrations, and register drem-orchestrator
as the first project end-to-end. Kyle polls the orchestrator API for its
single registered project and produces a unified report. The per-project
compose template is the artifact Kyle lives and dies by — get it right here.

### Slices

| # | Slice | Tag | Scope |
|---|---|---|---|
| 4.1 | `~/.drem/projects.toml` schema — `name`, `bare_repo_path`, `language`, `orch_url`. `drem project list` reads, prints. `drem project remove` deletes entry + stops per-project compose. | [G4] | CLI + TOML schema |
| 4.2 | `drem project register <name> --lang <lang> --bare-repo <path>` — generates `~/.drem/projects/<name>/compose.yml` from an embedded template, adds TOML entry, optionally runs `docker compose -f <path> up -d`. | [Opus] | Template design is durable; careful about per-project port allocation / network naming |
| 4.3 | Per-project compose template — services: orchestrator, csuite-watcher, mike, alex, ross, seth. (Originally included a `merger-pool` of 2 containers; removed because `drem-merger` is a per-task one-shot binary that crash-loops with no argv. The template now keeps a `merger-template` stub gated behind `profiles: ["never"]` purely for image priming. Spawn-on-demand wiring tracked in `plans/merger-spawn-on-demand.md`.) Each service mounts the project's SQLite data volume. Shared network `drem_shared` joins for SGLang/GQ access. Per-project token env for `POST /internal/logs`. | [Opus] | Long-lived artifact; change cost is high once projects exist |
| 4.4 | `Dockerfile.orch` — orchestrator baked image. Entrypoint runs the orchestrator against `/data/drem.db` (per-project volume) and `/config/drem.toml` (bind-mounted). Dev-mode variant bind-mounts the source and rebuilds on start. | [Opus] | Two entrypoints; judgment on dev ergonomics |
| 4.5 | Container labels — every spawn by spawner or compose applies labels `drem.project`, `drem.agent_type`, `drem.worker_id` (if ephemeral), `drem.task_id` (if applicable), `drem.image_sha`. Orchestrator records image_sha per run (US 49). | [G4] | Label contract |
| 4.6 | `cmd/kyle` binary + `Dockerfile.kyle` — reads `~/.drem/projects.toml`, for each project calls the API client, aggregates into a single report. No per-project logic; Kyle is pure orchestration of the API. | [Opus] | New binary; report format design |
| 4.7 | Kyle's `docker_query` escape hatch — read-only proxy container (socat + allowlist) exposing `inspect`, `logs`, `ps`, `stats` only. Kyle calls it via a narrow HTTP wrapper. Never sees the socket. | [Opus] | Security-sensitive |
| 4.8 | End-to-end: register drem-orchestrator, `docker compose up`, run a coder task, observe Kyle's report reflect it. | [G4] | Smoke test |

### Acceptance criteria

- [ ] `drem project register` creates TOML entry + per-project compose file + starts the per-project stack.
- [ ] Every running container for a registered project carries the documented label set.
- [ ] Kyle's report distinguishes tasks, workers, and events per project from a single host invocation.
- [ ] `docker_query` proxy rejects any write-verb request.
- [ ] Kyle runs from the baked image, not bind-mounted source.
- [x] `drem project register --update <name>` regenerates per-project compose.yml + drem.toml from current master templates, preserving SharedToken / OrchHostPort / DevMode / ContainerImageOverrides. Drift detection surfaces hand-patches before overwriting; `--dry-run`, `--force`, `--fail-on-drift`, and `--regenerate-token` flags cover the operator-choice matrix. compose.override.yml and operator-owned sidecars are never touched. _(2026-04-20: `internal/projects/state.go` extracts SharedToken + OrchHostPort from on-disk compose.yml; `internal/projects/drift.go` emits structural diffs; `cmd/drem/project.go` wires the flags. See `plans/drem-project-register-update.md`.)_

---

## Phase 5: worker-cpp image + drem-canvas as second project

**User stories**: 2 (register new project with single command), 3 (per-project isolated SQLite + compose), 43 (C/C++ toolchain in worker image), 51 (Kyle aggregates resource usage across projects).

### What to build

Validate multi-project. Build the C++ worker image, register drem-canvas as
the second project, drive one coder task through it, and confirm Kyle's
cross-project report is coherent.

### Slices

| # | Slice | Tag | Scope |
|---|---|---|---|
| 5.1 | `Dockerfile.worker-cpp` — Clang/GCC, CMake, make, ninja, common build tools, `claude` CLI, watchdog, git. Matches the worker-go contract (same entrypoint shape, same labels). | [Opus] | Toolchain selection + pinning; non-trivial size budget |
| 5.2 | Image selection test: `language = "cpp"` in drem-canvas's `drem.toml` causes the spawner to select `drem-worker-cpp`. Spawner rejects if the image is not in the local registry. | [G4] | Config test |
| 5.3 | Register drem-canvas via `drem project register drem-canvas --lang cpp --bare-repo <path>`. Manually craft a trivial C++ task. Confirm it drives through the full state machine and lands a commit. | [Opus] | Cross-project integration; real drem-canvas checkout |
| 5.4 | Kyle cross-project report aggregates CPU, memory, and network stats across both projects' container sets, grouped by project. | [G4] | Kyle report extension |
| 5.5 | Isolation verification — induce a write error in drem-canvas's SQLite; confirm drem-orchestrator's orchestrator + API are unaffected. | [Opus] | Chaos test |

### Acceptance criteria

- [ ] Both projects registered, both compose stacks running, Kyle reports both.
- [ ] A C++ task drives end-to-end through drem-canvas.
- [ ] Blast-radius test: DB corruption in one project does not affect the other.

---

## Phase 6: Merger warm pool + watchdog + Docker-event recovery

**User stories**: 9 (merger warm pool), 22 (merger clones into container fs), 23 (merger deletes feature branch), 24 (merger reports structured records), 17 (Opus watchdog commits+pushes), 18 (G4 watchdog), 19 (orchestrator detects worker death and respawns), 25 (orchestrator subscribes to Docker lifecycle events), 46 (tests run in worker then in merger).

### What to build

Replace the remaining host-side operations (merge, stall detection) with their
container-native equivalents. Merger becomes a warm pool per project that
clones integration + feature branch into a fresh workspace, runs the project's
test command, merges, pushes, deletes the feature branch, and posts a
structured record to the API. Worker image gains a watchdog baked in that
commits and pushes to the bare repo every minute and after every test pass.
Orchestrator subscribes to Docker lifecycle events and respawns workers on
unexpected exit, resuming from the most recent pushed commit.

### Slices

| # | Slice | Tag | Scope |
|---|---|---|---|
| 6.1 | `internal/gitref` — replaces `internal/worktree`. Tracks branch names and commit shas in SQLite. No filesystem ops. Callers: orchestrator state machine (when reading current branch), merger (when told which branches to fetch). | [Opus] | Package replacement; API design inflection point |
| 6.2 | `internal/merger` — the merge operation as a pure function of `(bare_repo, integration_branch, feature_branch, test_cmd)` returning `MergeResult`. Tested with existing `testutil.SetupBareRepo` helpers. | [Opus] | New package; prior art in `internal/merge/` informs but does not dictate |
| 6.3 | `Dockerfile.merger` — same base as worker-go (so `go test` runs identically) plus merger binary as entrypoint. Compose spawns a warm pool of 2 per project. | [G4] | 1 Dockerfile + compose |
| 6.4 | Merger workspace reset — between merges, each pool container wipes `/workspace` and re-clones integration branch. Tests verify no state leaks between runs. | [Opus] | Resource lifecycle |
| 6.5 | Feature-branch deletion — on successful merge, the merger deletes the feature branch from the bare repo. Failure to delete is logged but does not fail the merge. | [G4] | 1 function + test |
| 6.6 | `cmd/watchdog` — baked into worker image. Timer-based: if working tree has diffs, commit (auto-message + task id) and push to the feature branch in the bare repo. On test-command zero exit, commit + push immediately. Sleeps when no diffs. | [Opus] | Timing + correctness; bake into worker image |
| 6.7 | Docker-event subscription — orchestrator subscribes via runtime `SubscribeEvents` for every container labeled with its project. On `die` event for a worker, record exit code + OOM flag, mark task for respawn. On respawn, spawner clones the same branch; worker's watchdog resumes from HEAD of the pushed feature branch. | [Opus] | Cross-package; event backpressure + ordering |
| 6.8 | Two-gate testing — worker runs project tests before its final push (fail fast). Merger re-runs tests in its own container before merging (authoritative). Both outcomes POSTed to the API as structured records. | [G4] | Integration in watchdog + merger |
| 6.9 | Structured merge records — `{task_id, status: ok|conflict|test_fail, commit_sha?, conflict_files?, test_output_ref?}`. API endpoint `POST /internal/logs` already exists; extend the extraction package's taxonomy. | [G4] | Schema extension |

### Acceptance criteria

- [ ] A worker can crash mid-task and be respawned; the respawned worker's first commit includes work up to the last watchdog push from the crashed container.
- [x] A merge failure due to test failure is recorded as a structured event in the orchestrator DB and visible via the API. _(2026-04-19: exit-code routing in `executeMerge` maps tests_failed to `failTask` + `merge_tests_failed` event; the merger container POSTs the full `merge_result` record to `/internal/logs`. See `plans/merger-spawn-on-demand-impl.md`.)_
- [x] A merge success deletes the feature branch in the bare repo. _(2026-04-19: merger binary already does this via `internal/merger/merger.go`; spawn-on-demand wiring in `dispatchMerge` makes the path reachable.)_
- [x] Planner runs as a long-lived warm container rather than a host-side OpenCode subprocess. _(2026-04-20: orch's `dispatchPlanHTTP` POSTs `/plan` to the warm `drem-planner` service on drem-net when `[agents.planner].provider` resolves to `claude`. Container shells out to the `claude` CLI per request against Anthropic Opus, returns plan.json inline in the response body. Subscription-only auth via a read-only bind-mount of `~/.claude/.credentials.json` — no ANTHROPIC_API_KEY passthrough anywhere in the generated compose. See `plans/warm-planner-pivot.md`. The earlier spawn-on-demand design in `plans/warm-direct-planner.md` landed briefly in c279f32..b2024ee but was replaced before T2 canary because per-task container polling reintroduced the tick-starvation pressure we moved classifier OUT of orch to fix.)_
- [x] Claude-backed workers (coder/reviewer/fixer/tester/supervisor) receive the host operator's subscription credentials via a read-only bind-mount at `/home/drem/.claude/.credentials.json`; merger deliberately skips the mount. _(2026-04-20: `spawner.SpawnWorkerParams.CredsMount` + orch's `credsMountRequired` table + worker-base Dockerfile pre-creating `/home/drem/.claude` with drem ownership + per-project compose rendering `DREM_WORKER_CREDS_PATH` on orch. No ANTHROPIC_API_KEY plumbing anywhere on the default path; the orch boundary rejects the env var with `reason=policy_violation_api_key` if it ever lands. See `plans/worker-subscription-auth.md`.)_
- [x] Claude-backed workers receive their task prompt via a read-only bind-mount at `/home/drem/.drem/prompt.md`; orch renders via `internal/prompt.Generate` and writes atomically to a host dir the spawner bind-mounts into each worker. _(2026-04-20: `spawner.SpawnWorkerParams.PromptMount` + orch's `promptRequired` table + atomic `renderAndWritePrompt` (tmp + rename) + worker-base Dockerfile pre-creating `/home/drem/.drem` with drem ownership + per-project compose rendering `DREM_PROMPT_ROOT_HOST` on orch and bind-mounting the same host path rw. The spawner sets `DREM_PROMPT_PATH` deterministically so callers cannot regress the contract. Missing prompt file at spawn time fails closed with `reason=prompt_render_failed`; merger omits the mount. See `plans/worker-prompt-delivery.md`.)_
- [ ] No host-side git worktree exists anywhere after this phase.

---

## Phase 7: Retire tmux + worktree packages; final cutover

**User stories**: 11 (host-side worktrees eliminated), 41 (remove `internal/tmux/`), 42 (replace `internal/worktree/` with `internal/gitref/`), 52 (state machine preserved), 53 (supervisor loop continues), 54 (ephemeral C-Suite one-shots in containers), 39 (drem CLI continues to work).

### What to build

Delete what's been orphaned, confirm nothing depends on it, move ephemeral
C-Suite one-shots into containers, and sign off on the rearch. No new features
in this phase — it's the graduation.

### Slices

| # | Slice | Tag | Scope |
|---|---|---|---|
| 7.1 | Import audit — `grep -R "internal/tmux"` and `grep -R "internal/worktree"` across the repo; expected zero non-test hits. | [G4] | Mechanical |
| 7.2 | Delete `internal/tmux/` and `internal/worktree/`. Delete tmux-related drem.toml fields. Update ARCHITECTURE.md to reflect the new module shape. | [G4] | Deletion PR |
| 7.3 | Supervisor loop port — the existing supervisor (context-window monitoring, stall detection, fixer spawning) continues to run inside the orchestrator container. Confirm no callsite assumed tmux or host worktree. | [Opus] | Subtle behavior preservation |
| 7.4 | Ephemeral C-Suite one-shot containerization — each one-shot prompt invocation runs as a short-lived container spawned inside the project's C-Suite context. Warm C-Suite container remains available for chat-style turns. | [Opus] | Mode design |
| 7.5 | Final cutover checklist: all seven phases green, no in-flight drem tasks, host SGLang/GQ/systemd disabled, bare repo is the only host-side git state. Operator signs off. | [G4] | Checklist |

### Acceptance criteria

- [ ] `internal/tmux/` and `internal/worktree/` are gone.
- [ ] ARCHITECTURE.md reflects container-native architecture; old assumptions removed.
- [ ] Supervisor loop's existing guarantees pass a regression test inside the orchestrator container.
- [ ] One-shot C-Suite prompts launch and exit as ephemeral containers.
- [ ] Operator confirms rearch complete.

---

## Phase 8: Warm direct-agent containers (classifier first; planner + prep later)

**User stories**: 40 (classifier runs outside orch), 52 (state machine unchanged), 53 (supervisor loop continues).

### What to build

Lift the direct agents (classifier, planner, prep) out of the orch
process and into long-lived containers on drem-net. Each role gets its
own binary (`cmd/drem-classifier`, `cmd/drem-planner`, `cmd/drem-prep`)
and its own image; orch POSTs classify/plan/prep jobs over HTTP.
Isolates failure modes, bounds orch thread count, and lets each role
be scaled / replaced independently.

### Slices

| # | Slice | Tag | Scope |
|---|---|---|---|
| 8.1 | Warm **drem-classifier** (done, see `plans/warm-direct-classifier.md`). `agent.Classify` refactor + HTTP server + Dockerfile + compose entry + orch routing toggle + template updates + walkthrough. | [Opus] | Done |
| 8.2 | Warm **drem-planner** — HTTP server that execs claude CLI per /plan request, long-lived single replica. Subscription-only auth via `~/.claude/.credentials.json` bind-mount (no API-key fallback). Tracked in `plans/warm-planner-pivot.md`; supersedes the spawn-on-demand draft in `plans/warm-direct-planner.md`. | [Opus] | Parallel role |
| 8.3 | Warm **drem-prep** — same shape as planner; tracked in `plans/warm-direct-prep.md`. | [Opus] | Parallel role |
| 8.4 | Drop the orch-side endpoint-health circuit breaker once each role's container has its own /healthz probe wired to gq (see plans/warm-direct-classifier.md §9 Q3). | [G4] | Cleanup |

### Acceptance criteria

- [x] `drem-classifier` container is the default classify path for freshly-registered projects.
- [ ] `drem-planner` and `drem-prep` containers land with companion plans.
- [ ] Orch's `endpointHealth.IsHealthy()` gate is either removed or repurposed for a container-reachability check.
- [ ] Every warm agent image has /healthz, /metrics, and Bearer auth via `DREM_AGENTMON_TOKEN`.

---

## Sequencing and gating

1. **Phase 1 is green-lit** per pivot msg §3. Start immediately, coordinate
   with Seth (Dockerfile seeds in parallel). Ship before Phase 2.
2. Phases 2 and 3 can partially overlap once the container runtime
   abstraction (2.1) lands — 3.1–3.2 only need the runtime interface, not the
   spawner. Freeze HTTP contract (3.1) before starting 3.3+.
3. Phase 4 requires the HTTP API (Phase 3) because Kyle is a pure API
   consumer.
4. Phase 5 is pure integration; requires Phase 4 compose template and Phase 2
   worker-image recipe.
5. Phase 6 can start alongside Phase 4 because merger/gitref/watchdog are
   independent of Kyle. Gate the final respawn wiring (6.7) behind Phase 3.4
   (so Docker events can be ingested by agentmon).
6. Phase 7 is the last phase before Phase 8; gated by zero in-flight drem tasks.
7. Phase 8 runs after Phase 7; each warm-agent slice (8.1 classifier, 8.2
   planner, 8.3 prep) is independent and can be tackled in any order.
   8.4 (drop the orch-side circuit breaker) gates on 8.1–8.3 all landing.

## Tag distribution

- **[G4]** tasks: 22 (tight scope, clear success, single-file/single-dockerfile work). Good candidates for G4 temps.
- **[Opus]** tasks: 27 (cross-package, novel design, interface decisions, cross-project integration).

Kyle + I review any [Opus]-tagged task's design sketch before dispatch.

## Out of this plan (deferred, per PRD §Out of Scope)

Remote-host deployment; remote image registry; GPU scheduling across projects;
non-Linux host support; multi-tenant isolation; secrets vault; hot-reload for
orchestrator in dev; volume-based full-filesystem restore; restricting worker
egress; TUI rewrite; custom agent harness replacing `claude`/`opencode`;
migration of in-flight tasks.
