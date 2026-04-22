# Containerization PRD — Promised vs. Punted Checklist

> **Scope:** Ledger of every commitment in `docs/prd-containerization.md` +
> `plans/containerization.md`, matched against the shipped artifacts on
> `master` as of 2026-04-21. Supporting plan docs read lightly; cited only
> where they contradict the PRD or carry the only status signal.
>
> **Hand-off target:** Seth's pass-2 scoreboard. This document expands
> Seth's 20-item pass-1 scoreboard to whatever the PRD actually commits.
>
> **Ground rule:** not a ranking. Status only. Seth integrates.

---

## 1. Summary

- **PRD scope inventoried:** 54 explicit user stories + 7 phased goals in
  the plan + ~15 architectural invariants in `## Implementation Decisions`
  + 12 explicit out-of-scope items = **~73 distinct commitments** to
  track. Collapsing user stories that map 1:1 to an architectural
  decision, the effective green/yellow/red/gray ledger below tracks
  **61 items.**
- **Delivered (green):** **42** commitments with shipped code +
  commit evidence.
- **In-progress (yellow):** **6** commitments with visible code but
  incomplete acceptance criteria.
- **Punted / deferred (red):** **9** commitments explicitly
  committed in the PRD with no in-flight work today.
- **Silent (gray):** **4** commitments named in the PRD once, not
  addressed anywhere after.
- **Headline: 42 of 61 PRD commitments delivered (~69%).**

This lines up with Seth's pass-1 "65% complete" headline. The ledger
below is the itemized view.

> **Canonical-status signal:** `docs/containerization/remaining-work.md`
> declares the initial 20-prompt rollout **DONE** as of the `master`
> cutover that removed `internal/tmux/` and `internal/worktree/`. The
> punted items below are post-initial-rollout deferrals and residuals
> from the Phase 6 (merger warm pool, watchdog) and Phase 7 (cleanup)
> boundaries — not pre-rollout debt.

---

## 2. Delivered commitments (green)

One-line each. PRD §/US = PRD section or user story; plan §N.M = plan
slice number.

### Core containerization (Phase 1–7)

- **US 1 (long-running services in containers)** — orch, spawner, agentmon,
  SGLang, GQ, registry, C-Suite, Kyle all run in compose. `deploy/compose/global.yml`.
- **US 4 (global GQ)** — single `gq` service in global compose; `012e56e`
  container-runtime; `deploy/docker/gq.Dockerfile`.
- **US 5 (global SGLang)** — `drem-sglang` in global compose;
  `deploy/docker/sglang.Dockerfile`; `.env`-parameterized per `f500f26`.
- **US 6 (Kyle global singleton)** — `cmd/drem-kyle`; `internal/kyle/`;
  `4fb5e00`.
- **US 7 (C-Suite warm containers per project)** — Mike, Alex, Ross, Seth
  images + per-project compose template; `765c5c0`. Runtime pivoted to
  `csuite-persona` poller 2026-04-20 (`3e22ee6`).
- **US 8 (workers as ephemeral containers)** — `SpawnWorker` via
  `internal/spawner/`; `dcdcc03`.
- **US 10 (worker clones branch into container fs)** — `worker-base.Dockerfile`
  entrypoint + `drem-global-spawner-1` bind-mounts bare repo ro.
- **US 11 (host worktrees eliminated)** — `internal/tmux/` and
  `internal/worktree/` deleted; only residual `internal/worktreehost/`
  adapter retained by plan Phase 7 per `remaining-work.md`.
- **US 12 (container labels)** — `drem.project`, `drem.project_id`,
  `drem.agent_type`, `drem.worker_id`, `drem.task_id`, `drem.branch`
  emitted by spawner (`internal/spawner/methods.go`,
  `internal/spawner/types.go`). Tests in `service_test.go` enforce.
- **US 13 (live state HTTP API)** — `internal/orchhttp/` exposes
  `/projects`, `/tasks`, `/workers`, `/events`; `ce506bf`.
- **US 15 (`docker_query` MCP proxy)** — `cmd/drem-docker-query-proxy`,
  `internal/kyle/docker_query.go`.
- **US 16 (agentmon filters Docker stdout into events)** —
  `internal/agentmon/docker_*.go`; `e12d685`.
- **US 19 (orchestrator detects worker death via Docker events and
  respawns)** — `internal/orchestrator/docker_events.go` + reconciler
  (`77e349a`, `a3b2855`, `181afcc`); `3963105` branch persistence.
