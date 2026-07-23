# PRD: Containerization of Drem Orchestrator Services and Agents

## Problem Statement

The current Drem Orchestrator runs all services, daemons, and agent harnesses directly on the host. This causes several recurring pains:

- **Split-brain world state.** Kyle and the rest of the C-Suite cannot produce reliable reports because "what is running" must be reconstructed from tmux panes, `ps` output, the orchestrator database, and scattered log files. None of these sources is authoritative, and they drift apart under load.
- **Environment drift.** SGLang, `claude` CLI, `opencode`, and language toolchains are pinned to whatever is installed on the host. Upgrading one project's toolchain risks breaking another. There is no way to reproduce a given agent's environment with confidence.
- **Opaque agent state and exit codes.** When an agent dies, determining why, when, and with what exit status requires manual forensics across logs. Crashes pollute the host with tmp files, partial worktrees, and zombie processes.
- **Single-project assumption.** The current layout (one bare repo, one `drem.toml`, one csuite, shared `~/.drem-csuite/`) cannot cleanly serve a second concurrent project. The user wants to apply this setup to `drem-canvas` (C/C++) while continuing to use it on `drem-orchestrator` (Go).

The user is operating solo today but is also developing with Kyle and the other C-Suite agents as peers. Any solution must make their world model accurate, not just the user's.

## Solution

Containerize every long-running service and every agent harness. Run each agent's tool execution (Read, Write, Edit, Bash, Glob, Grep) entirely inside its own container, against a container-local copy of the project checkout. The host never bind-mounts a worktree into an agent; the host's bare repository is the single source of truth and is mounted read-only for cloning and read-write only to the merger service.

Orchestrator, spawner, agentmon (extended to absorb log extraction), and per-project C-Suite containers run continuously. Workers, merger invocations, and one-shot C-Suite prompts run as ephemeral containers spawned on demand. Kyle is a single globally-running container that calls each registered project's orchestrator HTTP API to produce a unified view across projects.

Host-side Git worktrees are eliminated entirely. All working copies live inside ephemeral containers and are discarded when the task completes. Recovery from mid-session container crashes is handled by a watchdog baked into worker images that commits and pushes to the bare repository at regular intervals and after every passing test.

## User Stories