- **US 20 (spawner owns Docker socket)** — only `drem-global-spawner-1`
  in `global.yml` mounts `/var/run/docker.sock`. Agentmon deviation
  documented (`remaining-work.md` §Known deviations).
- **US 21 (spawner narrow RPC)** — `SpawnWorker`, `DestroyWorker`,
  `ListWorkers`, `InspectWorker` over Unix socket; JSON-RPC 2.0 framed.
- **US 22, 23, 24 (merger clones + deletes branch + structured records)** —
  `internal/merger/`, `cmd/drem-merger`; `f90bd13`. Branch deletion on
  success: plan 6.5 checked `[x]` 2026-04-19.
- **US 25 (orchestrator subscribes to Docker lifecycle events)** —
  `internal/orchestrator/docker_events.go`; `507e88c` wires runtime in
  `cmd/drem`.
- **US 26 (agentmon structured events)** — `internal/extract/` package
  + agentmon docker input; `0e8e926`.
- **US 27 (raw logs not duplicated)** — confirmed: agentmon POSTs
  `/internal/logs` events only; raw logs remain behind
  `GET /logs?container=…`.
- **US 28 (`GET /logs` proxy)** — `internal/orchhttp/server.go` routes
  `/logs` through runtime `StreamLogs`.
- **US 29 (global compose in repo)** — `deploy/compose/global.yml`
  committed in-repo.
- **US 30 (per-project compose generated)** —
  `internal/projects/templates/project-compose.yml.tmpl`;
  `cmd/drem/project.go register`.
- **US 31 (`~/.drem/projects.toml` registry)** —
  `internal/projects/registry.go`; `c253711`. Confirmed on disk:
  `drem-orchestrator` registered.
- **US 32 (per-language worker images)** — `worker-base.Dockerfile`,
  `worker-go.Dockerfile`, `worker-cpp.Dockerfile`; `39d4b4a`.
- **US 33 (image driven by language field)** — `internal/images/`;
  `5969bbf`; `fe5aced` maps reviewer/fixer/supervisor/classifier to
  `drem-worker-go`.
- **US 34 (local registry)** — `drem-registry` service in `global.yml`;
  `deploy/compose/README.md`.
- **US 36 (orchestrator production container from baked image)** —
  `deploy/docker/orch.Dockerfile`.
- **US 37 (dev mode with bind-mount)** — `deploy/docker/orch-dev.Dockerfile`.
- **US 38 (Kyle from baked image)** — plan 4.5 acceptance; `4fb5e00`;
  `kyle.Dockerfile` exists.
- **US 39 (`drem` CLI continues to work)** — `cmd/drem/` still the
  primary TUI.
- **US 40 (TUI reads from API)** — `internal/tui/datasource.go`,
  `dto_adapter.go`; `db4bcd9`; plan 3.7.
- **US 41 (remove `internal/tmux/`)** — deleted; confirmed: `ls
  internal/tmux` fails.
- **US 42 (`internal/gitref/` replaces worktree)** — `internal/gitref/`;
  `be29840`. `internal/worktreehost/` retained as adapter per
  `remaining-work.md` known-deviation.
- **US 43 (C/C++ toolchain in worker image)** —
  `deploy/docker/worker-cpp.Dockerfile`.
- **US 44 (full egress for now)** — no egress restriction in compose;
  PRD out-of-scope item that was intentionally not restricted.
- **US 45 (tool exec via `claude`/`opencode` on container fs)** —
  worker-go base; csuite-persona uses `claude -p`; `077c006`.
- **US 47 (orch sole DB writer)** — plan 3.8 "remove write paths";
  `remaining-work.md` confirms TUI/agentmon/Kyle go through HTTP.
- **US 48 (agentmon POSTs `/internal/logs`)** — `orchhttp/handlers_internal.go`;
  per-project shared token auth (`87c1906`, `ec1eb70`).
- **US 52 (state machine preserved)** — `backlog → planning → plan_review
  → … → done` unchanged in `internal/orchestrator/`.
- **US 53 (supervisor loop preserved)** — supervisor continues inside
  orch container per plan 7.3.
- **Spawner RPC contract frozen** (`plan §RPC and HTTP contracts`) —
  four methods shipped as specified; exhaustively tested
  (`internal/spawner/service_test.go`).