1. As the developer, I want every long-running service in the stack (orchestrator, spawner, agentmon, SGLang, GQ, per-project C-Suite) to run in its own container, so that I can upgrade, restart, and inspect each one independently.
2. As the developer, I want to register a new project with a single command, so that I can apply the Drem Orchestrator setup to `drem-canvas` without hand-editing configuration.
3. As the developer, I want each registered project to have its own isolated SQLite database and its own per-project compose file, so that a failure or data corruption in one project does not affect another.
4. As the developer, I want a single global GQ service to manage rate limits across all projects, so that the shared resources (SGLang, the Anthropic API) are fairly allocated.
5. As the developer, I want a single global SGLang container serving all projects, so that GPU resources are not fragmented.
6. As the developer, I want Kyle to be a single global container that reports on every registered project, so that I have one surface to ask "what is the state of the world" across all my work.
7. As the developer, I want the C-Suite agents (Mike, Alex, Seth) to run as warm containers per project, so that one-shot prompts return quickly without paying container startup cost.
8. As the developer, I want workers to run as ephemeral containers, so that any crash, leaked file, or stuck process is automatically cleaned up by destroying the container.
9. As the developer, I want the merger to run as a warm pool of containers per project, so that merges start instantly when a task completes.
10. As the developer, I want each worker container to clone its branch from the bare repository into its own container filesystem at startup, so that workers never touch the host filesystem directly.
11. As the developer, I want Git worktrees on the host to be eliminated entirely, so that the bare repository is the single source of truth and there is no scattered working state to clean up.
12. As Kyle, I want every running container to carry structured labels identifying its project, agent type, worker ID, and task ID, so that I can attribute resource usage and log lines without guessing.
13. As Kyle, I want a single HTTP API per project orchestrator that returns live state for all workers, running tasks, and recent events, so that I can answer "what is happening now" in one call per project.
14. As Kyle, I want the same HTTP API to return historical state (recently completed tasks, exit codes, crash reasons), so that I can answer "what went wrong" without log archaeology.
15. As Kyle, I want a read-only `docker_query` tool (via an MCP proxy) as an escape hatch for deep-dive questions, so that I can inspect container status, exit codes, and live log tails when the orchestrator API is insufficient.
16. As Kyle, I want agentmon to filter every container's stdout for structured telemetry (commits, pushes, test results, build errors, crashes) and write it into each project's SQLite, so that my reports do not require string-matching raw logs while task transitions remain exclusively driven by typed control-plane records.
17. As an Opus coder agent, I want my container to include a watchdog process that commits my in-progress work and pushes to the bare repository every minute and after every passing test, so that if my container dies I can resume from a recent checkpoint.
18. As a G4 worker agent, I want the same watchdog behavior, so that my ephemeral container's crash does not lose work.
19. As the orchestrator, I want to consume typed terminal worker observations from the spawner and apply retry policy through the persisted WorkerAttempt lifecycle, so that replacement is idempotent and never inferred from inventory absence or log telemetry.
20. As the orchestrator, I want to delegate container creation to a separate spawner service that holds the Docker socket, so that I never need direct Docker socket access and the privilege surface is minimized.
21. As the spawner service, I want a narrow RPC surface (SpawnWorker, DestroyWorker, ListWorkers, InspectWorker) exposed over a Unix socket, so that the orchestrator and any other caller have a well-defined, easily-audited contract.
22. As the merger service, I want to pull a worker's branch plus the integration branch into a fresh container filesystem, run the project's test command, merge, and push the result to the bare repository, so that no host-side working copy is required.
23. As the merger service, I want to delete a feature branch from the bare repository after a successful merge, so that dead branches do not accumulate.
24. As the merger service, I want to report merge conflicts and test failures as structured telemetry to the orchestrator, so that Kyle can surface them without scraping logs; the merger's typed exit and authoritative target ref remain the state-machine evidence.
25. As the orchestrator, I want to subscribe to Docker lifecycle events and poll the spawner's typed worker inventory, so that I can react to crashes, OOM kills, and exits in near real time without making event delivery a correctness dependency.
26. As agentmon, I want to tail each container's stdout and filter for structured telemetry (git operations, test results, build errors, heartbeat, tool call counts, crash indicators), so that the orchestrator database receives a clean observational event stream rather than raw logs.
27. As agentmon, I want to leave raw log content in place (Docker's log driver retains it) and not duplicate it into SQLite, so that the database stays small and the source of truth for full logs remains a `docker logs` call.
28. As the orchestrator, I want to expose a `GET /logs?container=...` endpoint that proxies `docker logs` on demand, so that deeper investigation is available without duplicating data.
29. As a new developer cloning the drem-orchestrator repository, I want the global `docker-compose.yml` and all service Dockerfiles to live in the repository, so that `docker compose up` (or an equivalent command) is sufficient to start the stack.
30. As the developer, I want per-project compose files generated from a template into `~/.drem/projects/<name>/compose.yml`, so that per-project configuration is separate from the shared tool repository.
31. As the developer, I want a host-wide project registry at `~/.drem/projects.toml` listing each project's name, bare repository path, language, and orchestrator API URL, so that Kyle and other services can discover projects without manual configuration.
32. As the developer, I want per-language worker images (`drem-worker-go`, `drem-worker-cpp`), so that the image pinned for each project contains exactly the toolchain that project needs.
33. As the developer, I want the image selected for a worker to be driven by the project's declared `language` field in configuration, so that the wrong image cannot be spawned for a project.
34. As the developer, I want all container images pushed to a local registry running on the host, so that repeated container spawns do not re-pull from a public registry.
35. As the developer, I want the option to migrate to a remote registry later, so that multi-host deployment is not architecturally blocked.
36. As the developer, I want the orchestrator's production container to run the baked image, so that deployments are reproducible.
37. As the developer, I want the option to run the orchestrator in a development mode with the source directory bind-mounted and a rebuild-on-restart entrypoint, so that iteration is fast without rebuilding the image.
38. As the developer, I want Kyle to run from a baked image (not bind-mounted source), so that my reporting agent is pinned and reproducible.
39. As the developer, I want the `drem` CLI on the host to continue to work as the primary TUI and management interface, so that my existing workflow does not break.
40. As the developer, I want the TUI to read from the orchestrator HTTP API rather than directly from SQLite, so that the TUI is decoupled from the database schema and can run against a remote orchestrator in the future.
41. As the developer, I want to remove the `internal/tmux/` package, so that agents are no longer coupled to tmux pane addressing.
42. As the developer, I want the `internal/worktree/` package to be replaced by a smaller `internal/gitref/` package that tracks branch names only, so that the orchestrator no longer performs host filesystem operations.
43. As a C/C++ project worker, I want my container image to include a C/C++ toolchain (GCC or Clang, CMake, make, common build tools), so that my agent can compile and test without needing host access.
44. As a worker container, I want full outbound network access for now (for fetching dependencies), so that build systems like vcpkg, conan, cargo, go modules, and npm function normally.
45. As the developer, I want agent tool execution (Read, Write, Edit, Bash, Glob, Grep) to use the existing `claude` CLI or `opencode` built-in tools operating against the container's own filesystem, so that no custom MCP tool shadows are required and the agent behaves identically to its current configuration.
46. As the developer, I want tests to run in the worker container before the worker pushes its branch (fail fast) and again in the merger container before merging to integration (authoritative gate), so that broken code cannot reach integration.
47. As the developer, I want the orchestrator to be the sole writer to each project's SQLite database, so that write concurrency is serialized and data corruption is impossible.
48. As agentmon, I want to POST structured state records to each project's orchestrator over an internal HTTP endpoint, so that the orchestrator remains the sole DB writer.
49. As the developer, I want each worker, merger, and C-Suite container's startup image identity and labels recorded in the orchestrator database, so that Kyle can attribute each run to a specific image version.
50. As the developer, I want to register the two initial projects (`drem-orchestrator` and `drem-canvas`) through the same registration flow, so that the multi-project story is validated from day one.
51. As Kyle, when reporting resource usage, I want to aggregate CPU, memory, and network statistics across every container in every project, so that I can answer "which project is using the most resources."
52. As the developer, I want the migration to preserve the existing state machine (backlog → planning → plan_review → test_writing → test_review → in_progress → testing_ready → merging → done), so that the orchestrator's core logic does not have to be re-validated.
53. As the developer, I want the supervisor loop (context window monitoring, stall detection, fixer spawning) to continue functioning after containerization, so that I retain the existing recovery guarantees.
54. As the developer, I want ephemeral C-Suite one-shot prompts to run as short-lived containers spawned inside the project's C-Suite context, so that each prompt invocation is independent while the warm container remains available.

## Implementation Decisions

### Architecture

- **Container filesystem model.** Every worker, merger, C-Suite ephemeral invocation, and one-shot prompt runs in a container with its own filesystem. The bare repository is the only host-side persistent Git state. No Git worktrees on the host.
- **Spawner service owns the Docker socket.** The orchestrator never touches the Docker socket directly. All container lifecycle operations are routed through a narrow RPC surface on the spawner service.
- **Orchestrator is the single state surface per project.** Kyle and the TUI read exclusively from each project's orchestrator HTTP API. Direct database access from agents is forbidden.
- **Kyle is a global singleton.** One Kyle container runs per host and calls each registered project's orchestrator API using static configuration from `~/.drem/projects.toml`.
- **Agentmon absorbs log shipping.** The existing transcript-extraction logic is extended to subscribe to Docker container stdout for every labeled container. Agentmon POSTs structured state records (commits, pushes, test results, crashes, heartbeat, tool-call counts, build errors) to the correct project's orchestrator over an internal HTTP endpoint. Raw logs remain inside Docker's log driver; the orchestrator proxies `docker logs` on demand.
- **Warm containers.** Orchestrator, spawner, agentmon, SGLang, GQ, Kyle, and each project's C-Suite (Mike, Alex, Seth) are warm. (Originally a per-project merger pool of 2–3 containers was also planned; see the merger note under "Ephemeral containers".)
- **C-Suite runtime pivot (Wave 2, 2026-04-20; OpenCode correction 2026-04-24).** Each C-Suite persona's warm container now runs the `csuite-persona` inbox poller (`cmd/csuite-persona`) as its PID-1 process, not a long-lived interactive `claude` process. The poller scans `~/.drem-csuite/<persona>/inbox/` on a 2 s tick and invokes `opencode run` once per message with the persona prompt embedded in the turn prompt, writing any well-formed outbox files to `~/.drem-csuite/<persona>/outbox/` and archiving the processed message. This closes a dead-end in the prior design where the interactive CLI had no mechanism to observe inbox files. Authentication remains subscription-only via the read-only OpenCode/Codex subscription auth mount; no Claude API token or Anthropic API key is part of this path. See `plans/csuite-persona-pivot.md` and `docs/containerization/install.md` §"C-Suite personas: the persona poller runtime".
- **Ephemeral containers.** Workers (Opus coders, G4 workers), merger invocations, one-shot C-Suite prompts. NOTE: the original PRD called for a warm merger pool, but `drem-merger` is implemented as a per-task one-shot binary that crash-loops when run with no argv. The merger-pool service was removed from the per-project compose template; merger now runs only as on-demand spawns. Spawn-on-demand wiring is tracked in `plans/merger-spawn-on-demand.md`.
- **Recovery strategy.** A watchdog process baked into worker images commits and pushes to the bare repository every minute and after every passing test. On worker container death, the orchestrator detects the exit via a Docker event, spawns a replacement container, and resumes from the most recent pushed commit. Volume-based full-filesystem restore is explicitly out of scope for the first cut.

### Compose topology

- **Global compose (lives in the drem-orchestrator repository).** Services: Kyle, SGLang, GQ, local image registry, spawner, agentmon.
- **Per-project compose (generated into `~/.drem/projects/<name>/compose.yml`).** Services: orchestrator, csuite-watcher, three warm C-Suite containers (Mike, Alex, Seth). The merger image is referenced by an image-prime stub (`merger-template`, `profiles: ["never"]`) so `docker compose pull` primes the tag; the warm merger pool was removed pending spawn-on-demand wiring (`plans/merger-spawn-on-demand.md`).
- **Ephemeral containers** (workers, merger invocations, one-shot C-Suite prompts) are spawned on demand by the spawner service and are not listed in any compose file.

### Project registry

- A host-wide registry file at `~/.drem/projects.toml` lists every registered project with `name`, `bare_repo_path`, `language`, and `orch_url`. It is created and maintained by new CLI commands on the `drem` binary: `drem project register`, `drem project list`, `drem project remove`. `register` also generates the per-project compose file from a template.

### Images

- **Per-language worker images.** `drem-worker-go`, `drem-worker-cpp`. Image selection is driven by the project's declared `language` field. No fat universal image.
- **Other images.** `drem-merger`, `drem-csuite-mike`, `drem-csuite-alex`, `drem-csuite-seth`, `drem-kyle`, `drem-orch`, `drem-spawner`, `drem-agentmon`.
- **Registry.** Local registry container running on the host. Remote registry is a future concern.

### Dev workflow

- Production orchestrator runs the baked image.
- Development orchestrator bind-mounts the source directory and uses an entrypoint that rebuilds and execs the binary on container start. Restart on code change. Kyle always runs the baked image.

### Modules to be built or modified

**New modules and binaries:**

- A container runtime abstraction that hides the Docker API behind a small interface (spawn, inspect, stream logs, subscribe events, destroy). Used by the orchestrator, spawner, and agentmon.
- A spawner service binary exposing `SpawnWorker`, `DestroyWorker`, `ListWorkers`, `InspectWorker` over a Unix socket.
- A Git reference package that replaces the existing worktree package. Tracks branch names only; performs no filesystem operations.
- A merger package that implements the merge operation (clone integration, merge branch, run tests, push, delete branch) and is invoked inside a merger container.
- An orchestrator HTTP API package exposing read-only endpoints for Kyle and the TUI plus an internal endpoint for agentmon ingestion. Includes a client library for Kyle.
- An extraction package for parsing log lines into typed state events. Shared between agentmon's new Docker input path and the existing transcript-tailing code path.
- A watchdog binary baked into worker images. Timer-based auto-commit and push.
- A Kyle binary and container image.

**Modified modules:**

- The orchestrator package has its worktree operations replaced by spawner RPC calls and Docker event subscriptions. State machine unchanged. Dispatch sites migrated as of 2026-04-20: subtask-coder (`subtask_scheduling.go`), reviewer + fixer public entrypoints (`session_spawning.go`), test-failure fixer re-dispatch (`test_execution.go`) now prefer `o.Spawner.SpawnWorker` via `spawnTypedWorker` when wired. The legacy `runner.SpawnAgent` host-subprocess path remains as a nil-Spawner fallback for local development on a host with the claude CLI installed, and for the two explicitly-retained subprocess fallbacks (warm-planner subprocess fallback in `task_processing.go`, warm-classifier subprocess fallback in `classifying.go`). See `plans/phase-3.5-subtask-dispatch-migration.md`.
- The agent package routes spawn operations through the spawner RPC instead of direct subprocess exec. Agent-type-to-image mapping added.
- The `drem` CLI gains `project register`, `project list`, and `project remove` commands.
- The agentmon package is extended to subscribe to Docker container stdout sources in addition to the existing Claude transcript tailing.

**Deleted modules:**

- The tmux package is removed.
- The worktree package is removed.

### Configuration changes

- `drem.toml` gains a `language` field and an optional `container_image` override per agent type. Tmux-related fields are removed.
- A new host-wide `~/.drem/projects.toml` registry is introduced.

### RPC and HTTP contracts

- **Spawner RPC (Unix socket, JSON-RPC 2.0 framed):** `SpawnWorker(project, agent_type, worker_id, branch, labels) → {container_id, endpoint}`; `DestroyWorker(container_id) → {}`; `ListWorkers(project?) → [WorkerInfo]`; `InspectWorker(container_id) → {status, exit_code, started_at, finished_at, oom_killed}`.
- **Orchestrator public HTTP (read-only, consumed by Kyle and TUI):** `GET /projects`; `GET /projects/:name/tasks`; `GET /projects/:name/workers`; `GET /workers/:id`; `GET /workers/:id/history`; `GET /events?since=`; `GET /logs?container=&since=` (proxies `docker logs`).
- **Orchestrator internal HTTP (consumed by agentmon):** `POST /internal/logs` accepting batched structured telemetry records. Ingestion creates `TaskEvent` rows only and cannot mutate task context or advance, fail, retry, or approve a task.

### Networking and security

- Worker containers have full outbound network access for the first cut. Egress restriction is a future concern.
- The Docker socket is mounted only into the spawner service. All other services talk to the spawner over a Unix socket.
- Kyle's `docker_query` tool reads the Docker API through a read-only proxy container (socat plus an allowlist, or a thin HTTP service). Kyle never receives the raw socket.
- Each project's orchestrator authenticates agentmon's `POST /internal/logs` via a per-project shared token set by compose configuration.

### Lifecycle and recovery

- A worker's watchdog commits and pushes every minute if the working tree has diffs, and immediately after any test command returns a zero exit code.
- On worker container death, the orchestrator records a terminal observation before applying its task effect, finalizes the typed WorkerAttempt, and applies the ordinary retry policy. Docker events are a latency optimization; spawner polling supplies the same typed terminal observation.
- The merger pool resets each container's workspace between merges (remove workspace, re-clone integration branch).
- On successful merge, the merger deletes the feature branch from the bare repository.
- On orchestrator restart, it resumes typed WorkerAttempt lifecycle processing from SQLite and a fresh `ListWorkers` call. A terminal spawner record may be consumed; absence from the inventory is not evidence of death and cannot mutate or respawn a task.

## Testing Decisions

Good tests in this project assert external behavior (what a module returns, what side effects it produces at its interface) rather than implementation details (which helpers it called, what the intermediate state looked like). Test fidelity comes from exercising real interfaces against real dependencies where practical, and from small deterministic fakes where a real dependency would be too slow or non-hermetic.

Tests will be written for every new module:

- **Container runtime abstraction.** A fake runtime implementation exists in the test package. Tests assert that callers (spawner, agentmon, orchestrator) invoke the runtime's interface correctly for each lifecycle event, and that the fake's recorded calls match expectations. The real Docker runtime is exercised by a small integration test that spawns a hello-world container on a real daemon.
- **Merger.** Tests set up a bare repository via `testutil.SetupBareRepo`, create two diverging branches using the existing `testutil.CommitFile` helper, invoke the merger, and assert on the resulting state of the bare repository (branches merged, feature branch deleted, test command output captured). Prior art: existing merge tests in `internal/merge/` and `internal/orchestrator/merge_execution.go`.
- **Extraction.** Pure function tests. Given a log line or a batch of log lines, assert that the correct typed event is emitted (or nil). No fixtures beyond string inputs.
- **Git reference registry.** Tests assert CRUD correctness on branch records in SQLite and integration with the bare repository (branch exists, branch not found, concurrent registration). Uses existing `testutil.NewTestDB` and `testutil.SetupBareRepo` helpers.
- **Orchestrator HTTP API.** Integration tests that spin up the orchestrator against a test database, issue HTTP requests, and assert JSON response shape and content. A client-library test uses an `httptest.Server` to stub the server side.
- **Host-authoritative delivery.** `testing_ready` freezes an exact branch,
  commit, base, and preliminary evidence record. External verification and
  integration are authenticated, versioned, idempotent mutations; the merger
  re-checks the authorized SHAs inside its container before changing the
  integration branch, and `done` atomically links the resulting merge commit
  to the accepted evidence chain.
- **Spawner RPC.** Tests use the fake container runtime and assert that each RPC produces the expected runtime calls and returns the expected response.
- **Watchdog.** Tests exercise the commit-and-push loop against a bare repository, asserting that commits appear on the feature branch when the working tree has diffs and that the loop is a no-op when there are no diffs or the test command has not yet passed.

Prior art conventions from the existing codebase should be preserved: database factories and Git repo helpers live in `internal/testutil/`; tests never define local copies. GORM hooks remain consolidated. File-length and function-count ceilings in `ARCHITECTURE.md` apply to every new file.

## Out of Scope

- **Remote host deployment.** The first cut assumes every container runs on the single developer host. Multi-host scheduling, cross-host networking, and shared state across hosts are deferred.
- **Remote image registry.** The first cut uses a local registry container. Pushing to GHCR, Docker Hub, or another remote registry is a follow-up.
- **GPU scheduling across projects.** A single SGLang container is shared, with GQ serializing access. Fine-grained per-project GPU allocation is deferred.
- **Windows host support.** Linux remains the primary all-in-one deployment.
  A bounded macOS Docker Desktop topology is supported when inference is
  supplied by a remote GQ/SGLang host and final project verification runs
  natively on the Mac; see `plans/macos-remote-inference-control-plane.md`.
- **Multi-user or multi-tenant isolation.** The entire stack runs under a single operating-system user.
- **Secrets management.** Environment variables in compose files are acceptable for the first cut. A vault integration is a follow-up.
- **Hot reload for the orchestrator in development.** Restart-on-rebuild is sufficient. Automatic reload (for example via `air` or `reflex`) is optional and can be added later.
- **Volume-based full-filesystem restore for crashed coder containers.** Commit-and-push recovery is the only supported path in the first cut. A named-volume enhancement for Opus coder sessions can be evaluated once the baseline is in production.
- **Restricting worker outbound network access.** Workers have full outbound network access initially. A scoped network policy (allowlist for package registries and the project's bare repository only) is a follow-up.
- **TUI rewrite.** The TUI continues to function as-is, switching its data source from direct database queries to the orchestrator HTTP API. No visual or interaction changes are in scope.
- **Retiring the `claude` CLI or `opencode` in favor of a custom agent harness.** The existing agent entry points continue to be used inside the worker containers. Only the surrounding orchestration is containerized.
- **Migration of existing in-flight tasks.** The cutover is expected to happen against a clean state (no running tasks). Any in-flight tasks at the time of migration are completed or cancelled on the old system first.

## Further Notes

### Phased rollout

A natural sequencing that keeps the host-side system usable throughout:

1. Stand up the global compose (local registry, SGLang, GQ) and migrate SGLang plus GQ to containers. No agent-facing changes. Validates the shared-infra story.
2. Build the spawner service, the container runtime abstraction, and worker images for Go. Route Opus coder tasks through the new path for the drem-orchestrator project. Host-side workers remain available as a fallback during the transition.
3. Extend agentmon to subscribe to container stdout. Add the orchestrator HTTP API and migrate the TUI to call it.
4. Build Kyle and the per-project compose generator. Register drem-orchestrator as the first project and validate end-to-end reporting.
5. Build worker-cpp image, register drem-canvas as the second project, and validate multi-project operation.
6. Migrate merger to a warm pool. Build the watchdog and enable crash recovery.
7. Retire the tmux and worktree packages once no code path depends on them.

### Blast radius design principles

Two invariants govern the architecture: (1) each project's persistent data (SQLite, logs in the orchestrator database) is isolated to that project so corruption cannot spread; (2) shared services (SGLang, GQ, Kyle, the spawner) are designed to fail closed — if they are unavailable, individual projects pause rather than proceed with stale or partial data.

### Open items not yet decided

- Whether Kyle's query API is pull (Kyle polls each orchestrator on demand) or push (each orchestrator streams events to Kyle via a long-lived connection). Pull is simpler and assumed in the first cut; push is a follow-up if freshness latency matters.
- Exact image tagging strategy (semver, git sha, rolling `latest`). Assumed to be git-sha-pinned in compose files with an optional `latest` convenience tag for development.
- Whether the spawner should accept an explicit image tag from the orchestrator or resolve it from the project's configuration. Assumed to resolve from configuration with an orchestrator-supplied override.

### Relationship to existing architecture documents

This PRD supersedes any prior implicit assumptions about host-side worktrees and tmux-based agent hosting. The state machine and agent role definitions in `ARCHITECTURE.md` remain authoritative. The `[enforced]` structural limits (file length, function count, package imports) continue to apply to all new code.