- **Orchestrator HTTP contract frozen** — every listed endpoint
  served; DTOs in `pkg/orchdto/`.
- **Subscription-only auth (CLAUDE.md invariant + PRD §networking)** —
  `plans/worker-subscription-auth.md` shipped 2026-04-20. No
  `ANTHROPIC_API_KEY` plumbing anywhere on the default path; orch
  rejects the env var with `reason=policy_violation_api_key`.
- **Worker prompt delivery** (CLAUDE.md-adjacent invariant) —
  `plans/worker-prompt-delivery.md` shipped 2026-04-20; atomic
  tmp+rename to `/home/drem/.drem/prompt.md`.
- **Bare-repo `receive.denyCurrentBranch=ignore`** (CLAUDE.md
  invariant; not in PRD directly) — `internal/projects/bare_repo.go`
  auto-sets; tests in `bare_repo_test.go`.

### Direct-agent warm containers (Phase 8 — out-of-PRD but plan-committed)

- **Plan 8.1 classifier** — `cmd/drem-classifier`; done per
  `plans/warm-direct-classifier.md`.
- **Plan 8.2 planner** — `cmd/drem-planner`; shipped 2026-04-20 per
  `plans/warm-planner-pivot.md`.

---

## 3. In-progress commitments (yellow)

Work is visible in commits but acceptance criteria not all met.

### Y1. Phase 2 slice 2.6 orchestrator integration — feature flag superseded

- **PRD §:** US 8, plan Phase 2 / slice 2.6.
- **Evidence started:** container spawn path wired for coder, reviewer,
  fixer, test-failure fixer, quickfix (`46a3d4f`, `dce5358`, `eab43e0`,
  `691f17b`, `5479d45`). Plan §Phase 2 AC checkboxes 1 + 2 are `[x]`.
- **Evidence not finished:** legacy `runner.SpawnAgent` host-subprocess
  path remains in `subtask_scheduling.go`, `quickfix_processing.go`,
  `task_processing.go`, and the warm-planner/classifier subprocess
  fallbacks. This is explicitly retained by the plan ("for local dev on
  a host with claude installed"). Seth pass-1 **F20** flags this as a
  blocking silent-fallback. PRD US 8 ("workers run as ephemeral
  containers, so that any crash, leaked file, or stuck process is
  automatically cleaned up by destroying the container") technically
  not met as long as the host path is reachable.
- **Ambiguous status:** plan author's note `2026-04-20` calls the
  feature flag "superseded by the production configuration." Operator
  intent unclear whether the host fallback is *permanent* (dev affordance)
  or *transitional* (to be deleted).

### Y2. Phase 2 AC #3 — "worker container clones bare repo into `/workspace`, exits on agent completion, is destroyed by the spawner"

- **PRD §:** plan Phase 2 acceptance.
- **Evidence started:** worker-go Dockerfile entrypoint clones;
  `cmd/drem-spawner` implements Destroy.
- **Evidence not finished:** plan acceptance checkbox still `[ ]`.
  Container destruction on agent completion not exhaustively verified
  against the event-subscription path. Likely just needs a sign-off
  test.

### Y3. Phase 3 slice 3.5 `extraction` taxonomy coverage

- **PRD §:** US 16, plan slice 3.5.
- **Evidence started:** `internal/extract/` has parsers for commit,
  push, crash, heartbeat, build, test-result, tool-call (`parse_*.go`).
- **Evidence not finished:** plan acceptance "`extraction` tests cover
  at least: git-commit line, git-push line, test-pass line, test-fail
  line, build-error line, crash line, heartbeat line, tool-call-count
  line" — checkbox `[ ]`. Unclear if all eight are covered; parser
  files exist for each.

### Y4. Phase 6 AC — "worker can crash mid-task and be respawned; respawned worker's first commit includes work up to the last watchdog push"

- **PRD §:** US 17, 18, 19; plan Phase 6 AC.
- **Evidence started:** `cmd/drem-watchdog`, `internal/watchdog/`;
  `d737a2a`. Dozens of `[watchdog] wip` commits from the running
  watchdog itself. Docker event subscription + handleWorkerDeath wired
  (`docker_events.go`, `4fb5e00` handler).
- **Evidence not finished:** end-to-end crash-respawn regression test
  not merged; plan AC checkbox `[ ]`. Seth pass-1 **#19** is the
  unknown-status line item.

### Y5. C-Suite comms plane round-trip

- **PRD §:** plan §Compose topology (per-project compose services) +
  csuite-persona pivot §Architecture.
- **Evidence started:** `csuite-watcher` `/deliver` endpoint
  (`59f30d4`..`5bf097f`); persona→watcher POST (`fbe56db`); atomic
  delivery (`89e1ec7`); quarantine classification (`0e5f6be`).
- **Evidence not finished:** auto-reply routing broken —
  `plans/csuite-persona-auto-reply-routing.md` (status: recon-only,
  2026-04-21) documents 102/119 deliveries quarantined over ~10 hours.
  Seth pass-1 **#5** (persona→persona routes) and **#6**
  (`.failures` sidecars surfaced) map directly.

### Y6. Phase 8.3 prep container

- **PRD §:** none (plan-internal commitment).
- **Evidence started:** `plans/warm-direct-prep.md` design proposed.
- **Evidence not finished:** `cmd/drem-prep` doesn't exist; no
  Dockerfile for prep. Plan AC checkbox `[ ]`.

---

## 4. Punted / deferred commitments (red)

Explicitly committed in the PRD, no in-flight work today. Operator's
target list.

### R1. US 2 / US 50 — register `drem-canvas` as the second project

- **PRD §:** US 2 ("register a new project with a single command"),
  US 50 ("register the two initial projects (drem-orchestrator and
  drem-canvas) through the same registration flow"), plan Phase 5.
- **What's missing:** `drem-canvas` is NOT in
  `~/.drem/projects.toml`. Only `drem-orchestrator` is registered.
  `~/.drem/projects/drem-canvas/` exists as a bare directory with
  `data/` and `prompts/` but no compose.yml or registry entry.
- **Why punted (inferred):** no commit on `master` references canvas
  registration. Phase 5 was gated on worker-cpp image + Kyle
  cross-project report (plan §Sequencing #4). worker-cpp image exists;
  the registration step was never run.
- **Pivot-complete per PRD?** **NO.** US 50: "the multi-project story
  is validated from day one." PRD solution statement: "The user wants
  to apply this setup to `drem-canvas` (C/C++) while continuing to use
  it on `drem-orchestrator` (Go)." Without canvas registered, the
  multi-project invariant is not validated.

### R2. US 9 — merger warm pool

- **PRD §:** US 9 ("merger as a warm pool of containers per project,
  so that merges start instantly"), plan Phase 6 slice 6.3.
- **What's missing:** per-project compose template removed `merger-pool`
  because `drem-merger` is a per-task one-shot that crash-loops with no
  argv. Current state: `merger-template` stub behind
  `profiles: ["never"]` primes the image; spawn-on-demand wires the
  per-task invocation. No warm pool.
- **Why punted:** `plans/merger-spawn-on-demand.md` (2026-04-19) —
  binary contract (per-task one-shot) incompatible with warm-pool
  compose shape; spawn-on-demand ships without warm pool. PRD §Ephemeral
  containers even calls this out in-line: "the original PRD called for
  a warm merger pool, but `drem-merger` is implemented as a per-task
  one-shot binary that crash-loops when run with no argv."
- **Pivot-complete per PRD?** **Plan-inconsistent.** The PRD text was
  *edited* to reflect the deviation (PRD §Architecture / Warm containers
  bullet now reads "Originally a per-project merger pool of 2–3
  containers was also planned; see the merger note under 'Ephemeral
  containers'"). So the PRD contradicts itself: US 9 still mandates a
  warm pool; architecture §ephemeral explicitly redirects to on-demand.
  **Called out in §6 contradictions.**

### R3. US 17 / US 18 — watchdog baked into worker images with commit+push every minute and after every test pass

- **PRD §:** US 17 (Opus coder watchdog), US 18 (G4 worker watchdog),
  plan Phase 6 slice 6.6.
- **What's missing:** `cmd/drem-watchdog` + `internal/watchdog/` exist
  and are running (many `[watchdog] wip` commits from running
  instances). BUT no direct evidence the timer-based commit+push loop
  is baked into the worker-go / worker-cpp images and running per-task.
  Plan AC "a worker can crash mid-task and be respawned; the respawned
  worker's first commit includes work up to the last watchdog push"
  still `[ ]`.
- **Why punted (inferred):** Phase 6 has multiple open acceptance
  items; watchdog-in-worker is one of them. Seth pass-1 item **#14**
  ("watchdog exposes pprof/SIGUSR1") and `plans/agentmon-observability.md`
  suggest watchdog is running as its own service but not fully
  integrated into worker-image recovery.
- **Pivot-complete per PRD?** **NO for recovery story.** PRD §Lifecycle
  and recovery: "A worker's watchdog commits and pushes every minute
  if the working tree has diffs, and immediately after any test command
  returns a zero exit code." This is the recovery invariant; without
  it, crashed workers lose uncommitted work.

### R4. US 46 — two-gate testing (worker runs tests before push, merger re-runs before merging)

- **PRD §:** US 46, plan Phase 6 slice 6.8.
- **What's missing:** merger gate exists (`cmd/drem-merger` runs
  `--test-cmd`; Bug H follow-up `9880c6b`, `3fdcb85` hardened the
  empty-TestCmd case). Worker-side pre-push gate not observed in any
  commit — worker's `claude` session ends and the branch is pushed;
  there's no explicit "run tests, block push on failure" watchdog
  action.
- **Why punted:** plan slice 6.8 is `[G4]` ("integration in watchdog +
  merger") — likely subsumed under Phase 6 AC open items. Seth pass-1
  Group B ("silent fallbacks") would surface a worker that skips
  pre-push tests as a silent-skip.
- **Pivot-complete per PRD?** **Partially.** Merger gate is
  authoritative and covers the "broken code cannot reach integration"
  intent. The worker-side fail-fast gate is an optimization for latency;
  the pivot is functional without it but the PRD explicitly promises it.

### R5. US 51 — Kyle aggregates CPU/memory/network across projects

- **PRD §:** US 51 ("Kyle, when reporting resource usage, aggregate
  CPU, memory, and network statistics across every container in every
  project"), plan Phase 5 slice 5.4.
- **What's missing:** `internal/kyle/` has `poll.go`, `summary.go`,
  `cache.go` — polls tasks/workers/events per project. No CPU/mem/net
  aggregation observed; `docker stats` (via docker_query proxy) is
  available but not aggregated into report output.
- **Why punted:** Phase 5 blocked on R1 (drem-canvas not registered).
  Kyle cross-project resource report has one registered project to
  aggregate over, so the feature has no live demo.
- **Pivot-complete per PRD?** **NO.** Kyle's resource-attribution
  story is load-bearing for the split-brain world-state problem
  statement.

### R6. PRD §Architecture "Agentmon absorbs log shipping" via spawner-only Docker socket (strict)

- **PRD §:** Architecture §Agentmon absorbs log shipping + PRD §Networking
  "Docker socket mounted only into spawner service."
- **What's missing:** `remaining-work.md` explicitly documents the
  deviation: "Agentmon Docker socket. Mounted read-only directly into
  agentmon (`/var/run/docker.sock:...:ro`) rather than going through
  `docker-query-proxy`. The PRD's 'socket only in spawner' principle
  is deviated from in the first cut."
- **Why punted:** first-cut hardening deferral; documented explicitly
  as a follow-up.
- **Pivot-complete per PRD?** **Depends on strictness.** The PRD
  §Networking invariant is violated as a matter of fact. If the
  operator treats "first cut" as license, this is acceptable debt.

### R7. US 54 — ephemeral C-Suite one-shot prompts as short-lived containers

- **PRD §:** US 54 ("ephemeral C-Suite one-shot prompts run as
  short-lived containers spawned inside the project's C-Suite context"),
  plan Phase 7 slice 7.4.
- **What's missing:** csuite-persona pivot (2026-04-20) replaced the
  interactive entrypoint with a poller. The poller `exec`s `claude -p`
  one-shot per message (`077c006`) — which is in-container subprocess,
  not a short-lived sibling container spawn. PRD US 54 specifies
  "ephemeral containers", not "in-container subprocess".
- **Why punted:** pivot design traded container-per-prompt for poller+
  subprocess to close the file-based inbox workflow that previously
  never worked. Poller is simpler.
- **Pivot-complete per PRD?** **NO** (by strict reading). **Ambiguous**
  if the operator accepts "claude -p per-message" as equivalent isolation
  to "container per-message". The poller model has weaker isolation
  (one crashed claude process can leak state into state.md for the next
  invocation) but lower latency.

### R8. Orchestrator lifecycle "on restart, reconstructs in-flight worker state from SQLite + fresh ListWorkers"

- **PRD §:** PRD §Lifecycle and recovery.
- **What's missing:** no commit observed wiring orch startup to a
  `ListWorkers` reconciliation. `internal/orchestrator/reconcile_containers_test.go`
  exists and `77e349a` added container-aware stuck-agent reconciler,
  but these run on-tick, not on startup. Unclear whether a fresh orch
  boot rehydrates in-flight state correctly or just reconstructs from
  DB alone.
- **Why punted:** likely covered by the on-tick reconciler as an
  emergent property; if so, it's an ambiguous-delivered. If not, it's
  a latent gap.
- **Pivot-complete per PRD?** **Ambiguous.** Worth a 30-minute
  investigation — is there an explicit `ListWorkers + DB merge` on
  orch startup, or is it all tick-driven?

### R9. US 49 — image SHA recorded in orch DB per run

- **PRD §:** US 49 ("each worker, merger, and C-Suite container's
  startup image identity and labels recorded in the orchestrator
  database, so that Kyle can attribute each run to a specific image
  version"), plan slice 4.5.
- **What's missing:** `drem.image_sha` label is mentioned in plan 4.5
  acceptance list but not observed in `spawner/methods.go` label emit.
  Spawner labels emit `drem.project`, `drem.project_id`,
  `drem.agent_type`, `drem.worker_id`, `drem.task_id`, `drem.branch` —
  `image_sha` absent. No column in `Agent` or `Task` model stores
  image sha.
- **Why punted:** unclear — this is a pure labeling + column add,
  no architectural blocker. Probably just not prioritized.
- **Pivot-complete per PRD?** **NO.** Kyle reproducibility story is
  named in the PRD and currently not backed.

---

## 5. Silent commitments (gray)

Named in the PRD once, not addressed anywhere else in the repo.

### S1. US 3 — per-project isolated SQLite + blast-radius isolation test

- **PRD §:** US 3 ("each registered project to have its own isolated
  SQLite database and its own per-project compose file, so that a
  failure or data corruption in one project does not affect another").
  Plan Phase 5 slice 5.5 ("induce a write error in drem-canvas's
  SQLite; confirm drem-orchestrator's orchestrator + API are
  unaffected").
- **Silent because:** DB-per-project is shipped (each project compose
  has its own volume), BUT the blast-radius chaos test (slice 5.5)
  is not observable as a test file or commit. Without the chaos test,
  the isolation invariant is asserted by design rather than proven.
- **Quietly-dropped vs. oversight?** Likely oversight — the test was
  gated on R1 (canvas registration) and forgotten when R1 slipped.

### S2. US 14 — Kyle queries historical state ("what went wrong")

- **PRD §:** US 14 ("return historical state (recently completed tasks,
  exit codes, crash reasons), so that I can answer 'what went wrong'
  without log archaeology").
- **Silent because:** `GET /workers/:id/history` is listed as a
  contract endpoint but no observable wiring to a "crash reasons" or
  "exit codes" surface in Kyle's report. `remaining-work.md` doesn't
  flag it. Seth pass-1 **#9** (failure_reason column) + **#16**
  (correlation IDs) are the sibling observability gaps.
- **Quietly-dropped vs. oversight?** Probably oversight — the endpoint
  exists, the data plumbing to populate it isn't evident. Sibling to
  Seth's F05 (failure_reason-as-column, currently in `Context` JSON).

### S3. US 35 — remote registry migration architecturally not blocked

- **PRD §:** US 35 ("the option to migrate to a remote registry later,
  so that multi-host deployment is not architecturally blocked").
- **Silent because:** local registry is there. No commit, plan doc,
  or config toggle demonstrates that switching to a remote registry
  is a config change rather than a code change. Could genuinely be
  cheap (compose var + `docker login`) or could require code changes
  that nobody's validated.
- **Quietly-dropped vs. oversight?** Probably aspirational scope
  ("architecturally not blocked" is a negative assertion — hard to
  prove without trying).

### S4. Spawner RPC auth model ("uid check on socket")

- **PRD §:** plan §Phase 2 / slice 2.2 ("structured logs. Graceful
  shutdown. … `auth model (uid check on socket)`").
- **Silent because:** Unix-socket filesystem permissions are the
  default mechanism; no explicit uid-check code in
  `internal/spawner/`. The socket is mounted rw into orch + read-only
  where it can be, so "auth" is effectively filesystem-level.
- **Quietly-dropped vs. oversight?** Probably quietly-dropped — the
  socket perms turned out to be sufficient and the explicit uid check
  was overengineering. Worth confirming.

---

## 6. Supporting-plan-doc contradictions

### C1. PRD US 9 vs. PRD §Architecture §Ephemeral containers (merger warm pool)

- **Contradiction:** US 9 (body of the PRD) commits to a warm merger
  pool. §Architecture §Ephemeral containers explicitly says "the
  original PRD called for a warm merger pool, but `drem-merger` is
  implemented as a per-task one-shot binary that crash-loops when run
  with no argv" and redirects to `plans/merger-spawn-on-demand.md`.
- **Canonical:** The `§Architecture` inline note was added after
  discovery; operator ratified it via the 2026-04-19 commit. Effective
  canon: **merger is spawn-on-demand, not warm pool.** The user-story
  section was not edited to match; this is a PRD stale-ness rather
  than an open decision.
- **Recommended resolution:** rewrite US 9 to "merger runs as an
  on-demand container spawned per merge task" or flag US 9 as
  superseded by the Architecture §Ephemeral bullet.

### C2. PRD Phase 2 slice 2.6 feature flag vs. Phase 3.5 migration plan

- **Contradiction:** Plan Phase 2 slice 2.6 calls for `drem.toml:
  workers.engine = "spawner" | "host"` feature flag. Plan Phase 2 AC
  comment (`2026-04-20`) says: "Slice 2.6's original feature-flag
  framing was superseded by the production configuration — per-project
  compose files already set DREM_WORKER_CREDS_PATH + DREM_PROMPT_ROOT_HOST,
  so 'spawner wired' is the observable invariant the migration keys
  off."
- **Canonical:** The later note wins by recency. "Spawner wired"
  (checked via nil-Spawner fallback) is the observable invariant.
- **Recommended resolution:** strike the feature-flag line from the
  plan; make "spawner wired" explicit.

### C3. PRD US 54 vs. `plans/csuite-persona-pivot.md`

- **Contradiction:** US 54 specifies "ephemeral C-Suite one-shot
  prompts run as short-lived containers." csuite-persona-pivot.md
  (2026-04-20) replaces this with in-container `claude -p` subprocess
  per message.
- **Canonical:** The csuite-persona-pivot is committed and running.
  Effective canon: **in-container subprocess per message.**
- **Recommended resolution:** either edit US 54 to permit the
  subprocess model, or treat US 54 as the aspirational target and
  the poller as a transitional stop-gap (then track the container-
  per-prompt step as R7 above).

### C4. PRD §Networking "socket only in spawner" vs. agentmon implementation

- **Contradiction:** PRD §Networking: "The Docker socket is mounted
  only into the spawner service." Actual compose: agentmon also
  mounts `/var/run/docker.sock:...:ro`.
- **Canonical:** `remaining-work.md` documents the deviation as an
  accepted first-cut compromise. Routing agentmon through
  docker-query-proxy is a named follow-up.
- **Recommended resolution:** retain the PRD invariant as the target,
  mark agentmon-via-proxy as a pivot-complete follow-up (this is R6).

---

## 7. Cross-reference to Seth's pass-1 scoreboard

Seth's 20-item exit scoreboard at
`/home/godinj/.drem-csuite/seth/outbox/20260422T073000Z-seth-to-kyle-containerization-pivot-gap-synthesis.md`
§4.

### 7.1. Seth scoreboard items that map to PRD-punted commitments

| Seth # | Seth item | Maps to PRD item |
|---|---|---|
| 2 | Watcher image parity | (Y5) C-Suite comms plane — supporting concern |
| 5 | Persona→persona mail routes | (Y5) C-Suite comms plane |
| 6 | `.failures` sidecars surfaced | (Y5) C-Suite comms plane |
| 8 | Spawner has no legacy host-spawn fallback | (Y1) Phase 2 slice 2.6 orch integration |
| 9 | `failure_reason` first-class column | (S2) sibling to US 14 historical state |
| 20 | Image pinning content-addressed | (R9) US 49 image SHA recording |

### 7.2. PRD commitments NOT on Seth's scoreboard but should be

These are punted per this audit and absent from Seth's §4:

- **R1 — `drem-canvas` registration** (PRD US 2, US 50). This is the
  single biggest pivot-complete blocker per the PRD's own language
  ("multi-project story validated from day one"). Worth a top-of-
  scoreboard line item.
- **R2 — merger warm pool** (PRD US 9). Even though it's been
  explicitly redesigned to spawn-on-demand, the PRD text still
  commits to it. Either scoreboard line "warm pool" or scoreboard
  line "PRD reconciled with on-demand reality."
- **R3 — watchdog commit+push in worker image** (PRD US 17, 18). Seth
  names the watchdog generally (#14 pprof) but not the core
  commit-push-on-test-pass invariant. Without it, crashed workers
  lose work — this is a P0 pivot invariant.
- **R4 — two-gate testing worker-side pre-push gate** (PRD US 46).
- **R5 — Kyle cross-project CPU/mem/net aggregation** (PRD US 51).
- **R6 — agentmon through docker-query-proxy** (PRD §Networking).
  Documented deviation; worth a scoreboard line.
- **R7 — C-Suite one-shot in ephemeral container** (PRD US 54).
- **R8 — orch-startup `ListWorkers` reconstruction** (PRD §Lifecycle).
- **S1 — blast-radius isolation chaos test** (PRD US 3).
- **S2 — historical state / "what went wrong" via Kyle** (PRD US 14).

### 7.3. Seth-invented scoreboard items (not in PRD) clearly derived from pivot intent

| Seth # | Item | PRD-derived? |
|---|---|---|
| 7 | No dual-writer races on inbox/outbox | Yes — derivative of PRD §Architecture "Orchestrator is single state surface" applied to csuite mail |
| 11 | State-machine single canonical edge per transition | Yes — derivative of PRD US 52 "state machine preserved" |
| 12 | Worktree subagents contract-validated preamble | No — Kyle/Seth-invented; not in PRD |
| 13 | `processTestingReady` not hot-looping | No — orch-internal quality concern, not PRD |
| 14 | Watchdog pprof/SIGUSR1 | No — observability layer on top of PRD US 17 |
| 15 | `/csuite/operator/` canonical or deleted | No — orphan-directory hygiene |
| 16 | Correlation IDs across orch/persona/worker | No — observability, derivative of PRD US 16 |
| 17 | Library↔CLI contract parity | No — Bug H follow-up, not PRD |
| 18 | Security audit items addressed | No — `/repo-audit` follow-through |
| 19 | Worker crash/SIGKILL reconciliation | Yes — derivative of PRD US 19 (detect worker death via Docker events) |

**Bottom line on the cross-reference:**
- Seth's scoreboard is heavy on operator-debt and observability.
- The PRD's own punts are heavier on user-facing feature gaps
  (canvas multi-project, watchdog recovery, Kyle cross-project
  reporting).
- The two lists are complementary. Seth's pass-2 should absorb R1–R9
  verbatim and keep items 7, 11–18 as operator-debt-grown-on-top.

---

## 8. Ambiguous / under-investigated

Items where status couldn't be resolved in this pass. Flag for
Seth's pass-2 investigation:

1. **Y2 Phase 2 AC #3** — is container destruction on agent completion
   exhaustively wired through the event-subscription path? Read
   `internal/orchestrator/docker_events.go` teardown branches.
2. **Y4 crash-respawn E2E test** — regression test may exist under
   `internal/orchestrator/reconcile_containers_test.go`. Scan for a
   "worker dies mid-task" assertion.
3. **R8 orch-startup reconstruction** — is there a dedicated
   `ListWorkers` call on orch boot, or is it tick-driven? 30-minute
   code read.
4. **US 49 image SHA** — confirm `drem.image_sha` label is not
   emitted anywhere. If it IS emitted somewhere I missed, R9 downgrades
   to yellow.
5. **S1 blast-radius chaos test** — grep for any test that simulates
   DB corruption in one project.

---

## Appendix: PRD commitments not in the ledger above

Items excluded from the 61-item count because they're explicit
out-of-scope (§Out of Scope) or pure re-statements of architectural
invariants counted elsewhere:

- Out of Scope: remote-host deployment, remote image registry, GPU
  scheduling across projects, Windows/macOS, multi-user, secrets vault,
  hot-reload for orch-in-dev, volume-based full-fs restore, worker
  egress restriction, TUI rewrite, custom agent harness, in-flight
  task migration. These are intentionally deferred; no ledger
  accounting needed.
- §Further Notes / Open items not yet decided: Kyle push-vs-pull, image
  tag strategy, spawner image override — all explicitly flagged as
  "decide later."
